package popup

import (
	"errors"
	"fmt"
	"github.com/polydb/polydb/pkg/changes"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/complete"
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/transport"
)

// --- shared test fakes (qx-prefixed; other feature tests live in this package) ---

type qxCtx struct {
	backend backend.Backend
	cols    []string
	tables  []string
	base    string
}

func (c *qxCtx) CurrentFrame() *tuidata.Frame      { return nil }
func (c *qxCtx) CurrentRow() int                   { return 0 }
func (c *qxCtx) SheetFieldCursor() int             { return 0 }
func (c *qxCtx) SheetFilter() string               { return "" }
func (c *qxCtx) EditQueue() *changes.Queue         { return nil }
func (c *qxCtx) CurrentTableNamespace() string     { return "" }
func (c *qxCtx) BaseCrumb() string                 { return c.base }
func (c *qxCtx) Crumbs() []string                  { return nil }
func (c *qxCtx) ColumnNames() []string             { return c.cols }
func (c *qxCtx) TableNames() []string              { return c.tables }
func (c *qxCtx) Backend() backend.Backend          { return c.backend }
func (c *qxCtx) KV() backend.KVBackend             { return nil }
func (c *qxCtx) Client() transport.Client          { return nil }
func (c *qxCtx) Theme() *theme.Theme               { return nil }
func (c *qxCtx) ThemeName() string                 { return "" }
func (c *qxCtx) ShowBorders() bool                 { return false }
func (c *qxCtx) ShowRowNumbers() bool              { return false }
func (c *qxCtx) Tabs() []tui.TabInfo               { return nil }
func (c *qxCtx) ActiveTab() int                    { return 0 }
func (c *qxCtx) ActivePaneID() int                 { return 0 }
func (c *qxCtx) PendingEdit() *tui.PendingEdit     { return nil }
func (c *qxCtx) PendingDelete() *tui.PendingDelete { return nil }

type qxFakeBackend struct {
	last string
	res  backend.Result
	err  error
}

func (b *qxFakeBackend) Kind() string  { return "test" }
func (b *qxFakeBackend) Title() string { return "test" }
func (b *qxFakeBackend) Run(stmt string) (backend.Result, error) {
	b.last = stmt
	return b.res, b.err
}
func (b *qxFakeBackend) Namespaces() ([]string, error)   { return nil, nil }
func (b *qxFakeBackend) Tables(string) ([]string, error) { return nil, nil }
func (b *qxFakeBackend) FetchTable(string, string, int) (*tuidata.Frame, error) {
	return nil, nil
}
func (b *qxFakeBackend) PrimaryKeys(string, string) ([]string, error)               { return nil, nil }
func (b *qxFakeBackend) ColumnsMeta(string, string) ([]backend.ColumnMeta, error)   { return nil, nil }
func (b *qxFakeBackend) ColumnIndexTypes(string, string) (map[string]string, error) { return nil, nil }
func (b *qxFakeBackend) Close() error                                               { return nil }

// qxTestFrame builds a two-column frame: name (str) and age (i64).
func qxTestFrame() *tuidata.Frame {
	return &tuidata.Frame{Columns: []tuidata.Column{
		tuidata.NewColumn("name", tuidata.TypeString, []any{"ann", "bob", "cyd"}),
		tuidata.NewColumn("age", tuidata.TypeInt, []any{int64(1), int64(2), int64(3)}),
	}}
}

// qxSend feeds keys (by qxKey name) to an overlay and returns the final
// overlay and the last command.
func qxSend(ov tui.Overlay, keys ...string) (tui.Overlay, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		ov, cmd = ov.Update(qxKey(k))
	}
	return ov, cmd
}

// qxTypeText feeds a string one printable key at a time.
func qxTypeText(ov tui.Overlay, s string) (tui.Overlay, tea.Cmd) {
	var cmd tea.Cmd
	for _, r := range s {
		ov, cmd = ov.Update(qxKey(string(r)))
	}
	return ov, cmd
}

// qxDrain runs a command tree (expanding batches) and collects all messages.
func qxDrain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, qxDrain(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func qxFindMsg[T tea.Msg](msgs []tea.Msg) (T, bool) {
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func qxTestTheme() *theme.Theme {
	return theme.New(theme.Palette{
		Name: "test", Bg: "#000000", Fg: "#ffffff", BgSoft: "#111111",
		FgDim: "#888888", Header: "#ffffff", Accent: "#5555ff",
		AccentFg: "#000000", Highlight: "#ff00ff", Error: "#ff0000",
		Warning: "#ffff00", Success: "#00ff00",
		Series: []string{"#111111", "#222222", "#333333", "#444444"},
	})
}

// --- tests --------------------------------------------------------------------

func TestQxQuoteIdent(t *testing.T) {
	if got := qxQuoteIdent("_"); got != `"_"` {
		t.Errorf(`qxQuoteIdent("_") = %s`, got)
	}
	if got := qxQuoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("qxQuoteIdent embedded quote = %s", got)
	}
}

func TestQxTargetAndBuilders(t *testing.T) {
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, cols: []string{"name", "age"}, base: "people"}
	if got := qxTarget(ctx); got != `"people"` {
		t.Errorf("live target = %s, want quoted table", got)
	}
	if got := qxSelectSQL(ctx, "name, age"); got != `SELECT name, age FROM "people"` {
		t.Errorf("select sql = %s", got)
	}
	if got := qxFilterSQL(ctx, "age > 1"); got != `SELECT * FROM "people" WHERE age > 1` {
		t.Errorf("filter sql = %s", got)
	}
	if got := qxOrderSQL(ctx, "age DESC"); got != `SELECT * FROM "people" ORDER BY age DESC` {
		t.Errorf("order sql = %s", got)
	}

	live := &qxCtx{backend: &qxFakeBackend{}, base: "users"}
	if got := qxFilterSQL(live, "id = 1"); got != `SELECT * FROM "users" WHERE id = 1` {
		t.Errorf("live-mode filter sql = %s", got)
	}
}

// qxSourceCtx is a live context whose pane records a real source table.
type qxSourceCtx struct {
	*qxCtx
	ns, table string
}

func (c *qxSourceCtx) TableSource() (string, string) { return c.ns, c.table }

func TestQxTargetUsesRecordedSourceTable(t *testing.T) {
	// Recorded source beats the tab-title heuristic, ns-qualified with the
	// dialect's own identifier quoting (pkg/changes; the fake kind "fake"
	// quotes with double quotes).
	ctx := &qxSourceCtx{
		qxCtx: &qxCtx{backend: &qxFakeBackend{}, base: "SELECT a, b, c"},
		ns:    "sales", table: "orders",
	}
	if got := qxTarget(ctx); got != `"sales"."orders"` {
		t.Errorf("qualified target = %s, want \"sales\".\"orders\"", got)
	}

	// Empty recorded namespace passes through to the table spelling alone.
	ctx.ns = ""
	if got := qxTarget(ctx); got != `"orders"` {
		t.Errorf("unqualified target = %s, want \"orders\"", got)
	}

	// No recorded source (query-opened tab) keeps the tab-title heuristic.
	ctx3 := &qxSourceCtx{
		qxCtx: &qxCtx{backend: &qxFakeBackend{}, base: "users"},
		ns:    "", table: "",
	}
	if got := qxTarget(ctx3); got != `"users"` {
		t.Errorf("heuristic target = %s, want quoted title", got)
	}
}

func TestQxShortTitle(t *testing.T) {
	if got := qxShortTitle("select   *\nfrom _"); got != "select * from _" {
		t.Errorf("short title = %q", got)
	}
	long := "SELECT aaaaaaaaaa, bbbbbbbbbb FROM t"
	if got := qxShortTitle(long); got != "SELECT aaaaaaaaaa, b" || len([]rune(got)) != 20 {
		t.Errorf("truncated title = %q (len %d)", got, len([]rune(got)))
	}
	if got := qxShortTitle("   "); got != "query" {
		t.Errorf("empty title = %q", got)
	}
}

func TestQxRunRouting(t *testing.T) {
	// Backend frame result takes precedence over the engine.
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, base: "users"}
	f, info, err := qxRun(ctx, "SELECT 1")
	if err != nil || f == nil || info != nil {
		t.Fatalf("backend frame: f=%v info=%v err=%v", f, info, err)
	}
	if be.last != "SELECT 1" {
		t.Errorf("backend saw %q", be.last)
	}

	// Backend exec result.
	be.res = backend.Result{Exec: &backend.ExecResult{RowsAffected: 7}}
	f, info, err = qxRun(ctx, "DELETE FROM t")
	if err != nil || f != nil || info == nil || info.rowsAffected != 7 {
		t.Fatalf("backend exec: f=%v info=%+v err=%v", f, info, err)
	}

	// Backend error.
	be.res, be.err = backend.Result{}, errors.New("boom")
	if _, _, err := qxRun(ctx, "SELECT 1"); err == nil {
		t.Error("backend error not propagated")
	}

	// No backend at all: clear error.
	if _, _, err := qxRun(&qxCtx{}, "SELECT 1"); err == nil {
		t.Error("expected error with no backend")
	}
}

func TestQxRunCmdMessages(t *testing.T) {
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, base: "people"}
	onFrame := func(f *tuidata.Frame) tea.Msg {
		return tui.ApplyFrameMsg{Frame: f, Crumb: "x"}
	}

	msgs := qxDrain(qxRunCmd(ctx, `SELECT * FROM "people"`, onFrame))
	if _, ok := qxFindMsg[tui.ApplyFrameMsg](msgs); !ok {
		t.Errorf("frame result: got %v, want ApplyFrameMsg", msgs)
	}

	bad := &qxFakeBackend{err: errors.New("syntax error")}
	msgs = qxDrain(qxRunCmd(&qxCtx{backend: bad}, "SELECT nope FROM nowhere", onFrame))
	if _, ok := qxFindMsg[tui.ErrorMsg](msgs); !ok {
		t.Errorf("bad sql: got %v, want ErrorMsg", msgs)
	}

	be2 := &qxFakeBackend{res: backend.Result{Exec: &backend.ExecResult{RowsAffected: 3}}}
	msgs = qxDrain(qxRunCmd(&qxCtx{backend: be2}, "UPDATE t SET x=1", onFrame))
	toast, ok := qxFindMsg[tui.ToastMsg](msgs)
	if !ok || toast.Text != "3 rows affected" {
		t.Errorf("exec result: got %v, want rows-affected toast", msgs)
	}
}

func TestQxInputEditing(t *testing.T) {
	in := tui.NewLineInput("")
	feed := func(keys ...string) {
		for _, k := range keys {
			in.Handle(qxKey(k))
		}
	}

	feed("a", "b", "c")
	if in.String() != "abc" || in.Cursor() != 3 {
		t.Fatalf("after typing: %q cursor %d", in.String(), in.Cursor())
	}
	feed("left", "backspace")
	if in.String() != "ac" || in.Cursor() != 1 {
		t.Fatalf("after left+backspace: %q cursor %d", in.String(), in.Cursor())
	}
	feed("home", "delete")
	if in.String() != "c" || in.Cursor() != 0 {
		t.Fatalf("after home+delete: %q cursor %d", in.String(), in.Cursor())
	}
	feed("end", "x")
	if in.String() != "cx" || in.Cursor() != 2 {
		t.Fatalf("after end+insert: %q cursor %d", in.String(), in.Cursor())
	}

	in = tui.NewLineInput("one two")
	in.Handle(qxKey("ctrl+w"))
	if in.String() != "one " {
		t.Errorf("ctrl+w: %q", in.String())
	}
	in.Handle(qxKey("ctrl+u"))
	if in.String() != "" || in.Cursor() != 0 {
		t.Errorf("ctrl+u: %q cursor %d", in.String(), in.Cursor())
	}

	// Prefill puts the cursor at the end.
	in = tui.NewLineInput("sql")
	if in.String() != "sql" || in.Cursor() != 3 {
		t.Errorf("prefill: %q cursor %d", in.String(), in.Cursor())
	}
}

func TestQxInputApplySuggestion(t *testing.T) {
	in := tui.NewLineInput("select na")
	qxApplySuggestion(&in, "name")
	if in.String() != "select name" || in.Cursor() != len("select name") {
		t.Errorf("apply at end: %q cursor %d", in.String(), in.Cursor())
	}

	// Mid-string replacement keeps the tail.
	in = tui.NewLineInput("SELECT na FROM t")
	in.SetCursor(len("SELECT na"))
	qxApplySuggestion(&in, "name")
	if in.String() != "SELECT name FROM t" {
		t.Errorf("apply mid-string: %q", in.String())
	}
	if in.Cursor() != len("SELECT name") {
		t.Errorf("cursor after mid-string apply = %d", in.Cursor())
	}
}

func TestQxSuggest(t *testing.T) {
	var s qxSuggest
	sc := complete.Schema{
		Current: []string{"name", "age"},
		Tables:  map[string][]string{"nums": nil},
	}
	// Column context after SELECT: column, then function, then keywords —
	// no table names.
	s.recompute("SELECT n", len("SELECT n"), sc)
	got := make([]string, len(s.items))
	for i, it := range s.items {
		got[i] = it.Text
	}
	want := []string{"name", "nullif(", "not", "null"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if !s.open() || s.selected().Text != "name" || s.selected().Kind != complete.KindColumn {
		t.Fatalf("open=%v selected=%+v", s.open(), s.selected())
	}
	s.cycle(1)
	if s.selected().Text != "nullif(" {
		t.Errorf("after cycle: %+v", s.selected())
	}
	s.cycle(-2)
	if s.selected().Text != "null" {
		t.Errorf("wrap backwards: %+v", s.selected())
	}
	s.clear()
	if s.open() || s.selected().Text != "" {
		t.Error("clear did not close the list")
	}

	// Table context completes table names (empty token included).
	s.recompute("SELECT name FROM ", len("SELECT name FROM "), sc)
	if len(s.items) != 1 || s.items[0].Text != "nums" || s.items[0].Kind != complete.KindTable {
		t.Fatalf("table context items = %+v", s.items)
	}
}

func TestQxSuggestViewScrollsPastCap(t *testing.T) {
	var s qxSuggest
	sc := complete.Schema{Current: []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}}
	// 7 columns + abs( + avg( + all/and/as/asc = 13 candidates.
	s.recompute("a", 1, sc)
	if len(s.items) <= qxMaxSuggestions {
		t.Fatalf("need more than %d items, got %d", qxMaxSuggestions, len(s.items))
	}

	th := qxTestTheme()
	lines := s.view(th)
	if len(lines) != qxMaxSuggestions+1 { // window + position line
		t.Fatalf("%d lines, want %d visible + position", len(lines), qxMaxSuggestions)
	}
	if !strings.Contains(lines[0], "a1") || !strings.Contains(lines[len(lines)-1], fmt.Sprintf("1/%d", len(s.items))) {
		t.Errorf("initial window: first=%q last=%q", lines[0], lines[len(lines)-1])
	}

	// Cycling past the window scrolls it; the selected item stays visible.
	for i := 0; i < qxMaxSuggestions; i++ {
		s.cycle(1)
	}
	lines = s.view(th)
	found := false
	for _, l := range lines {
		if strings.Contains(l, s.selected().Text) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("selected %q not visible after scrolling: %v", s.selected().Text, lines)
	}
}

func TestQxSuggestViewShowsKindAndDetail(t *testing.T) {
	var s qxSuggest
	sc := complete.Schema{Tables: map[string][]string{"users": {"name"}}}
	s.recompute("select users.na from users", len("select users.na"), sc)
	if len(s.items) != 1 {
		t.Fatalf("items = %+v", s.items)
	}
	out := strings.Join(s.view(qxTestTheme()), "\n")
	for _, want := range []string{"name", "col", "users"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q: %q", want, out)
		}
	}
}

func TestQxSchemaOfFallback(t *testing.T) {
	// Contexts without CompletionSchema get current columns plus bare table
	// names with unknown column sets.
	ctx := &qxCtx{cols: []string{"a", "b"}, tables: []string{"t1", "t2"}}
	sc := qxSchemaOf(ctx)
	if !reflect.DeepEqual(sc.Current, []string{"a", "b"}) {
		t.Errorf("Current = %v", sc.Current)
	}
	if len(sc.Tables) != 2 || sc.Tables["t1"] != nil || sc.Tables["t2"] != nil {
		t.Errorf("Tables = %v", sc.Tables)
	}
}

func TestPasteSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"select *\r\nfrom t", "select * from t"},
		{"a\tb", "a b"},
		{"plain", "plain"},
		{"\r\x00\x1b\x7f", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := tui.SanitizePaste(c.in); got != c.want {
			t.Errorf("SanitizePaste(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQxInputPaste(t *testing.T) {
	// A multi-character paste is one edit at the cursor.
	in := tui.NewLineInput("SELECT  FROM t")
	in.SetCursor(len("SELECT "))
	if !in.Paste("name,\nage") {
		t.Fatal("paste should report a change")
	}
	if in.String() != "SELECT name, age FROM t" {
		t.Fatalf("text = %q", in.String())
	}
	if in.Cursor() != len("SELECT name, age") {
		t.Fatalf("cursor = %d, want %d", in.Cursor(), len("SELECT name, age"))
	}

	// Empty and control-only pastes change nothing.
	before := in.String()
	if in.Paste("") || in.Paste("\r") {
		t.Fatal("empty paste must not report a change")
	}
	if in.String() != before {
		t.Fatalf("text changed by empty paste: %q", in.String())
	}
}
