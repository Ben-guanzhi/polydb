package tui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// newLocalClient 组装一个跑在临时目录上的本机 transport.Client
// （真实 SQLite 存储 + app-core，与 cmd/polydb-tui 的本机模式同构）。
func newLocalClient(t *testing.T) transport.Client {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "polydb.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.Open(dir)
	if err != nil {
		t.Fatalf("keyring.Open: %v", err)
	}
	return transport.NewLocal(appcore.New(db, kr))
}

// TestLocalSmokeConnectBrowseEdit 端到端证明合并后的 TUI 数据面：
// 连接 CRUD → Adapter → 建表/插入/浏览/主键/编辑，全部经 transport。
func TestLocalSmokeConnectBrowseEdit(t *testing.T) {
	cli := newLocalClient(t)
	ctx := context.Background()

	// 连接列表初始为空；创建后可见。
	conns, err := cli.ListConnections()
	if err != nil || len(conns) != 0 {
		t.Fatalf("initial ListConnections = %v, %v", conns, err)
	}
	shop := filepath.Join(t.TempDir(), "shop.db")
	info, err := cli.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "shop", Kind: protocol.DatabaseKindSQLite, Database: shop,
	})
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if err := cli.Connect(ctx, info.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { cli.Disconnect(info.ID) })
	conns, err = cli.ListConnections()
	if err != nil || len(conns) != 1 {
		t.Fatalf("ListConnections after create = %v, %v", conns, err)
	}

	ad := backend.NewAdapter(cli, info)

	// DDL + 插入经 Adapter.Run。
	if _, err := ad.Run("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, score REAL)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if res, err := ad.Run("INSERT INTO users VALUES (1, 'alice', 9.5)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	} else if res.Exec == nil || res.Exec.RowsAffected != 1 {
		t.Fatalf("INSERT exec = %+v", res.Exec)
	}

	// 浏览：表列表、取行、主键、列元数据。
	tables, err := ad.Tables("")
	if err != nil || len(tables) != 1 || tables[0] != "users" {
		t.Fatalf("Tables = %v, %v", tables, err)
	}
	f, err := ad.FetchTable("", "users", 100)
	if err != nil {
		t.Fatalf("FetchTable: %v", err)
	}
	if f.NumRows() != 1 || f.CellString(0, 1) != "alice" {
		t.Fatalf("fetched frame = %v", f)
	}
	pks, err := ad.PrimaryKeys("", "users")
	if err != nil || len(pks) != 1 || pks[0] != "id" {
		t.Fatalf("PrimaryKeys = %v, %v", pks, err)
	}
	meta, err := ad.ColumnsMeta("", "users")
	if err != nil || len(meta) != 3 || meta[0].Name != "id" {
		t.Fatalf("ColumnsMeta = %+v, %v", meta, err)
	}

	// 查询：结果集经 Run 转 Frame。
	res, err := ad.Run("SELECT name, score FROM users WHERE id = 1")
	if err != nil || res.Frame == nil || res.Frame.NumRows() != 1 {
		t.Fatalf("SELECT = %+v, %v", res, err)
	}

	// celledit 的 PK-keyed UPDATE 路径（quoting 走 pkg/changes）。
	stmt, err := buildUpdate(protocol.DatabaseKindSQLite, "users", pks, f, 0, "name", "alice2")
	if err != nil {
		t.Fatalf("buildUpdate: %v", err)
	}
	if want := `UPDATE "users" SET "name"='alice2' WHERE "id"='1'`; stmt != want {
		t.Fatalf("UPDATE stmt = %q, want %q", stmt, want)
	}
	if _, err := ad.Run(stmt); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
}
