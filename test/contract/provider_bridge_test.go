package contract

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

// TestContractProviderBridge 覆盖 behavior.md §16.9 的异步 SQL 转发契约：
// provider_attach/detach/result + provider_exec，参考 provider 是**同一进程内**
// 的第二条 WS 连接（attach 后收到 provider_exec，经 REST 用真 SQLite 执行，
// 再发 provider_result）。全部双端 parity。
//
// 三个已知的测试纪律（对应 §16.9.6）：
//  1. provider goroutine 不能用 t.Fatalf（runtime.Goexit 只对主 goroutine
//     生效，会挂死测试）——用返回 error 的 sendErr/recvErr；
//  2. provider 必须先 broker_register（CheckAndAcquire 要求 query 能力）
//     再 provider_attach（attach 本身不校验 broker 注册）；
//  3. 握手帧（provider_ack + broker_state + broker_event）同步读完后才起
//     执行循环，避免控制帧与 provider_exec 交织。
func TestContractProviderBridge(t *testing.T) {
	// provider attach 是 deny-by-default（behavior.md §16.10.1）：桥接场景需要
	// 显式授权这些 key。
	bs := backendsWithPolicy(t, "", []string{
		"contract:bridge-fwd",
		"contract:bridge-withhold",
		"contract:bridge-unload",
		"contract:bridge-roll",
	})
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			providerBridgeForwarding(t, b)
			providerBridgeWithhold(t, b)
			providerBridgeUnload(t, b)
			providerBridgeRollingUpdate(t, b)
		})
	}
}

// refProvider 是进程内参考 provider。
type refProvider struct {
	t         *testing.T
	w         *wsConn
	base      string
	connID    string
	done      chan struct{}
	attachAck map[string]any
}

// sendErr / recvErr 是 wsConn.send/recv 的返回 error 变体（provider goroutine
// 里禁止 t.Fatalf）。wsConn.c 同包可访问。
func (p *refProvider) sendErr(msg map[string]any) error {
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return err
	}
	return p.w.c.WriteMessage(websocket.BinaryMessage, data)
}

func (p *refProvider) recvErr(timeout time.Duration) (string, map[string]any, error) {
	_ = p.w.c.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := p.w.c.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	var m map[string]any
	if err := msgpack.Unmarshal(data, &m); err != nil {
		return "", nil, err
	}
	typ, _ := m["type"].(string)
	return typ, m, nil
}

// startRefProvider 完成 attach + broker_register 握手（同步读 3 帧），然后
// 起执行循环：provider_exec → REST 真 SQLite → provider_result。
func startRefProvider(t *testing.T, b *Backend, key, connID string) *refProvider {
	t.Helper()
	p := &refProvider{t: t, w: wsDial(t, b.base), base: b.base, connID: connID, done: make(chan struct{})}

	// attach（acquisition）→ provider_ack 单帧。
	p.sendErr(map[string]any{"type": "provider_attach", "key": key})
	// broker_register（CheckAndAcquire 要求 query 能力）→ 2 帧。
	p.sendErr(map[string]any{"type": "broker_register", "provider": brokerProvider(key, 1)})
	handshake := collectBrokerFramesUntil(t, p.w, "provider_ack", "broker_state", "broker_event")
	if _, ok := handshake["provider_ack"]; !ok {
		t.Fatalf("missing provider_ack in handshake: %v", handshake)
	}
	// 记下 attach 的回执，供 rolling update 场景断言。
	p.attachAck = handshake["provider_ack"]

	go func() {
		defer close(p.done)
		for {
			typ, msg, err := p.recvErr(30 * time.Second)
			if err != nil {
				return // 连接关闭（测试清理）即退出
			}
			if typ != "provider_exec" {
				continue // rolling update / 后续控制帧：跳过
			}
			qid, _ := msg["query_id"].(string)
			sql, _ := msg["sql"].(string)
			// 注意：provider_exec 无 params 时不要回传显式 null——
			// Rust 侧 QueryRequest.params: Vec<Value> 拒绝 null（expected a sequence）。
			reqBody := map[string]any{"sql": sql}
			if params, ok := msg["params"].([]any); ok && len(params) > 0 {
				reqBody["params"] = params
			}
			status, body, doErr := NewClient(p.base).Do(
				"POST", "/api/connections/"+p.connID+"/query", reqBody)
			res := map[string]any{"type": "provider_result", "query_id": qid}
			switch {
			case doErr == nil && status == http.StatusOK:
				res["result"] = body
			default:
				if body == nil {
					body = map[string]any{
						"code":    "POLYDB_ERR_QUERY_FAILED",
						"message": fmt.Sprintf("provider exec failed: status=%d err=%v", status, doErr),
					}
				}
				res["error"] = body
			}
			if err := p.sendErr(res); err != nil {
				return
			}
		}
	}()
	return p
}

func (p *refProvider) close() {
	_ = p.w.c.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

// consumerQuery 发一条带 provider_key 的查询并消费 QueryStarted。
func consumerQuery(t *testing.T, w *wsConn, id, qid, sql, key string, timeoutMs int) {
	t.Helper()
	body := map[string]any{
		"type":          "query",
		"query_id":      qid,
		"connection_id": id,
		"sql":           sql,
		"provider_key":  key,
	}
	if timeoutMs > 0 {
		body["timeout_ms"] = timeoutMs
	}
	w.send(body)
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_started" {
		t.Fatalf("want query_started, got %q: %v", typ, msg)
	}
}

// assertQueryError 断言下一帧是 query_error 且错误码匹配。
func assertQueryError(t *testing.T, w *wsConn, wantCode string) map[string]any {
	t.Helper()
	typ, msg := w.recv(5 * time.Second)
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
	return msg
}

// providerBridgeForwarding：正常转发（真 SQLite 结果）→ 未 attach 拒绝 →
// detach 后拒绝。
func providerBridgeForwarding(t *testing.T, b *Backend) {
	t.Helper()
	const key = "contract:bridge-fwd"
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	c := NewClient(b.base)
	_, _, _ = c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(x INTEGER)"})
	_, _, _ = c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO t VALUES(7)"})

	p := startRefProvider(t, b, key, id)
	defer p.close()

	w := wsDial(t, b.base)
	defer w.Close()

	// 未 attach 的 key（未注册）：DRIVER_NOT_AVAILABLE（不发 QueryStarted）。
	w.send(map[string]any{
		"type":          "query",
		"query_id":      "11111111-2222-3333-4444-555555555555",
		"connection_id": id,
		"sql":           "SELECT 1",
		"provider_key":  "contract:never-attached",
	})
	assertQueryError(t, w, "POLYDB_ERR_DRIVER_NOT_AVAILABLE")

	// 正常转发：SQL 由 provider 经 REST 在真 SQLite 上执行。
	consumerQuery(t, w, id, "22222222-3333-4444-5555-666666666666",
		"SELECT x FROM t", key, 0)
	typ, msg := w.recv(10 * time.Second)
	if typ != "query_result" {
		t.Fatalf("want query_result, got %q: %v", typ, msg)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("query_result missing result: %v", msg)
	}
	rows, _ := result["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %v", result)
	}
	row, _ := rows[0].([]any)
	if len(row) != 1 || fmt.Sprint(row[0]) != "7" {
		t.Fatalf("want row [7], got %v", rows)
	}

	// detach（acquisition 的逆）。provider_ack 帧由 provider goroutine 消费
	// （控制帧被 skip）——主测试与 provider goroutine 读同一条连接会竞态，
	// 因此这里不读 ack，改用「detach 后查询被拒」验证 detach 生效。
	p.sendErr(map[string]any{"type": "provider_detach", "key": key})
	deadline := time.Now().Add(3 * time.Second)
	for {
		w.send(map[string]any{
			"type":          "query",
			"query_id":      "33333333-4444-5555-6666-777777777777",
			"connection_id": id,
			"sql":           "SELECT 1",
			"provider_key":  key,
		})
		typ, msg := w.recv(3 * time.Second)
		if typ == "query_error" {
			if err, ok := msg["error"].(map[string]any); ok {
				if c, _ := err["code"].(string); c == "POLYDB_ERR_DRIVER_NOT_AVAILABLE" {
					break // detach 已生效
				}
				t.Fatalf("unexpected error after detach: %v", err)
			}
		}
		if typ == "query_result" && time.Now().After(deadline) {
			t.Fatalf("provider_detach never took effect: %v", msg)
		}
		if time.Now().After(deadline) {
			t.Fatalf("detach never took effect: last %q: %v", typ, msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// providerBridgeWithhold：cancel 与 timeout 的 withhold + compensation。
// 消费者拿到 query_cancelled / TIMEOUT，provider 迟到的结果被扣住。
func providerBridgeWithhold(t *testing.T, b *Backend) {
	t.Helper()
	const key = "contract:bridge-withhold"
	// 与 go/pkg/server/server_test.go 的 slowSQLiteCTE 同款：足够慢且可被取消。
	const slowSQL = "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 100000000) SELECT count(*) FROM n"
	id := wsCreateSQLiteConn(t, NewClient(b.base))

	p := startRefProvider(t, b, key, id)
	defer p.close()

	w := wsDial(t, b.base)
	defer w.Close()

	// cancel：慢查询在途 → query_cancel → query_cancelled（结果被扣住）。
	consumerQuery(t, w, id, "44444444-5555-6666-7777-888888888888",
		slowSQL, key, 0)
	w.send(map[string]any{"type": "query_cancel", "query_id": "44444444-5555-6666-7777-888888888888"})
	typ, msg := w.recv(10 * time.Second)
	if typ != "query_cancelled" {
		t.Fatalf("want query_cancelled (withheld result), got %q: %v", typ, msg)
	}

	// timeout：timeout_ms 到点 → TIMEOUT（同样扣住迟到的 provider_result）。
	consumerQuery(t, w, id, "55555555-6666-7777-8888-999999999999",
		slowSQL, key, 300)
	typ, msg = w.recv(10 * time.Second)
	if typ != "query_error" {
		t.Fatalf("want query_error(TIMEOUT), got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if c, _ := err["code"].(string); c != "POLYDB_ERR_TIMEOUT" {
		t.Fatalf("error code = %q, want POLYDB_ERR_TIMEOUT: %v", c, err)
	}
}

// providerBridgeRollingUpdate：§16.9.4 —— 新 provider 会话接管同一 key 后，
// 新 exec 路由到新会话；旧会话在途完成后自动降级（回 provider_ack
// deactivating），且新会话的登记保留。
func providerBridgeRollingUpdate(t *testing.T, b *Backend) {
	t.Helper()
	const key = "contract:bridge-roll"
	id := wsCreateSQLiteConn(t, NewClient(b.base))

	// 旧 provider 会话。
	old := startRefProvider(t, b, key, id)
	defer old.close()

	// 新 provider 会话接管同一 key（rolling update）。attach 回执已在
	// startRefProvider 内读取（握手帧按 type 归集，不假设帧序）。
	fresh := startRefProvider(t, b, key, id)
	defer fresh.close()

	ack := fresh.attachAck
	if e, _ := ack["event"].(string); e != "activating" {
		t.Fatalf("re-attach event = %q, want activating: %v", e, ack)
	}
	attached, _ := ack["attached"].([]any)
	if len(attached) != 1 {
		t.Fatalf("want 1 attached key, got %v", ack)
	}

	// 转发仍工作（走新会话）。
	w := wsDial(t, b.base)
	defer w.Close()
	consumerQuery(t, w, id, "88888888-9999-aaaa-bbbb-cccccccccccc",
		"SELECT 1 AS v", key, 0)
	typ, msg := w.recv(10 * time.Second)
	if typ != "query_result" {
		t.Fatalf("want query_result after rolling update, got %q: %v", typ, msg)
	}
}

// providerBridgeUnload：provider 会话断开 → 宿主自动 detach（§16.9.6）→
// 之后的查询被拒。断连收尾是异步的，轮询直至生效。
func providerBridgeUnload(t *testing.T, b *Backend) {
	t.Helper()
	const key = "contract:bridge-unload"
	id := wsCreateSQLiteConn(t, NewClient(b.base))

	p := startRefProvider(t, b, key, id)

	w := wsDial(t, b.base)
	defer w.Close()
	consumerQuery(t, w, id, "66666666-7777-8888-9999-aaaaaaaaaaaa",
		"SELECT 1", key, 0)
	if typ, msg := w.recv(10 * time.Second); typ != "query_result" {
		t.Fatalf("pre-close forwarding broke: %q: %v", typ, msg)
	}

	p.w.c.Close()
	<-p.done

	deadline := time.Now().Add(3 * time.Second)
	for {
		w.send(map[string]any{
			"type":          "query",
			"query_id":      "77777777-8888-9999-aaaa-bbbbbbbbbbbb",
			"connection_id": id,
			"sql":           "SELECT 1",
			"provider_key":  key,
		})
		typ, msg := w.recv(3 * time.Second)
		if typ == "query_error" {
			if err, ok := msg["error"].(map[string]any); ok {
				if c, _ := err["code"].(string); c == "POLYDB_ERR_DRIVER_NOT_AVAILABLE" {
					return // 断连自动 detach 已生效
				}
				t.Fatalf("unexpected error after unload: %v", err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session unload did not auto-detach: last %q: %v", typ, msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
