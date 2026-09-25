package schemadiff

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func eq(t *testing.T, got, want string, label string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s:\n got = %q\nwant = %q", label, got, want)
	}
}

func TestNormalizeTypeWhitespaceAndCase(t *testing.T) {
	pg := protocol.DatabaseKindPostgres
	eq(t, NormalizeType(pg, "NUMERIC (10, 2)"), "numeric(10,2)", "numeric 空白折叠")
	eq(t, NormalizeType(pg, "character\tvarying\n(20)"), "character varying(20)", "制表/换行折叠")
	eq(t, NormalizeType(pg, "TEXT [ ]"), "text[]", "数组括号")
	eq(t, NormalizeType(pg, "  int   unsigned  "), "int unsigned", "首尾与中间空白")
}

func TestNormalizeTypeMySQLWidthAndZerofill(t *testing.T) {
	my := protocol.DatabaseKindMySQL
	eq(t, NormalizeType(my, "int(11)"), "int", "整数显示宽度")
	eq(t, NormalizeType(my, "INT(10) UNSIGNED ZEROFILL"), "int unsigned", "zerofill 丢弃")
	eq(t, NormalizeType(my, "bigint(20) unsigned"), "bigint unsigned", "bigint 宽度")
	eq(t, NormalizeType(my, "decimal(10,2)"), "decimal(10,2)", "小数保留")
	eq(t, NormalizeType(my, "varchar(11)"), "varchar(11)", "变长字符保留")
	eq(t, NormalizeType(protocol.DatabaseKindSQLite, "int(11)"), "int(11)", "非 MySQL 不去宽度")
}

func TestNormalizeTypeOracle(t *testing.T) {
	oc := protocol.DatabaseKindOracle
	eq(t, NormalizeType(oc, "BPCHAR(3)"), "char(3)", "bpchar 归一")
	eq(t, NormalizeType(oc, "bpchar"), "char", "裸 bpchar")
	eq(t, NormalizeType(oc, "NUMBER(10, 0)"), "number(10)", "scale 0 归一")
	eq(t, NormalizeType(oc, "NUMBER(10,2)"), "number(10,2)", "非 0 scale 保留")
	eq(t, NormalizeType(oc, "VARCHAR2(50)"), "varchar2(50)", "varchar2 保留")
}

func sp(s string) *string { return &s }

func TestNormalizeDefaultCollapsesNoise(t *testing.T) {
	cases := []struct {
		in   *string
		want *string
	}{
		{nil, nil},
		{sp("   "), nil},
		{sp("NULL"), nil},
		{sp("(NULL)"), nil},
		{sp("(('0'))"), sp("'0'")},
		{sp("'0'::character varying"), sp("'0'")},
		{sp("'0'::numeric::text"), sp("'0'")},
		{sp("((CURRENT_TIMESTAMP))"), sp("current_timestamp")},
		{sp("nextval('orders_id_seq'::regclass)"), sp("nextval('orders_id_seq'::regclass)")},
		{sp("'ABC'"), sp("'ABC'")},
		{sp("'abc'"), sp("'abc'")},
		{sp("DEFAULT_UPPER('ABC') || 'x'"), sp("default_upper('ABC') || 'x'")},
	}
	for i, c := range cases {
		got := NormalizeDefault(c.in)
		if !sameOptStr(got, c.want) {
			t.Fatalf("第 %d 例：got=%v want=%v", i, deref(got), deref(c.want))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func strOps(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Op)
	}
	return out
}

func TestDiffTableLevelOpsAndSelection(t *testing.T) {
	pg := protocol.DatabaseKindPostgres
	empty := func(name string) TableSnapshot {
		return TableSnapshot{Name: name, Type: protocol.TableTypeTable}
	}
	src := &Snapshot{Kind: pg, Schema: "public", Tables: []TableSnapshot{empty("legacy")}}
	tgt := &Snapshot{Kind: pg, Schema: "public", Tables: []TableSnapshot{empty("users")}}
	out, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{string(OpDropTable), string(OpCreateTable)}
	if got := strOps(out); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("相位排序错乱：%v", got)
	}
	if out[0].Phase != 40 || out[1].Phase != 50 {
		t.Fatalf("相位值错误：%d %d", out[0].Phase, out[1].Phase)
	}
	if out[0].Table != "legacy" || out[1].Table != "users" {
		t.Fatalf("表名归属错误：%s %s", out[0].Table, out[1].Table)
	}
	if !out[0].Destructive || out[0].Selected || !out[1].Selected {
		t.Fatal("破坏性默认勾选状态错误")
	}
	if out[0].ID != "legacy//drop_table" {
		t.Fatalf("id 错误：%s", out[0].ID)
	}
	if out[0].Flags != nil {
		t.Fatalf("Flags 必须为 nil，实得 %#v", out[0].Flags)
	}
}

func TestDiffSelectDestructiveFlipsDrops(t *testing.T) {
	col := func(name, typ string) protocol.ColumnInfo {
		return protocol.ColumnInfo{Name: name, DataType: typ}
	}
	src := &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{
		{Name: "legacy", Columns: []protocol.ColumnInfo{col("a", "int")}},
	}}
	tgt := &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{
		{Name: "legacy"},
	}}
	out, err := Diff(src, tgt, &Options{SelectDestructive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Op != OpDropColumn || !out[0].Selected {
		t.Fatalf("select_destructive 未生效：%#v", out)
	}
	if out[0].ID != "legacy/a/drop_column" {
		t.Fatalf("id 错误：%s", out[0].ID)
	}
}

func TestDiffAlterColumnFlagsAndReason(t *testing.T) {
	src := &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{{
		Name: "users",
		Columns: []protocol.ColumnInfo{
			{Name: "name", DataType: "character varying(20)", DefaultValue: sp("'x'")},
		},
	}}}
	tgt := &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{{
		Name: "users",
		Columns: []protocol.ColumnInfo{
			{Name: "name", DataType: "VARCHAR(30)", Nullable: true, DefaultValue: sp("('x'::text)")},
		},
	}}}
	out, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Op != OpAlterColumn {
		t.Fatalf("应只有一条 alter_column：%#v", out)
	}
	e := out[0]
	wantFlags := []ColumnFlag{FlagType, FlagNullability}
	if len(e.Flags) != 2 || e.Flags[0] != wantFlags[0] || e.Flags[1] != wantFlags[1] {
		t.Fatalf("flags 错误：%#v", e.Flags)
	}
	if deref(e.From) != "character varying(20)" || deref(e.To) != "varchar(30)" {
		t.Fatalf("from/to 错误：%v %v", deref(e.From), deref(e.To))
	}
	eq(t, e.Reason, "类型 character varying(20) → varchar(30)；可空性 NOT NULL → NULL", "reason")
}

func TestDiffIgnoreDefaults(t *testing.T) {
	mk := func(d string) *Snapshot {
		return &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{{
			Name:    "t",
			Columns: []protocol.ColumnInfo{{Name: "a", DataType: "int", Nullable: true, DefaultValue: sp(d)}},
		}}}
	}
	src, tgt := mk("1"), mk("2")
	out, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Reason != "默认值 1 → 2" {
		t.Fatalf("默认值差异未产出：%#v", out)
	}
	out2, err := Diff(src, tgt, &Options{IgnoreDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out2) != 0 {
		t.Fatalf("ignore_defaults 未生效：%#v", out2)
	}
}

func TestDiffIdentifierMatchingIsCaseInsensitive(t *testing.T) {
	src := &Snapshot{Kind: protocol.DatabaseKindMySQL, Tables: []TableSnapshot{{
		Name:    "Users",
		Columns: []protocol.ColumnInfo{{Name: "ID", DataType: "INT(11)"}},
	}}}
	tgt := &Snapshot{Kind: protocol.DatabaseKindMySQL, Tables: []TableSnapshot{{
		Name:    "users",
		Columns: []protocol.ColumnInfo{{Name: "id", DataType: "int"}},
	}}}
	out, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("大小写/显示宽度造成误判：%#v", out)
	}
}

func TestDiffCrossKindAndRedis(t *testing.T) {
	s := &Snapshot{Kind: protocol.DatabaseKindSQLite}
	m := &Snapshot{Kind: protocol.DatabaseKindMySQL}
	r := &Snapshot{Kind: protocol.DatabaseKindRedis}
	check := func(err error, msg string) {
		t.Helper()
		if err == nil {
			t.Fatal("应当报错")
		}
		pe, ok := err.(*protocol.PolyDBError)
		if !ok || pe.Code != protocol.ErrNotSupported || pe.Message != msg {
			t.Fatalf("错误不符：%#v", err)
		}
	}
	_, err := Diff(s, m, &Options{})
	check(err, "跨种类比对未支持：sqlite vs mysql（v1 只比同一种数据库）")
	_, err = Diff(r, r, &Options{})
	check(err, "Redis 无模式概念，不参与结构比对")
	_, err = Diff(s, r, &Options{})
	check(err, "Redis 无模式概念，不参与结构比对")
}

func TestDiffCreateTablesOrderedByForeignKeyDependency(t *testing.T) {
	src := &Snapshot{Kind: protocol.DatabaseKindPostgres}
	tgt := &Snapshot{Kind: protocol.DatabaseKindPostgres, Tables: []TableSnapshot{
		{
			Name:    "orders",
			Columns: []protocol.ColumnInfo{{Name: "user_id", DataType: "int", Nullable: false}},
			ForeignKeys: []protocol.ForeignKeyInfo{{
				Name: "fk_orders_user", Columns: []string{"user_id"},
				ReferencedTable: "users", ReferencedColumns: []string{"id"},
			}},
		},
		{Name: "users", Columns: []protocol.ColumnInfo{{Name: "id", DataType: "int", Nullable: false}}},
	}}
	out, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Table != "users" || out[1].Table != "orders" {
		t.Fatalf("拓扑序错误：%#v", out)
	}
}
