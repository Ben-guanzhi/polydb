package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/storage"
)

// ─── 测试脚手架 ────────────────────────────────────────────

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "server-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	srv := httptest.NewServer(New(appcore.New(db, kr)).Handler())
	t.Cleanup(func() {
		srv.Close()
		db.Close()
	})
	return srv
}

// mpDo 发送 msgpack 请求并解码响应；body 为 nil 时不携带请求体。
// 返回状态码、解码后的响应体与响应头。
func mpDo(t *testing.T, srv *httptest.Server, method, path string, body any) (int, any, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := msgpack.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %v: %v", body, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/msgpack")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var out any
	if len(raw) > 0 {
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "msgpack") {
			if err := msgpack.Unmarshal(raw, &out); err != nil {
				t.Fatalf("decode msgpack: %v (bytes=%q)", err, raw)
			}
		} else if strings.Contains(ct, "json") {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("decode json: %v (bytes=%q)", err, raw)
			}
		}
	}
	return resp.StatusCode, out, resp.Header
}

// rawDo 发送请求并返回原始响应体（用于响应非 JSON 的场景）。
func rawDo(t *testing.T, srv *httptest.Server, method, path string, body []byte, ct string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, raw
}

func createSQLiteConn(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	status, got, _ := mpDo(t, srv, "POST", "/api/connections",
		map[string]any{"name": "srv-test", "kind": "sqlite", "database": ":memory:"})
	if status != http.StatusCreated {
		t.Fatalf("create connection: status=%d body=%v", status, got)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("create connection: unexpected body %v", got)
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("create connection: missing id: %v", got)
	}
	return id
}

func errCode(t *testing.T, out any) string {
	t.Helper()
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T: %v", out, out)
	}
	code, _ := m["code"].(string)
	return code
}

func rowCount(out any) int {
	m, ok := out.(map[string]any)
	if !ok {
		return -1
	}
	rows, _ := m["rows"].([]any)
	return len(rows)
}

// ─── 基础 ──────────────────────────────────────────────────

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	status, got, _ := mpDo(t, srv, "GET", "/api/health", nil)
	if status != http.StatusOK {
		t.Fatalf("health: status=%d body=%v", status, got)
	}
	m := got.(map[string]any)
	if m["status"] != "ok" {
		t.Fatalf("health: status field = %v", m["status"])
	}
}

func TestUnknownRoute(t *testing.T) {
	srv := newTestServer(t)
	// 未知路由：标准库 ServeMux 返回 404 纯文本（非 wire 契约，仅断言状态码）。
	status, _, _ := mpDo(t, srv, "GET", "/api/nonexistent", nil)
	if status != http.StatusNotFound {
		t.Fatalf("unknown route: status=%d", status)
	}
}

// ─── 连接 CRUD ─────────────────────────────────────────────

func TestConnectionLifecycle(t *testing.T) {
	srv := newTestServer(t)

	id := createSQLiteConn(t, srv)

	status, got, _ := mpDo(t, srv, "GET", "/api/connections/"+id, nil)
	if status != http.StatusOK {
		t.Fatalf("get connection: status=%d", status)
	}
	m := got.(map[string]any)
	if m["name"] != "srv-test" || m["kind"] != "sqlite" {
		t.Fatalf("get connection: unexpected fields %v", m)
	}
	// 红线：password 不随 ConnectionInfo 返回。
	if _, exists := m["password"]; exists {
		t.Fatalf("connection info must not contain password: %v", m)
	}

	status, got, _ = mpDo(t, srv, "PUT", "/api/connections/"+id,
		map[string]any{"name": "srv-test-renamed"})
	if status != http.StatusOK {
		t.Fatalf("update connection: status=%d body=%v", status, got)
	}

	_, got, _ = mpDo(t, srv, "GET", "/api/connections", nil)
	list, ok := got.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("list connections: expected 1, got %v", got)
	}

	status, _, _ = mpDo(t, srv, "DELETE", "/api/connections/"+id, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete connection: status=%d", status)
	}

	status, got, _ = mpDo(t, srv, "GET", "/api/connections/"+id, nil)
	if status != http.StatusNotFound || errCode(t, got) != "POLYDB_ERR_CONNECTION_NOT_FOUND" {
		t.Fatalf("get deleted: status=%d body=%v", status, got)
	}

	status, got, _ = mpDo(t, srv, "DELETE", "/api/connections/"+id, nil)
	if status != http.StatusNotFound || errCode(t, got) != "POLYDB_ERR_CONNECTION_NOT_FOUND" {
		t.Fatalf("delete deleted: status=%d body=%v", status, got)
	}
}

func TestInvalidConnectionID(t *testing.T) {
	srv := newTestServer(t)
	// 非法 UUID 的连接 id → 400 INVALID_PARAM（与 Rust 侧 parse_conn_id 对齐）。
	status, got, _ := mpDo(t, srv, "GET", "/api/connections/not-a-uuid", nil)
	if status != http.StatusBadRequest || errCode(t, got) != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("invalid id: status=%d body=%v", status, got)
	}
}

// ─── 查询 ──────────────────────────────────────────────────

func TestQueryFlow(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(v INTEGER)"})
	if status != http.StatusOK {
		t.Fatalf("create table: status=%d body=%v", status, got)
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO t VALUES(5), (6)"})
	if status != http.StatusOK {
		t.Fatalf("insert: status=%d body=%v", status, got)
	}
	if m := got.(map[string]any); m["affected_rows"] != int64(2) {
		t.Fatalf("insert affected_rows = %v", m["affected_rows"])
	}

	// query_id 回显：客户端提供时必须在 X-Query-ID 头返回。
	qid := "123e4567-e89b-12d3-a456-426614174000"
	status, got, hdr := mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT v FROM t WHERE v = ?1", "params": []any{int64(5)}, "query_id": qid})
	if status != http.StatusOK {
		t.Fatalf("select: status=%d body=%v", status, got)
	}
	if hdr.Get("X-Query-ID") != qid {
		t.Fatalf("X-Query-ID = %q, want %q", hdr.Get("X-Query-ID"), qid)
	}
	if got := rowCount(got); got != 1 {
		t.Fatalf("select rows = %d, want 1", got)
	}

	// 未提供 query_id 时服务端生成一个并回显。
	_, _, hdr = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT 1"})
	if hdr.Get("X-Query-ID") == "" {
		t.Fatalf("X-Query-ID missing for generated query id")
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT * FROM missing_table"})
	if status != http.StatusInternalServerError || errCode(t, got) != "POLYDB_ERR_QUERY_FAILED" {
		t.Fatalf("missing table: status=%d body=%v", status, got)
	}
}

func TestQueryMaxRows(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	_, _, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE seq(v INTEGER)"})
	seed := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		seed = append(seed, fmt.Sprintf("%d", i))
	}
	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO seq(v) VALUES(" + strings.Join(seed, "),(") + ")"})
	if status != http.StatusOK {
		t.Fatalf("seed rows: status=%d body=%v", status, got)
	}

	// max_rows=10：截断到 10 行，truncated=true，total_rows=25（behavior.md §5）。
	status, got, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT v FROM seq ORDER BY v", "max_rows": int64(10)})
	if status != http.StatusOK {
		t.Fatalf("max_rows query: status=%d", status)
	}
	m := got.(map[string]any)
	if rowCount(got) != 10 {
		t.Fatalf("max_rows=10: rows = %d, want 10", rowCount(got))
	}
	if m["truncated"] != true {
		t.Fatalf("max_rows=10: truncated = %v, want true", m["truncated"])
	}
	if m["total_rows"] != int64(25) {
		t.Fatalf("max_rows=10: total_rows = %v, want 25", m["total_rows"])
	}

	// max_rows=0：使用服务端默认（10000），25 行不截断。
	status, got, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT v FROM seq ORDER BY v", "max_rows": int64(0)})
	if status != http.StatusOK || rowCount(got) != 25 {
		t.Fatalf("max_rows=0: status=%d rows=%d", status, rowCount(got))
	}
	if m := got.(map[string]any); m["truncated"] != false {
		t.Fatalf("max_rows=0: truncated = %v, want false", m["truncated"])
	}
}

func TestQueryTimeout(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	// 超时（behavior.md §2.3）：慢查询 + timeout_ms=200 → 408 POLYDB_ERR_TIMEOUT。
	// 1e8 次递归在纯 Go sqlite 上远超 200ms，保证超时先于查询完成。
	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{
			"sql":        "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000) SELECT MAX(x) FROM c",
			"timeout_ms": int64(200),
		})
	if status != http.StatusRequestTimeout {
		t.Fatalf("timeout: status=%d body=%v", status, got)
	}
	if code := errCode(t, got); code != "POLYDB_ERR_TIMEOUT" {
		t.Fatalf("timeout: code = %q", code)
	}
	if m := got.(map[string]any); m["retryable"] != true {
		t.Fatalf("timeout: retryable = %v, want true (behavior.md §4)", m["retryable"])
	}
}

func TestBatchQuery(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	_, _, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(v INTEGER)"})

	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/query/batch",
		map[string]any{
			"statements": []any{
				map[string]any{"sql": "INSERT INTO t VALUES(1)"},
				map[string]any{"sql": "SELECT v FROM t"},
			},
			"stop_on_error": false,
		})
	if status != http.StatusOK {
		t.Fatalf("batch: status=%d body=%v", status, got)
	}
	results, _ := got.(map[string]any)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("batch: results len = %d", len(results))
	}

	// stop_on_error=true：第一个失败后停止。
	status, got, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query/batch",
		map[string]any{
			"statements": []any{
				map[string]any{"sql": "SELECT * FROM missing_table"},
				map[string]any{"sql": "SELECT 1"},
			},
			"stop_on_error": true,
		})
	if status != http.StatusOK {
		t.Fatalf("batch stop_on_error: status=%d", status)
	}
	results, _ = got.(map[string]any)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("batch stop_on_error: results len = %d, want 1", len(results))
	}
}

func TestCancelQuery(t *testing.T) {
	srv := newTestServer(t)

	status, got, _ := mpDo(t, srv, "POST", "/api/queries/99999999-1111-2222-3333-444444444444/cancel", nil)
	if status != http.StatusNotFound || errCode(t, got) != "POLYDB_ERR_QUERY_NOT_FOUND" {
		t.Fatalf("cancel unknown: status=%d body=%v", status, got)
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/queries/not-a-uuid/cancel", nil)
	if status != http.StatusBadRequest || errCode(t, got) != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("cancel bad uuid: status=%d body=%v", status, got)
	}
}

// ─── 事务 ──────────────────────────────────────────────────

func TestTransactions(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	_, _, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(v INTEGER)"})

	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/transactions", map[string]any{})
	if status != http.StatusCreated {
		t.Fatalf("begin: status=%d body=%v", status, got)
	}
	txnID, _ := got.(map[string]any)["id"].(string)
	if txnID == "" {
		t.Fatalf("begin: missing txn id: %v", got)
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/transactions/"+txnID+"/execute",
		map[string]any{"sql": "INSERT INTO t VALUES(1)"})
	if status != http.StatusOK {
		t.Fatalf("execute in tx: status=%d body=%v", status, got)
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/transactions/"+txnID+"/commit", nil)
	if status != http.StatusOK {
		t.Fatalf("commit: status=%d body=%v", status, got)
	}

	// 只成功 finalize 一次（spec §10.1）。
	status, got, _ = mpDo(t, srv, "POST", "/api/transactions/"+txnID+"/commit", nil)
	if status != http.StatusNotFound || errCode(t, got) != "POLYDB_ERR_TRANSACTION_NOT_FOUND" {
		t.Fatalf("second commit: status=%d body=%v", status, got)
	}

	status, got, _ = mpDo(t, srv, "POST", "/api/transactions/txn-nope/rollback", nil)
	if status != http.StatusNotFound || errCode(t, got) != "POLYDB_ERR_TRANSACTION_NOT_FOUND" {
		t.Fatalf("unknown txn: status=%d body=%v", status, got)
	}
}

// ─── 元数据 ────────────────────────────────────────────────

func TestMetadata(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	_, _, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE meta_t(a INTEGER PRIMARY KEY, b TEXT)"})

	status, got, _ := mpDo(t, srv, "GET", "/api/connections/"+id+"/schemas", nil)
	if status != http.StatusOK {
		t.Fatalf("schemas: status=%d", status)
	}
	if schemas, _ := got.([]any); len(schemas) == 0 {
		t.Fatalf("schemas empty: %v", got)
	}

	status, got, _ = mpDo(t, srv, "GET", "/api/connections/"+id+"/schemas/main/tables", nil)
	if status != http.StatusOK {
		t.Fatalf("tables: status=%d", status)
	}
	found := false
	for _, e := range got.([]any) {
		if m, ok := e.(map[string]any); ok && m["name"] == "meta_t" {
			found = true
		}
	}
	if !found {
		t.Fatalf("meta_t not in tables: %v", got)
	}

	status, got, _ = mpDo(t, srv, "GET", "/api/connections/"+id+"/schemas/main/tables/meta_t/ddl", nil)
	if status != http.StatusOK {
		t.Fatalf("ddl: status=%d", status)
	}
	if sql, _ := got.(map[string]any)["sql"].(string); !strings.Contains(sql, "CREATE TABLE") {
		t.Fatalf("ddl missing CREATE TABLE: %v", got)
	}
}

// ─── 编解码与能力降级 ──────────────────────────────────────

func TestDecodeGarbageBody(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)
	status, raw := rawDo(t, srv, "POST", "/api/connections/"+id+"/query",
		[]byte{0xff, 0x00}, "application/msgpack")
	if status != http.StatusInternalServerError {
		t.Fatalf("garbage body: status=%d body=%q", status, raw)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode error body: %v (body=%q)", err, raw)
	}
	if errCode(t, out) != "POLYDB_ERR_UNKNOWN" {
		t.Fatalf("garbage body: code = %v, want UNKNOWN", out)
	}
}

func TestKVNotSupportedOnSQLite(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)
	status, got, _ := mpDo(t, srv, "POST", "/api/connections/"+id+"/kv/scan",
		map[string]any{"cursor": int64(0), "count": int64(10)})
	if status != http.StatusNotImplemented || errCode(t, got) != "POLYDB_ERR_NOT_SUPPORTED" {
		t.Fatalf("kv on sqlite: status=%d body=%v", status, got)
	}
}

// ─── WebSocket ─────────────────────────────────────────────

type wsClient struct {
	t *testing.T
	c *websocket.Conn
}

func wsDial(t *testing.T, srv *httptest.Server) *wsClient {
	t.Helper()
	url := strings.Replace(srv.URL, "http", "ws", 1) + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return &wsClient{t: t, c: c}
}

func (w *wsClient) send(msg map[string]any) {
	w.t.Helper()
	data, err := msgpack.Marshal(msg)
	if err != nil {
		w.t.Fatalf("ws marshal: %v", err)
	}
	if err := w.c.WriteMessage(websocket.BinaryMessage, data); err != nil {
		w.t.Fatalf("ws write: %v", err)
	}
}

func (w *wsClient) recv(timeout time.Duration) (string, map[string]any) {
	w.t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := w.c.ReadMessage()
	if err != nil {
		w.t.Fatalf("ws read: %v", err)
	}
	var msg map[string]any
	if err := msgpack.Unmarshal(data, &msg); err != nil {
		w.t.Fatalf("ws decode: %v", err)
	}
	typ, _ := msg["type"].(string)
	return typ, msg
}

func (w *wsClient) close() { _ = w.c.Close() }

func TestWSHelloQuery(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)
	_, _, _ = mpDo(t, srv, "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(x INTEGER); INSERT INTO t VALUES(7)"})

	w := wsDial(t, srv)
	defer w.close()

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
	typ, _ = w.recv(3 * time.Second)
	if typ != "query_started" {
		t.Fatalf("expected query_started, got %q", typ)
	}
	typ, msg = w.recv(3 * time.Second)
	if typ != "query_result" {
		t.Fatalf("expected query_result, got %q: %v", typ, msg)
	}
	result, _ := msg["result"].(map[string]any)
	rows, _ := result["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %v", rows)
	}
}

func TestWSQueryBeforeHello(t *testing.T) {
	srv := newTestServer(t)
	w := wsDial(t, srv)
	defer w.close()
	w.send(map[string]any{"type": "query", "query_id": "11111111-2222-3333-4444-555555555555", "sql": "SELECT 1"})
	typ, msg := w.recv(3 * time.Second)
	if typ != "query_error" {
		t.Fatalf("expected query_error before hello, got %q: %v", typ, msg)
	}
	if err := msg["error"].(map[string]any); err["code"] != "POLYDB_ERR_INVALID_PARAM" {
		t.Fatalf("expected INVALID_PARAM, got %v", err)
	}
}
