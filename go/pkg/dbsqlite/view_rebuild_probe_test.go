package dbsqlite

// E6.0 取证：SQLite 视图按名字在查询期解析，「重建表」（CREATE tmp → INSERT SELECT → DROP 原表
// → RENAME）到底怎么处理依赖它的视图？docs/plan-compare-sync.md §7 一直记着"没证据就不写进规则"。
// 这里用 bundled 真引擎把四条事实钉死，结论写回该文档 A.15；是否据此改生成器另见 E6.5 的拍板。

import (
	"context"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/schemadiff"
)

func execReport(t *testing.T, c *Conn, q string) string {
	t.Helper()
	if _, err := c.Execute(context.Background(), q); err != nil {
		return "ERR: " + err.Error()
	}
	return "OK"
}

func scalarJSON(t *testing.T, c *Conn, q string) string {
	t.Helper()
	res, err := c.Execute(context.Background(), q)
	if err != nil {
		return "ERR: " + err.Error()
	}
	if len(res.Rows) == 0 {
		return "<no rows>"
	}
	raw, err := res.Rows[0][0].MarshalJSON()
	if err != nil {
		return "ERR: " + err.Error()
	}
	return string(raw)
}

// probeScript 走完整管线取脚本：真快照 → diff → 勾选该表的整组条目 → 生成。
func probeScript(t *testing.T, srcStmts, tgtStmts []string, table string) *schemadiff.Script {
	t.Helper()
	src := snapshot(t, connWith(t, srcStmts), "main")
	tgt := snapshot(t, connWith(t, tgtStmts), "main")
	p := plan(t, src, tgt)
	t.Logf("diff 条目：%+v", summary(p))
	ids := []string{}
	for _, e := range p {
		if e.Table == table {
			ids = append(ids, e.ID)
		}
	}
	sel, err := schemadiff.SelectEntries(src, tgt, p, ids)
	if err != nil {
		t.Fatalf("SelectEntries: %v", err)
	}
	script, err := schemadiff.Generate(src, tgt, sel, &schemadiff.Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, s := range script.Statements {
		t.Logf("  [%d] phase=%d 包裹条=%v 不可逆=%v 警告=%v %s",
			i, s.Phase, len(s.EntryIDs) == 0, s.NonReversible, s.Warnings, s.SQL)
	}
	t.Logf("能力位 pragmas_outside_tx=%v ddl_in_tx=%v", script.PragmasOutsideTx, script.DdlInTx)
	return script
}

var srcWithViewDDL = []string{
	"CREATE TABLE orders (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL DEFAULT 1)",
	"INSERT INTO orders (id, qty) VALUES (1, 10), (2, 20)",
	"CREATE VIEW v_orders AS SELECT id, qty FROM orders",
	"CREATE TRIGGER t_after_ins AFTER INSERT ON orders BEGIN SELECT 1; END",
}

var tgtWiderQtyDDL = []string{
	// qty 换类型 ⇒ 整表重建
	"CREATE TABLE orders (id INTEGER PRIMARY KEY, qty TEXT NOT NULL DEFAULT '')",
}

// 事实 1：依赖视图不是"静默破掉"，而是**让重建在 RENAME 那一步直接失败**——
// SQLite 的 ALTER TABLE RENAME 会重解析所有引用该名字的视图，此时原表刚被 DROP，视图解析不出来。
func TestRebuildWithDependentViewFailsAtRename(t *testing.T) {
	script := probeScript(t, srcWithViewDDL, tgtWiderQtyDDL, "orders")
	c := connWith(t, srcWithViewDDL)

	results := []string{}
	for _, s := range script.Statements {
		results = append(results, execReport(t, c, s.SQL))
	}
	t.Logf("逐条执行结果：%v", results)

	rename := -1
	for i, s := range script.Statements {
		if strings.HasPrefix(s.SQL, "ALTER TABLE") {
			rename = i
		}
	}
	if rename < 0 {
		t.Fatal("取证前提：这是整表重建脚本")
	}
	if !strings.Contains(results[rename], "no such table: main.orders") {
		t.Fatalf("RENAME 必须因依赖视图而失败，实际：%s", results[rename])
	}
	if !strings.Contains(results[rename], "error in view v_orders") {
		t.Fatalf("错误里必须点出是哪个视图，实际：%s", results[rename])
	}
	// 逐条（等价 ddl_in_tx=false 的分派）跑完 ⇒ 中间态是**丢表**：原表已 DROP，数据困在临时表里。
	if got := execReport(t, c, "SELECT count(*) FROM orders"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("中间态下 orders 应已不存在：%s", got)
	}
	if got := scalarJSON(t, c, "SELECT count(*) FROM polydb_tmp_orders"); got != "2" {
		t.Fatalf("数据应困在临时表里（2 行）：%s", got)
	}
	// 触发器随原表一起消失（与取证 G4 一致）。
	if got := scalarJSON(t, c, "SELECT count(*) FROM sqlite_master WHERE type='trigger'"); got != "0" {
		t.Fatalf("触发器应随 DROP 一起没了：%s", got)
	}
}

// 事实 2：SQLite 的真实分派是「首尾 PRAGMA 事务外 + 主体单事务」。走这一支时 RENAME 失败
// ⇒ 整体回滚 ⇒ 表、数据、视图、列定义全部毫发无损。事务保护在这条路径上确实兜住了。
func TestRebuildWithDependentViewRollsBackCleanInsideTx(t *testing.T) {
	script := probeScript(t, srcWithViewDDL, tgtWiderQtyDDL, "orders")
	c := connWith(t, srcWithViewDDL)

	if got := execReport(t, c, "BEGIN"); got != "OK" {
		t.Fatalf("BEGIN：%s", got)
	}
	failed := ""
	for _, s := range script.Statements {
		if s.Phase == 0 || s.Phase == 255 {
			continue // 取证 G6：PRAGMA 包裹条本就在事务外
		}
		if r := execReport(t, c, s.SQL); strings.HasPrefix(r, "ERR") {
			failed = s.SQL + " ⇒ " + r
			break
		}
	}
	if failed == "" {
		t.Fatal("取证前提：主体必须真的失败")
	}
	t.Logf("首条失败：%s", failed)
	if got := execReport(t, c, "ROLLBACK"); got != "OK" {
		t.Fatalf("ROLLBACK：%s", got)
	}
	for _, q := range []string{
		"SELECT count(*) FROM orders",
		"SELECT count(*) FROM v_orders",
		"SELECT typeof(qty) FROM orders LIMIT 1",
		"SELECT count(*) FROM polydb_tmp_orders", // 回滚后临时表不该残留
	} {
		t.Logf("  回滚后 %-46s ⇒ %s", q, scalarJSON(t, c, q))
	}
	if got := scalarJSON(t, c, "SELECT count(*) FROM orders"); got != "2" {
		t.Fatalf("回滚后 orders 行数应复原：%s", got)
	}
	if got := scalarJSON(t, c, "SELECT count(*) FROM v_orders"); got != "2" {
		t.Fatalf("回滚后视图必须还能查：%s", got)
	}
	if got := scalarJSON(t, c, "SELECT count(*) FROM polydb_tmp_orders"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("回滚后临时表不该残留：%s", got)
	}
}

// 事实 3：不涉及重建、只是 drop_table 时，DROP **静默成功**并把破视图留在库里
// （sqlite_master 里条目还在，查询才报错）——与取证 G4「重建静默丢触发器」同类，
// 而生成器目前对此一个字都不说。
func TestDropTableLeavesDanglingViewSilently(t *testing.T) {
	src := snapshot(t, connWith(t, srcWithViewDDL), "main")
	tgt := snapshot(t, connWith(t, nil), "main")
	p := plan(t, src, tgt)
	ids := []string{}
	for _, e := range p {
		ids = append(ids, e.ID)
	}
	sel, err := schemadiff.SelectEntries(src, tgt, p, ids)
	if err != nil {
		t.Fatalf("SelectEntries: %v", err)
	}
	script, err := schemadiff.Generate(src, tgt, sel, &schemadiff.Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var dropWarnings []string
	for _, s := range script.Statements {
		if strings.HasPrefix(s.SQL, "DROP TABLE") {
			dropWarnings = s.Warnings
		}
	}
	t.Logf("drop_table 语句的警告：%v（diff 条目：%+v）", dropWarnings, summary(p))

	c := connWith(t, srcWithViewDDL)
	for _, s := range script.Statements {
		if r := execReport(t, c, s.SQL); r != "OK" {
			t.Fatalf("删表脚本必须全程无错（静默）：%s", r)
		}
	}
	if got := scalarJSON(t, c, "SELECT count(*) FROM sqlite_master WHERE type='view'"); got != "1" {
		t.Fatalf("SQLite 不会连带删视图，sqlite_master 里应还留着：%s", got)
	}
	if got := execReport(t, c, "SELECT * FROM v_orders"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("视图此时必须已破：%s", got)
	}
	// 今天生成器对"删表会留下破视图"一个字都不说（是否补警告见 E6.5 拍板，故此处只记录不断言）。
	t.Logf("实测：删表语句现有警告 = %v", dropWarnings)
}

// 事实 4：生成器的临时名去重集看不见视图 ⇒ 库里已有同名**视图**时脚本一开始就撞车。
func TestTempNameCollidesWithExistingView(t *testing.T) {
	srcDDL := []string{
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL DEFAULT 1)",
		"INSERT INTO orders (id, qty) VALUES (1, 10)",
		"CREATE VIEW polydb_tmp_orders AS SELECT 1 AS one",
	}
	script := probeScript(t, srcDDL, tgtWiderQtyDDL, "orders")
	if !strings.Contains(script.Statements[1].SQL, `"polydb_tmp_orders"`) {
		t.Fatalf("取证前提：生成器用的临时名必须正好是那个视图名：%s", script.Statements[1].SQL)
	}
	c := connWith(t, srcDDL)
	first := ""
	for _, s := range script.Statements {
		if r := execReport(t, c, s.SQL); strings.HasPrefix(r, "ERR") {
			first = s.SQL + " ⇒ " + r
			break
		}
	}
	t.Logf("首个失败：%s", first)
	if !strings.Contains(first, "already exists") {
		t.Fatalf("临时名撞上同名视图必须立刻失败，实际：%q", first)
	}
}
