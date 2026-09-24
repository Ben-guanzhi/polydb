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
	// 固定主题档位：非 tty 下 termenv 自动探测为 Ascii 会剥离样式，
	// 令断言 ANSI 的测试不确定；显式设 ANSI 深色底使样式可预期。
	os.Setenv("POLYDB_TUI_COLOR", "ansi")
	os.Setenv("POLYDB_TUI_THEME", "dark")
	initTheme()
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

// enterBrowse 驱动模型进入 viewBrowse（表 t：id=1, name=hello 单行）。
func enterBrowse(t *testing.T) tea.Model {
	t.Helper()
	m, _ := enterBrowseApp(t)
	return m
}

// typeInto 向当前聚焦的 textinput 逐字符输入。
func typeInto(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		var cmd tea.Cmd
		m, cmd = m.Update(runeKey(r))
		m = drive(t, m, cmd)
	}
	return m
}

func TestModelBrowseFilter(t *testing.T) {
	m := enterBrowse(t)
	if mm := md(m); mm.brErr != nil || len(mm.brRes.Rows) != 1 {
		t.Fatalf("setup browse: err=%v rows=%d", mm.brErr, len(mm.brRes.Rows))
	}
	// / 进入过滤栏编辑，草稿落在当前列 id、op=eq、值字段聚焦
	var cmd tea.Cmd
	m, cmd = m.Update(runeKey('/'))
	m = drive(t, m, cmd)
	if mm := md(m); !mm.brEditing || mm.brDraft.Column != "id" || mm.brField != 2 {
		t.Fatalf("enter filter: editing=%v col=%q field=%d", mm.brEditing, mm.brDraft.Column, mm.brField)
	}
	m = typeInto(t, m, "1")
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	mm := md(m)
	if mm.brEditing || len(mm.brConds) != 1 {
		t.Fatalf("apply filter: editing=%v conds=%d", mm.brEditing, len(mm.brConds))
	}
	if mm.brConds[0].Column != "id" || mm.brConds[0].Op != protocol.FilterOpEq {
		t.Fatalf("condition: %+v", mm.brConds[0])
	}
	if mm.brRes == nil || len(mm.brRes.Rows) != 1 {
		t.Fatalf("id=1 should match 1 row, got %+v", mm.brRes)
	}
	if v := m.View(); !strings.Contains(v, "过滤:") || !strings.Contains(v, "id =") {
		t.Errorf("filter bar missing:\n%s", v)
	}
	// 非匹配过滤 → 0 行
	m, cmd = m.Update(runeKey('/'))
	m = drive(t, m, cmd)
	m = typeInto(t, m, "999")
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if r := md(m).brRes; r == nil || len(r.Rows) != 0 {
		t.Fatalf("id=999 should match 0 rows, got %+v", r)
	}
}

func TestModelBrowseCountSort(t *testing.T) {
	m := enterBrowse(t)
	m, cmd := m.Update(runeKey('c'))
	m = drive(t, m, cmd)
	mm := md(m)
	if !mm.brCountOK || mm.brCount == nil || *mm.brCount != 1 {
		t.Fatalf("count should be 1: ok=%v count=%v", mm.brCountOK, mm.brCount)
	}
	if v := m.View(); !strings.Contains(v, "共 1 行") {
		t.Errorf("count summary missing:\n%s", v)
	}
	m, cmd = m.Update(runeKey('s'))
	m = drive(t, m, cmd)
	mm = md(m)
	if len(mm.brSort) != 1 || mm.brSort[0].Column != "id" || mm.brSort[0].Dir != protocol.SortAsc {
		t.Fatalf("sort: %+v", mm.brSort)
	}
	m, cmd = m.Update(runeKey('s'))
	m = drive(t, m, cmd)
	if d := md(m).brSort[0].Dir; d != protocol.SortDesc {
		t.Fatalf("second sort toggle should be desc, got %s", d)
	}
}

// enterBrowseApp 同 enterBrowse，但额外返回 appcore 以便直接核对库内数据。
func enterBrowseApp(t *testing.T) (tea.Model, *appcore.AppCore) {
	t.Helper()
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	m, cmd = m.Update(key(tea.KeyF7))
	m = drive(t, m, cmd)
	return m, app
}

// TestModelBrowseEditCommit 覆盖 T3 端到端：单元格编辑入队 → 脏格显示 → 审阅 →
// 单事务提交 → 重新拉取后新值落库；并覆盖撤销与只读/无主键门禁。
func TestModelBrowseEditCommit(t *testing.T) {
	m, app := enterBrowseApp(t)
	mm := md(m)
	if mm.brErr != nil || len(mm.brRes.Rows) != 1 {
		t.Fatalf("setup browse: err=%v rows=%+v", mm.brErr, mm.brRes)
	}
	if len(mm.brPK) != 1 || mm.brPK[0] != "id" {
		t.Fatalf("PK 应经 ListColumns 推导为 [id]，实际 %v", mm.brPK)
	}
	// 光标移到 name 列（id 在 col 0）
	m = drive(t, m, func() tea.Msg { return key(tea.KeyShiftRight) })
	if mm := md(m); mm.brCol != 1 || mm.currentBrowseColumn() != "name" {
		t.Fatalf("列光标未移动：col=%d name=%q", mm.brCol, mm.currentBrowseColumn())
	}
	// e 进入单元格编辑
	m = drive(t, m, func() tea.Msg { return runeKey('e') })
	mm = md(m)
	if !mm.brCellEdit || mm.brEditCol != "name" || mm.brEditOrig != "hello" {
		t.Fatalf("未进入单元格编辑：%+v", mm)
	}
	m = typeInto(t, m, "world")
	if v := md(m).brVal.Value(); v != "world" {
		t.Fatalf("输入值 = %q", v)
	}
	// Enter 暂存
	m = drive(t, m, func() tea.Msg { return key(tea.KeyEnter) })
	mm = md(m)
	if mm.brCellEdit || mm.brQueue.Size() != 1 {
		t.Fatalf("暂存后：editing=%v size=%d", mm.brCellEdit, mm.brQueue.Size())
	}
	if !strings.Contains(mm.status, "已暂存 1 项变更") {
		t.Fatalf("status = %q", mm.status)
	}
	if v := m.View(); !strings.Contains(v, "world") || !strings.Contains(v, "待提交 1 项") {
		t.Errorf("脏格/待提交栏未渲染：\n%s", v)
	}
	// u 撤销后队列清空，脏格消失
	m = drive(t, m, func() tea.Msg { return runeKey('u') })
	if mm := md(m); mm.brQueue.Size() != 0 || !strings.Contains(mm.status, "已撤销") {
		t.Fatalf("撤销失败：size=%d status=%q", mm.brQueue.Size(), mm.status)
	}
	// 重新编辑并提交
	m = drive(t, m, func() tea.Msg { return runeKey('e') })
	m = typeInto(t, m, "world")
	m = drive(t, m, func() tea.Msg { return key(tea.KeyEnter) })
	// w 打开审阅：应生成一条 UPDATE
	m = drive(t, m, func() tea.Msg { return runeKey('w') })
	mm = md(m)
	if !mm.brReview || len(mm.brStmts) != 1 {
		t.Fatalf("审阅面板：review=%v stmts=%+v", mm.brReview, mm.brStmts)
	}
	stmt := mm.brStmts[0].SQL
	if !strings.Contains(stmt, `SET "name" = 'world'`) || !strings.Contains(stmt, `WHERE "id" = 1`) {
		t.Fatalf("审阅 SQL 不符：%s", stmt)
	}
	if v := m.View(); !strings.Contains(v, "审阅变更（1 项）") {
		t.Errorf("审阅面板未渲染：\n%s", v)
	}
	// Enter 提交（单事务），随后自动重拉当前页
	m = drive(t, m, func() tea.Msg { return key(tea.KeyEnter) })
	mm = md(m)
	if strings.Contains(mm.status, "提交失败") {
		t.Fatalf("提交失败：%s", mm.status)
	}
	if !strings.Contains(mm.status, "提交成功") {
		t.Fatalf("status = %q", mm.status)
	}
	if mm.brQueue.Size() != 0 || mm.brReview {
		t.Fatalf("提交后未复位：size=%d review=%v", mm.brQueue.Size(), mm.brReview)
	}
	if got := cellString(mm.brRes.Rows[0][1]); got != "world" {
		t.Fatalf("重拉结果未反映新值：%q", got)
	}
	// 直接核对库内数据
	res, err := app.Execute(context.Background(), mm.selectedID, "SELECT name FROM t WHERE id=1")
	if err != nil {
		t.Fatalf("校验查询失败: %v", err)
	}
	if len(res.Rows) != 1 || cellString(res.Rows[0][0]) != "world" {
		t.Fatalf("库内值未更新：%+v", res.Rows)
	}
}

// TestModelBrowseEditGates 覆盖编辑门禁：只读连接与无主键表都不进入编辑态。
func TestModelBrowseEditGates(t *testing.T) {
	m, _ := enterBrowseApp(t)
	md(m).brReadOnly = true
	m = drive(t, m, func() tea.Msg { return runeKey('e') })
	if mm := md(m); mm.brCellEdit || !strings.Contains(mm.status, "只读") {
		t.Fatalf("只读连接应拦住编辑：editing=%v status=%q", mm.brCellEdit, mm.status)
	}
	md(m).brReadOnly = false
	md(m).brPK = nil
	m = drive(t, m, func() tea.Msg { return runeKey('e') })
	if mm := md(m); mm.brCellEdit || !strings.Contains(mm.status, "无主键") {
		t.Fatalf("无主键应拦住编辑：editing=%v status=%q", mm.brCellEdit, mm.status)
	}
	// 无变更时 w 只提示，不进审阅
	m = drive(t, m, func() tea.Msg { return runeKey('w') })
	if mm := md(m); mm.brReview || !strings.Contains(mm.status, "无待提交变更") {
		t.Fatalf("空队列不应打开审阅：review=%v status=%q", mm.brReview, mm.status)
	}
}

func TestModelBrowsePageSize(t *testing.T) {
	m := enterBrowse(t)
	if md(m).brLimit != 200 {
		t.Fatalf("default page size = %d, want 200", md(m).brLimit)
	}
	var cmd tea.Cmd
	m, cmd = m.Update(runeKey('p')) // 200 → 1000
	m = drive(t, m, cmd)
	if md(m).brLimit != 1000 {
		t.Fatalf("after p = %d, want 1000", md(m).brLimit)
	}
	m, cmd = m.Update(runeKey('p')) // 1000 → 50
	m = drive(t, m, cmd)
	if md(m).brLimit != 50 {
		t.Fatalf("after p = %d, want 50", md(m).brLimit)
	}
	m, cmd = m.Update(runeKey('p')) // 50 → 200 回环
	m = drive(t, m, cmd)
	if md(m).brLimit != 200 {
		t.Fatalf("after p wrap = %d, want 200", md(m).brLimit)
	}
	if v := m.View(); !strings.Contains(v, "页 200") {
		t.Errorf("page size missing in bar:\n%s", v)
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

// ─── 主题 token 化（NO_COLOR / dark-light / 强制档位）────────

func TestListWindow(t *testing.T) {
	cases := []struct {
		total, cursor, avail int
		start, end           int
	}{
		{0, 0, 10, 0, 0},       // 空列表
		{5, 2, 10, 0, 5},       //  avail 足够 → 全量
		{100, 0, 10, 0, 10},    // 顶部光标
		{100, 99, 10, 90, 100}, // 底部光标
		{100, 50, 10, 45, 55},  // 居中
		{20, 18, 5, 15, 20},    // 尾部贴边
		{100, 3, 10, 0, 10},    // 光标靠上，窗口顶格
	}
	for _, c := range cases {
		s, e := listWindow(c.total, c.cursor, c.avail)
		if s != c.start || e != c.end {
			t.Errorf("listWindow(%d,%d,%d) = (%d,%d), want (%d,%d)", c.total, c.cursor, c.avail, s, e, c.start, c.end)
		}
		if e < s || e > c.total || s < 0 {
			t.Errorf("listWindow(%d,%d,%d) out of bounds: (%d,%d)", c.total, c.cursor, c.avail, s, e)
		}
	}
}

// TestModelHelpPanel 验证 F1 开合覆盖式帮助面板，打开时任意键仅关闭、不透传给视图。
func TestModelHelpPanel(t *testing.T) {
	m := New(nil)
	v := m.View()
	if strings.Contains(v, "快捷键") {
		t.Fatalf("help panel should be closed by default:\n%s", v)
	}
	if !strings.Contains(v, "F1 帮助") {
		t.Fatalf("footer should hint F1:\n%s", v)
	}
	// F1 打开
	mm, _ := m.Update(key(tea.KeyF1))
	m = md(mm)
	if !m.helpOpen {
		t.Fatalf("F1 should open help")
	}
	v = m.View()
	if !strings.Contains(v, "快捷键") || !strings.Contains(v, "Ctrl+C") {
		t.Fatalf("help view missing content:\n%s", v)
	}
	// 打开态任意键只关闭，不触发视图动作（此处 q 不应退出）
	mm, _ = m.Update(runeKey('q'))
	m = md(mm)
	if m.helpOpen {
		t.Fatalf("any key should close help")
	}
	if m.quit {
		t.Fatalf("q while help open must not quit")
	}
	// 关闭后 q 正常退出
	mm, _ = m.Update(runeKey('q'))
	if !md(mm).quit {
		t.Fatalf("q after closing help should quit")
	}
}

func TestModelQueryHistory(t *testing.T) {
	t.Setenv("POLYDB_DATA_DIR", t.TempDir())
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter)) // → tables
	m = drive(t, m, cmd)
	m, cmd = m.Update(key(tea.KeyEnter)) // → table detail
	m = drive(t, m, cmd)
	m, cmd = m.Update(runeKey('q')) // → query，预填 SELECT
	if md(m).view != viewQuery {
		t.Fatalf("setup: view=%v", md(m).view)
	}
	// 执行 → queryMsg 落一条历史
	m, cmd = m.Update(key(tea.KeyF5))
	m = drive(t, m, cmd)
	if md(m).queryErr != nil || md(m).queryResult == nil {
		t.Fatalf("query did not run: err=%v result=%v", md(m).queryErr, md(m).queryResult)
	}
	// F4 打开历史抽屉
	m, cmd = m.Update(key(tea.KeyF4))
	m = drive(t, m, cmd)
	mm := md(m)
	if !mm.histOpen || len(mm.hist) != 1 {
		t.Fatalf("F4: open=%v hist=%d", mm.histOpen, len(mm.hist))
	}
	if v := m.View(); !strings.Contains(v, "查询历史") || !strings.Contains(v, "SELECT") {
		t.Errorf("history view missing:\n%s", v)
	}
	// Enter 回填编辑器并关闭
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	mm = md(m)
	if mm.histOpen {
		t.Fatalf("Enter should close history")
	}
	if !strings.Contains(mm.queryInput.Value(), "SELECT") {
		t.Fatalf("Enter should prefill editor, got %q", mm.queryInput.Value())
	}
	// 重复执行同一条 SQL 不应再增历史
	m, cmd = m.Update(key(tea.KeyF5))
	m = drive(t, m, cmd)
	m, cmd = m.Update(key(tea.KeyF4))
	m = drive(t, m, cmd)
	if got := len(md(m).hist); got != 1 {
		t.Fatalf("dedup failed: hist=%d, want 1", got)
	}
}

func TestThemeProfileSelection(t *testing.T) {
	keys := []string{"NO_COLOR", "POLYDB_TUI_NO_COLOR", "POLYDB_TUI_COLOR", "POLYDB_TUI_THEME"}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			defer os.Setenv(k, v)
		} else {
			defer os.Unsetenv(k)
		}
	}
	defer initTheme()

	// 强制 ANSI + 深底：失败标红、灰徽标用亮黑（90）
	os.Unsetenv("NO_COLOR")
	os.Unsetenv("POLYDB_TUI_NO_COLOR")
	os.Setenv("POLYDB_TUI_COLOR", "ansi")
	os.Setenv("POLYDB_TUI_THEME", "dark")
	initTheme()
	if !strings.HasPrefix(failText("boom"), "\x1b[31m") {
		t.Fatalf("ANSI dark fail not red: %q", failText("boom"))
	}
	if !strings.Contains(selected("x"), "\x1b[7m") {
		t.Fatalf("ANSI selected not reverse: %q", selected("x"))
	}
	if s := stBadge(protocol.StatementTypeOther); !strings.Contains(s, "\x1b[90m") {
		t.Fatalf("dark gray badge not bright-black: %q", s)
	}

	// 浅底：灰徽标改用黑（30）以保证可见
	os.Setenv("POLYDB_TUI_THEME", "light")
	initTheme()
	if s := stBadge(protocol.StatementTypeOther); !strings.Contains(s, "\x1b[30m") {
		t.Fatalf("light gray badge not black: %q", s)
	}

	// NO_COLOR：剥离所有样式
	os.Setenv("NO_COLOR", "1")
	initTheme()
	if failText("boom") != "boom" {
		t.Fatalf("NO_COLOR should strip fail: %q", failText("boom"))
	}
	if selected("x") != "x" {
		t.Fatalf("NO_COLOR should strip reverse: %q", selected("x"))
	}
	if stBadge(protocol.StatementTypeSelect) != "SELECT" {
		t.Fatalf("NO_COLOR should strip badge: %q", stBadge(protocol.StatementTypeSelect))
	}
}

// ─── Quick-open（Ctrl+P 跨 schema 搜表）──────────────────────

func TestQuickFilter(t *testing.T) {
	items := []quickItem{
		{"main", "users"},
		{"main", "orders"},
		{"public", "user_roles"},
	}
	if got := quickFilter(items, ""); len(got) != 3 {
		t.Fatalf("empty query should return all, got %d", len(got))
	}
	// 大小写不敏感 + 命中 schema.table 子串
	got := quickFilter(items, "USER")
	if len(got) != 2 || got[0].table != "users" || got[1].table != "user_roles" {
		t.Fatalf("filter USER = %+v, want users+user_roles", got)
	}
	// schema 前缀命中
	got = quickFilter(items, "public.")
	if len(got) != 1 || got[0].table != "user_roles" {
		t.Fatalf("filter 'public.' = %+v, want [user_roles]", got)
	}
	if got = quickFilter(items, "zzz"); len(got) != 0 {
		t.Fatalf("no match should be empty, got %+v", got)
	}
}

func TestModelQuickOpenFlow(t *testing.T) {
	app, _ := setupApp(t)
	var m tea.Model = New(transport.NewLocal(app))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(t, m, m.Init())

	// 打开连接 → viewTables
	var cmd tea.Cmd
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables {
		t.Fatalf("setup: view=%v, want viewTables (status=%s)", md(m).view, md(m).status)
	}

	// Ctrl+P 进入 Quick-open，加载全部表
	m, cmd = m.Update(key(tea.KeyCtrlP))
	if md(m).view != viewQuickOpen {
		t.Fatalf("after Ctrl+P: view=%v, want viewQuickOpen", md(m).view)
	}
	m = drive(t, m, cmd)
	if md(m).busy != "" {
		t.Fatalf("after allTables loaded: busy=%q, want empty", md(m).busy)
	}
	// 渲染冒烟：Quick-open 视图应能渲染且列出候选表（t 与 u）
	if v := m.View(); !strings.Contains(v, "t") || !strings.Contains(v, "u") {
		t.Fatalf("quick-open view missing table list:\n%s", v)
	}
	// 空输入应列出全部表（t 与 u），且不含已过滤
	if len(md(m).qoFiltered) != 2 {
		t.Fatalf("qoFiltered=%+v, want 2 tables", md(m).qoFiltered)
	}

	// 输入 'u' 过滤到 u
	m, _ = m.Update(runeKey('u'))
	if len(md(m).qoFiltered) != 1 || md(m).qoFiltered[0].table != "u" {
		t.Fatalf("filter 'u' = %+v, want [u]", md(m).qoFiltered)
	}

	// Enter 选定 → tablesCmd(带 pending) → detailCmd → viewTable(u)
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	mm := md(m)
	if mm.view != viewTable || mm.tableDetail == nil {
		t.Fatalf("after pick: view=%v detail=%v, want viewTable(u)", mm.view, mm.tableDetail)
	}
	if mm.tables[mm.tableCur].Name != "u" {
		t.Fatalf("selected table=%q, want u", mm.tables[mm.tableCur].Name)
	}
	if mm.pendingTablePick != "" {
		t.Fatalf("pendingTablePick should be cleared, got %q", mm.pendingTablePick)
	}
	cols := map[string]bool{}
	for _, c := range mm.tableDetail.columns {
		cols[c.Name] = true
	}
	if !cols["email"] {
		t.Fatalf("u columns=%+v, want email present", mm.tableDetail.columns)
	}
}
