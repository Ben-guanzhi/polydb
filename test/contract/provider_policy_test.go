package contract

import (
	"testing"
	"time"
)

// TestContractProviderPolicy 覆盖 behavior.md §16.10 的 bridge 访问控制：
//   - attach 是 deny-by-default：未授权的 key 不可 attach（PERMISSION_DENIED）
//     且**不产生执行端**；
//   - 已授权的 key 可 attach；
//   - broker_set_granted 把生效能力收紧后，带该 key 的查询被拒
//     （PERMISSION_DENIED）；
//   - 放宽尝试（granted ⊄ declared）被拒（INVALID_PARAM）；
//   - 能力衰减属 neutral：不影响声明、不触发 reload。
//
// 双端 parity。
func TestContractProviderPolicy(t *testing.T) {
	// 后端 A：无任何授权 → attach 一律被拒。
	denyBackends := backendsWithPolicy(t, "", nil)
	for _, b := range denyBackends {
		b := b
		t.Run("deny/"+b.name, func(t *testing.T) {
			providerAttachDeniedByDefault(t, b)
		})
	}

	// 后端 B：授权两个 key → 授权内可用、未授权仍被拒。
	const allowedKey = "contract:policy-allowed"
	const deniedKey = "contract:policy-denied"
	allowBackends := backendsWithPolicy(t, "", []string{allowedKey})
	for _, b := range allowBackends {
		b := b
		t.Run("allow/"+b.name, func(t *testing.T) {
			providerAttachAllowed(t, b, allowedKey, deniedKey)
			providerSetGrantedTightens(t, b, allowedKey)
		})
	}
}

// providerAttachDeniedByDefault：未授权 key 的 attach 必须被拒，且不产生
// 执行端（随后带该 key 的查询也拿不到执行端）。
func providerAttachDeniedByDefault(t *testing.T, b *Backend) {
	t.Helper()
	const key = "contract:policy-denied"
	w := wsDial(t, b.base)
	defer w.Close()

	w.send(map[string]any{"type": "provider_attach", "key": key})
	typ, msg := w.recv(3 * time.Second)
	if typ != "provider_ack" {
		t.Fatalf("want provider_ack, got %q: %v", typ, msg)
	}
	err, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("attach must be refused with error: %v", msg)
	}
	if c, _ := err["code"].(string); c != "POLYDB_ERR_PERMISSION_DENIED" {
		t.Fatalf("error code = %q, want POLYDB_ERR_PERMISSION_DENIED: %v", c, err)
	}
	if e, _ := msg["event"].(string); e != "neutral" {
		t.Fatalf("refused attach event = %q, want neutral: %v", e, msg)
	}
	// 不得产生执行端：attached 列表为空。
	if attached, _ := msg["attached"].([]any); len(attached) != 0 {
		t.Fatalf("refused attach must not register an endpoint: %v", msg)
	}
}

// providerAttachAllowed：授权内 attach 成功；未授权的 key 仍被拒。
func providerAttachAllowed(t *testing.T, b *Backend, allowedKey, deniedKey string) {
	t.Helper()
	w := wsDial(t, b.base)
	defer w.Close()

	w.send(map[string]any{"type": "provider_attach", "key": allowedKey})
	typ, msg := w.recv(3 * time.Second)
	if typ != "provider_ack" {
		t.Fatalf("want provider_ack, got %q: %v", typ, msg)
	}
	if err, ok := msg["error"].(map[string]any); ok {
		t.Fatalf("allowed attach must not fail: %v", err)
	}
	if e, _ := msg["event"].(string); e != "activating" {
		t.Fatalf("event = %q, want activating: %v", e, msg)
	}
	attached, _ := msg["attached"].([]any)
	if len(attached) != 1 {
		t.Fatalf("want 1 attached endpoint, got %v", msg)
	}

	// 同一后端上，未授权的 key 仍被拒。
	w2 := wsDial(t, b.base)
	defer w2.Close()
	w2.send(map[string]any{"type": "provider_attach", "key": deniedKey})
	typ, msg = w2.recv(3 * time.Second)
	if typ != "provider_ack" {
		t.Fatalf("want provider_ack, got %q: %v", typ, msg)
	}
	err, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("unauthorized attach must be refused: %v", msg)
	}
	if c, _ := err["code"].(string); c != "POLYDB_ERR_PERMISSION_DENIED" {
		t.Fatalf("error code = %q, want PERMISSION_DENIED: %v", c, err)
	}
}

// providerSetGrantedTightens：broker_set_granted 把生效能力收紧后，
// 带该 key 的查询按新能力被拒（PERMISSION_DENIED）；放宽尝试被拒
// （INVALID_PARAM）；衰减本身是 neutral（不 reload）。
func providerSetGrantedTightens(t *testing.T, b *Backend, key string) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))

	// 注册 provider（声明 query/execute/metadata），并 attach。
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	drainBrokerFrames(t, w, 2)
	w.send(map[string]any{"type": "provider_attach", "key": key})
	typ, msg := w.recv(3 * time.Second)
	if typ != "provider_ack" {
		t.Fatalf("want provider_ack, got %q: %v", typ, msg)
	}
	if err, ok := msg["error"].(map[string]any); ok {
		t.Fatalf("attach failed: %v", err)
	}

	// 放宽尝试：granted 含未声明的 kv → 拒绝（INVALID_PARAM）。
	w.send(map[string]any{
		"type":                 "broker_set_granted",
		"key":                  key,
		"granted_capabilities": []string{"query", "kv"},
	})
	frames := collectBrokerFramesUntil(t, w, "broker_state", "broker_event")
	st := frames["broker_state"]
	errObj, ok := st["error"].(map[string]any)
	if !ok {
		t.Fatalf("widening attempt must fail: %v", st)
	}
	if c, _ := errObj["code"].(string); c != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("widening error code = %q, want INVALID_PARAM: %v", c, errObj)
	}

	// 收紧：granted = [metadata]（不含 query）→ 查询被拒。
	w.send(map[string]any{
		"type":                 "broker_set_granted",
		"key":                  key,
		"granted_capabilities": []string{"metadata"},
	})
	frames = collectBrokerFramesUntil(t, w, "broker_state", "broker_event")
	st = frames["broker_state"]
	if errObj, ok := st["error"].(map[string]any); ok {
		t.Fatalf("tightening must succeed: %v", errObj)
	}
	// 衰减属 neutral：event=neutral（不 reload、依赖图不动）。
	ev := frames["broker_event"]
	if e, _ := ev["event"].(string); e != "neutral" {
		t.Fatalf("tightening event = %q, want neutral: %v", e, ev)
	}
	// 视图里的 granted_capabilities 已收窄。
	providers, _ := st["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("want 1 provider in view: %v", st)
	}
	p, _ := providers[0].(map[string]any)
	granted, _ := p["granted_capabilities"].([]any)
	if len(granted) != 1 {
		t.Fatalf("granted_capabilities must be narrowed to 1: %v", p)
	}
	declared, _ := p["capabilities"].([]any)
	if len(declared) != 3 {
		t.Fatalf("declared capabilities must be unchanged: %v", p)
	}

	// 查询按新能力被拒（query 能力已被收走）。
	w.send(map[string]any{
		"type":          "query",
		"query_id":      "88888888-9999-aaaa-bbbb-cccccccccccc",
		"connection_id": id,
		"sql":           "SELECT 1",
		"provider_key":  key,
	})
	typ, msg = w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("want query_error after attenuation, got %q: %v", typ, msg)
	}
	qErr, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("query_error missing error: %v", msg)
	}
	if c, _ := qErr["code"].(string); c != "POLYDB_ERR_PERMISSION_DENIED" {
		t.Fatalf("query error code = %q, want PERMISSION_DENIED: %v", c, qErr)
	}
}
