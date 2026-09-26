package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

// 结构比对与同步向导（E5.2）的按键驱动测试：真 app-core + Local transport，
// 基准侧与期望侧各开一个 :memory: SQLite，比对/生成/执行/回滚全链路真跑。

// addSQLiteConn 再建一个 SQLite 连接（:memory: 各自独立），并按 ddl 建表。
func addSQLiteConn(t *testing.T, app *appcore.AppCore, name string, ddl ...string) string {
	t.Helper()
	conn, err := app.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     name,
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create connection %s: %v", name, err)
	}
	for _, s := range ddl {
		if _, err := app.Execute(context.Background(), conn.ID, s); err != nil {
			t.Fatalf("exec %q on %s: %v", s, name, err)
		}
	}
	return conn.ID
}

// openTables 把模型推进到库表视图（已打开 setupApp 的首个连接）。
func openTables(t *testing.T, app *appcore.AppCore) tea.Model {
	t.Helper()
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, m.Init())
	m, cmd := m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables {
		t.Fatalf("setup: view=%v want viewTables (status=%s)", md(m).view, md(m).status)
	}
	return m
}

// enterCompare 按 c 进入向导并断言落在第①段。
func enterCompare(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m, cmd := m.Update(runeKey('c'))
	if cmd != nil {
		t.Fatalf("openCompare should be synchronous")
	}
	if md(m).view != viewCompare || md(m).cmpStage != cmpStagePick {
		t.Fatalf("after c: view=%v stage=%v", md(m).view, md(m).cmpStage)
	}
	return m
}

// cmpEntryIDs 返回向导里当前计划的条目 id（服务端原序）。
func cmpEntryIDs(m tea.Model) []string {
	out := []string{}
	for _, e := range md(m).cmpEntries {
		out = append(out, e.ID)
	}
	return out
}

// realDiff 绕开向导直接问 app-core，用来复核界面断言不是自说自话。
func realDiff(t *testing.T, app *appcore.AppCore, source, target string) []string {
	t.Helper()
	plan, err := app.SchemaDiff(context.Background(), &protocol.SchemaDiffRequest{
		Source: protocol.SchemaDiffTarget{ConnectionID: source},
		Target: protocol.SchemaDiffTarget{ConnectionID: target},
	})
	if err != nil {
		t.Fatalf("real diff: %v", err)
	}
	ids := []string{}
	for _, e := range plan.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// fillGapsAndGenerate 按 f 补齐重建组缺口后按 g 生成脚本，断言落到第③段。
func fillGapsAndGenerate(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m, cmd := m.Update(runeKey('f'))
	if cmd != nil {
		t.Fatalf("f should be synchronous")
	}
	if len(md(m).cmpGaps) != 0 {
		t.Fatalf("f left gaps: %+v", md(m).cmpGaps)
	}
	m, cmd = m.Update(runeKey('g'))
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStageScript {
		t.Fatalf("after g: stage=%v status=%s", md(m).cmpStage, md(m).status)
	}
	return m
}

func TestCompareWizardSyncAndRollback(t *testing.T) {
	app, baseID := setupApp(t)
	const tgtName = "期望库"
	// 期望侧：t.name 变宽 + 多一列 email、新增 new_t；基准侧独有的 u 在期望侧没有 ⇒ 删表（破坏性）
	tgtID := addSQLiteConn(t, app, tgtName,
		`CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(30), email TEXT)`,
		`CREATE TABLE new_t(id INTEGER)`)
	const wantJoined = "u//drop_table,new_t//create_table,t/email/add_column,t/name/alter_column"

	m := openTables(t, app)
	if len(md(m).conns) != 2 {
		t.Fatalf("conns=%d want 2", len(md(m).conns))
	}
	m = enterCompare(t, m)
	if got := len(md(m).cmpCandidates); got != 2 {
		t.Fatalf("candidates=%d want 2（同类 sqlite 全进候选）", got)
	}

	// ① 光标下移一位（候选首个是基准侧自己），Enter 比对
	m, cmd := m.Update(runeKey('j'))
	if got := md(m).connName(md(m).cmpTargetID()); got != tgtName {
		t.Fatalf("cursor on %q want %q", got, tgtName)
	}
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStageDiff {
		t.Fatalf("after diff: stage=%v status=%s", md(m).cmpStage, md(m).status)
	}
	if got := strings.Join(cmpEntryIDs(m), ","); got != wantJoined {
		t.Fatalf("entries=%s want %s", got, wantJoined)
	}
	if len(md(m).cmpSel) != 3 {
		t.Fatalf("default selection=%v want 3 条（破坏性不勾）", md(m).cmpSel)
	}
	if !strings.Contains(m.View(), "删除表（相位 40）") {
		t.Fatalf("phase header missing:\n%s", m.View())
	}

	// ↓↓ 到 t/email/add_column，space 取消勾选 ⇒ 同表重建组出现缺口 ⇒ g 被挡
	m, _ = m.Update(key(tea.KeyDown))
	m, _ = m.Update(key(tea.KeyDown))
	if got := md(m).cmpEntries[md(m).cmpCur].ID; got != "t/email/add_column" {
		t.Fatalf("cursor on %q", got)
	}
	m, cmd = m.Update(key(tea.KeySpace))
	if cmd != nil || len(m.(*model).cmpGaps) == 0 {
		t.Fatalf("unchecking a rebuild-table entry should open a gap (cmd=%v gaps=%v)", cmd, md(m).cmpGaps)
	}
	m, cmd = m.Update(runeKey('g'))
	if cmd != nil || !strings.Contains(md(m).status, "重建组") {
		t.Fatalf("g must be blocked by gap: cmd=%v status=%q", cmd, md(m).status)
	}
	m = fillGapsAndGenerate(t, m)
	s := md(m).cmpScript
	if s == nil || len(s.Statements) == 0 {
		t.Fatalf("script empty: %+v status=%s", s, md(m).status)
	}
	// 重建表 ⇒ 首尾 PRAGMA 包裹（相位 0 / 255），且两个能力位都为真
	if s.Statements[0].Phase != 0 || s.Statements[len(s.Statements)-1].Phase != 255 {
		t.Fatalf("pragma wrap missing: first=%d last=%d", s.Statements[0].Phase, s.Statements[len(s.Statements)-1].Phase)
	}
	if !s.PragmasOutsideTx || !s.DdlInTx {
		t.Fatalf("capability bits: pragma_outside=%v ddl_in_tx=%v", s.PragmasOutsideTx, s.DdlInTx)
	}
	if v := m.View(); !strings.Contains(v, "正向语句") || !strings.Contains(v, "单事务") {
		t.Fatalf("script view missing headings:\n%s", v)
	}

	// x 执行正向
	m, cmd = m.Update(runeKey('x'))
	m = drive(t, m, cmd)
	if !md(m).cmpApplied {
		t.Fatalf("forward apply failed: status=%s outcome=%+v", md(m).status, md(m).cmpOutcome)
	}
	if o := md(m).cmpOutcome; o == nil || o.Ran != len(s.Statements) || len(o.Failed) != 0 {
		t.Fatalf("outcome=%+v want Ran=%d", md(m).cmpOutcome, len(s.Statements))
	}
	if got := strings.Join(realDiff(t, app, baseID, tgtID), ","); got != "u//drop_table" {
		t.Fatalf("after sync: %s want u//drop_table", got)
	}

	// c 复查：只动状态行，不覆盖向导里的计划
	before := strings.Join(cmpEntryIDs(m), ",")
	m, cmd = m.Update(runeKey('c'))
	m = drive(t, m, cmd)
	if !strings.Contains(md(m).status, "仍有 1 条差异") {
		t.Fatalf("recheck status=%q", md(m).status)
	}
	if strings.Join(cmpEntryIDs(m), ",") != before {
		t.Fatalf("recheck must not overwrite the wizard plan")
	}

	// r 代跑回滚：确认词输错 ⇒ 不执行
	m, cmd = m.Update(runeKey('r'))
	if cmd != nil || !md(m).cmpConfirmOn {
		t.Fatalf("r should open confirm: cmd=%v on=%v", cmd, md(m).cmpConfirmOn)
	}
	m = typeInto(t, m, "nope")
	m, cmd = m.Update(key(tea.KeyEnter))
	if cmd != nil || md(m).cmpConfirmOn || !strings.Contains(md(m).status, "不符") {
		t.Fatalf("wrong confirm word must not execute: cmd=%v status=%q", cmd, md(m).status)
	}
	if got := strings.Join(realDiff(t, app, baseID, tgtID), ","); got != "u//drop_table" {
		t.Fatalf("rollback should not have run: %s", got)
	}

	// 再按 r，逐字输入基准侧连接名 ⇒ 回滚脚本代跑
	m, cmd = m.Update(runeKey('r'))
	if cmd != nil || !md(m).cmpConfirmOn {
		t.Fatalf("r should open confirm again")
	}
	m = typeInto(t, m, "tui-test")
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpApplied {
		t.Fatalf("applied should reset after rollback")
	}
	if !strings.Contains(md(m).status, "回滚脚本已执行") {
		t.Fatalf("rollback status=%q outcome=%+v", md(m).status, md(m).cmpOutcome)
	}
	if got := strings.Join(realDiff(t, app, baseID, tgtID), ","); got != wantJoined {
		t.Fatalf("after rollback: %s want %s", got, wantJoined)
	}
}

func TestCompareWizardEntryGuards(t *testing.T) {
	app, _ := setupApp(t)
	ro := true
	m := New(transport.NewLocal(app))
	m.view = viewTables

	// Redis 基准侧不参与比对
	m.conns = []protocol.ConnectionInfo{{ID: "r1", Name: "redis", Kind: protocol.DatabaseKindRedis}}
	m.selectedID = "r1"
	m.openCompare()
	if m.view != viewTables || !strings.Contains(m.status, "Redis") {
		t.Fatalf("redis guard: view=%v status=%q", m.view, m.status)
	}

	// 只读基准侧：执行侧不可写，挡在门外
	m.conns = []protocol.ConnectionInfo{{ID: "s1", Name: "ro", Kind: protocol.DatabaseKindSQLite, ReadOnly: &ro}}
	m.selectedID = "s1"
	m.openCompare()
	if m.view != viewTables || !strings.Contains(m.status, "只读") {
		t.Fatalf("readonly guard: view=%v status=%q", m.view, m.status)
	}

	// 基准侧连接信息缺失
	m.conns = nil
	m.selectedID = "ghost"
	m.openCompare()
	if m.view != viewTables || !strings.Contains(m.status, "基准侧连接信息缺失") {
		t.Fatalf("missing conn: view=%v status=%q", m.view, m.status)
	}
}

// TestCompareWizardBackChain 校验三段后退与退出：②→①→回到进入时的视图。
func TestCompareWizardBackChain(t *testing.T) {
	app, _ := setupApp(t)
	addSQLiteConn(t, app, "期望库", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, email TEXT)`)
	m := openTables(t, app)
	m = enterCompare(t, m)
	m, cmd := m.Update(key(tea.KeyEnter)) // 比对基准侧自己 ⇒ 空计划
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStageDiff || len(md(m).cmpEntries) != 0 {
		t.Fatalf("self-compare: stage=%v entries=%v", md(m).cmpStage, cmpEntryIDs(m))
	}
	if !strings.Contains(md(m).status, "结构一致") {
		t.Fatalf("empty plan status=%q", md(m).status)
	}
	// 空计划里按键不炸、不发包
	m, cmd = m.Update(runeKey('g'))
	if cmd != nil {
		t.Fatalf("g on empty plan should be blocked")
	}
	m, cmd = m.Update(key(tea.KeyDown))
	m, cmd = m.Update(key(tea.KeySpace))
	m, cmd = m.Update(key(tea.KeyEsc))
	if cmd != nil || md(m).cmpStage != cmpStagePick {
		t.Fatalf("esc from ②: stage=%v cmd=%v", md(m).cmpStage, cmd)
	}
	m, cmd = m.Update(key(tea.KeyEsc))
	if cmd != nil || md(m).view != viewTables {
		t.Fatalf("esc from ① should exit wizard: view=%v", md(m).view)
	}
}

// TestCompareWizardHelpAndFooter 保证每段都有页脚与键位表（F1 面板不缺字）。
func TestCompareWizardHelpAndFooter(t *testing.T) {
	app, _ := setupApp(t)
	addSQLiteConn(t, app, "期望库", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, email TEXT)`)
	m := openTables(t, app)
	m = enterCompare(t, m)
	for _, stage := range []cmpStage{cmpStagePick, cmpStageDiff, cmpStageScript} {
		md(m).cmpStage = stage
		if md(m).footer() == "" {
			t.Fatalf("empty footer for stage %v", stage)
		}
		if len(md(m).viewHelp()) == 0 {
			t.Fatalf("empty help for stage %v", stage)
		}
		if !strings.Contains(md(m).title(), "结构比对与同步") {
			t.Fatalf("title=%q", md(m).title())
		}
	}
	md(m).cmpStage = cmpStageScript
	if !strings.Contains(md(m).footer(), "x 执行正向") {
		t.Fatalf("script footer=%q", md(m).footer())
	}
	md(m).cmpConfirmOn = true
	if !strings.Contains(md(m).footer(), "基准侧连接名") {
		t.Fatalf("confirm footer=%q", md(m).footer())
	}
	if !strings.Contains(md(m).viewHelp()[0].desc, "基准侧连接名") {
		t.Fatalf("confirm help=%+v", md(m).viewHelp())
	}
	md(m).cmpConfirmOn = false
	// i/n 两个服务端口径选项都要出现在页脚与键位表里（与 GUI/Web 的可选项对齐）
	md(m).cmpStage = cmpStageDiff
	if !strings.Contains(md(m).footer(), "i 忽略默认值") {
		t.Fatalf("diff footer=%q", md(m).footer())
	}
	var hasI bool
	for _, e := range md(m).viewHelp() {
		if e.key == "i" {
			hasI = true
		}
	}
	if !hasI {
		t.Fatalf("diff help missing i: %+v", md(m).viewHelp())
	}
	if !strings.Contains(md(m).helpView(), "结构比对与同步") {
		t.Fatalf("help panel missing view name:\n%s", md(m).helpView())
	}
	// 渲染三段正文都不 panic（脚本段此时无脚本，走空态文案）
	for _, stage := range []cmpStage{cmpStagePick, cmpStageDiff, cmpStageScript} {
		md(m).cmpStage = stage
		if md(m).compareView() == "" {
			t.Fatalf("empty body for stage %v", stage)
		}
	}
}

// TestApplyScriptSyncDispatch 直接测执行编排：事务内失败要回滚，
// ddl_in_tx=false 时逐条跑、失败留下中间态；PRAGMA 首尾留在事务外。
func TestApplyScriptSyncDispatch(t *testing.T) {
	app, id := setupApp(t)
	local := transport.NewLocal(app)

	// 包事务：首条就炸 ⇒ 一条没跑成、事务已回滚
	out := applyScriptSync(local, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `ALTER TABLE nope ADD COLUMN x TEXT;`, Phase: 60, Table: "nope"},
		},
		DdlInTx: true,
	}, cmpRunForward)
	if out.Ran != 0 || len(out.Failed) != 1 || !out.RolledBack {
		t.Fatalf("tx failure: %+v", out)
	}

	// 不包事务：第一条落地、第二条失败 ⇒ 中间态留着、没得回滚
	out = applyScriptSync(local, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `CREATE TABLE ok1(a INTEGER);`, Phase: 50, Table: "ok1"},
			{SQL: `CREATE TABLE ok1(b INTEGER);`, Phase: 50, Table: "ok1"},
			{SQL: `CREATE TABLE ok2(c INTEGER);`, Phase: 50, Table: "ok2"},
		},
	}, cmpRunForward)
	if out.Ran != 1 || len(out.Failed) != 1 || out.RolledBack {
		t.Fatalf("non-tx failure: %+v", out)
	}
	if f := out.Failed[0]; f.Index != 1 || !strings.Contains(f.Reason, "ok1") {
		t.Fatalf("failed statement: %+v", f)
	}
	if out.Skipped != 1 {
		t.Fatalf("skipped=%d want 1", out.Skipped)
	}
	if !strings.Contains(out.Failed[0].SQL, "CREATE TABLE ok1(b") {
		t.Fatalf("failed sql=%q", out.Failed[0].SQL)
	}

	// PRAGMA 包裹：首尾在事务外、主体在事务内，三条全落地
	out = applyScriptSync(local, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `PRAGMA foreign_keys=OFF;`, Phase: 0, Table: ""},
			{SQL: `CREATE TABLE ok3(d INTEGER);`, Phase: 50, Table: "ok3"},
			{SQL: `PRAGMA foreign_keys=ON;`, Phase: 255, Table: ""},
		},
		DdlInTx:          true,
		PragmasOutsideTx: true,
	}, cmpRunForward)
	if out.Ran != 3 || len(out.Failed) != 0 {
		t.Fatalf("pragma wrapped run: %+v", out)
	}
	if _, err := app.Execute(context.Background(), id, `SELECT d FROM ok3`); err != nil {
		t.Fatalf("ok3 should exist after wrapped run: %v", err)
	}
}

// TestCompareScriptViewScrollWindow 脚本长于可视区时必须开窗口并给出行数提示：
// 否则 cmpScroll 是死字段，长脚本会把终端推爆。
func TestCompareScriptViewScrollWindow(t *testing.T) {
	app, baseID := setupApp(t)
	m := New(transport.NewLocal(app))
	m.height, m.width = 16, 80
	m.view, m.cmpStage = viewCompare, cmpStageScript
	m.cmpBase = protocol.ConnectionInfo{ID: baseID, Name: "tui-test", Kind: protocol.DatabaseKindSQLite}
	stmts := make([]protocol.DdlStatement, 0, 40)
	for i := range 40 {
		stmts = append(stmts, protocol.DdlStatement{
			SQL:   fmt.Sprintf("ALTER TABLE t%d ADD COLUMN c%d TEXT;", i, i),
			Phase: 70, Table: fmt.Sprintf("t%d", i),
		})
	}
	m.cmpScript = &protocol.DdlScript{Statements: stmts, DdlInTx: true, PragmasOutsideTx: true}

	body := strings.Split(m.compareView(), "\n")
	if len(body) > m.cmpAvail()+8 {
		t.Fatalf("unbounded body: %d lines\n%s", len(body), strings.Join(body, "\n"))
	}
	// 未滚动：看得到首条、看不到末条，尾部有行数提示
	if v := m.compareView(); !strings.Contains(v, "ALTER TABLE t0 ") || strings.Contains(v, "t39 ADD COLUMN") || !strings.Contains(v, "下方还有") {
		t.Fatalf("head window wrong:\n%s", v)
	}
	for range 20 {
		m.cmpScrollDown(1)
	}
	v := m.compareView()
	if strings.Contains(v, "ALTER TABLE t0 ") || !strings.Contains(v, "t39") {
		t.Fatalf("scroll did not window the body:\n%s", v)
	}
	if !strings.Contains(v, "上方还有") {
		t.Fatalf("missing head hint after scroll:\n%s", v)
	}
}

// TestCompareWizardOptionKeys i/n 是两个**服务端**口径选项：按下去要翻状态并立刻
// 重跑比对（勾选集由服务端按选项重算），且比对结果为空时也改得动——否则误开
// 「忽略默认值」后发现两侧一致，就被空计划的早退锁死关不掉了。
func TestCompareWizardOptionKeys(t *testing.T) {
	app, _ := setupApp(t)
	addSQLiteConn(t, app, "期望库", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`)
	m := openTables(t, app)
	m = enterCompare(t, m)
	m, cmd := m.Update(runeKey('j'))
	if cmd != nil {
		t.Fatalf("j should be synchronous")
	}
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStageDiff {
		t.Fatalf("stage=%v want diff", md(m).cmpStage)
	}
	if len(md(m).cmpEntries) == 0 {
		t.Fatalf("setup: 无可勾条目，选项键无从验证")
	}

	m, cmd = m.Update(runeKey('i'))
	if cmd == nil {
		t.Fatalf("i 必须重跑比对：选项在服务端生效")
	}
	if !md(m).cmpIgnoreOpt {
		t.Fatalf("i 未切换 ignore_defaults")
	}
	if !strings.Contains(md(m).status, "忽略默认值 = 开") {
		t.Fatalf("status=%q", md(m).status)
	}
	if opt := md(m).cmpDiffRequest(md(m).cmpTargetID()).Options; !opt.IgnoreDefaults || opt.SelectDestructive {
		t.Fatalf("比对请求选项=%+v want ignore_defaults only", opt)
	}
	// 比对在途时 busy 门禁会吞键，必须先让这一轮落地再按 n
	m = drive(t, m, cmd)

	m, cmd = m.Update(runeKey('n'))
	if cmd == nil {
		t.Fatalf("n 必须重跑比对")
	}
	if opt := md(m).cmpDiffRequest(md(m).cmpTargetID()).Options; !opt.IgnoreDefaults || !opt.SelectDestructive {
		t.Fatalf("n 之后选项=%+v want both on（别把 i 顶掉）", opt)
	}
	m = drive(t, m, cmd)

	// 空计划（两侧一致）时 i 仍要能关回
	md(m).cmpEntries = nil
	m, _ = m.Update(runeKey('i'))
	if md(m).cmpIgnoreOpt {
		t.Fatalf("空计划下 i 被锁死，关不掉 ignore_defaults")
	}
}
