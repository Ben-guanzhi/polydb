package contract

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// collectBrokerFramesUntil 收集帧直到 wanted 里的每个 type 都出现。
// 阶段 4 起 provider 会话的 socket 上会混入 provider_exec / provider_cancel
// 等控制帧（宿主把转发与 compensation 发给 attached 会话），且宿主对 A/B 两
// 条连接的处理并发、帧序不保证——按 type 归集并跳过无关帧，而不是按帧数读。
func collectBrokerFramesUntil(t *testing.T, w *wsConn, wanted ...string) map[string]map[string]any {
	t.Helper()
	got := map[string]map[string]any{}
	want := map[string]bool{}
	for _, k := range wanted {
		want[k] = true
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < len(wanted) {
		_ = w.c.SetReadDeadline(time.Now().Add(time.Until(deadline)))
		_, data, err := w.c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v (got %v, want %v)", err, got, wanted)
		}
		var m map[string]any
		if err := msgpack.Unmarshal(data, &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		typ, _ := m["type"].(string)
		if want[typ] {
			got[typ] = m
		}
	}
	return got
}

// asInt64 把 msgpack 解码出来的任意整数类型收敛成 int64（编码器差异）。
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return -1
	}
}

// TestContractBrokerQueryGuard 覆盖 behavior.md §16.8 的查询绑定：
// 能力校验（interception 生效点）、未注册 key 拒绝、在途查询期间
// ¬ relied_n(γ) 守卫拒绝撤回、查询完成后可撤回、会话断开自动回收。
// 全部双端 parity。
func TestContractBrokerQueryGuard(t *testing.T) {
	// provider attach 是 deny-by-default（§16.10.1）：守卫场景要 attach 才能
	// 走转发路径，故显式授权该 key。
	bs := backendsWithPolicy(t, "", []string{"contract:reliance"})
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			brokerCapabilityEnforced(t, b)
			brokerRelianceGuardOnInFlightQuery(t, b)
			brokerSessionUnloadRevokesOwnKeys(t, b)
		})
	}
}

// brokerCapabilityEnforced：未注册 key → DRIVER_NOT_AVAILABLE；
// 注册但衰减掉 query 能力 → PERMISSION_DENIED。两种都必须在 QueryStarted
// 之前拒绝，且不进 in_flight（下一帧就是 query_error）。
func brokerCapabilityEnforced(t *testing.T, b *Backend) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	w := wsDial(t, b.base)
	defer w.Close()

	const key = "contract:capability"
	w.send(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	drainBrokerFrames(t, w, 2)

	assertQueryRejected := func(wantCode string) {
		t.Helper()
		w.send(map[string]any{
			"type":          "query",
			"query_id":      "11111111-2222-3333-4444-555555555555",
			"connection_id": id,
			"sql":           "SELECT 1 AS x",
			"provider_key":  key,
		})
		typ, msg := w.recv(3 * time.Second)
		if typ != "query_error" {
			t.Fatalf("want query_error, got %q: %v", typ, msg)
		}
		err, ok := msg["error"].(map[string]any)
		if !ok {
			t.Fatalf("query_error missing error: %v", msg)
		}
		if c, _ := err["code"].(string); c != wantCode {
			t.Fatalf("error code = %q, want %q: %v", c, wantCode, err)
		}
	}

	// 未注册 key。
	w.send(map[string]any{
		"type":          "query",
		"query_id":      "22222222-3333-4444-5555-666666666666",
		"connection_id": id,
		"sql":           "SELECT 1 AS x",
		"provider_key":  "contract:not-registered",
	})
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("want query_error, got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if c, _ := err["code"].(string); c != "POLYDB_ERR_DRIVER_NOT_AVAILABLE" {
		t.Fatalf("unregistered key code = %q: %v", c, err)
	}

	// 把能力衰减到只剩 metadata（§16.4 interception，neutral、不 reload）。
	// 注意：set_granted 没有 wire 消息（阶段 2 只在 broker 骨架里），
	// 因此这里通过重新注册一份只声明 metadata 的 provider 来等效构造。
	// 先撤回原 provider，再注册同名但无 query 能力的版本。
	w.send(map[string]any{"type": "broker_revoke", "key": key})
	drainBrokerFrames(t, w, 2)
	w.send(map[string]any{
		"type": "broker_register",
		"provider": map[string]any{
			"key":              key,
			"kind":             "duckdb",
			"protocol_version": 1,
			"capabilities":     []string{"metadata"},
		},
	})
	drainBrokerFrames(t, w, 2)
	assertQueryRejected("POLYDB_ERR_PERMISSION_DENIED")

	// 收尾。
	w.send(map[string]any{"type": "broker_revoke", "key": key})
	drainBrokerFrames(t, w, 2)
	_ = id
}

// brokerRelianceGuardOnInFlightQuery：连接 A 发慢查询并绑定 provider，
// 连接 B 在查询在途期间 revoke → 守卫拒绝（DRIVER_NOT_AVAILABLE）；
// 查询完成后再次 revoke → 成功。论文 Thm 70 的双端验证。
func brokerRelianceGuardOnInFlightQuery(t *testing.T, b *Backend) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	wA := wsDial(t, b.base)
	defer wA.Close()
	wB := wsDial(t, b.base)
	defer wB.Close()

	const key = "contract:reliance"
	// 阶段 4：provider_key 查询要求 attach（§16.9.2 step 2）。B 把自己登记为
	// 执行端——转发在途期间 reliance 同样 ≥1，正是 §16.9.2 step 1「reliance
	// 覆盖整个在途期，含等待 provider 结果」的验证。
	wB.send(map[string]any{"type": "provider_attach", "key": key})
	wB.send(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	collectBrokerFramesUntil(t, wB, "provider_ack", "broker_state", "broker_event")

	// 慢查询：递归 CTE 给在途窗口；带 provider_key 触发 acquire。
	const qid = "33333333-4444-5555-6666-777777777777"
	wA.send(map[string]any{
		"type":          "query",
		"query_id":      qid,
		"connection_id": id,
		// 1 亿递归 CTE 保证查询真的在跑；timeout_ms 让它 500ms 后必然结束，
		// 避免 Go/modernc 上跑满 20s 读超时，同时给 B 的 revoke 留出稳定
		// 在途窗口。timeout 也是 §16.8 要求 release 覆盖的出口之一。
		"sql":          "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 100000000) SELECT count(*) FROM n",
		"timeout_ms":   500,
		"provider_key": key,
	})
	typ, msg := wA.recv(3 * time.Second)
	if typ != "query_started" {
		t.Fatalf("want query_started, got %q: %v", typ, msg)
	}
	// 窥视：重复注册同 key（失败但仍回 broker_state 终态视图，不产生转移），
	// 确认 A 的查询确实已 acquire（reliance >= 1）。若这里是 0，说明
	// provider_key 没被解码或绑定未接线，后续守卫断言就没有意义。
	// B 的 socket 上可能混入宿主转发的 provider_exec，按 type 归集跳过。
	wB.send(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	peek := collectBrokerFramesUntil(t, wB, "broker_state", "broker_event")
	peekState := peek["broker_state"]
	peekProviders, _ := peekState["providers"].([]any)
	if len(peekProviders) != 1 {
		t.Fatalf("peek: want 1 provider, got %v", peekState)
	}
	pp, _ := peekProviders[0].(map[string]any)
	// msgpack 解码到 map[string]any 时整数类型随编码器而异（Go 常给 int8/
	// uint64，Rust rmp-serde 给 u64/int64），不能硬断言某一个具体类型。
	if rel := asInt64(pp["reliance"]); rel < 1 {
		t.Fatalf("query did not acquire reliance (got %v): %v", rel, pp)
	}

	// B 立刻 revoke：reliance=1，¬ relied_n(γ) 必须拒绝。
	wB.send(map[string]any{"type": "broker_revoke", "key": key})
	frames := collectBrokerFramesUntil(t, wB, "broker_state", "broker_event")
	st := frames["broker_state"]
	if st == nil {
		t.Fatalf("missing broker_state: %v", frames)
	}
	err, ok := st["error"].(map[string]any)
	if !ok {
		t.Fatalf("guard must put error in broker_state.error: %v", st)
	}
	if c, _ := err["code"].(string); c != "POLYDB_ERR_DRIVER_NOT_AVAILABLE" {
		t.Fatalf("guard error code = %q, want DRIVER_NOT_AVAILABLE: %v", c, err)
	}
	ev := frames["broker_event"]
	if e, _ := ev["event"].(string); e != "deactivating" {
		t.Fatalf("guard event = %q, want deactivating: %v", e, ev)
	}
	if r, _ := ev["reason"].(string); r == "" {
		t.Fatalf("guard must carry reason: %v", ev)
	}
	// 视图仍有 1 个 provider（失败不产生转移）。
	providers, _ := st["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("guard rejection must not change view: %v", st)
	}

	// A 的查询在 timeout 出口结束（§16.8 要求 release 覆盖的出口之一；
	// 阶段 4 下 forwarding 在途同样被 timeout 扣住并补偿）。
	typ, msg = wA.recv(10 * time.Second)
	if typ != "query_error" {
		t.Fatalf("want query_error(TIMEOUT), got %q: %v", typ, msg)
	}
	if e, ok := msg["error"].(map[string]any); ok {
		if c, _ := e["code"].(string); c != "POLYDB_ERR_TIMEOUT" {
			t.Fatalf("error code = %q, want POLYDB_ERR_TIMEOUT: %v", c, e)
		}
	}

	// 查询已 release → B 再次 revoke 应成功。
	wB.send(map[string]any{"type": "broker_revoke", "key": key})
	frames = collectBrokerFramesUntil(t, wB, "broker_state", "broker_event")
	assertBrokerState(t, frames, 0)
	assertBrokerEvent(t, frames, "deactivating", key)
}

// brokerSessionUnloadRevokesOwnKeys：连接 A 注册 keyA 后关闭，
// 连接 B 注册 keyB 并查询 A 的 key 是否已被自动回收（Thm 68：
// 只撤自己的，不撤别人的）。
func brokerSessionUnloadRevokesOwnKeys(t *testing.T, b *Backend) {
	t.Helper()
	const keyA = "contract:unload-a"
	const keyB = "contract:unload-b"

	wA := wsDial(t, b.base)
	wA.send(map[string]any{"type": "broker_register", "provider": brokerProvider(keyA, 1)})
	drainBrokerFrames(t, wA, 2)
	wA.Close() // 触发会话卸载：逐个 revoke 本会话 keys

	wB := wsDial(t, b.base)
	defer wB.Close()
	wB.send(map[string]any{"type": "broker_register", "provider": brokerProvider(keyB, 1)})
	frames := drainBrokerFrames(t, wB, 2)
	assertBrokerEvent(t, frames, "activating", keyB)

	// wA.Close() 的服务端收尾是异步的：轮询窥视视图直到 A 的登记被回收，
	// 最多等 3 秒。窥视手段是重复注册 keyB（失败但仍回 broker_state 终态视图，
	// 不产生转移，Def 19），因此不会改变依赖图。
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := frames["broker_state"]
		providers, _ := st["providers"].([]any)
		if len(providers) == 1 {
			p, _ := providers[0].(map[string]any)
			if k, _ := p["key"].(string); k != keyB {
				t.Fatalf("unexpected surviving provider %q: %v", k, providers)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session unload did not revoke its own key: %v", st)
		}
		time.Sleep(20 * time.Millisecond)
		wB.send(map[string]any{"type": "broker_register", "provider": brokerProvider(keyB, 1)})
		frames = drainBrokerFrames(t, wB, 2)
	}

	wB.send(map[string]any{"type": "broker_revoke", "key": keyB})
	drainBrokerFrames(t, wB, 2)
}
