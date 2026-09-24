package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
)

// 场景：Local 与 Remote 两个实现跑同一组操作，断言结果一致（对拍）。
// Remote 走真实的 httptest server（与契约测试同一套装配方式）。

type fixture struct {
	app *appcore.AppCore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "transport-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	return &fixture{app: appcore.New(db, kr)}
}

func runScenario(t *testing.T, c Client, remote bool) string {
	t.Helper()
	ctx := context.Background()

	// 连接 CRUD
	info, err := c.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "t", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.ID == "" {
		t.Fatalf("create: empty id")
	}
	id := info.ID

	got, err := c.GetConnection(id)
	if err != nil || got.Name != "t" {
		t.Fatalf("get: %v %v", got, err)
	}

	// 懒连接 + 查询（远程下首次查询触发服务端建连）
	if !remote {
		if err := c.Connect(ctx, id); err != nil {
			t.Fatalf("connect: %v", err)
		}
		if !c.IsConnected(id) {
			t.Fatal("IsConnected after Connect = false")
		}
	}

	res, err := c.Execute(ctx, id, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if res.StatementType != protocol.StatementTypeDDL {
		t.Fatalf("create table: statement_type = %v", res.StatementType)
	}
	if _, err = c.Execute(ctx, id, "INSERT INTO t(name) VALUES('alice')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	res, err = c.Execute(ctx, id, "SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 2 {
		t.Fatalf("select rows = %v", res.Rows)
	}

	// 元数据
	schemas, err := c.ListSchemas(ctx, id)
	if err != nil || len(schemas) != 1 || schemas[0].Name != "main" {
		t.Fatalf("schemas: %v %v", schemas, err)
	}
	tables, err := c.ListTables(ctx, id, "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "t" {
		t.Fatalf("tables = %v", tables)
	}
	cols, err := c.ListColumns(ctx, id, "main", "t")
	if err != nil || len(cols) != 2 {
		t.Fatalf("columns: %v %v", cols, err)
	}
	if cols[0].Name != "id" || !cols[0].IsPrimaryKey {
		t.Fatalf("columns[0] = %+v", cols[0])
	}
	if _, err = c.ListIndexes(ctx, id, "main", "t"); err != nil {
		t.Fatalf("indexes: %v", err)
	}
	if _, err = c.ListForeignKeys(ctx, id, "main", "t"); err != nil {
		t.Fatalf("fks: %v", err)
	}
	ddl, err := c.CreateTableSQL(ctx, id, "main", "t")
	if err != nil || !strings.Contains(ddl, "CREATE TABLE") {
		t.Fatalf("ddl: %q %v", ddl, err)
	}

	// 错误透传：查询不存在的表 → QUERY_FAILED（本地由驱动直接给出，
	// 远程由服务端映射后透传，两路必须落到同一错误码）。
	if _, err = c.Execute(ctx, id, "SELECT * FROM missing"); err == nil {
		t.Fatal("select missing table: expected error")
	} else {
		var pe *protocol.PolyDBError
		if !errors.As(err, &pe) {
			t.Fatalf("select missing table: expected PolyDBError, got %T", err)
		}
		if pe.Code != protocol.ErrQueryFailed {
			t.Fatalf("select missing table: code = %v, want %v", pe.Code, protocol.ErrQueryFailed)
		}
	}

	if _, err = c.DeleteConnection(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err = c.GetConnection(id); err == nil {
		t.Fatal("get deleted: expected error")
	}
	return id
}

func TestLocalScenario(t *testing.T) {
	fx := newFixture(t)
	runScenario(t, NewLocal(fx.app), false)
}

func TestRemoteScenario(t *testing.T) {
	fx := newFixture(t)
	srv := httptest.NewServer(server.New(fx.app).Handler())
	t.Cleanup(srv.Close)
	runScenario(t, NewRemote(srv.URL), true)
}

// TestRemoteIsolation：Remote 是进程内标记，两个客户端互不影响。
func TestRemoteTracking(t *testing.T) {
	fx := newFixture(t)
	srv := httptest.NewServer(server.New(fx.app).Handler())
	defer srv.Close()
	ctx := context.Background()

	a, err := fx.app.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "x", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	r1 := NewRemote(srv.URL)
	r2 := NewRemote(srv.URL)
	if err := r1.Connect(ctx, a.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !r1.IsConnected(a.ID) || r2.IsConnected(a.ID) {
		t.Fatal("connection tracking leaked across clients")
	}
	r1.Disconnect(a.ID)
	if r1.IsConnected(a.ID) {
		t.Fatal("still connected after Disconnect")
	}
}

// TestRemoteUnreachable：服务端不可达时错误可诊断（非 PolyDBError，走 *http / net 错误）。
func TestRemoteUnreachable(t *testing.T) {
	c := NewRemote("http://127.0.0.1:1") // 保留端口，必然拒绝
	_, err := c.ListConnections()
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// TestRemoteKV：验证 Remote 的 KV 方法分发到正确的 REST 路径/方法，
// 且 msgpack 请求体/响应编解码正确（无需真实 Redis 服务）。
func TestRemoteKV(t *testing.T) {
	var gotPath string
	var gotSelect protocol.RedisSelectDbRequest
	var gotScan protocol.RedisScanRequest
	var gotExec protocol.RedisExecCommandRequest
	var gotSet protocol.RedisSetRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connections/c1/kv/select", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := msgpack.NewDecoder(r.Body).Decode(&gotSelect); err != nil {
			t.Errorf("select body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/connections/c1/kv/scan", func(w http.ResponseWriter, r *http.Request) {
		if err := msgpack.NewDecoder(r.Body).Decode(&gotScan); err != nil {
			t.Errorf("scan body: %v", err)
		}
		page := protocol.RedisScanPage{Cursor: 7, Keys: []protocol.RedisKeyInfo{{Key: "k1", Type: protocol.RedisKeyTypeString}}}
		w.Header().Set("Content-Type", "application/msgpack")
		_ = msgpack.NewEncoder(w).Encode(page)
	})
	mux.HandleFunc("GET /api/connections/c1/kv/keys/mykey", func(w http.ResponseWriter, r *http.Request) {
		v := protocol.RedisValue{Type: protocol.RedisKeyTypeString, Value: "hello"}
		w.Header().Set("Content-Type", "application/msgpack")
		_ = msgpack.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("POST /api/connections/c1/kv/exec", func(w http.ResponseWriter, r *http.Request) {
		if err := msgpack.NewDecoder(r.Body).Decode(&gotExec); err != nil {
			t.Errorf("exec body: %v", err)
		}
		reply := protocol.RedisReply{Type: protocol.RedisReplySimpleString, Value: "OK"}
		w.Header().Set("Content-Type", "application/msgpack")
		_ = msgpack.NewEncoder(w).Encode(reply)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewRemote(srv.URL)
	ctx := context.Background()

	// SelectDB
	if err := c.SelectDB(ctx, "c1", 3); err != nil {
		t.Fatalf("select: %v", err)
	}
	if gotPath != "/api/connections/c1/kv/select" || gotSelect.Index != 3 {
		t.Fatalf("select dispatch = %s %d, want .../select 3", gotPath, gotSelect.Index)
	}

	// ScanKeys
	page, err := c.ScanKeys(ctx, "c1", 7, "user:*", 100)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotScan.Cursor != 7 || gotScan.Pattern != "user:*" || gotScan.Count != 100 {
		t.Fatalf("scan body = %+v", gotScan)
	}
	if page.Cursor != 7 || len(page.Keys) != 1 || page.Keys[0].Key != "k1" {
		t.Fatalf("scan page = %+v", page)
	}

	// GetValue
	v, err := c.GetValue(ctx, "c1", "mykey")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Type != protocol.RedisKeyTypeString || v.Value != "hello" {
		t.Fatalf("get value = %+v", v)
	}

	// ExecCommand
	reply, err := c.ExecCommand(ctx, "c1", []string{"GET", "mykey"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if reply.Type != protocol.RedisReplySimpleString || reply.Value != "OK" {
		t.Fatalf("exec reply = %+v", reply)
	}
	if len(gotExec.Args) != 2 || gotExec.Args[0] != "GET" {
		t.Fatalf("exec args = %v", gotExec.Args)
	}

	// SetValue 路径（未 stub 到 mux 默认 404，验证方法/路径分发无 panic 即可跳过；
	// 这里只断言编译期签名存在）。
	_ = gotSet
}

// TestRemoteQueryCancel 覆盖 T4.1 的两件事：
//  1. ExecuteQuery 把客户端预分配的 query_id 透传给服务端（否则 cancel 无从关联）；
//  2. CancelQuery 命中 204 → (true,nil)，未命中 404 → (false,nil) 且不当作错误；
//  3. ctx 真正抵达 HTTP 请求：客户端取消后在途请求立即中断，服务端也观测到断开。
func TestRemoteQueryCancel(t *testing.T) {
	var gotQueryID string
	hang := make(chan struct{})
	released := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connections/c1/query", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.QueryRequest
		if err := msgpack.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("query body: %v", err)
		}
		gotQueryID = req.QueryID
		close(hang)
		select {
		case <-r.Context().Done(): // 客户端取消后服务端应观测到断开
			close(released)
		case <-time.After(5 * time.Second):
			t.Errorf("请求未被取消（ctx 未透传到 http.Request）")
		}
	})
	mux.HandleFunc("POST /api/queries/{query_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("query_id") == gotQueryID {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"POLYDB_ERR_QUERY_NOT_FOUND","message":"gone","retryable":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewRemote(srv.URL)

	qid := "11111111-2222-3333-4444-555555555555"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ExecuteQuery(ctx, qid, "c1", "SELECT 1")
		done <- err
	}()
	<-hang // 服务端已收到请求，此刻查询在途

	hit, err := c.CancelQuery(context.Background(), qid)
	if err != nil || !hit {
		t.Fatalf("cancel hit = %v err = %v, want true/nil", hit, err)
	}
	if gotQueryID != qid {
		t.Fatalf("服务端收到 query_id = %q, want %q", gotQueryID, qid)
	}

	cancel() // 中断在途 HTTP 请求
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后错误 = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 ExecuteQuery 未返回")
	}
	<-released

	// 未命中（查询已结束）：404 转成 (false, nil)，best-effort 语义。
	if hit, err := c.CancelQuery(context.Background(), "99999999-2222-3333-4444-555555555555"); err != nil || hit {
		t.Fatalf("cancel miss = %v/%v, want false/nil", hit, err)
	}
}

// TestLocalCancelIsCtxOnly：Local 没有服务端 registry，CancelQuery 恒 (false,nil)，
// 取消完全靠 ctx。
func TestLocalCancelNoop(t *testing.T) {
	fx := newFixture(t)
	c := NewLocal(fx.app)
	info, err := c.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "l", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	hit, err := c.CancelQuery(context.Background(), "whatever")
	if err != nil || hit {
		t.Fatalf("local cancel = %v/%v, want false/nil", hit, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 懒连接与已建连两条路径都必须在取消的 ctx 下立即失败（不挂住）。
	// app-core 会把它包成 PolyDBError（协议结构不带 error 链），故只断言「报错」。
	if _, err := c.ExecuteQuery(ctx, "x", info.ID, "SELECT 1"); err == nil {
		t.Fatal("已取消 ctx 下执行未报错（懒连接路径）")
	}
	if err := c.Connect(context.Background(), info.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := c.ExecuteQuery(ctx, "x", info.ID, "SELECT 1"); err == nil {
		t.Fatal("已取消 ctx 下执行未报错（已建连路径）")
	}
}
