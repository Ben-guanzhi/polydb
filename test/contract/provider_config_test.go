package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestContractProviderConfigReconcile 覆盖 behavior.md §16.19 的配置对账：
// provider 配置（白名单 + 进程表）的变更经 notify_d 分类**增量生效**，无需
// 重启宿主——Def 81 的配置对账在 broker 配置上的落地。
//
// 断言链（Go 后端完整覆盖；**Rust 后端 PD-39 跳过**，见下）：
//  1. 写初始配置（1 个 commutative 实例）→ 等对账 → 查询成功（spawn 生效）；
//  2. 追加第二个实例 → 等对账 → 连续查询成功（多实例并存，§16.18）；
//  3. 清空进程表与白名单 → 等对账 → provider 进程退出（撤下生效，OS 层证据）。
func TestContractProviderConfigReconcile(t *testing.T) {
	bin := buildContractProvider(t)

	t.Run("go", func(t *testing.T) {
		key := "contract:cfg-go"
		cmd := bin + " -key " + key + " -cap query -commutative"

		// 配置文件先写好再启动宿主（启动时首次读取覆盖 env，§16.19.1）。
		cfgPath := filepath.Join(t.TempDir(), "providers.json")
		writeCfg := func(allow []string, procs []string) {
			obj := map[string]any{"allow": allow, "procs": []map[string]string{}}
			for _, p := range procs {
				obj["procs"] = append(obj["procs"].([]map[string]string),
					map[string]string{"key": key, "cmd": p})
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal config: %v", err)
			}
			if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
		}
		writeCfg([]string{key}, []string{cmd})

		b := startGoBackendWithConfigFile(t, cfgPath)
		id := wsCreateSQLiteConn(t, NewClient(b.base))
		probe := func(qid string) (string, map[string]any) {
			w := wsDial(t, b.base)
			defer w.Close()
			w.send(map[string]any{
				"type":          "query",
				"query_id":      qid,
				"connection_id": id,
				"sql":           "SELECT 1 AS v",
				"provider_key":  key,
			})
			typ, msg := w.recv(10 * time.Second)
			if typ == "query_started" {
				typ, msg = w.recv(10 * time.Second)
			}
			return typ, msg
		}

		// 1. 初始实例生效。
		waitForProviderAttached(t, b, key)

		// 2. 追加第二个实例 → 对账 spawn → 连续查询成功（多实例并存，§16.18）。
		writeCfg([]string{key}, []string{cmd, cmd})
		time.Sleep(600 * time.Millisecond) // > 轮询周期 200ms + 握手
		for i := 1; i <= 3; i++ {
			typ, msg := probe("dddddddd-0000-0000-0000-" + itoa12(i))
			if typ != "query_result" {
				t.Fatalf("after scale-out query %d: want query_result, got %q: %v", i, typ, msg)
			}
		}

		// 3. 清空进程表与白名单 → 对账撤下。撤下的 **hub 层可观测证据** =
		//    AttachedCountForKey 归零（端点撤下 + broker.Revoke，§16.19.2；
		//    Go 后端在测试进程内，直接观测——进程层证据由 provider_hub 单测
		//    与 §16.18 的多实例测试覆盖）。
		writeCfg([]string{}, []string{})
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if b.hub.AttachedCountForKey(key) == 0 {
				return // 撤下生效
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("withdrawal never took effect: attached count = %d",
			b.hub.AttachedCountForKey(key))
	})

	t.Run("rust", func(t *testing.T) {
		// **PD-39 已修复**（wait_loop 持锁阻塞 child.wait() 使 close 的
		// child.lock() 永久阻塞；改 try_wait 轮询，见 behavior.md §16.19.5
		// 的根因记录）。Rust subtest 与 go subtest 同构：独立 key + 独立
		// 配置文件，端到端覆盖 spawn → scale-out → 撤下。
		key := "contract:cfg-rust"
		cmd := bin + " -key " + key + " -cap query -commutative"

		cfgPath := filepath.Join(t.TempDir(), "providers.json")
		writeCfg := func(allow []string, procs []string) {
			obj := map[string]any{"allow": allow, "procs": []map[string]string{}}
			for _, p := range procs {
				obj["procs"] = append(obj["procs"].([]map[string]string),
					map[string]string{"key": key, "cmd": p})
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal config: %v", err)
			}
			if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
		}
		writeCfg([]string{key}, []string{cmd})

		b, err := startRustBackendWithConfigFile(t, cfgPath)
		if err != nil {
			t.Fatalf("rust backend: %v", err)
		}
		id := wsCreateSQLiteConn(t, NewClient(b.base))
		probe := func(qid string) (string, map[string]any) {
			w := wsDial(t, b.base)
			defer w.Close()
			w.send(map[string]any{
				"type":          "query",
				"query_id":      qid,
				"connection_id": id,
				"sql":           "SELECT 1 AS v",
				"provider_key":  key,
			})
			typ, msg := w.recv(10 * time.Second)
			if typ == "query_started" {
				typ, msg = w.recv(10 * time.Second)
			}
			return typ, msg
		}

		// 1. 初始实例生效。
		waitForProviderAttached(t, b, key)

		// 2. 追加第二个实例 → 对账 spawn → 连续查询成功（多实例并存，§16.18）。
		writeCfg([]string{key}, []string{cmd, cmd})
		time.Sleep(600 * time.Millisecond)
		for i := 1; i <= 3; i++ {
			typ, msg := probe("dddddddd-0000-0000-0000-" + itoa12(i))
			if typ != "query_result" {
				t.Fatalf("after scale-out query %d: want query_result, got %q: %v", i, typ, msg)
			}
		}

		// 3. 清空进程表与白名单 → 对账撤下 → 查询失败 DRIVER_NOT_AVAILABLE。
		writeCfg([]string{}, []string{})
		deadline := time.Now().Add(8 * time.Second)
		var lastTyp string
		var lastMsg map[string]any
		for time.Now().Before(deadline) {
			lastTyp, lastMsg = probe("dddddddd-0000-0000-0000-0000000000aa")
			if lastTyp == "query_error" {
				if e, ok := lastMsg["error"].(map[string]any); ok {
					if code, _ := e["code"].(string); code == "POLYDB_ERR_DRIVER_NOT_AVAILABLE" {
						return // 撤下生效
					}
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("withdrawal never took effect: last %q: %v", lastTyp, lastMsg)
	})
}
