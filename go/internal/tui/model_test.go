package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// ─── 渲染辅助 ───────────────────────────────────────────────

// TestMain 关闭心跳：自续的 tea.Tick 命令链会让 drive() 直接调用 Update 的
// 测试阻塞 30s/跳；心跳逻辑由 TestHeartbeatArming 单独覆盖。
func TestMain(t *testing.M) {
	heartbeatDisabled = true
	os.Exit(t.Run())
}

// TestHeartbeatArming 验证心跳开关与挂起/续表行为（不依赖真实定时器）。
func TestHeartbeatArming(t *testing.T) {
	defer func() { heartbeatDisabled = true }()

	// 关闭态：armHB 返回 nil。
	heartbeatDisabled = true
	m := New(nil)
	if m.armHB() != nil {
		t.Fatalf("disabled heartbeat should arm nil")
	}
	// 开启态：armHB 返回可执行命令（不运行它）。
	heartbeatDisabled = false
	if m.armHB() == nil {
		t.Fatalf("enabled heartbeat should arm cmd")
	}
	// tick 到达且有活动连接：续表 + 发起 ping（批量命令）。
	m.selectedID = "x"
	var mm tea.Model = m
	res, cmd := mm.Update(hbTickMsg{})
	if cmd == nil {
		t.Fatalf("tick with active conn should return cmd")
	}
	if md2, ok := res.(*model); !ok || md2.selectedID != "x" {
		t.Fatalf("tick should preserve state")
	}
	// 无活动连接：只续表不 ping。
	m2 := New(nil)
	res2, cmd2 := m2.Update(hbTickMsg{})
	_ = res2
	if cmd2 == nil {
		t.Fatalf("tick should re-arm even without connection")
	}
}

func TestCellString(t *testing.T) {
	cases := []struct {
		v    protocol.Value
		want string
	}{
		{protocol.NewNullValue(), "NULL"},
		{protocol.NewStringValue("alice"), "alice"},
		{protocol.NewStringValue("带引号\"x\""), "带引号\"x\""},
		{protocol.NewIntValue(42), "42"},
		{protocol.NewBoolValue(true), "true"},
		{protocol.NewFloatValue(1.5), "1.5"},
	}
	for _, c := range cases {
		if got := cellString(c.v); got != c.want {
			t.Errorf("cellString(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	// CJK 字符宽度为 2；省略号占 1 列，故 4 列宽只能放下 1 个汉字
	if got := truncate("数据库客户端", 4); got != "数…" {
		t.Errorf("truncate CJK = %q, want %q", got, "数…")
	}
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
}

func TestResultTable(t *testing.T) {
	cols := []protocol.ResultColumn{{Name: "id"}, {Name: "name"}}
	rows := [][]protocol.Value{
		{protocol.NewIntValue(1), protocol.NewStringValue("alice")},
		{protocol.NewIntValue(2), protocol.NewStringValue("bob")},
	}
	out := resultTable(cols, rows, 80, 10)
	for _, want := range []string{"id", "name", "alice", "bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("resultTable missing %q:\n%s", want, out)
		}
	}
	// 超行数截断提示
	out = resultTable(cols, rows, 80, 1)
	if !strings.Contains(out, "仅显示前 1 行") {
		t.Errorf("resultTable truncation hint missing:\n%s", out)
	}
}

func TestCellFullText(t *testing.T) {
	// JSON 对象/数组字面量自动美化
	if got := cellFullText(protocol.NewStringValue(`{"a":1}`)); !strings.Contains(got, "\n") || !strings.Contains(got, `"a": 1`) {
		t.Errorf("object not pretty-printed:\n%s", got)
	}
	if got := cellFullText(protocol.NewStringValue("[1,2]")); !strings.Contains(got, "\n") {
		t.Errorf("array not pretty-printed:\n%s", got)
	}
	// 非 JSON 原样返回；伪 JSON（无法解析）也原样
	if got := cellFullText(protocol.NewStringValue("plain")); got != "plain" {
		t.Errorf("plain = %q", got)
	}
	if got := cellFullText(protocol.NewStringValue(`{bad`)); got != "{bad" {
		t.Errorf("invalid JSON = %q, want as-is", got)
	}
	if got := cellFullText(protocol.NewNullValue()); got != "NULL" {
		t.Errorf("NULL = %q", got)
	}
	if got := cellFullText(protocol.NewIntValue(7)); got != "7" {
		t.Errorf("int = %q", got)
	}
}

func TestRenderTableCursorAndWindow(t *testing.T) {
	cols := []protocol.ResultColumn{{Name: "id"}, {Name: "name"}}
	rows := [][]protocol.Value{
		{protocol.NewIntValue(1), protocol.NewStringValue("alice")},
		{protocol.NewIntValue(2), protocol.NewStringValue("bob")},
		{protocol.NewIntValue(3), protocol.NewStringValue("carol")},
	}
	// 无光标（resultTable 包装）不出现反显转义
	if out := resultTable(cols, rows, 80, 10); strings.Contains(out, "\x1b[7m") {
		t.Errorf("no-cursor render should not highlight:\n%s", out)
	}
	// 有光标：绝对行 1 第 0 列反显
	out := renderTable(cols, rows, 80, 10, 0, 1, 0)
	if !strings.Contains(out, "\x1b[7m") {
		t.Errorf("cursor highlight missing:\n%s", out)
	}
	// 窗口化：从第 1 行起、每页 1 行 → 上方还有 + 下方还有提示
	out = renderTable(cols, rows, 80, 1, 1, 1, 0)
	if !strings.Contains(out, "上方还有 1 行") || !strings.Contains(out, "下方还有 1 行") {
		t.Errorf("window hints missing:\n%s", out)
	}
	if strings.Contains(out, "仅显示前") {
		t.Errorf("windowed render should not say 仅显示前:\n%s", out)
	}
	// startRow 越界钳制不 panic
	if out := renderTable(cols, rows, 80, 2, 10, 10, 0); out == "" {
		t.Error("clamped startRow render empty")
	}
}

func TestHeaderLabelAndStatusColor(t *testing.T) {
	cols := []protocol.ResultColumn{
		{Name: "id", DataType: "INTEGER"},
		{Name: "name"},
	}
	rows := [][]protocol.Value{{protocol.NewIntValue(1), protocol.NewStringValue("a")}}
	out := renderTable(cols, rows, 120, 10, 0, -1, -1)
	if !strings.Contains(out, "id (INTEGER)") {
		t.Errorf("header type badge missing:\n%s", out)
	}
	if strings.Contains(out, "name (") {
		t.Errorf("empty DataType header should stay bare:\n%s", out)
	}
	if got := colorStatus("查询失败: x"); !strings.HasPrefix(got, "\x1b[31m") {
		t.Errorf("failure status not red: %q", got)
	}
	if got := colorStatus("已连接"); got != "已连接" {
		t.Errorf("normal status altered: %q", got)
	}
}

// ─── 状态机驱动 ─────────────────────────────────────────────

// drive 同步推进模型：执行 cmd 获得消息，回灌 Update，直至无后续命令。
func drive(t *testing.T, m tea.Model, cmds ...tea.Cmd) tea.Model {
	t.Helper()
	for _, c := range cmds {
		if c == nil {
			continue
		}
		msg := c()
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		for cmd != nil {
			msg = cmd()
			m, cmd = m.Update(msg)
		}
	}
	return m
}

func key(t tea.KeyType) tea.Msg { return tea.KeyMsg{Type: t} }
func runeKey(r rune) tea.Msg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
func md(m tea.Model) *model     { return m.(*model) }

// setupApp 构造 appcore：内存存储 + sqlite :memory: 连接，预建表 t(id, name) 并插入一行。
func setupApp(t *testing.T) (*appcore.AppCore, string) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	app := appcore.New(db, kr)
	conn, err := app.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "tui-test",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if _, err := app.Execute(context.Background(), conn.ID, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := app.Execute(context.Background(), conn.ID, "CREATE TABLE u(id INTEGER PRIMARY KEY, name TEXT, email TEXT)"); err != nil {
		t.Fatalf("create table u: %v", err)
	}
	if _, err := app.Execute(context.Background(), conn.ID, "INSERT INTO t(name) VALUES('hello')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return app, conn.ID
}

func TestModelConnectionQueryFlow(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())

	if md(m).view != viewConns || len(md(m).conns) != 1 {
		t.Fatalf("after init: view=%v conns=%d", md(m).view, len(md(m).conns))
	}

	// Enter 打开连接 → schemas → tables
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables {
		t.Fatalf("after open: view=%v, want viewTables (status=%s)", md(m).view, md(m).status)
	}
	if len(md(m).tables) == 0 || md(m).tables[0].Name != "t" {
		t.Fatalf("tables = %+v, want [t]", md(m).tables)
	}
	if v := m.View(); !strings.Contains(v, "t") {
		t.Fatalf("tables view missing table name:\n%s", v)
	}

	// Enter 查看表详情
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTable || md(m).tableDetail == nil {
		t.Fatalf("after detail: view=%v detail=%v", md(m).view, md(m).tableDetail)
	}
	names := map[string]bool{}
	for _, c := range md(m).tableDetail.columns {
		names[c.Name] = true
	}
	if !names["id"] || !names["name"] {
		t.Fatalf("columns = %+v, want id+name", md(m).tableDetail.columns)
	}
	if !strings.Contains(m.View(), "PRIMARY") && !strings.Contains(m.View(), "PK") {
		t.Fatalf("table view should show PK flag:\n%s", m.View())
	}

	// q 进入查询视图（预填 SELECT * FROM "main"."t"）
	m, cmd = m.Update(runeKey('q'))
	if cmd != nil {
		t.Fatalf("gotoQuery should not return cmd")
	}
	if md(m).view != viewQuery || !strings.Contains(md(m).queryInput.Value(), "SELECT") {
		t.Fatalf("after q: view=%v sql=%q", md(m).view, md(m).queryInput.Value())
	}

	// F5 执行 → 结果集
	m, cmd = m.Update(key(tea.KeyF5))
	m = drive(t, m, cmd)
	if md(m).queryErr != nil {
		t.Fatalf("query error: %v", md(m).queryErr)
	}
	if md(m).queryResult == nil || len(md(m).queryResult.Rows) != 1 {
		t.Fatalf("query result = %+v, want 1 row", md(m).queryResult)
	}
	v := m.View()
	for _, want := range []string{"hello", "SELECT", "id", "name"} {
		if !strings.Contains(v, want) {
			t.Errorf("query view missing %q:\n%s", want, v)
		}
	}
}

// TestModelTableNavigation 验证 viewTable 中 ↑/↓ 切换表并加载新详情。
func TestModelTableNavigation(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m = drive(t, m, m.Init())
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables || len(md(m).tables) != 2 {
		t.Fatalf("after open: view=%v tables=%v (want 2)", md(m).view, md(m).tables)
	}
	// 进入详情
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	first := md(m).tables[0].Name
	if md(m).view != viewTable {
		t.Fatalf("not in viewTable: %v", md(m).view)
	}
	// ↓ 切换
	m, cmd = m.Update(key(tea.KeyDown))
	m = drive(t, m, cmd)
	if md(m).tableCur != 1 {
		t.Fatalf("after down: tableCur=%d", md(m).tableCur)
	}
	if got := md(m).tables[md(m).tableCur].Name; got == first {
		t.Fatalf("table did not advance; still %q", got)
	}
	// ↑ 回到首个
	m, cmd = m.Update(key(tea.KeyUp))
	m = drive(t, m, cmd)
	if md(m).tableCur != 0 || md(m).tables[md(m).tableCur].Name != first {
		t.Fatalf("after up: cur=%d name=%q want first=%q", md(m).tableCur, md(m).tables[md(m).tableCur].Name, first)
	}
}

// TestModelQueryAutocomplete 验证查询视图的 Tab 补全：先出表名，接受后再出列名。
func TestModelQueryAutocomplete(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m = drive(t, m, m.Init())
	// 打开连接
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables || len(md(m).tables) != 2 {
		t.Fatalf("setup: view=%v tables=%v", md(m).view, md(m).tables)
	}
	// 进入查询视图（q 不预填）
	m, cmd = m.Update(runeKey('q'))
	if cmd != nil {
		t.Fatalf("gotoQuery should be synchronous")
	}
	if md(m).view != viewQuery {
		t.Fatalf("view=%v", md(m).view)
	}
	// 输入 SELECT * FROM t
	for _, r := range "SELECT * FROM t" {
		m, _ = m.Update(runeKey(r))
	}
	// Tab 触发补全，应给出表名候选
	m, _ = m.Update(key(tea.KeyTab))
	if len(md(m).acCandidates) == 0 {
		t.Fatalf("no table candidates after Tab; sql=%q", md(m).queryInput.Value())
	}
	v := m.View()
	if !strings.Contains(v, "表 main") {
		t.Errorf("panel missing table header:\n%s", v)
	}
	// 找到 "t" 并选中
	idx := -1
	for i, c := range md(m).acCandidates {
		if c == "t" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("candidate 't' not found: %v", md(m).acCandidates)
	}
	// ↓ 到 idx 位置
	for md(m).acIdx < idx {
		m, _ = m.Update(key(tea.KeyDown))
	}
	if md(m).acCandidates[md(m).acIdx] != "t" {
		t.Fatalf("cursor not on 't': %q", md(m).acCandidates[md(m).acIdx])
	}
	// Enter 接受
	m, _ = m.Update(key(tea.KeyEnter))
	if got := md(m).queryInput.Value(); !strings.HasSuffix(got, "t") {
		t.Fatalf("after accept table: sql=%q", got)
	}
	// 加 "." 前缀，触发列名补全
	m, _ = m.Update(runeKey('.'))
	m, cmd = m.Update(key(tea.KeyTab))
	if cmd == nil {
		t.Fatalf("expected columns loading cmd, got none; sql=%q", md(m).queryInput.Value())
	}
	m = drive(t, m, cmd)
	if len(md(m).acCandidates) == 0 {
		t.Fatalf("no column candidates")
	}
	colNames := map[string]bool{}
	for _, c := range md(m).acCandidates {
		colNames[c] = true
	}
	if !colNames["id"] || !colNames["name"] {
		t.Fatalf("column candidates = %v", md(m).acCandidates)
	}
	if md(m).acTable != "t" {
		t.Fatalf("acTable = %q", md(m).acTable)
	}
	// Esc 关闭面板不退出视图
	m, _ = m.Update(key(tea.KeyEsc))
	if md(m).view != viewQuery || len(md(m).acCandidates) != 0 {
		t.Fatalf("after esc: view=%v cands=%v", md(m).view, md(m).acCandidates)
	}
}

// TestModelResultsNavigation 验证 F6 结果导航视图：进入、光标语义、完整值面板、Esc 两段返回。
func TestModelResultsNavigation(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter)) // 打开连接 → tables
	m = drive(t, m, cmd)
	m, cmd = m.Update(key(tea.KeyEnter)) // tables → detail
	m = drive(t, m, cmd)
	m, _ = m.Update(runeKey('q'))
	if md(m).view != viewQuery {
		t.Fatalf("setup: view=%v", md(m).view)
	}
	// 无结果时 F6 不进入
	m, _ = m.Update(key(tea.KeyF6))
	if md(m).view != viewQuery {
		t.Fatalf("F6 without result should stay in query view: %v", md(m).view)
	}
	// 执行 SELECT 后 F6 进入导航
	m, cmd = m.Update(key(tea.KeyF5))
	m = drive(t, m, cmd)
	if md(m).queryResult == nil {
		t.Fatalf("query produced no result: %v", md(m).queryErr)
	}
	m, _ = m.Update(key(tea.KeyF6))
	mm := md(m)
	if mm.view != viewResults || mm.resRow != 0 || mm.resCol != 0 || mm.cellFull {
		t.Fatalf("after F6: view=%v row=%d col=%d full=%v", mm.view, mm.resRow, mm.resCol, mm.cellFull)
	}
	// 渲染含光标定位行头与高亮
	v := m.View()
	if !strings.Contains(v, "行 1/1") || !strings.Contains(v, "\x1b[7m") {
		t.Errorf("results view missing header/highlight:\n%s", v)
	}
	// 只有 1 行 × 2 列：边界钳制
	m, _ = m.Update(key(tea.KeyDown))
	m, _ = m.Update(key(tea.KeyRight))
	mm = md(m)
	if mm.resRow != 0 || mm.resCol != 1 {
		t.Fatalf("clamped move: row=%d col=%d", mm.resRow, mm.resCol)
	}
	m, _ = m.Update(key(tea.KeyRight))
	if md(m).resCol != 1 {
		t.Fatalf("col should clamp at last column: %d", md(m).resCol)
	}
	// Enter 打开完整值面板（name=hello 非 JSON，原样显示）
	m, _ = m.Update(key(tea.KeyEnter))
	mm = md(m)
	if !mm.cellFull {
		t.Fatalf("Enter should open full-value panel")
	}
	if v := m.View(); !strings.Contains(v, "完整值") || !strings.Contains(v, "hello") {
		t.Errorf("full value panel missing:\n%s", v)
	}
	// Esc 第一段只关面板
	m, _ = m.Update(key(tea.KeyEsc))
	if md(m).cellFull || md(m).view != viewResults {
		t.Fatalf("esc should close panel only: full=%v view=%v", md(m).cellFull, md(m).view)
	}
	// Esc 第二段回查询视图
	m, _ = m.Update(key(tea.KeyEsc))
	if md(m).view != viewQuery {
		t.Fatalf("second esc should return to query: %v", md(m).view)
	}
	// 重新执行查询后光标复位
	m, cmd = m.Update(key(tea.KeyF6))
	_ = cmd
	m, cmd = m.Update(key(tea.KeyF5))
	m = drive(t, m, cmd)
	m, _ = m.Update(key(tea.KeyF6))
	if mm := md(m); mm.resRow != 0 || mm.resCol != 0 {
		t.Fatalf("cursor should reset after new query: row=%d col=%d", mm.resRow, mm.resCol)
	}
}

func TestModelBrowseNavigation(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter)) // 打开连接 → tables
	m = drive(t, m, cmd)
	if md(m).view != viewTables {
		t.Fatalf("setup: view=%v", md(m).view)
	}
	// F7 进入数据浏览（表 t：1 行 id/name）
	m, cmd = m.Update(key(tea.KeyF7))
	if md(m).view != viewBrowse || md(m).busy == "" {
		t.Fatalf("F7: view=%v busy=%q", md(m).view, md(m).busy)
	}
	m = drive(t, m, cmd)
	mm := md(m)
	if mm.brErr != nil || mm.brRes == nil {
		t.Fatalf("browse failed: err=%v", mm.brErr)
	}
	if mm.brTable != "t" || mm.brOffset != 0 || len(mm.brRes.Rows) != 1 {
		t.Fatalf("browse page: table=%q offset=%d rows=%d", mm.brTable, mm.brOffset, len(mm.brRes.Rows))
	}
	v := m.View()
	if !strings.Contains(v, "浏览 main.t") || !strings.Contains(v, "行 1–1") {
		t.Errorf("browse view missing header:\n%s", v)
	}
	if !strings.Contains(v, "\x1b[7m") {
		t.Errorf("browse view missing cursor highlight:\n%s", v)
	}
	// 单页 1 行：↓ 钳制、→ 无下一页不动
	m, _ = m.Update(key(tea.KeyDown))
	m, _ = m.Update(key(tea.KeyRight))
	mm = md(m)
	if mm.brRow != 0 || mm.brOffset != 0 {
		t.Fatalf("clamped: row=%d offset=%d", mm.brRow, mm.brOffset)
	}
	// Shift+Right 移到 name 列，Enter 打开完整值面板
	m, _ = m.Update(key(tea.KeyShiftRight))
	if md(m).brCol != 1 {
		t.Fatalf("Shift+Right should move column: %d", md(m).brCol)
	}
	m, _ = m.Update(key(tea.KeyEnter))
	if !md(m).brFull {
		t.Fatalf("Enter should open full-value panel")
	}
	if v := m.View(); !strings.Contains(v, "完整值") || !strings.Contains(v, "hello") {
		t.Errorf("browse full-value panel missing:\n%s", v)
	}
	// Esc 两段：先关面板，再回 tables（未经表详情，tableDetail==nil）
	m, _ = m.Update(key(tea.KeyEsc))
	if md(m).brFull || md(m).view != viewBrowse {
		t.Fatalf("esc should close panel only: full=%v view=%v", md(m).brFull, md(m).view)
	}
	m, _ = m.Update(key(tea.KeyEsc))
	if md(m).view != viewTables {
		t.Fatalf("second esc should return to tables: %v", md(m).view)
	}
}

func TestModelTestAndDelete(t *testing.T) {
	app, id := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m = drive(t, m, m.Init())

	// t 测试连接
	var cmd tea.Cmd
	m, cmd = m.Update(runeKey('t'))
	m = drive(t, m, cmd)
	if st := md(m).connStatus[id]; st == nil || !st.Connected {
		t.Fatalf("test status = %+v, want connected", st)
	}

	// d d 两连击删除
	m, cmd = m.Update(runeKey('d'))
	if cmd != nil || md(m).pendingDel != id {
		t.Fatalf("first d: pendingDel=%q cmd=%v", md(m).pendingDel, cmd)
	}
	m, cmd = m.Update(runeKey('d'))
	m = drive(t, m, cmd)
	if len(md(m).conns) != 0 {
		t.Fatalf("after delete: conns=%d, want 0", len(md(m).conns))
	}
}

func TestModelCreateForm(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m = drive(t, m, m.Init())

	// n 打开表单
	var cmd tea.Cmd
	m, cmd = m.Update(runeKey('n'))
	if cmd != nil || md(m).view != viewForm {
		t.Fatalf("after n: view=%v", md(m).view)
	}

	// 填名称：光标在第一字段，直接输入字符
	for _, r := range "pg-demo" {
		m, _ = m.Update(runeKey(r))
	}
	// Tab 到类型字段，→ 切到 postgres（sqlite → mysql → postgres，共两下）
	m, _ = m.Update(key(tea.KeyTab))
	m, _ = m.Update(key(tea.KeyRight))
	m, _ = m.Update(key(tea.KeyRight))
	if got := string(md(m).form.kind()); got != "postgres" {
		t.Fatalf("kind = %q, want postgres", got)
	}
	// Tab 到主机、端口、数据库、用户名，逐项输入
	m, _ = m.Update(key(tea.KeyTab))
	for _, r := range "127.0.0.1" {
		m, _ = m.Update(runeKey(r))
	}
	m, _ = m.Update(key(tea.KeyTab))
	for _, r := range "5432" {
		m, _ = m.Update(runeKey(r))
	}
	m, _ = m.Update(key(tea.KeyTab))
	for _, r := range "postgres" {
		m, _ = m.Update(runeKey(r))
	}
	m, _ = m.Update(key(tea.KeyTab))
	for _, r := range "pg" {
		m, _ = m.Update(runeKey(r))
	}
	// Tab 越过密码与 SSH 字段（均留空），到最后一项后 Enter 提交
	for i := 0; i < 7; i++ {
		m, _ = m.Update(key(tea.KeyTab))
	}

	req := md(m).form.request()
	if req.Name != "pg-demo" || req.Kind != protocol.DatabaseKindPostgres ||
		req.Host != "127.0.0.1" || req.Port != 5432 || req.Database != "postgres" || req.Username != "pg" ||
		req.Password != "" || req.SSHTunnel != nil {
		t.Fatalf("form request = %+v", req)
	}

	// Enter 提交 → 回连接列表
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewConns || len(md(m).conns) != 2 {
		t.Fatalf("after create: view=%v conns=%d (status=%s)", md(m).view, len(md(m).conns), md(m).status)
	}
}

// TestProgramSmoke 无头运行完整 tea.Program（内存输入/输出），验证 Init→tea 集成 不崩溃。
// 帧内容渲染由各模型级 View 断言覆盖；非 TTY 渲染器不落文本到输出缓冲。
func TestProgramSmoke(t *testing.T) {
	app, _ := setupApp(t)
	p := tea.NewProgram(
		New(transport.NewLocal(app)),
		tea.WithInput(strings.NewReader("q")),
		tea.WithOutput(io.Discard),
	)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not exit")
	}
}

// ─── Redis KV 视图（M6）────────────────────────────────────

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"GET user:1", []string{"GET", "user:1"}},
		{`SET k "a b c"`, []string{"SET", "k", "a b c"}},
		{`EXPIRE 'k' 60`, []string{"EXPIRE", "k", "60"}},
		{`  HSET h  f1  v1  `, []string{"HSET", "h", "f1", "v1"}},
		{``, []string{}},
		{"a\\ b", []string{"a\\", "b"}},
		// 引号内反斜杠转义
		{`SET k "a\"b"`, []string{"SET", "k", `a"b`}},
	}
	for _, c := range cases {
		got := splitArgs(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitArgs(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestKVValueText(t *testing.T) {
	str := protocol.RedisValue{Type: protocol.RedisKeyTypeString, Value: "hello"}
	if got := kvValueText(str, 10); got != "  hello" {
		t.Errorf("string value = %q", got)
	}
	li := protocol.RedisValue{Type: protocol.RedisKeyTypeList, Value: []string{"a", "b"}}
	if got := kvValueText(li, 10); !strings.Contains(got, "1) a") || !strings.Contains(got, "2) b") {
		t.Errorf("list value = %q", got)
	}
	zs := protocol.RedisValue{Type: protocol.RedisKeyTypeZSet, Value: []protocol.RedisZSetMember{{Member: "m", Score: 1.5}}}
	if got := kvValueText(zs, 10); !strings.Contains(got, "m") || !strings.Contains(got, "1.5") {
		t.Errorf("zset value = %q", got)
	}
	h := protocol.RedisValue{Type: protocol.RedisKeyTypeHash, Value: map[string]string{"f": "v"}}
	if got := kvValueText(h, 10); !strings.Contains(got, "f = v") {
		t.Errorf("hash value = %q", got)
	}
	// 截断
	many := protocol.RedisValue{Type: protocol.RedisKeyTypeList, Value: []string{"a", "b", "c", "d"}}
	if got := kvValueText(many, 2); !strings.Contains(got, "仅显示前 2 / 4 项") {
		t.Errorf("truncation hint missing: %q", got)
	}
}

func TestRedisReplyText(t *testing.T) {
	null := protocol.RedisReply{Type: protocol.RedisReplyNull}
	if got := redisReplyText(&null); got != "(nil)" {
		t.Errorf("null = %q", got)
	}
	er := protocol.RedisReply{Type: protocol.RedisReplyError, Value: "ERR no such key"}
	if got := redisReplyText(&er); got != "ERR ERR no such key" {
		t.Errorf("error = %q", got)
	}
	ikv := protocol.RedisReply{Type: protocol.RedisReplyInteger, Value: 42}
	if got := redisReplyText(&ikv); got != "42" {
		t.Errorf("integer = %q", got)
	}
	arr := protocol.RedisReply{Type: protocol.RedisReplyArray, Value: []protocol.RedisReply{
		{Type: protocol.RedisReplyBulkString, Value: "x"},
	}}
	if got := redisReplyText(&arr); !strings.Contains(got, "1) x") {
		t.Errorf("array = %q", got)
	}
}

// TestKVViewNavigation 验证 Redis KV 视图的键导航状态机（直接灌入键列表）。
func TestKVViewNavigation(t *testing.T) {
	app, _ := setupApp(t)
	m := New(transport.NewLocal(app))
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := md(m)
	mm.view = viewKV
	mm.selectedID = "fake-redis"
	mm.kvPattern = "*"
	mm.kvKeys = []protocol.RedisKeyInfo{
		{Key: "a", Type: protocol.RedisKeyTypeString},
		{Key: "b", Type: protocol.RedisKeyTypeHash},
	}
	// j / ↓ 前进
	mm.kvKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if mm.kvCur != 1 {
		t.Fatalf("after j: kvCur=%d, want 1", mm.kvCur)
	}
	mm.kvKey(tea.KeyMsg{Type: tea.KeyDown})
	if mm.kvCur != 1 {
		t.Fatalf("down at end should clamp: kvCur=%d", mm.kvCur)
	}
	// k / ↑ 后退
	mm.kvKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if mm.kvCur != 0 {
		t.Fatalf("after k: kvCur=%d, want 0", mm.kvCur)
	}
	// / 打开过滤输入
	mm.kvKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !mm.kvFilterOn {
		t.Fatalf("/ should open filter input")
	}
	// Esc 关闭过滤
	mm.kvKey(tea.KeyMsg{Type: tea.KeyEsc})
	if mm.kvFilterOn {
		t.Fatalf("esc should close filter")
	}
	// c 打开命令输入
	mm.kvKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if !mm.kvCmdOn {
		t.Fatalf("c should open command input")
	}
	// Esc 回到连接列表
	mm.kvKey(tea.KeyMsg{Type: tea.KeyEsc})
	if mm.kvCmdOn {
		t.Fatalf("esc in cmd input should close it")
	}
	// 再次进入后 Esc 退出视图
	mm.kvKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	mm.kvCmdOn = false
	mm.kvKey(tea.KeyMsg{Type: tea.KeyEsc})
	if mm.view != viewConns {
		t.Fatalf("esc from kv list should return to viewConns, got %v", mm.view)
	}
	// 渲染冒烟
	mm.view = viewKV
	if v := mm.View(); !strings.Contains(v, "db 0") || !strings.Contains(v, "a") || !strings.Contains(v, "b") {
		t.Fatalf("kv view missing content:\n%s", v)
	}
}
