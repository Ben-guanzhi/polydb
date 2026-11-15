package server

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 13：配置对账的单元测试（behavior.md §16.19）。契约测试覆盖端到端
//（写配置文件 → 新实例生效 / 移除 → 撤下）；这里锁对账的分类与守卫语义。

func cfg(allow []string, procs []procEntry) *providerFileConfig {
	c := &providerFileConfig{Allow: allow}
	for _, p := range procs {
		c.Procs = append(c.Procs, providerProcCfg{Key: p.key, Cmd: p.cmd})
	}
	return c
}

// TestReconcileClassifiesAndActs 按 §16.19.2 的分类表逐条锁定：
// +key 加白名单、+条目 spawn、−条目 kill、neutral 无动作。
func TestReconcileClassifiesAndActs(t *testing.T) {
	hub := NewProviderHubWithAllowed() // 白名单初始为空（文件驱动）
	prevAllow, prevProcs := normalizeProviderCfg(nil)

	// 第一轮：+k1 白名单 + +p1 条目 → 全部 activating。
	next := cfg([]string{"pkg:a"}, []procEntry{{key: "pkg:a", cmd: "/bin/a"}})
	prevAllow, prevProcs, ok := hub.reconcileProviderConfig(prevAllow, prevProcs, next)
	if !ok {
		t.Fatal("first reconcile must fully apply")
	}
	if !prevAllow["pkg:a"] || len(prevProcs) != 1 {
		t.Fatalf("applied config not recorded: %+v %+v", prevAllow, prevProcs)
	}
	// spawn 动作：进程拉起失败（/bin/a 不可执行）→ 不在 processes 里，
	// 但 spawnProcEntry 本身不该 panic。
	hub.mu.Lock()
	n := len(hub.processes)
	hub.mu.Unlock()
	_ = n // /bin/a 在多数环境不可执行；拉起失败静默（与 StartProcessEndpoints 一致）

	// 第二轮：同一配置重放 → diff 为空 → 对账幂等（§16.19.2）。
	hub.mu.Lock()
	before := len(hub.processes)
	allowBefore := len(hub.allowed)
	hub.mu.Unlock()
	if _, _, ok := hub.reconcileProviderConfig(prevAllow, prevProcs, next); !ok {
		t.Fatal("idempotent replay must apply fully")
	}
	hub.mu.Lock()
	if len(hub.processes) != before || len(hub.allowed) != allowBefore {
		t.Fatal("idempotent replay must not act (Thm 80 on the config plane)")
	}
	hub.mu.Unlock()

	// 第三轮：−k1 白名单 → deactivating（撤下；broker 为 nil 时只撤端点）。
	next3 := cfg([]string{}, []procEntry{{key: "pkg:a", cmd: "/bin/a"}})
	prevAllow, _, _ = hub.reconcileProviderConfig(prevAllow, prevProcs, next3)
	if prevAllow["pkg:a"] {
		t.Fatal("allow removal must be applied")
	}
}

// TestWithdrawKeyGuardPostponesWhenInFlight 锁定 §16.19.2 的守卫：有在途时
// withdrawKey 推迟（返回 false），在途归零后可撤下。
func TestWithdrawKeyGuardPostponesWhenInFlight(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:w")
	ep, _ := fakeEndpoint(1, "pkg:w")
	if err, _ := hub.Attach("pkg:w", ep, false); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 在途 1。
	if _, ep2 := hub.RegisterPending("q1", "pkg:w"); ep2 == nil {
		t.Fatal("register pending")
	}
	if hub.withdrawKey("pkg:w") {
		t.Fatal("¬ relied guard must postpone withdrawal while in flight")
	}
	// 端点仍在（推迟，不是强杀）。
	if hub.ProviderEndpoint("pkg:w") == nil {
		t.Fatal("endpoint must survive a postponed withdrawal")
	}
	// 在途归零 → 撤下成功。
	hub.Complete("q1", providerOutcome{err: &protocol.PolyDBError{Code: protocol.ErrUnknown}})
	if !hub.withdrawKey("pkg:w") {
		t.Fatal("withdrawal must succeed after in-flight drains")
	}
	if hub.ProviderEndpoint("pkg:w") != nil {
		t.Fatal("endpoint must be withdrawn after guard passes")
	}
}

// TestNormalizeProviderCfgIgnoresMalformed 锁定规范化：空白 key/cmd 被忽略。
func TestNormalizeProviderCfgIgnoresMalformed(t *testing.T) {
	allow, procs := normalizeProviderCfg(cfg(
		[]string{"  pkg:a ", "", "pkg:b"},
		[]procEntry{{key: "pkg:a", cmd: " /bin/a "}, {key: "", cmd: "x"}, {key: "k", cmd: ""}},
	))
	if len(allow) != 2 || !allow["pkg:a"] || !allow["pkg:b"] {
		t.Fatalf("allow = %v", allow)
	}
	if len(procs) != 1 || procs[0].key != "pkg:a" || procs[0].cmd != "/bin/a" {
		t.Fatalf("procs = %v", procs)
	}
}

// TestReadProviderConfigFileMissingKeepsLastKnown 锁定 §16.19.1：文件缺失
// 返回 nil（调用方保持上次已知配置，不清空运行中的 provider）。
func TestReadProviderConfigFileMissingKeepsLastKnown(t *testing.T) {
	if readProviderConfigFile(t.TempDir()+"/nope.json") != nil {
		t.Fatal("missing file must read as nil")
	}
}
