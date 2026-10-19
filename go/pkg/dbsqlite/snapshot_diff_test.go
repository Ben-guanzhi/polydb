package dbsqlite

// E2.0 真快照复核（Go 侧，逐条镜像 rust/crates/db-sqlite/tests/snapshot_diff.rs）：
// 用真实驱动采集元数据喂 schemadiff，验证**驱动填出的字段**是否满足 diff 的假设——
// 外键无名且稳定、主键能被 primary 认出、ON DELETE/ON UPDATE 落到 DTO、视图带 type。
// diff 规则本身由 test/schema-diff-vectors 锁死，这里不重复测。

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/schemadiff"
)

// 基线 DDL：两张被引用表 + 一张带两个外键与一个索引的表 + 一张复合主键表。
var baseDDL = []string{
	"CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
	"CREATE TABLE products (id INTEGER PRIMARY KEY, sku VARCHAR(20) NOT NULL DEFAULT '')",
	"CREATE TABLE orders (id INTEGER PRIMARY KEY, " +
		"customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE, " +
		"product_id INTEGER NOT NULL REFERENCES products(id), " +
		"qty INTEGER NOT NULL DEFAULT 1)",
	"CREATE INDEX idx_orders_customer ON orders(customer_id)",
	"CREATE TABLE pairs (a INTEGER NOT NULL, b INTEGER NOT NULL, PRIMARY KEY (a, b))",
}

func connWith(t *testing.T, stmts []string) *ProtocolDriver {
	t.Helper()
	c, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	pd := NewProtocolDriver(c)
	for _, q := range stmts {
		if _, err := pd.Execute(context.Background(), q); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	return pd
}

// snapshot 用驱动的四个元数据方法组装——E2 服务端要做的正是这段编排。
func snapshot(t *testing.T, c *ProtocolDriver, schema string) *schemadiff.Snapshot {
	t.Helper()
	ctx := context.Background()
	tables, err := c.ListTables(ctx, schema)
	if err != nil {
		t.Fatalf("list_tables: %v", err)
	}
	snap := &schemadiff.Snapshot{Kind: protocol.DatabaseKindSQLite, Schema: schema}
	for _, tb := range tables {
		// 与 app-core 的 collect_snapshot 同口径（E6.5）：视图只带名字与类型，零元数据查询。
		if tb.Type != protocol.TableTypeTable {
			snap.Tables = append(snap.Tables, schemadiff.TableSnapshot{Name: tb.Name, Type: tb.Type})
			continue
		}
		cols, err := c.ListColumns(ctx, schema, tb.Name)
		if err != nil {
			t.Fatalf("list_columns %s: %v", tb.Name, err)
		}
		idxs, err := c.ListIndexes(ctx, schema, tb.Name)
		if err != nil {
			t.Fatalf("list_indexes %s: %v", tb.Name, err)
		}
		fks, err := c.ListForeignKeys(ctx, schema, tb.Name)
		if err != nil {
			t.Fatalf("list_foreign_keys %s: %v", tb.Name, err)
		}
		snap.Tables = append(snap.Tables, schemadiff.TableSnapshot{
			Name: tb.Name, Type: tb.Type, Columns: cols, Indexes: idxs, ForeignKeys: fks,
		})
	}
	return snap
}

func plan(t *testing.T, source, target *schemadiff.Snapshot) []schemadiff.Entry {
	t.Helper()
	got, err := schemadiff.Diff(source, target, &schemadiff.Options{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return got
}

type entryKey struct{ op, table, object string }

func summary(entries []schemadiff.Entry) []entryKey {
	out := make([]entryKey, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryKey{string(e.Op), e.Table, e.Object})
	}
	return out
}

func tableByName(snap *schemadiff.Snapshot, name string) *schemadiff.TableSnapshot {
	for i := range snap.Tables {
		if snap.Tables[i].Name == name {
			return &snap.Tables[i]
		}
	}
	return nil
}

func TestIdenticalRealDatabasesProduceEmptyPlan(t *testing.T) {
	got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), snapshot(t, connWith(t, baseDDL), "main"))
	if len(got) != 0 {
		t.Fatalf("同一份 DDL 的两个库必须零差异，实际：%+v", summary(got))
	}
}

func TestForeignKeysUnnamedAndStable(t *testing.T) {
	type keyed struct {
		cols   string
		refTab string
		name   string
	}
	keyOf := func(snap *schemadiff.Snapshot) []keyed {
		orders := tableByName(snap, "orders")
		if orders == nil {
			t.Fatal("未采到 orders 表")
		}
		out := make([]keyed, 0, len(orders.ForeignKeys))
		for _, f := range orders.ForeignKeys {
			out = append(out, keyed{strings.Join(f.Columns, ","), f.ReferencedTable, f.Name})
		}
		return out
	}
	// 两端读的是同一个 C 引擎的 PRAGMA，发射序一致（实测为 SQLite 内部序，与声明顺序相反）。
	a := keyOf(snapshot(t, connWith(t, baseDDL), "main"))
	b := keyOf(snapshot(t, connWith(t, baseDDL), "main"))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("外键标识必须确定，不能随采集次数变化：%+v vs %+v", a, b)
	}
	want := []keyed{{"product_id", "products", ""}, {"customer_id", "customers", ""}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("SQLite 外键本来无名，须留空由 diff 按内容配对：%+v", a)
	}
}

func TestAddingOneRealForeignKeyDoesNotDisturbTheOthers(t *testing.T) {
	// 回归：驱动曾把外键命名为 fk_0/fk_1（位置编号）。新增一条外键会让其余外键的
	// 编号整体错位，凭空产出成串的 drop/add 对。
	stmts := append(append([]string{}, baseDDL...), "CREATE TABLE warehouses (id INTEGER PRIMARY KEY)")
	conn := connWith(t, stmts)
	if _, err := conn.Execute(context.Background(),
		"ALTER TABLE orders ADD COLUMN warehouse_id INTEGER REFERENCES warehouses(id)"); err != nil {
		t.Fatalf("alter: %v", err)
	}
	got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), snapshot(t, conn, "main"))
	want := []entryKey{
		{"create_table", "warehouses", ""},
		{"add_column", "orders", "warehouse_id"},
		{"add_foreign_key", "orders", "fk(warehouse_id)"},
	}
	if !reflect.DeepEqual(summary(got), want) {
		t.Fatalf("只应产出 1 张新表 + 1 条新列 + 1 条新外键，实际：%+v", summary(got))
	}
}

func TestForeignKeyActionsComeFromRealMetadata(t *testing.T) {
	orders := tableByName(snapshot(t, connWith(t, baseDDL), "main"), "orders")
onDelete:
	for _, tc := range []struct {
		col  string
		want protocol.ForeignKeyAction
	}{
		{"customer_id", protocol.FKActionCascade},
		// SQLite 未写 ON 子句时的默认动作是 NO ACTION，不是「无信息」。
		{"product_id", protocol.FKActionNoAction},
	} {
		for _, f := range orders.ForeignKeys {
			if len(f.Columns) == 1 && f.Columns[0] == tc.col {
				if f.OnDelete != tc.want {
					t.Fatalf("%s 的 ON DELETE = %q，期望 %q", tc.col, f.OnDelete, tc.want)
				}
				if f.OnUpdate != protocol.FKActionNoAction {
					t.Fatalf("%s 的 ON UPDATE = %q，期望 no_action", tc.col, f.OnUpdate)
				}
				continue onDelete
			}
		}
		t.Fatalf("未采到 %s 上的外键", tc.col)
	}
}

func TestPrimaryKeyIndexMarkedFromRealMetadata(t *testing.T) {
	snap := snapshot(t, connWith(t, baseDDL), "main")

	pairs := tableByName(snap, "pairs")
	var pks []string
	for _, i := range pairs.Indexes {
		if i.Primary {
			pks = append(pks, i.Name)
		}
	}
	if len(pks) != 1 {
		t.Fatalf("复合主键表必须恰有一条 primary 索引，实际：%v", pks)
	}
	if !strings.HasPrefix(pks[0], "sqlite_autoindex_") {
		t.Fatalf("SQLite 主键索引是隐式的，实际：%s", pks[0])
	}

	// rowid 别名式主键（INTEGER PRIMARY KEY）不建索引，orders 不得被误标 primary。
	orders := tableByName(snap, "orders")
	for _, i := range orders.Indexes {
		if i.Primary {
			t.Fatalf("orders 的主键是 rowid 别名，不该出现 primary：%+v", i)
		}
	}
}

func TestRealPrimaryKeyChangeRebuildsTheIndex(t *testing.T) {
	stmts := append([]string{}, baseDDL...)
	stmts[4] = "CREATE TABLE pairs (b INTEGER NOT NULL, a INTEGER NOT NULL, PRIMARY KEY (b, a))"
	got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), snapshot(t, connWith(t, stmts), "main"))
	want := []entryKey{
		{"drop_index", "pairs", "sqlite_autoindex_pairs_1"},
		{"add_index", "pairs", "sqlite_autoindex_pairs_1"},
	}
	if !reflect.DeepEqual(summary(got), want) {
		t.Fatalf("主键列序变化必须走主键重建路径，实际：%+v", summary(got))
	}
	for _, e := range got {
		if e.Reason != "主键列变化，需重建" {
			t.Fatalf("原因文案必须是主键专用，实际：%q", e.Reason)
		}
	}
}

func TestRealDifferencesBetweenTwoFilesProduceExpectedPlan(t *testing.T) {
	stmts := append([]string{}, baseDDL...)
	stmts[1] = "CREATE TABLE products (id INTEGER PRIMARY KEY, sku varchar(30) NOT NULL DEFAULT '')"
	stmts[2] = "CREATE TABLE orders (id INTEGER PRIMARY KEY, " +
		"customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE, " +
		"product_id INTEGER NOT NULL REFERENCES products(id), " +
		"qty INTEGER NOT NULL DEFAULT 1, note TEXT)"
	stmts[3] = "CREATE INDEX idx_orders_note ON orders(note)"
	got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), snapshot(t, connWith(t, stmts), "main"))
	want := []entryKey{
		{"drop_index", "orders", "idx_orders_customer"},
		{"add_column", "orders", "note"},
		{"alter_column", "products", "sku"},
		{"add_index", "orders", "idx_orders_note"},
	}
	if !reflect.DeepEqual(summary(got), want) {
		t.Fatalf("相位序：删索引(20) → 加列(60) → 改列(70) → 加索引(80)，实际：%+v", summary(got))
	}
	for _, e := range got {
		switch e.Object {
		case "sku":
			if e.Reason != "类型 varchar(20) → varchar(30)" {
				t.Fatalf("类型差异文案不符：%q", e.Reason)
			}
			if len(e.Flags) != 1 {
				t.Fatalf("只应有一个 flag，实际：%+v", e.Flags)
			}
		case "note":
			if !e.Selected {
				t.Fatalf("新增列不是破坏性动作，必须默认勾选")
			}
		case "idx_orders_customer":
			if e.Selected {
				t.Fatalf("删索引是破坏性动作，默认不勾选（D4）")
			}
		}
	}
}

func TestRealViewsAreSkipped(t *testing.T) {
	stmts := append([]string{}, baseDDL...)
	stmts = append(stmts, "CREATE VIEW v_orders AS SELECT id FROM orders")
	target := snapshot(t, connWith(t, stmts), "main")
	view := tableByName(target, "v_orders")
	if view == nil {
		t.Fatal("驱动未列出视图")
	}
	if view.Type != protocol.TableTypeView {
		t.Fatalf("驱动必须区分表与视图，实际：%q", view.Type)
	}
	if got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), target); len(got) != 0 {
		t.Fatalf("视图不参与比对：%+v", summary(got))
	}
}

func TestAddAndDropARealTableMatchThePhaseOrder(t *testing.T) {
	stmts := append([]string{}, baseDDL[:4]...)
	stmts = append(stmts, "CREATE TABLE shipments (id INTEGER PRIMARY KEY, orders_id INTEGER NOT NULL REFERENCES orders(id))")
	got := plan(t, snapshot(t, connWith(t, baseDDL), "main"), snapshot(t, connWith(t, stmts), "main"))
	want := []entryKey{
		{"drop_table", "pairs", ""},
		{"create_table", "shipments", ""},
	}
	if !reflect.DeepEqual(summary(got), want) {
		t.Fatalf("删表(40) 必须先于建表(50)，实际：%+v", summary(got))
	}
	if got[0].Selected {
		t.Fatalf("drop_table 是破坏性动作，默认不勾选")
	}
	if !got[1].Selected {
		t.Fatalf("create_table 必须默认勾选")
	}
}

func TestUniqueConstraintIndexIsNotAPrimaryKey(t *testing.T) {
	stmts := []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, email TEXT UNIQUE)",
		"CREATE TABLE u (id INTEGER PRIMARY KEY, email TEXT UNIQUE)",
	}
	if got := plan(t, snapshot(t, connWith(t, stmts), "main"), snapshot(t, connWith(t, stmts), "main")); len(got) != 0 {
		t.Fatalf("UNIQUE 隐式索引两侧一致即无差异：%+v", summary(got))
	}
	table := tableByName(snapshot(t, connWith(t, stmts), "main"), "t")
	for _, i := range table.Indexes {
		if !strings.HasPrefix(i.Name, "sqlite_autoindex_") {
			continue
		}
		if !i.Unique || i.Primary {
			t.Fatalf("origin='u' 只能是唯一索引，不是主键：%+v", i)
		}
	}
}
