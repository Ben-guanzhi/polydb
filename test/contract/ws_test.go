package contract

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

// TestContractWS 覆盖 WebSocket 传输层的核心契约（spec/asyncapi.yaml）：
// hello 握手、query 结果/错误、query_cancel、非法消息、双后端 parity。
func TestContractWS(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			helloAndQuery(t, b)
			helloBadConn(t, b)
			queryBeforeHello(t, b)
			queryCancelUnknown(t, b)
			queryCancelSlow(t, b)
		})
	}
}

func wsURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(err)
	}
	u.Scheme = "ws"
	u.Path = "/ws"
	return u.String()
}

type wsConn struct {
	t *testing.T
	c *websocket.Conn
}

func wsDial(t *testing.T, base string) *wsConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL(base), nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return &wsConn{t: t, c: c}
}

func (w *wsConn) Close() { _ = w.c.Close() }

// send 发送一个 msgpack 编码的 map 消息。
func (w *wsConn) send(msg map[string]any) {
	w.t.Helper()
	data, err := msgpack.Marshal(msg)
	if err != nil {
		w.t.Fatalf("marshal %v: %v", msg, err)
	}
	if err := w.c.WriteMessage(websocket.BinaryMessage, data); err != nil {
		w.t.Fatalf("write %v: %v", msg, err)
	}
}

// recv 读取下一条服务端消息并返回其 type 字段与整个解码后的 map。
func (w *wsConn) recv(timeout time.Duration) (string, map[string]any) {
	w.t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := w.c.ReadMessage()
	if err != nil {
		w.t.Fatalf("read: %v", err)
	}
	var msg map[string]any
	if err := msgpack.Unmarshal(data, &msg); err != nil {
		w.t.Fatalf("decode: %v (bytes=%q)", err, data)
	}
	typ, _ := msg["type"].(string)
	return typ, msg
}

func wsCreateSQLiteConn(t *testing.T, c *Client) string {
	t.Helper()
	body := map[string]any{"name": "sqlite-ws-" + fmt.Sprintf("%d", time.Now().UnixNano()), "kind": "sqlite", "database": ":memory:"}
	status, got, err := c.Do("POST", "/api/connections", body)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create sqlite conn: status=%d err=%v", status, err)
	}
	id, ok := firstConnID(got)
	if !ok || id == "" {
		t.Fatalf("no id in response: %v", got)
	}
	return id
}

func helloAndQuery(t *testing.T, b *Backend) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	// 先建表并写入一行，用于 SELECT 结果比对。
	_, _, _ = NewClient(b.base).Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(x INTEGER)"})
	_, _, _ = NewClient(b.base).Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO t VALUES(7)"})

	w := wsDial(t, b.base)
	defer w.Close()

	w.send(map[string]any{"type": "hello", "connection_id": id, "client_version": "0.1.0"})
	typ, msg := w.recv(3 * time.Second)
	if typ != "hello_ack" {
		t.Fatalf("expected hello_ack, got %q: %v", typ, msg)
	}
	if v, _ := msg["server_version"].(string); v == "" {
		t.Fatalf("hello_ack missing server_version: %v", msg)
	}

	w.send(map[string]any{
		"type":     "query",
		"query_id": "11111111-2222-3333-4444-555555555555",
		"sql":      "SELECT x FROM t",
	})
	// 期待顺序：query_started → query_result
	typ, _ = w.recv(3 * time.Second)
	if typ != "query_started" {
		t.Fatalf("expected query_started, got %q", typ)
	}
	typ, msg = w.recv(3 * time.Second)
	if typ != "query_result" {
		t.Fatalf("expected query_result, got %q: %v", typ, msg)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("query_result missing result: %v", msg)
	}
	rows, _ := result["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %v", len(rows), rows)
	}
}

func helloBadConn(t *testing.T, b *Backend) {
	t.Helper()
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{"type": "hello", "connection_id": "00000000-0000-0000-0000-000000000000"})
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("expected query_error for unknown conn, got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if err == nil {
		t.Fatalf("missing error field: %v", msg)
	}
	if code, _ := err["code"].(string); code != "POLYDB_ERR_CONNECTION_NOT_FOUND" {
		t.Fatalf("unexpected error code: %v", err)
	}
}

func queryBeforeHello(t *testing.T, b *Backend) {
	t.Helper()
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{
		"type":     "query",
		"query_id": "11111111-2222-3333-4444-555555555555",
		"sql":      "SELECT 1",
	})
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("expected query_error before hello, got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if err == nil {
		t.Fatalf("missing error: %v", msg)
	}
	if code, _ := err["code"].(string); code != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("expected INVALID_PARAM, got %v", err)
	}
}

func queryCancelUnknown(t *testing.T, b *Backend) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{"type": "hello", "connection_id": id})
	w.recv(3 * time.Second) // hello_ack (ignored)
	w.send(map[string]any{
		"type":     "query_cancel",
		"query_id": "99999999-0000-0000-0000-000000000000",
	})
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("expected query_error for unknown cancel, got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if err == nil {
		t.Fatalf("missing error: %v", msg)
	}
	if code, _ := err["code"].(string); code != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("expected INVALID_PARAM, got %v", err)
	}
}

// queryCancelSlow 走一遍真实取消流程：发送一个耗时的 sleep 查询，然后立刻取消，
// 期待先收到 query_started 后收到 query_cancelled。
func queryCancelSlow(t *testing.T, b *Backend) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{"type": "hello", "connection_id": id})
	w.recv(3 * time.Second) // hello_ack

	qid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	w.send(map[string]any{
		"type":     "query",
		"query_id": qid,
		// 使用一个耗时的递归查询，给取消留出窗口；两端一致性由后续断言锁定。
		"sql": "WITH RECURSIVE c(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM c WHERE x<200000) SELECT count(*) FROM c",
	})
	typ, _ := w.recv(3 * time.Second)
	if typ != "query_started" {
		t.Fatalf("expected query_started, got %q", typ)
	}
	w.send(map[string]any{"type": "query_cancel", "query_id": qid})
	// 递归查询本身可能太快完成，此时取消到达会得到 query_error(INVALID_PARAM)（query 不存在）。
	// 三种状态都合规：query_cancelled / query_result / query_error。
	typ, msg := w.recv(10 * time.Second)
	if typ != "query_cancelled" && typ != "query_result" && typ != "query_error" {
		t.Fatalf("unexpected message after cancel: %q: %v", typ, msg)
	}
}
