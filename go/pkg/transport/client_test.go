package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
