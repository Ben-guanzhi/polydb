package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend/fake"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/protocol"
)

func testInfo() protocol.ConnectionInfo {
	return protocol.ConnectionInfo{
		ID: "c1", Name: "pg-main", Kind: protocol.DatabaseKindPostgres,
		Host: "db.internal", Port: 5432, Database: "shop", DefaultSchema: "public",
	}
}

func newTestAdapter(t *testing.T) (*Adapter, *fake.Client) {
	t.Helper()
	fc := &fake.Client{}
	return NewAdapter(fc, testInfo()), fc
}

func TestAdapterKindTitleNamespace(t *testing.T) {
	ad, fc := newTestAdapter(t)
	if ad.Kind() != "postgres" || ad.Title() != "pg-main" {
		t.Fatalf("kind/title = %q/%q", ad.Kind(), ad.Title())
	}
	if ad.ConnID() != "c1" || ad.CurrentNamespace() != "public" {
		t.Fatalf("connID/ns = %q/%q", ad.ConnID(), ad.CurrentNamespace())
	}
	fc.ListSchemasFunc = func(context.Context, string) ([]protocol.SchemaInfo, error) {
		return []protocol.SchemaInfo{{Name: "public"}, {Name: "sales"}}, nil
	}
	nss, err := ad.Namespaces()
	if err != nil || len(nss) != 2 || nss[0] != "public" {
		t.Fatalf("Namespaces = %v, %v", nss, err)
	}
	// 无 schema 的引擎退化为单个空命名空间。
	fc.ListSchemasFunc = func(context.Context, string) ([]protocol.SchemaInfo, error) { return nil, nil }
	nss, err = ad.Namespaces()
	if err != nil || len(nss) != 1 || nss[0] != "" {
		t.Fatalf("empty Namespaces = %v, %v", nss, err)
	}
}

func TestAdapterTablesAndPrimaryKeys(t *testing.T) {
	ad, fc := newTestAdapter(t)
	fc.ListTablesFunc = func(_ context.Context, _, schema string) ([]protocol.TableInfo, error) {
		if schema != "public" {
			t.Errorf("Tables schema = %q, want public", schema)
		}
		return []protocol.TableInfo{{Name: "users"}, {Name: "orders"}}, nil
	}
	tables, err := ad.Tables("public")
	if err != nil || len(tables) != 2 || tables[1] != "orders" {
		t.Fatalf("Tables = %v, %v", tables, err)
	}

	fc.ListColumnsFunc = func(_ context.Context, _, _, table string) ([]protocol.ColumnInfo, error) {
		if table != "users" {
			t.Errorf("Columns table = %q", table)
		}
		// 故意乱序返回，Adapter 必须按 OrdinalPosition 排序。
		return []protocol.ColumnInfo{
			{Name: "email", OrdinalPosition: 2},
			{Name: "id", IsPrimaryKey: true, OrdinalPosition: 1},
			{Name: "tenant_id", IsPrimaryKey: true, OrdinalPosition: 3},
		}, nil
	}
	pks, err := ad.PrimaryKeys("public", "users")
	if err != nil {
		t.Fatal(err)
	}
	if len(pks) != 2 || pks[0] != "id" || pks[1] != "tenant_id" {
		t.Fatalf("PrimaryKeys = %v, want [id tenant_id] in ordinal order", pks)
	}
}

func TestAdapterRunFrameAndExec(t *testing.T) {
	ad, fc := newTestAdapter(t)

	// 结果集：Execute 带列 → Frame。
	fc.ExecuteFunc = func(context.Context, string, string, ...protocol.Value) (*protocol.QueryResult, error) {
		return &protocol.QueryResult{
			Columns: []protocol.ResultColumn{
				{Name: "id", GenericType: protocol.GenericTypeInteger},
				{Name: "name", GenericType: protocol.GenericTypeText},
				{Name: "score", GenericType: protocol.GenericTypeFloat},
				{Name: "active", GenericType: protocol.GenericTypeBoolean},
			},
			Rows: [][]protocol.Value{
				{protocol.NewIntValue(1), protocol.NewStringValue("alice"), protocol.NewFloatValue(9.5), protocol.NewBoolValue(true)},
				{protocol.NewIntValue(2), protocol.NewStringValue("bob"), protocol.NewNullValue(), protocol.NewBoolValue(false)},
			},
		}, nil
	}
	res, err := ad.Run("SELECT * FROM users")
	if err != nil {
		t.Fatal(err)
	}
	f := res.Frame
	if f == nil || res.Exec != nil {
		t.Fatalf("Run result = %+v, want a frame", res)
	}
	if f.NumRows() != 2 || f.NumCols() != 4 {
		t.Fatalf("frame shape = %dx%d", f.NumRows(), f.NumCols())
	}
	if got := f.Cell(0, 0); got != int64(1) {
		t.Errorf("cell(0,0) = %#v, want int64(1)", got)
	}
	if got := f.Cell(0, 1); got != "alice" {
		t.Errorf("cell(0,1) = %#v, want alice", got)
	}
	if got := f.Cell(0, 2); got != 9.5 {
		t.Errorf("cell(0,2) = %#v, want 9.5", got)
	}
	if got := f.Cell(0, 3); got != true {
		t.Errorf("cell(0,3) = %#v, want true", got)
	}
	if got := f.Cell(1, 2); got != nil {
		t.Errorf("null cell = %#v, want nil", got)
	}
	// 列类型来自 GenericType 映射。
	if f.Columns[0].Type != tuidata.TypeInt || f.Columns[1].Type != tuidata.TypeString {
		t.Errorf("column types = %v/%v", f.Columns[0].Type, f.Columns[1].Type)
	}

	// exec：Execute 无列 → Exec{RowsAffected}。
	fc.ExecuteFunc = func(context.Context, string, string, ...protocol.Value) (*protocol.QueryResult, error) {
		return &protocol.QueryResult{AffectedRows: 7}, nil
	}
	res, err = ad.Run("DELETE FROM users")
	if err != nil {
		t.Fatal(err)
	}
	if res.Frame != nil || res.Exec == nil || res.Exec.RowsAffected != 7 {
		t.Fatalf("Run exec result = %+v", res)
	}

	// 错误穿透。
	fc.ExecuteFunc = func(context.Context, string, string, ...protocol.Value) (*protocol.QueryResult, error) {
		return nil, errors.New("boom")
	}
	if _, err := ad.Run("SELECT 1"); err == nil {
		t.Fatal("Run error not propagated")
	}
}

func TestAdapterFetchTable(t *testing.T) {
	ad, fc := newTestAdapter(t)
	var gotLimit uint32
	fc.BrowseRowsFunc = func(_ context.Context, _, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
		if schema != "public" || table != "users" {
			t.Errorf("BrowseRows target = %s.%s", schema, table)
		}
		gotLimit = req.Limit
		return &protocol.TableRowsResult{
			Columns: []protocol.ResultColumn{{Name: "id", GenericType: protocol.GenericTypeInteger}},
			Rows:    [][]protocol.Value{{protocol.NewIntValue(42)}},
		}, nil
	}
	f, err := ad.FetchTable("public", "users", 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotLimit != fetchTableLimit {
		t.Errorf("limit = %d, want default %d", gotLimit, fetchTableLimit)
	}
	if f.NumRows() != 1 || f.Cell(0, 0) != int64(42) {
		t.Fatalf("fetched frame = %v", f)
	}

	if _, err := ad.FetchTable("public", "users", 5); err != nil {
		t.Fatal(err)
	}
	if gotLimit != 5 {
		t.Errorf("limit = %d, want 5", gotLimit)
	}
}

func TestAdapterColumnsMetaAndIndexTypes(t *testing.T) {
	ad, fc := newTestAdapter(t)
	fc.ListColumnsFunc = func(context.Context, string, string, string) ([]protocol.ColumnInfo, error) {
		return []protocol.ColumnInfo{
			{Name: "id", DataType: "serial", Nullable: false, Comment: "pk"},
			{Name: "email", DataType: "varchar(255)", Nullable: true, DefaultValue: strPtr("''")},
		}, nil
	}
	meta, err := ad.ColumnsMeta("public", "users")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 2 {
		t.Fatalf("meta = %+v", meta)
	}
	if meta[0].IsNullable != "NO" || meta[0].Comment != "pk" || meta[0].DataType != "serial" {
		t.Errorf("meta[0] = %+v", meta[0])
	}
	if meta[1].IsNullable != "YES" || meta[1].Default != "''" {
		t.Errorf("meta[1] = %+v", meta[1])
	}

	fc.ListIndexesFunc = func(context.Context, string, string, string) ([]protocol.IndexInfo, error) {
		return []protocol.IndexInfo{
			{Name: "users_email_idx", Columns: []protocol.IndexColumn{{Name: "email"}}},
			{Name: "users_pkey", Primary: true, Columns: []protocol.IndexColumn{{Name: "id"}}},
			{Name: "users_email_key", Unique: true, Columns: []protocol.IndexColumn{{Name: "email"}}},
		}, nil
	}
	types, err := ad.ColumnIndexTypes("public", "users")
	if err != nil {
		t.Fatal(err)
	}
	if types["id"] != "PK" {
		t.Errorf("id label = %q, want PK", types["id"])
	}
	// UNIQUE 优先于普通 INDEX。
	if types["email"] != "UNIQUE" {
		t.Errorf("email label = %q, want UNIQUE", types["email"])
	}
}

func TestAdapterKVMethods(t *testing.T) {
	ad, fc := newTestAdapter(t)

	fc.ExecCommandFunc = func(_ context.Context, _ string, args []string) (protocol.RedisReply, error) {
		if len(args) != 2 || args[0] != "GET" || args[1] != "k" {
			t.Errorf("ExecCommand args = %v", args)
		}
		return protocol.RedisReply{Type: protocol.RedisReplyBulkString, Value: "v"}, nil
	}
	out, err := ad.Do([]string{"GET", "k"})
	if err != nil || out != "v" {
		t.Fatalf("Do = %q, %v", out, err)
	}

	// ScanKeys：cursor 翻页 + 类型过滤 + 上限。
	var cursorSeen []uint64
	fc.ScanKeysFunc = func(_ context.Context, _ string, cursor uint64, _ string, count int) (*protocol.RedisScanPage, error) {
		cursorSeen = append(cursorSeen, cursor)
		if count != scanPageSize {
			t.Errorf("scan count = %d, want %d", count, scanPageSize)
		}
		if cursor == 0 {
			return &protocol.RedisScanPage{Cursor: 7, Keys: []protocol.RedisKeyInfo{
				{Key: "a", Type: protocol.RedisKeyTypeString},
				{Key: "b", Type: protocol.RedisKeyTypeList},
			}}, nil
		}
		return &protocol.RedisScanPage{Cursor: 0, Keys: []protocol.RedisKeyInfo{
			{Key: "c", Type: protocol.RedisKeyTypeString},
		}}, nil
	}
	keys, err := ad.ScanKeys("string")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "c" {
		t.Fatalf("ScanKeys = %v, want [a c] (list b filtered)", keys)
	}
	if len(cursorSeen) != 2 || cursorSeen[1] != 7 {
		t.Errorf("cursor sequence = %v, want [0 7]", cursorSeen)
	}

	fc.GetValueFunc = func(context.Context, string, string) (protocol.RedisValue, error) {
		return protocol.RedisValue{Type: protocol.RedisKeyTypeString, Value: "hello"}, nil
	}
	v, err := ad.Value("k")
	if err != nil || v != `"hello"` {
		t.Fatalf("Value = %q, %v", v, err)
	}

	var gotDB int
	fc.SelectDBFunc = func(_ context.Context, _ string, index int) error {
		gotDB = index
		return nil
	}
	if err := ad.SelectDB(3); err != nil || gotDB != 3 {
		t.Fatalf("SelectDB = %d, %v", gotDB, err)
	}
}

func TestAdapterCloseIsNoop(t *testing.T) {
	ad, _ := newTestAdapter(t)
	if err := ad.Close(); err != nil {
		t.Fatalf("Close = %v, want nil (lifecycle owned by the composition root)", err)
	}
}

func strPtr(s string) *string { return &s }
