package schemadiff

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 本文件是 rust/crates/schema-diff/src/ddl.rs 测试模块的镜像：
// 同一组断言、同一份逐字 SQL 字面量，任一侧改动都必须同步。

func dcol(name, ty string, nullable bool, ord int32) protocol.ColumnInfo {
	return protocol.ColumnInfo{
		Name:            name,
		DataType:        ty,
		Nullable:        nullable,
		OrdinalPosition: ord,
	}
}

func dpkCol(name, ty string) protocol.ColumnInfo {
	c := dcol(name, ty, false, 1)
	c.IsPrimaryKey = true
	return c
}

func dwithDefault(c protocol.ColumnInfo, d string) protocol.ColumnInfo {
	c.DefaultValue = sp(d)
	return c
}

func didx(name string, primary, unique bool, cols ...string) protocol.IndexInfo {
	ic := make([]protocol.IndexColumn, len(cols))
	for i, c := range cols {
		ic[i] = protocol.IndexColumn{Name: c, Position: int32(i)}
	}
	return protocol.IndexInfo{Name: name, Unique: unique, Primary: primary, Columns: ic}
}

func dfk(cols []string, refs string, refCols []string) protocol.ForeignKeyInfo {
	return protocol.ForeignKeyInfo{
		Name:              "",
		Columns:           cols,
		ReferencedSchema:  "main",
		ReferencedTable:   refs,
		ReferencedColumns: refCols,
	}
}

func dtable(name string, columns ...protocol.ColumnInfo) TableSnapshot {
	return TableSnapshot{Name: name, Type: protocol.TableTypeTable, Columns: columns}
}

func dwithIndexes(t TableSnapshot, indexes ...protocol.IndexInfo) TableSnapshot {
	t.Indexes = indexes
	return t
}

func dwithFKs(t TableSnapshot, fks ...protocol.ForeignKeyInfo) TableSnapshot {
	t.ForeignKeys = fks
	return t
}

func dsnap(kind protocol.DatabaseKind, tables ...TableSnapshot) *Snapshot {
	return &Snapshot{Kind: kind, Schema: "main", Tables: tables}
}

func dentry(op Op, table, object string) Entry {
	return Entry{
		ID:          lowerASCII(table) + "/" + lowerASCII(object) + "/" + string(op),
		Op:          op,
		Phase:       op.Phase(),
		Table:       table,
		Object:      object,
		Destructive: op.Destructive(),
		Selected:    true,
	}
}

func sqls(stmts []Statement) []string {
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = s.SQL
	}
	return out
}

func eqStrs(t *testing.T, got, want []string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 数量不符\ngot  = %q\nwant = %q", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: 第 %d 项不符\ngot  = %q\nwant = %q", label, i, got, want)
		}
	}
}

func one(t *testing.T, stmts []Statement) string {
	t.Helper()
	if len(stmts) != 1 {
		t.Fatalf("期望单条语句：%q", sqls(stmts))
	}
	return stmts[0].SQL
}

func dgen(t *testing.T, source, target *Snapshot, entries []Entry) *Script {
	t.Helper()
	out, err := Generate(source, target, entries, &Options{})
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	return out
}

// dgenFromDiff 用真实 Diff 的输出喂生成器（勾选全部条目），断言两端镜像关系。
func dgenFromDiff(t *testing.T, source, target *Snapshot) *Script {
	t.Helper()
	opts := &Options{SelectDestructive: true}
	entries, err := Diff(source, target, opts)
	if err != nil {
		t.Fatalf("diff 失败：%v", err)
	}
	out, err := Generate(source, target, entries, opts)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	return out
}

func TestCreateTableRenderedFromTargetSnapshot(t *testing.T) {
	tgt := dwithIndexes(
		dtable("u",
			dpkCol("id", "INTEGER"),
			dcol("name", "varchar(20)", true, 2),
			dwithDefault(dcol("status", "text", false, 3), "'new'"),
		),
		didx("sqlite_autoindex_u_1", false, true, "name"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, tgt)
	src := dsnap(protocol.DatabaseKindSQLite)
	out := dgen(t, src, s, []Entry{dentry(OpCreateTable, "u", "")})
	eq(t, one(t, out.Statements),
		`CREATE TABLE "u"("id" integer NOT NULL,"name" varchar(20),"status" text NOT NULL DEFAULT 'new',PRIMARY KEY ("id"),UNIQUE ("name"));`,
		"建表语句")
	st := out.Statements[0]
	eqStrs(t, st.EntryIDs, []string{"u//create_table"}, "entry_ids")
	if st.Phase != 50 || st.Table != "u" || st.NonReversible {
		t.Fatalf("语句元信息不符：%+v", st)
	}
	// 单列 INTEGER 主键 ⇒ 额外提示 AUTOINCREMENT 无法还原
	eqStrs(t, st.Warnings, []string{WCreateTable, WAutoincrement}, "warnings")
	// 回滚 = 互换后同规则生成：建表的回滚就是删表，且按规则 6 不可逆
	eqStrs(t, sqls(out.Rollback), []string{`DROP TABLE "u";`}, "回滚")
	if !out.Rollback[0].NonReversible {
		t.Fatal("回滚的 drop_table 必须标不可逆")
	}
	eqStrs(t, out.Rollback[0].EntryIDs, []string{"u//drop_table"}, "回滚 entry_ids")
	if out.PragmasOutsideTx || !out.DdlInTx {
		t.Fatalf("事务标志不符：%+v", out)
	}
}

func TestColumnAndIndexOpsApplyDirectly(t *testing.T) {
	src := dwithIndexes(
		dtable("t", dcol("a", "text", true, 1), dcol("gone", "text", true, 2)),
		didx("idx_gone", false, false, "a"),
	)
	tgt := dwithIndexes(
		dtable("t", dcol("a", "text", true, 1), dwithDefault(dcol("b", "text", true, 2), "'x'")),
		didx("idx_a", false, false, "a"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	entries := []Entry{
		dentry(OpDropIndex, "t", "idx_gone"),
		dentry(OpDropColumn, "t", "gone"),
		dentry(OpAddColumn, "t", "b"),
		dentry(OpAddIndex, "t", "idx_a"),
	}
	out := dgen(t, s, tt, entries)
	eqStrs(t, sqls(out.Statements), []string{
		`DROP INDEX "idx_gone";`,
		`ALTER TABLE "t" DROP COLUMN "gone";`,
		`ALTER TABLE "t" ADD COLUMN "b" text DEFAULT 'x';`,
		`CREATE INDEX "idx_a" ON "t"("a");`,
	}, "直改脚本")
	eqStrs(t, out.Statements[1].Warnings, []string{WDropColumnCheck}, "删列警告")
	if !out.Statements[1].NonReversible {
		t.Fatal("drop_column 必须不可逆")
	}
	if out.Statements[0].NonReversible || out.Statements[2].NonReversible {
		t.Fatal("drop_index / add_column 必须可逆")
	}
	if len(out.Statements[2].Warnings) != 0 {
		t.Fatalf("常量默认值的 add_column 不该挂警告：%q", out.Statements[2].Warnings)
	}
	if out.PragmasOutsideTx {
		t.Fatal("纯直改脚本不得包 PRAGMA")
	}
}

func TestAlterColumnRebuildsAndWrapsPragmas(t *testing.T) {
	src := dtable("t", dpkCol("id", "INTEGER"), dcol("name", "varchar(20)", true, 2))
	tgt := dtable("t", dpkCol("id", "INTEGER"), dcol("name", "varchar(30)", true, 2))
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	e := dentry(OpAlterColumn, "t", "name")
	e.Flags = []ColumnFlag{FlagType}
	out := dgen(t, s, tt, []Entry{e})
	eqStrs(t, sqls(out.Statements), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,"name" varchar(30),PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id","name") SELECT "id","name" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		"PRAGMA foreign_keys=ON;",
	}, "重建脚本")
	if !out.PragmasOutsideTx {
		t.Fatal("含重建必须置 pragmas_outside_tx")
	}
	if out.Statements[0].Phase != phasePragmaFirst || out.Statements[5].Phase != phasePragmaLast {
		t.Fatalf("PRAGMA 相位不符：%d / %d", out.Statements[0].Phase, out.Statements[5].Phase)
	}
	if len(out.Statements[0].EntryIDs) != 0 {
		t.Fatalf("PRAGMA 包裹条不属于任何条目：%q", out.Statements[0].EntryIDs)
	}
	head := out.Statements[1]
	if head.Phase != 70 || head.NonReversible != true {
		t.Fatalf("type 变更不可逆（规则 6）：%+v", head)
	}
	eqStrs(t, head.EntryIDs, []string{"t/name/alter_column"}, "entry_ids")
	eqStrs(t, head.Warnings, []string{WTriggers, WGeneratedColumns, WCheckConstraints, WAutoincrement},
		"重建警告")
	// 只有默认值变化的 alter_column 可逆
	e2 := dentry(OpAlterColumn, "t", "name")
	e2.Flags = []ColumnFlag{FlagDefault}
	out2 := dgen(t, s, tt, []Entry{e2})
	if out2.Statements[1].NonReversible {
		t.Fatal("仅 default 变化的 alter_column 必须可逆")
	}
	// 反向脚本用同一套规则，只是基准换成源侧定义
	eqStrs(t, sqls(out.Rollback), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,"name" varchar(20),PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id","name") SELECT "id","name" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		"PRAGMA foreign_keys=ON;",
	}, "回滚脚本")
}

func TestDropColumnOnIndexedOrPrimaryColumnRebuilds(t *testing.T) {
	// 被索引引用 → 重建；纯列 → 直改（取证 G2）
	src := dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("note", "text", true, 2)),
		didx("idx_note", false, false, "note"),
	)
	tgt := dtable("t", dpkCol("id", "INTEGER"))
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	out := dgen(t, s, tt, []Entry{dentry(OpDropColumn, "t", "note")})
	if !out.PragmasOutsideTx || len(out.Statements) != 6 {
		t.Fatalf("被索引引用的删列必须重建：%q", sqls(out.Statements))
	}
	eq(t, out.Statements[1].SQL,
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,PRIMARY KEY ("id"));`, "重建定义")
	// 主键列本身也走重建
	src2 := dtable("t", dpkCol("id", "INTEGER"))
	tgt2 := dtable("t", dcol("x", "text", true, 2))
	out2 := dgen(t,
		dsnap(protocol.DatabaseKindSQLite, src2),
		dsnap(protocol.DatabaseKindSQLite, tgt2),
		[]Entry{dentry(OpDropColumn, "t", "id")})
	if !out2.PragmasOutsideTx {
		t.Fatal("删主键列必须走重建")
	}
}

func TestAutoIndexChangesRebuildAndRestoreAsTableConstraint(t *testing.T) {
	// sqlite_autoindex_* 既不能 CREATE 也不能 DROP（取证 G12/G13）→ 重建 + UNIQUE 还原
	src := dtable("t", dcol("a", "text", true, 1), dcol("b", "text", true, 2))
	tgt := dwithIndexes(
		dtable("t", dcol("a", "text", true, 1), dcol("b", "text", true, 2)),
		didx("sqlite_autoindex_t_1", false, true, "a", "b"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	out := dgen(t, s, tt, []Entry{dentry(OpAddIndex, "t", "sqlite_autoindex_t_1")})
	if !out.PragmasOutsideTx || len(out.Statements) != 6 {
		t.Fatalf("自动索引的新增必须重建：%q", sqls(out.Statements))
	}
	eq(t, out.Statements[1].SQL,
		`CREATE TABLE "polydb_tmp_t"("a" text,"b" text,UNIQUE ("a","b"));`, "UNIQUE 还原")
	// 删除自动索引同样走重建
	out2 := dgen(t, tt, s, []Entry{dentry(OpDropIndex, "t", "sqlite_autoindex_t_1")})
	if !out2.PragmasOutsideTx {
		t.Fatal("自动索引的删除必须重建")
	}
	eq(t, out2.Statements[1].SQL, `CREATE TABLE "polydb_tmp_t"("a" text,"b" text);`, "去掉约束")
}

func TestForeignKeyOpsRebuildWithInlineClause(t *testing.T) {
	src := dtable("orders", dcol("uid", "integer", true, 1))
	f := dfk([]string{"uid"}, "users", []string{"id"})
	f.OnDelete = protocol.FKActionCascade
	f.OnUpdate = protocol.FKActionNoAction
	tgt := dwithFKs(dtable("orders", dcol("uid", "integer", true, 1)), f)
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	out := dgen(t, s, tt, []Entry{dentry(OpAddForeignKey, "orders", "fk(uid)")})
	if len(out.Statements) != 6 {
		t.Fatalf("外键动作必须走重建：%q", sqls(out.Statements))
	}
	eq(t, out.Statements[1].SQL,
		`CREATE TABLE "polydb_tmp_orders"("uid" integer,FOREIGN KEY ("uid") REFERENCES "users"("id") ON DELETE CASCADE ON UPDATE NO ACTION);`,
		"外键子句")
}

func TestRebuildGroupAbsorbsEveryEntryOfTheTable(t *testing.T) {
	// 同一张表上：删列 + 改列 + 建索引（非自动）→ 只重建一次，三条条目都挂在组上
	src := dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("gone", "text", true, 2)),
	)
	tgt := dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("name", "text", false, 3)),
		didx("idx_name", false, false, "name"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	altered := dentry(OpAlterColumn, "t", "name")
	altered.Flags = []ColumnFlag{FlagNullability}
	entries := []Entry{
		dentry(OpDropColumn, "t", "gone"),
		altered,
		dentry(OpAddIndex, "t", "idx_name"),
	}
	out := dgen(t, s, tt, entries)
	if len(out.Statements) != 7 {
		t.Fatalf("PRAGMA×2 + 重建 4 条 + 补索引 1 条：%q", sqls(out.Statements))
	}
	eq(t, out.Statements[5].SQL, `CREATE INDEX "idx_name" ON "t"("name");`, "补索引")
	eqStrs(t, out.Statements[1].EntryIDs, []string{
		"t/gone/drop_column",
		"t/name/alter_column",
		"t/idx_name/add_index",
	}, "组吸收全部条目")
	for _, st := range out.Statements[1:6] {
		if !st.NonReversible {
			t.Fatal("组内语句必须继承删列的不可逆标记")
		}
	}
	if !out.PragmasOutsideTx {
		t.Fatal("含重建必须置 pragmas_outside_tx")
	}
}

func TestTmpTableNameAvoidsExistingTables(t *testing.T) {
	src := dwithIndexes(
		dtable("t", dcol("a", "text", true, 1)),
	)
	extra := dtable("polydb_tmp_t", dcol("z", "text", true, 1))
	tgt := dwithIndexes(
		dtable("t", dcol("a", "text", false, 1)),
		didx("sqlite_autoindex_t_1", false, true, "a"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, src, extra)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt)
	out := dgen(t, s, tt, []Entry{dentry(OpAddIndex, "t", "sqlite_autoindex_t_1")})
	if !strings.Contains(out.Statements[1].SQL, `"polydb_tmp_t_2"`) {
		t.Fatalf("临时表名必须避让已有表：%s", out.Statements[1].SQL)
	}
}

func dview(name string) TableSnapshot {
	return TableSnapshot{Name: name, Type: protocol.TableTypeView}
}

func TestViewNamesSortByLowercaseAndDedupCaseInsensitively(t *testing.T) {
	s := dsnap(protocol.DatabaseKindSQLite,
		dtable("t", dcol("a", "text", true, 1)),
		dview("v_b"), dview("V_A"), dview("v_a"), dview("v_c"),
	)
	// 键取小写 ⇒ 顺序按小写字节；同名只留首个，与 tableMap 的先到先得同规则
	eqStrs(t, viewNames(s), []string{"V_A", "v_b", "v_c"}, "视图名单")
	eqStrs(t, viewNames(dsnap(protocol.DatabaseKindSQLite, dview("v_a"))),
		[]string{"v_a"}, "单个视图")
	if got := viewNames(dsnap(protocol.DatabaseKindSQLite, dtable("t"))); len(got) != 0 {
		t.Fatalf("无视图时不该有名：%q", got)
	}
}

func TestViewScopeListsAtMostFiveNames(t *testing.T) {
	many := []string{"v_0", "v_1", "v_2", "v_3", "v_4", "v_5", "v_6"}
	if got, want := viewScope(many), "7 个视图（v_0, v_1, v_2, v_3, v_4 等）"; got != want {
		t.Fatalf("超量渲染不符：got %q want %q", got, want)
	}
	if got, want := viewScope([]string{"v_a"}), "1 个视图（v_a）"; got != want {
		t.Fatalf("单个渲染不符：%q", got)
	}
	if got, want := viewScope([]string{"v_a", "v_b"}), "2 个视图（v_a, v_b）"; got != want {
		t.Fatalf("两个渲染不符：%q", got)
	}
}

func TestDropTableAndRebuildWarnAboutViewsOnTheExecutedSide(t *testing.T) {
	src := dsnap(protocol.DatabaseKindSQLite,
		dtable("t", dpkCol("id", "INTEGER"), dcol("a", "text", true, 2)),
		dview("z_report"), dview("a_report"), dview("polydb_tmp_t"),
	)
	out := dgen(t, src, dsnap(protocol.DatabaseKindSQLite), []Entry{dentry(OpDropTable, "t", "")})
	eqStrs(t, out.Statements[0].Warnings, []string{wViewDrop(viewNames(src))}, "删表警告")
	// 目标侧没有视图 ⇒ 回滚（在目标侧建表）不该带视图警告
	if !strings.Contains(strings.Join(out.Rollback[0].Warnings, "|"), WCreateTable) {
		t.Fatalf("回滚应保留建表警告：%q", out.Rollback[0].Warnings)
	}
	for _, w := range out.Rollback[0].Warnings {
		if strings.Contains(w, "视图") {
			t.Fatalf("回滚侧无视图，不该出视图警告：%s", w)
		}
	}

	tgt := dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("a", "text", false, 2)),
		didx("sqlite_autoindex_t_1", false, true, "a"),
	)
	out2 := dgen(t, src, dsnap(protocol.DatabaseKindSQLite, tgt),
		[]Entry{dentry(OpAddIndex, "t", "sqlite_autoindex_t_1")})
	head := out2.Statements[1]
	last := head.Warnings[len(head.Warnings)-1]
	if last != wViewRebuild(viewNames(src)) {
		t.Fatalf("重建警告末条必须是视图警告：got %q want %q", last, wViewRebuild(viewNames(src)))
	}
	// 视图名占掉了 polydb_tmp_t ⇒ 临时表改名，且警告里点的是视图名
	if !strings.Contains(head.SQL, `"polydb_tmp_t_2"`) {
		t.Fatalf("临时表名必须避让视图：%s", head.SQL)
	}
	for _, needle := range []string{"a_report", "polydb_tmp_t", "z_report", "重建经 RENAME"} {
		if !strings.Contains(last, needle) {
			t.Fatalf("视图警告缺 %s：%s", needle, last)
		}
	}
}

func TestNoViewsMeansNoViewWarningAnywhere(t *testing.T) {
	src := dsnap(protocol.DatabaseKindSQLite, dtable("t", dcol("a", "text", true, 1)))
	tgt := dsnap(protocol.DatabaseKindSQLite, dtable("t", dcol("a", "text", false, 1)))
	out := dgen(t, src, tgt, []Entry{dentry(OpDropTable, "t", "")})
	if len(out.Statements[0].Warnings) != 0 {
		t.Fatalf("无视图时删表不该有警告：%q", out.Statements[0].Warnings)
	}
	e := dentry(OpAlterColumn, "t", "a")
	e.Flags = []ColumnFlag{FlagNullability}
	out2 := dgen(t, src, tgt, []Entry{e})
	for _, st := range append(append([]Statement{}, out2.Statements...), out2.Rollback...) {
		for _, w := range st.Warnings {
			if strings.Contains(w, "视图") {
				t.Fatalf("无视图时不该出现视图警告：%s", w)
			}
		}
	}
}

func TestAddColumnWithRiskyDefinitionWarns(t *testing.T) {
	src := dtable("t", dcol("a", "text", true, 1))
	tgtNN := dtable("t", dcol("a", "text", true, 1), dcol("b", "text", false, 2))
	tgtExpr := dtable("t",
		dcol("a", "text", true, 1),
		dwithDefault(dcol("b", "text", true, 2), "current_timestamp"),
	)
	s := dsnap(protocol.DatabaseKindSQLite, src)
	out := dgen(t, s, dsnap(protocol.DatabaseKindSQLite, tgtNN), []Entry{dentry(OpAddColumn, "t", "b")})
	eq(t, one(t, out.Statements), `ALTER TABLE "t" ADD COLUMN "b" text NOT NULL;`, "加非空列")
	eqStrs(t, out.Statements[0].Warnings, []string{WAddColumnLimits}, "非空无默认值警告")
	out2 := dgen(t, s, dsnap(protocol.DatabaseKindSQLite, tgtExpr), []Entry{dentry(OpAddColumn, "t", "b")})
	eqStrs(t, out2.Statements[0].Warnings, []string{WAddColumnLimits}, "非常量默认值警告")
}

func TestConstantDefaultRecognition(t *testing.T) {
	for _, c := range []struct {
		in   *string
		want bool
	}{
		{nil, true},
		{sp("NULL"), true},
		{sp("0"), true},
		{sp("-1"), true},
		{sp("+1.5"), true},
		{sp("1e3"), true},
		{sp("'it''s'"), true},
		{sp("x'1f'"), true},
		{sp("current_timestamp"), false},
		{sp("(1+2)"), false},
		{sp("'unterminated"), false},
		{sp("abc"), false},
	} {
		if got := isConstantDefault(c.in); got != c.want {
			t.Fatalf("isConstantDefault(%v) = %v，期望 %v", deref(c.in), got, c.want)
		}
	}
}

func TestEmptySelectionProducesEmptyScript(t *testing.T) {
	s := dsnap(protocol.DatabaseKindSQLite)
	tt := dsnap(protocol.DatabaseKindSQLite, dtable("u", dcol("a", "text", true, 1)))
	out := dgen(t, s, tt, nil)
	if len(out.Statements) != 0 || len(out.Rollback) != 0 {
		t.Fatalf("空勾选必须空脚本：%q / %q", sqls(out.Statements), sqls(out.Rollback))
	}
	if out.PragmasOutsideTx || !out.DdlInTx {
		t.Fatalf("事务标志不符：%+v", out)
	}
}

func TestMirrorIDInvertsOnlyTheOpSegment(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{mirrorID("u//create_table"), "u//drop_table"},
		{mirrorID("t/name/alter_column"), "t/name/alter_column"},
		{mirrorID("t/idx_a/add_index"), "t/idx_a/drop_index"},
		{mirrorID("t/fk(a,b)/add_foreign_key"), "t/fk(a,b)/drop_foreign_key"},
		{mirrorID("nonsense"), "nonsense"},
		{mirrorID("t/x/unknown_op"), "t/x/unknown_op"},
	} {
		eq(t, c.got, c.want, "mirror_id")
	}
}

func TestNonSQLiteDialectsAndRedisAreRejected(t *testing.T) {
	proto := func(err error) (string, string) {
		t.Helper()
		pe, ok := err.(*protocol.PolyDBError)
		if !ok {
			t.Fatalf("错误形状必须是 *protocol.PolyDBError，实得 %T（%v）", err, err)
		}
		return pe.Code, pe.Message
	}
	for _, kind := range []protocol.DatabaseKind{
		protocol.DatabaseKindPostgres,
		protocol.DatabaseKindMySQL,
		protocol.DatabaseKindMSSQL,
		protocol.DatabaseKindOracle,
	} {
		s := &Snapshot{Kind: kind, Schema: "main"}
		_, err := Generate(s, s, nil, &Options{})
		if err == nil {
			t.Fatalf("%s 必须被拒绝", kind)
		}
		code, msg := proto(err)
		eq(t, code, protocol.ErrNotSupported, "错误码")
		eq(t, msg, string(kind)+" 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）", "方言拒绝")
	}
	sqlite := &Snapshot{Kind: protocol.DatabaseKindSQLite, Schema: "main"}
	pg := &Snapshot{Kind: protocol.DatabaseKindPostgres, Schema: "public"}
	_, xerr := Generate(sqlite, pg, nil, &Options{})
	code, msg := proto(xerr)
	eq(t, code, protocol.ErrNotSupported, "错误码")
	eq(t, msg, "跨种类比对未支持：sqlite vs postgres（v1 只比同一种数据库）", "跨种类拒绝")
	redis := &Snapshot{Kind: protocol.DatabaseKindRedis}
	_, xerr = Generate(redis, redis, nil, &Options{})
	_, msg = proto(xerr)
	eq(t, msg, "Redis 无模式概念，不参与结构比对", "Redis 拒绝")
}

func TestMissingSnapshotObjectIsInvalidParam(t *testing.T) {
	s := dsnap(protocol.DatabaseKindSQLite)
	tt := dsnap(protocol.DatabaseKindSQLite, dtable("u"))
	_, err := Generate(s, tt, []Entry{dentry(OpAddColumn, "u", "zz")}, &Options{})
	pe, ok := err.(*protocol.PolyDBError)
	if !ok {
		t.Fatalf("错误形状：%T %v", err, err)
	}
	eq(t, pe.Code, protocol.ErrInvalidParam, "错误码")
	eq(t, pe.Message, "快照中缺少列 u.zz，无法生成 DDL", "错误文案")
	_, err = Generate(s, tt, []Entry{dentry(OpCreateTable, "gone", "")}, &Options{})
	pe, ok = err.(*protocol.PolyDBError)
	if !ok {
		t.Fatalf("错误形状：%T %v", err, err)
	}
	eq(t, pe.Message, "快照中缺少表 gone，无法生成 DDL", "缺表文案")
}

func TestGeneratedFromRealDiffIsSymmetric(t *testing.T) {
	// 用 E1 的真实计划（而非手写条目）验证镜像关系：正向与回滚的语句条数一致
	src := dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("name", "varchar(20)", true, 2)),
	)
	tgt := dtable("t",
		dpkCol("id", "INTEGER"),
		dcol("name", "varchar(30)", true, 2),
		dcol("extra", "text", true, 3),
	)
	extraTgt := dtable("u", dpkCol("id", "INTEGER"))
	s := dsnap(protocol.DatabaseKindSQLite, src)
	tt := dsnap(protocol.DatabaseKindSQLite, tgt, extraTgt)
	out := dgenFromDiff(t, s, tt)
	eqStrs(t, sqls(out.Statements), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "u"("id" integer NOT NULL,PRIMARY KEY ("id"));`,
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,"name" varchar(30),"extra" text,PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id","name") SELECT "id","name" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		"PRAGMA foreign_keys=ON;",
	}, "正向脚本")
	eqStrs(t, sqls(out.Rollback), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,"name" varchar(20),PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id","name") SELECT "id","name" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		`DROP TABLE "u";`,
		"PRAGMA foreign_keys=ON;",
	}, "回滚脚本")
}

// protoErr 取出 PolyDBError 的 code/message（与 Rust 的 to_protocol() 对应）。
func protoErr(t *testing.T, err error) (string, string) {
	t.Helper()
	pe, ok := err.(*protocol.PolyDBError)
	if !ok {
		t.Fatalf("错误形状必须是 *protocol.PolyDBError，实得 %T（%v）", err, err)
	}
	return pe.Code, pe.Message
}

// absorbPair 是 d14 形状：同表四条，其中只有 alter_column 触发重建。
func absorbPair() (*Snapshot, *Snapshot) {
	src := dsnap(protocol.DatabaseKindSQLite, dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("qty", "INTEGER", true, 2)),
		didx("idx_qty", false, false, "qty"),
	))
	tgt := dsnap(protocol.DatabaseKindSQLite, dwithIndexes(
		dtable("t",
			dpkCol("id", "INTEGER"),
			dcol("qty", "TEXT", true, 2),
			dcol("name", "TEXT", false, 3),
		),
		didx("idx_name", false, false, "name"),
	))
	return src, tgt
}

func TestRollbackRebuildsEveryTableTheForwardRebuilt(t *testing.T) {
	// 删一个被索引覆盖的列：正向整表重建（旧表连同它的索引一起消失）。
	// 回滚只走 ADD COLUMN + CREATE INDEX 的直改路径时，一旦某个镜像条目没对上，
	// 就会永久留下与源侧不同的定义 —— 所以正向重建过的表，回滚一律重建回源快照。
	src := dsnap(protocol.DatabaseKindSQLite, dwithIndexes(
		dtable("t", dpkCol("id", "INTEGER"), dcol("note", "text", true, 2)),
		didx("idx_note", false, false, "note"),
	))
	tgt := dsnap(protocol.DatabaseKindSQLite, dtable("t", dpkCol("id", "INTEGER")))
	opts := &Options{SelectDestructive: true}
	plan, err := Diff(src, tgt, opts)
	if err != nil {
		t.Fatal(err)
	}
	picked, err := SelectEntries(src, tgt, plan,
		[]string{"t/idx_note/drop_index", "t/note/drop_column"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(src, tgt, picked, opts)
	if err != nil {
		t.Fatal(err)
	}
	eqStrs(t, sqls(out.Statements), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id") SELECT "id" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		"PRAGMA foreign_keys=ON;",
	}, "正向脚本")
	eqStrs(t, sqls(out.Rollback), []string{
		"PRAGMA foreign_keys=OFF;",
		`CREATE TABLE "polydb_tmp_t"("id" integer NOT NULL,"note" text,PRIMARY KEY ("id"));`,
		`INSERT INTO "polydb_tmp_t"("id") SELECT "id" FROM "t";`,
		`DROP TABLE "t";`,
		`ALTER TABLE "polydb_tmp_t" RENAME TO "t";`,
		`CREATE INDEX "idx_note" ON "t"("note");`,
		"PRAGMA foreign_keys=ON;",
	}, "回滚脚本")
	// 回滚组承载的是镜像 id，按反向计划顺序
	eqStrs(t, out.Rollback[1].EntryIDs,
		[]string{"t/note/add_column", "t/idx_note/add_index"}, "回滚组条目")
}

func TestUnnamedIndexDropUsesTheExecutableName(t *testing.T) {
	// 快照里无名的 UNIQUE 索引：object 是合成的 idx(a,b)，DROP 必须打真名
	cols := []protocol.ColumnInfo{dcol("a", "text", true, 1), dcol("b", "text", true, 2)}
	src := dsnap(protocol.DatabaseKindSQLite, dwithIndexes(dtable("t", cols...),
		didx("", false, true, "a", "b")))
	tgt := dsnap(protocol.DatabaseKindSQLite, dtable("t", cols...))
	opts := &Options{SelectDestructive: true}
	plan, err := Diff(src, tgt, opts)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, plan[0].Object, "idx(a,b)", "无名索引的 object")
	picked, err := SelectEntries(src, tgt, plan, []string{"t/idx(a,b)/drop_index"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(src, tgt, picked, opts)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, one(t, out.Statements), `DROP INDEX "polydb_idx_t_a_b";`, "删除无名索引")
	eq(t, one(t, out.Rollback),
		`CREATE UNIQUE INDEX "polydb_idx_t_a_b" ON "t"("a","b");`, "回滚按可执行名建回")
}

func TestSelectEntriesKeepsPlanOrderAndSubset(t *testing.T) {
	src, tgt := absorbPair()
	plan, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(plan))
	for i, e := range plan {
		ids[i] = e.ID
	}
	eqStrs(t, ids, []string{
		"t/idx_qty/drop_index",
		"t/name/add_column",
		"t/qty/alter_column",
		"t/idx_name/add_index",
	}, "计划 id 顺序")
	// 只勾非重建表条目：合法，且输出顺序仍按计划走
	picked, err := SelectEntries(src, tgt, plan,
		[]string{"t/name/add_column", "t/idx_qty/drop_index"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(picked))
	for i, e := range picked {
		got[i] = e.ID
	}
	eqStrs(t, got, []string{"t/idx_qty/drop_index", "t/name/add_column"}, "勾选子集")
	// 空勾选 → 空计划
	none, err := SelectEntries(src, tgt, plan, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("空勾选：%v %d", err, len(none))
	}
	all, err := SelectEntries(src, tgt, plan, ids)
	if err != nil || len(all) != 4 {
		t.Fatalf("全勾 = 重建整组：%v %d", err, len(all))
	}
}

func TestSelectEntriesRejectsStaleIDs(t *testing.T) {
	src, tgt := absorbPair()
	plan, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = SelectEntries(src, tgt, plan,
		[]string{"t/nope/drop_column", "t/ghost/add_index"})
	code, msg := protoErr(t, err)
	eq(t, code, protocol.ErrInvalidParam, "错误码")
	eq(t, msg,
		"勾选的条目不在当前比对计划里：t/nope/drop_column、t/ghost/add_index（请重新比对后再执行）",
		"过期勾选")
	// 同一个过期 id 重复出现只在文案里出现一次（与 Rust select_entries_rejects_stale_ids 同断言）。
	_, err = SelectEntries(src, tgt, plan,
		[]string{"t/ghost/add_index", "t/ghost/add_index", "t/nope/drop_column"})
	code, msg = protoErr(t, err)
	eq(t, code, protocol.ErrInvalidParam, "错误码")
	eq(t, msg,
		"勾选的条目不在当前比对计划里：t/ghost/add_index、t/nope/drop_column（请重新比对后再执行）",
		"过期勾选去重")
}

func TestSelectEntriesRequiresWholeGroupForRebuiltTables(t *testing.T) {
	src, tgt := absorbPair()
	plan, err := Diff(src, tgt, &Options{})
	if err != nil {
		t.Fatal(err)
	}
	// alter_column 会让 t 整表重建：漏掉同表其它条目必须报错，
	// 否则未勾选的 name 列会被重建语句悄悄建出来。
	_, err = SelectEntries(src, tgt, plan, []string{"t/qty/alter_column"})
	code, msg := protoErr(t, err)
	eq(t, code, protocol.ErrInvalidParam, "错误码")
	eq(t, msg,
		"表 t 需要整表重建，同表变更必须整组勾选：缺少 t/idx_qty/drop_index、t/name/add_column、t/idx_name/add_index",
		"整组约束")
	// 不含重建触发者的子集不受此约束
	if _, err = SelectEntries(src, tgt, plan, []string{"t/idx_qty/drop_index"}); err != nil {
		t.Fatalf("未触发重建的子集不该报错：%v", err)
	}
}

// TestFKCycleKeysReportsOnlyLoopClosingTables 直测成环判据：自引用不算环（与拓扑排序的
// 过滤条件一致）、单向链不算环、指向本次计划外的表不算环、大小写不同的引用必须照样闭环。
func TestFKCycleKeysReportsOnlyLoopClosingTables(t *testing.T) {
	cyclic := func(tables ...TableSnapshot) []string {
		s := dsnap(protocol.DatabaseKindSQLite, tables...)
		entries := make([]Entry, 0, len(s.Tables))
		for i := range s.Tables {
			entries = append(entries, dentry(OpCreateTable, s.Tables[i].Name, ""))
		}
		return fkCycleKeys(entries, tableMap(s))
	}
	if got := cyclic(dwithFKs(
		dtable("t", dcol("parent_id", "integer", true, 1)),
		dfk([]string{"parent_id"}, "t", []string{"id"}),
	)); len(got) != 0 {
		t.Fatalf("自引用不该算环：%q", got)
	}
	if got := cyclic(
		dwithFKs(dtable("a", dcol("b_id", "integer", true, 1)), dfk([]string{"b_id"}, "b", []string{"id"})),
		dtable("b", dcol("id", "integer", false, 1)),
	); len(got) != 0 {
		t.Fatalf("单向链不该算环：%q", got)
	}
	if got := cyclic(
		dwithFKs(dtable("a", dcol("x_id", "integer", true, 1)), dfk([]string{"x_id"}, "outside", []string{"id"})),
		dtable("b", dcol("id", "integer", false, 1)),
	); len(got) != 0 {
		t.Fatalf("指向计划外的表不该算环：%q", got)
	}
	eqStrs(t, cyclic(
		dwithFKs(dtable("a", dcol("b_id", "integer", true, 1)), dfk([]string{"b_id"}, "b", []string{"id"})),
		dwithFKs(dtable("b", dcol("c_id", "integer", true, 1)), dfk([]string{"c_id"}, "c", []string{"id"})),
		// 反向引用写成大写 A：环的判据走小写键，必须照样闭环
		dwithFKs(dtable("c", dcol("a_id", "integer", true, 1)), dfk([]string{"a_id"}, "A", []string{"id"})),
	), []string{"a", "b", "c"}, "三表环")
}

// TestCreateTablesOnAForeignKeyCycleAreWarned：环上的两条 CREATE TABLE 各挂一条成环警告；
// 无环时一条都不许出。
func TestCreateTablesOnAForeignKeyCycleAreWarned(t *testing.T) {
	src := dsnap(protocol.DatabaseKindSQLite)
	cyc := dsnap(protocol.DatabaseKindSQLite,
		dwithFKs(
			dtable("orders", dcol("id", "INTEGER", false, 1), dcol("user_id", "INTEGER", true, 2)),
			dfk([]string{"user_id"}, "users", []string{"id"}),
		),
		dwithFKs(
			dtable("users", dcol("id", "INTEGER", false, 1), dcol("order_id", "INTEGER", true, 2)),
			dfk([]string{"order_id"}, "orders", []string{"id"}),
		),
	)
	entries := []Entry{dentry(OpCreateTable, "users", ""), dentry(OpCreateTable, "orders", "")}
	out := dgen(t, src, cyc, entries)
	if len(out.Statements) != 2 {
		t.Fatalf("应有两条建表语句：%q", sqls(out.Statements))
	}
	want := "外键依赖成环：2 个表（orders, users）互相引用，建表顺序无法保证父表在前（SQLite 到写入时才报错）"
	for i := range out.Statements {
		st := &out.Statements[i]
		last := st.Warnings[len(st.Warnings)-1]
		if last != want {
			t.Fatalf("%s 的环警告不符\ngot  = %q\nwant = %q", st.Table, last, want)
		}
	}
	acyclic := dsnap(protocol.DatabaseKindSQLite,
		dwithFKs(dtable("orders", dcol("user_id", "INTEGER", true, 1)),
			dfk([]string{"user_id"}, "users", []string{"id"})),
		dtable("users", dcol("id", "INTEGER", false, 1)),
	)
	out2 := dgen(t, src, acyclic, entries)
	for i := range out2.Statements {
		for _, w := range out2.Statements[i].Warnings {
			if strings.Contains(w, WFKCycle) {
				t.Fatalf("%s 不该有成环警告：%q", out2.Statements[i].Table, w)
			}
		}
	}
}

// TestCreateTableTopoOnCycleTerminatesAndIgnoresInputOrder：环上拓扑必须终止，且输出与
// 快照数组顺序无关（确定性来自小写键升序 + DFS 到达序）。逐字输出另由共享语料 17 号对拍。
func TestCreateTableTopoOnCycleTerminatesAndIgnoresInputOrder(t *testing.T) {
	users := func() TableSnapshot {
		return dwithFKs(dtable("users", dcol("id", "integer", false, 1)),
			dfk([]string{"order_id"}, "orders", []string{"id"}))
	}
	orders := func() TableSnapshot {
		return dwithFKs(dtable("orders", dcol("id", "integer", false, 1)),
			dfk([]string{"user_id"}, "users", []string{"id"}))
	}
	plan := func(tables []TableSnapshot) []string {
		src := dsnap(protocol.DatabaseKindPostgres)
		tgt := dsnap(protocol.DatabaseKindPostgres, tables...)
		out, err := Diff(src, tgt, &Options{})
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(out))
		for i := range out {
			names = append(names, out[i].Table)
		}
		return names
	}
	eqStrs(t, plan([]TableSnapshot{orders(), users()}), []string{"users", "orders"}, "先给 orders")
	eqStrs(t, plan([]TableSnapshot{users(), orders()}), []string{"users", "orders"}, "先给 users")
}
