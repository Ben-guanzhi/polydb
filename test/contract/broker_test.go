package contract

import (
	"testing"
	"time"
)

// TestContractBroker 覆盖 behavior.md §16 与 asyncapi.yaml 的进程外
// driver broker 消息：broker_register / broker_revoke / broker_state /
// broker_event，验证双端 parity、统一 2 帧响应、跨 WS 连接可见性，
// 以及失败不产生转移（Def 19 / 版本先探）。
func TestContractBroker(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			brokerRegisterRevokeAndVisibility(t, b)
			brokerFailuresAreNoTransitions(t, b)
			brokerMissingProviderRejects(t, b)
		})
	}
}

// brokerProvider 构造一份最小合法注册声明。
func brokerProvider(key string, version int) map[string]any {
	return map[string]any{
		"key":              key,
		"kind":             "duckdb",
		"protocol_version": version,
		"capabilities":     []string{"query", "execute", "metadata"},
	}
}

// drainBrokerFrames 收 n 帧并按 type 归集。统一 2 帧响应（§16.5）规定
// 每个请求恰好回 broker_state + broker_event；用归集而非固定帧序断言，
// 避免把「实现细节的发送顺序」误锁成契约。
func drainBrokerFrames(t *testing.T, w *wsConn, n int) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for i := 0; i < n; i++ {
		typ, msg := w.recv(3 * time.Second)
		out[typ] = msg
	}
	return out
}

func assertBrokerState(t *testing.T, frames map[string]map[string]any, wantProviders int) {
	t.Helper()
	st := frames["broker_state"]
	if st == nil {
		t.Fatalf("missing broker_state: %v", frames)
	}
	if err, ok := st["error"].(map[string]any); ok {
		t.Fatalf("broker_state has unexpected error: %v", err)
	}
	providers, _ := st["providers"].([]any)
	if len(providers) != wantProviders {
		t.Fatalf("want %d provider(s), got %d: %v", wantProviders, len(providers), st)
	}
}

func assertBrokerEvent(t *testing.T, frames map[string]map[string]any, wantEvent, wantKey string) {
	t.Helper()
	ev := frames["broker_event"]
	if ev == nil {
		t.Fatalf("missing broker_event: %v", frames)
	}
	if e, _ := ev["event"].(string); e != wantEvent {
		t.Fatalf("event = %q, want %q: %v", e, wantEvent, ev)
	}
	if k, _ := ev["key"].(string); k != wantKey {
		t.Fatalf("event key = %q, want %q: %v", k, wantKey, ev)
	}
	if r, ok := ev["reason"].(string); ok && r != "" {
		t.Fatalf("success must not carry reason: %v", ev)
	}
}

// brokerRegisterRevokeAndVisibility 验证注册/撤回与跨连接可见（§16.6：
// broker 是进程级视图，注册在连接 A 的状态对连接 B 可见）。
func brokerRegisterRevokeAndVisibility(t *testing.T, b *Backend) {
	t.Helper()
	const keyA = "contract:duckdb-a"
	const keyB = "contract:duckdb-b"

	w1 := wsDial(t, b.base)
	defer w1.Close()
	w2 := wsDial(t, b.base)
	defer w2.Close()

	// 连接 A 注册 keyA → 2 帧。
	w1.send(map[string]any{"type": "broker_register", "provider": brokerProvider(keyA, 1)})
	frames := drainBrokerFrames(t, w1, 2)
	assertBrokerState(t, frames, 1)
	assertBrokerEvent(t, frames, "activating", keyA)

	// 连接 B 注册 keyB → 它的 broker_state 必须已含 A 的登记（跨连接可见）。
	// 若 broker 是 per-session，这里只会看到 1 个 provider，测试即红。
	w2.send(map[string]any{"type": "broker_register", "provider": brokerProvider(keyB, 1)})
	frames = drainBrokerFrames(t, w2, 2)
	assertBrokerState(t, frames, 2)
	assertBrokerEvent(t, frames, "activating", keyB)
	st := frames["broker_state"]
	providers, _ := st["providers"].([]any)
	seen := map[string]bool{}
	for _, p := range providers {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("provider is not an object: %v", p)
		}
		k, _ := m["key"].(string)
		seen[k] = true
		// 声明与生效能力集合（§16.4）：初始未衰减，granted ⊆ capabilities。
		declared, _ := m["capabilities"].([]any)
		granted, _ := m["granted_capabilities"].([]any)
		if len(declared) != 3 || len(granted) != 3 {
			t.Fatalf("capabilities/granted_capabilities drift: %v", m)
		}
		if v, _ := m["reliance"].(int64); v != 0 {
			t.Fatalf("fresh provider reliance must be 0: %v", m)
		}
	}
	if !seen[keyA] || !seen[keyB] {
		t.Fatalf("broker view missing cross-connection registrations: %v", seen)
	}

	// 连接 A 撤回 keyA（在另一个连接上也能影响共享视图）。
	w1.send(map[string]any{"type": "broker_revoke", "key": keyA})
	frames = drainBrokerFrames(t, w1, 2)
	assertBrokerState(t, frames, 1)
	assertBrokerEvent(t, frames, "deactivating", keyA)

	// 连接 B 撤回 keyB → 终态为空（Thm 80：动态历史不留痕迹）。
	w2.send(map[string]any{"type": "broker_revoke", "key": keyB})
	frames = drainBrokerFrames(t, w2, 2)
	assertBrokerState(t, frames, 0)
	assertBrokerEvent(t, frames, "deactivating", keyB)
}

// brokerMissingProviderRejects 覆盖 behavior.md §16.5 登记的双实现不对称：
// 缺 provider 时 Go 回 broker_state(error)、Rust 回 query_error(decode)，
// 帧型不同但错误码都必须是 POLYDB_ERR_INVALID_PARAM，且都不产生转移。
// 因此这里只从任意一帧里找错误码，不锁帧型。
func brokerMissingProviderRejects(t *testing.T, b *Backend) {
	t.Helper()
	w := wsDial(t, b.base)
	defer w.Close()

	w.send(map[string]any{"type": "broker_register"}) // 故意缺 provider
	frames := drainBrokerFrames(t, w, 1)
	var code string
	for _, frame := range frames {
		if err, ok := frame["error"].(map[string]any); ok {
			code, _ = err["code"].(string)
		}
	}
	if code != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("missing-provider error code = %q, want POLYDB_ERR_INVALID_PARAM: %v", code, frames)
	}
}

// brokerFailuresAreNoTransitions 验证失败不产生转移（Def 19 / 版本先探）
// 且错误放在 broker_state.error、分类为 neutral、reason 非空（§16.5）。
func brokerFailuresAreNoTransitions(t *testing.T, b *Backend) {
	t.Helper()
	w := wsDial(t, b.base)
	defer w.Close()

	const key = "contract:failures"
	w.send(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	drainBrokerFrames(t, w, 2) // 消费成功 2 帧

	assertFailure := func(req map[string]any, wantCode string) {
		t.Helper()
		w.send(req)
		frames := drainBrokerFrames(t, w, 2)
		st := frames["broker_state"]
		if st == nil {
			t.Fatalf("missing broker_state: %v", frames)
		}
		// 失败不产生转移：视图仍是刚才那 1 个 provider。
		providers, _ := st["providers"].([]any)
		if len(providers) != 1 {
			t.Fatalf("failure must not change the view: %v", st)
		}
		err, ok := st["error"].(map[string]any)
		if !ok {
			t.Fatalf("broker_state.error missing on failure: %v", st)
		}
		if c, _ := err["code"].(string); c != wantCode {
			t.Fatalf("error code = %q, want %q: %v", c, wantCode, err)
		}
		ev := frames["broker_event"]
		if ev == nil {
			t.Fatalf("missing broker_event: %v", frames)
		}
		if e, _ := ev["event"].(string); e != "neutral" {
			t.Fatalf("failure event = %q, want neutral: %v", e, ev)
		}
		if r, _ := ev["reason"].(string); r == "" {
			t.Fatalf("failure must carry reason: %v", ev)
		}
	}

	// 重复注册（cannot provide twice）。
	assertFailure(map[string]any{
		"type": "broker_register", "provider": brokerProvider(key, 1),
	}, "POLYDB_ERR_INVALID_PARAM")
	// 版本不符（先探版本、再解引用）。
	assertFailure(map[string]any{
		"type": "broker_register", "provider": brokerProvider("contract:version", 999),
	}, "POLYDB_ERR_INVALID_PARAM")
	// 撤回未登记 key（revoke absent）。
	assertFailure(map[string]any{
		"type": "broker_revoke", "key": "contract:missing",
	}, "POLYDB_ERR_INVALID_PARAM")

	// 收尾：撤回最初登记的 key，恢复空视图。
	w.send(map[string]any{"type": "broker_revoke", "key": key})
	frames := drainBrokerFrames(t, w, 2)
	assertBrokerState(t, frames, 0)
	assertBrokerEvent(t, frames, "deactivating", key)
}
