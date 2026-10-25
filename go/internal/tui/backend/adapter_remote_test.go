package backend_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// TestAdapterRemoteEndToEnd 证明合并后 TUI 的 Adapter 数据面在真实
// HTTP+msgpack+Bearer 远端路径上完整可用：建连 → 建表/插入 → 查询 →
// FetchTable 落 tuidata.Frame → 事务提交。适配器是唯一真相，本测试
// 不依赖任何 sqltui 直连驱动，符合 AGENTS.md 铁律 2。
func TestAdapterRemoteEndToEnd(t *testing.T) {
	srv := httptest.NewServer(
		server.NewWithToken(appcore.New(mustOpen(t, ":memory:"), mustKeyring(t)), "secret-token").Handler(),
	)
	t.Cleanup(srv.Close)

	cli := transport.NewRemote(srv.URL)
	cli.SetToken("secret-token")

	ctx := context.Background()
	info, err := cli.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "remote-sqlite",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if info.ID == "" {
		t.Fatal("create connection: empty id")
	}
	if err := cli.Connect(ctx, info.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cli.Disconnect(info.ID) })

	// 建表 + 插入（事务提交，验证 tx 数据面）。
	tx, err := cli.BeginTransaction(ctx, info.ID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := cli.ExecuteInTx(ctx, tx.ID, "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := cli.ExecuteInTx(ctx, tx.ID, "INSERT INTO users (id, name) VALUES (1, 'alice'), (2, 'bob')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := cli.CommitTransaction(ctx, tx.ID); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	// 适配器把 transport 结果映射成 tuidata.Frame。
	ad := backend.NewAdapter(cli, info)
	if got := ad.Kind(); got != "sqlite" {
		t.Fatalf("adapter kind = %q, want sqlite", got)
	}
	schemas, err := cli.ListSchemas(ctx, info.ID)
	if err != nil {
		t.Fatalf("list schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no schemas returned")
	}
	schema := schemas[0].Name
	frame, err := ad.FetchTable(schema, "users", 100)
	if err != nil {
		t.Fatalf("fetch table: %v", err)
	}
	if frame == nil || frame.NumRows() != 2 {
		t.Fatalf("frame rows = %v, want 2", frame)
	}
	if frame.NumCols() != 2 {
		t.Fatalf("frame cols = %d, want 2", frame.NumCols())
	}

	// PrimaryKeys 经 ListColumns 的 is_primary_key 推断。
	pks, err := ad.PrimaryKeys(schema, "users")
	if err != nil {
		t.Fatalf("primary keys: %v", err)
	}
	if len(pks) != 1 || pks[0] != "id" {
		t.Fatalf("primary keys = %v, want [id]", pks)
	}

	// Run 直查走 ExecuteQuery → tuidata.Frame。
	res, err := ad.Run("SELECT name FROM users WHERE id = 2")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Frame.NumRows() != 1 {
		t.Fatalf("run rows = %d, want 1", res.Frame.NumRows())
	}
	if got := res.Frame.CellString(0, 0); got != "bob" {
		t.Fatalf("run cell = %q, want bob", got)
	}

	// CancelQuery 在远端路径不报错（本地为 no-op，远端尽力而为）。
	if _, err := cli.CancelQuery(ctx, info.ID); err != nil {
		t.Fatalf("cancel query: %v", err)
	}
}

// TestAdapterRemoteUnauthorized 证明缺少/错误 Bearer token 被服务端拒绝，
// 数据面不会泄露给未认证调用方。
func TestAdapterRemoteUnauthorized(t *testing.T) {
	srv := httptest.NewServer(
		server.NewWithToken(appcore.New(mustOpen(t, ":memory:"), mustKeyring(t)), "secret-token").Handler(),
	)
	t.Cleanup(srv.Close)

	cli := transport.NewRemote(srv.URL)
	cli.SetToken("wrong-token")
	if _, err := cli.ListConnections(); err == nil {
		t.Fatal("expected auth error with wrong token, got nil")
	}
}

func mustOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustKeyring(t *testing.T) *keyring.FileKeyring {
	t.Helper()
	kr, err := keyring.NewFileKeyring(t.TempDir(), "backend-remote-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	return kr
}
