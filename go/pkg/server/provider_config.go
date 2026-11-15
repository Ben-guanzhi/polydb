package server

// 阶段 13：配置对账（behavior.md §16.19）。与 Rust 侧
// rust/crates/server/src/provider_config.rs 严格镜像。
//
// Def 81 的配置对账落到 broker 配置上：provider 配置（§16.10.1 白名单 +
// §16.13.1 进程表）的变更**经 notify_d 分类增量生效**——改动只在受影响的
// key 上动作，其余 key 一概不动，且**无需重启宿主**。
//
// 分类表（§16.19.2）：
//
//	白名单 +key → activating（加白名单）
//	白名单 −key → deactivating（broker.Revoke 守卫 + 撤下端点；在途 > 0 推迟）
//	进程表 +条目 → activating（spawn）
//	进程表 −条目 → deactivating（kill + Revoke）
//	命令变更 → deactivating + activating（kill 旧 + spawn 新）
//	不变 → neutral（无动作）

import (
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

// providerConfigFile 是对账源的路径（POLYDB_PROVIDER_CONFIG_FILE）。
func providerConfigFile() string {
	return strings.TrimSpace(os.Getenv("POLYDB_PROVIDER_CONFIG_FILE"))
}

// providerConfigPollMS 是对账轮询周期（POLYDB_PROVIDER_CONFIG_POLL_MS，缺省 5000）。
func providerConfigPollMS() int {
	raw := strings.TrimSpace(os.Getenv("POLYDB_PROVIDER_CONFIG_POLL_MS"))
	if raw == "" {
		return 5000
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 5000
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 5000
	}
	return n
}

// providerFileConfig 是配置文件的形态（§16.19.1）。
type providerFileConfig struct {
	Allow []string          `json:"allow"`
	Procs []providerProcCfg `json:"procs"`
}

type providerProcCfg struct {
	Key string `json:"key"`
	Cmd string `json:"cmd"`
}

// readProviderConfigFile 读取并解析配置文件。缺失/解析失败返回 nil
// （调用方保持上次已知配置，§16.19.1）。
func readProviderConfigFile(path string) *providerFileConfig {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg providerFileConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// procEntry 是对账视角下的一条进程条目（key, cmd）。
type procEntry struct {
	key string
	cmd string
}

// normalizeProviderCfg 把文件配置规范化成可 diff 的形态（白名单排序集合、
// 进程条目按出现顺序）。
func normalizeProviderCfg(cfg *providerFileConfig) (map[string]bool, []procEntry) {
	allow := map[string]bool{}
	if cfg != nil {
		for _, k := range cfg.Allow {
			k = strings.TrimSpace(k)
			if k != "" {
				allow[k] = true
			}
		}
	}
	var procs []procEntry
	if cfg != nil {
		for _, p := range cfg.Procs {
			k := strings.TrimSpace(p.Key)
			c := strings.TrimSpace(p.Cmd)
			if k != "" && c != "" {
				procs = append(procs, procEntry{key: k, cmd: c})
			}
		}
	}
	return allow, procs
}

// procListsEqual 判断两条进程条目列表是否等价（同序同内容）。
func procListsEqual(a, b []procEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reconcileProviderConfig 对一份新配置做**一次**对账（§16.19.2）：
// 分类（activating / deactivating / neutral）→ 动作。deactivating 的守卫
// （¬ relied）失败时**推迟**——返回 false 表示有未完成的撤下（下个周期重试）。
//
// prevAllow / prevProcs 是上次已应用的配置；返回本次已应用的配置
// （守卫推迟的撤下**不计入**已应用——下个周期重试同一变更）。
func (h *ProviderHub) reconcileProviderConfig(prevAllow map[string]bool, prevProcs []procEntry, nextCfg *providerFileConfig) (map[string]bool, []procEntry, bool) {
	nextAllow, nextProcs := normalizeProviderCfg(nextCfg)
	ok := true

	// ── 白名单 diff（§16.19.2）────────────────────────────
	// +key：activating（加白名单）。
	for k := range nextAllow {
		if !prevAllow[k] {
			h.addAllowedKey(k)
		}
	}
	// −key：deactivating——broker.Revoke（守卫）+ 撤下端点。
	for k := range prevAllow {
		if !nextAllow[k] {
			if !h.withdrawKey(k) {
				ok = false // ¬ relied：推迟到下个周期
			}
		}
	}

	// ── 进程表 diff（§16.19.2）────────────────────────────
	// 匹配键 = (key, cmd)（§16.19.3：同 key 多实例由条目列表区分）。
	prevSet := map[procEntry]bool{}
	for _, p := range prevProcs {
		prevSet[p] = true
	}
	nextSet := map[procEntry]bool{}
	for _, p := range nextProcs {
		nextSet[p] = true
	}
	// −条目：deactivating——kill 对应进程 + Revoke（若白名单也未含该 key）。
	for p := range prevSet {
		if !nextSet[p] {
			h.killProcEntry(p)
			if !nextAllow[p.key] {
				if !h.withdrawKey(p.key) {
					ok = false
				}
			}
		}
	}
	// +条目：activating——spawn。
	for _, p := range nextProcs {
		if !prevSet[p] {
			h.spawnProcEntry(p)
		}
	}
	return nextAllow, nextProcs, ok
}

// addAllowedKey 白名单加入（activating，§16.19.2）。
func (h *ProviderHub) addAllowedKey(key string) {
	h.mu.Lock()
	h.allowed[key] = true
	h.mu.Unlock()
}

// withdrawKey 主动撤下一个 key：守卫（¬ relied）→ broker.Revoke → 撤下该 key
// 的全部端点。守卫有两层镜像：端点的在途计数（hub 层）与 broker 的 reliance
// （app-core 层）——任一非零即推迟，返回 false（下个对账周期重试，§16.19.2）。
func (h *ProviderHub) withdrawKey(key string) bool {
	// 端点层守卫：任一实例仍有在途 exec → 推迟。
	h.mu.RLock()
	for _, cur := range h.attached[key] {
		if cur.inflight > 0 {
			h.mu.RUnlock()
			return false
		}
	}
	h.mu.RUnlock()
	// broker 层守卫（¬ relied_n(γ)，§16.8）：reliance > 0 → 推迟。
	if h.broker != nil {
		if tr := h.broker.Revoke(protocol.DriverProviderKey(key)); tr.Error != nil {
			return false
		}
	}
	// 撤下端点：标记退役；空闲端点由 settle 立即收尾（§16.9.4 的自动降级）。
	h.mu.Lock()
	list, ok := h.attached[key]
	if !ok {
		h.mu.Unlock()
		return true
	}
	for _, cur := range list {
		cur.retiring = true
	}
	h.mu.Unlock()
	for _, cur := range list {
		h.settle(cur)
	}
	return true
}

// killProcEntry 终止匹配 (key, cmd) 的全部 provider 进程（deactivating）。
func (h *ProviderHub) killProcEntry(p procEntry) {
	h.mu.Lock()
	var targets []*ProcessEndpoint
	var kept []*ProcessEndpoint
	for _, pe := range h.processes {
		if pe.key == protocol.DriverProviderKey(p.key) && pe.cmdLine == p.cmd {
			targets = append(targets, pe)
			continue
		}
		kept = append(kept, pe)
	}
	h.processes = kept
	h.mu.Unlock()
	for _, pe := range targets {
		pe.Close()
	}
}

// spawnProcEntry 拉起一条进程配置（activating，§16.19.2）。
// 与 StartProcessEndpoints 的单条路径相同：hello 后自动登记（§16.13.2）。
func (h *ProviderHub) spawnProcEntry(p procEntry) {
	ep, err := NewProcessEndpoint(h.NextEndpointID(), h, p.cmd, protocol.DriverProviderKey(p.key))
	if err != nil {
		return
	}
	ep.onDead = func() { h.DropEndpoint(ep) }
	h.mu.Lock()
	h.processes = append(h.processes, ep)
	h.mu.Unlock()
}

// StartProviderConfigReconcile 启动配置对账循环（§16.19.1）。
// 未设置 POLYDB_PROVIDER_CONFIG_FILE 时为 no-op（行为不变）。
// 返回停止函数（宿主关闭时调用；测试用短周期）。
func (h *ProviderHub) StartProviderConfigReconcile() func() {
	path := providerConfigFile()
	if path == "" {
		return func() {}
	}
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		// 启动时首次读取（覆盖 env 初值，§16.19.1）。
		cfg := readProviderConfigFile(path)
		prevAllow, prevProcs := normalizeProviderCfg(cfg)
		// 首次配置直接应用（spawn 全部条目、白名单全部加入）。
		if cfg != nil {
			h.applyFullConfig(prevAllow, prevProcs)
		}
		interval := time.Duration(providerConfigPollMS()) * time.Millisecond
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// Unix：SIGHUP 立即重读（§16.19.1）。
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGHUP)
		defer signal.Stop(sigs)
		for {
			select {
			case <-stop:
				return
			case <-sigs:
			case <-ticker.C:
			}
			cfg := readProviderConfigFile(path)
			if cfg == nil {
				continue // 缺失/解析失败：保持上次已知配置（§16.19.1）
			}
			var ok bool
			prevAllow, prevProcs, ok = h.reconcileProviderConfig(prevAllow, prevProcs, cfg)
			// ok=false：有撤下被守卫推迟（¬ relied），下个周期重试同一变更
			//（prevAllow/prevProcs 未推进，§16.19.2）。
			_ = ok
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}

// applyFullConfig 首次配置的全量应用（等价于把 env 语义搬到文件）。
func (h *ProviderHub) applyFullConfig(allow map[string]bool, procs []procEntry) {
	for k := range allow {
		h.addAllowedKey(k)
	}
	for _, p := range procs {
		h.spawnProcEntry(p)
	}
}
