package tui

import (
	"context"
	"io"
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
