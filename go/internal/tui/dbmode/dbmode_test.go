package dbmode

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tui/backend/fake"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/pkg/transport"
)

func testTheme() *theme.Theme {
	return theme.New(theme.Palette{
		Name: "test",
		Bg:   "#000000", Fg: "#ffffff", BgSoft: "#111111", FgDim: "#888888",
		Header: "#ffcc00", Accent: "#3355ff", AccentFg: "#ffffff", Highlight: "#00ffcc",
		Error: "#ff0000", Warning: "#ffaa00", Success: "#00ff00",
		Series: []string{"#111111", "#222222", "#333333", "#444444"},
	})
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// typeText feeds a string rune by rune into an overlay.
func typeText(t *testing.T, ov tui.Overlay, s string) tui.Overlay {
	t.Helper()
	for _, r := range s {
		k := keyPress(string(r))
		if r == ' ' {
			k = keyPress("space")
		}
		ov, _ = ov.Update(k)
	}
	return ov
}

// --- fakes ---------------------------------------------------------------------

// fakeBackend is a backend.Backend stub for factory tests.
type fakeBackend struct {
	fetchNS    string
	fetchTable string
	fetchLimit int
	frame      *tuidata.Frame
	fetchErr   error
}

func (f *fakeBackend) Kind() string                  { return "fake" }
func (f *fakeBackend) Title() string                 { return "fake://test" }
func (f *fakeBackend) Run(string) (backend.Result, error) { return backend.Result{}, nil }
func (f *fakeBackend) Namespaces() ([]string, error) { return []string{""}, nil }
func (f *fakeBackend) Tables(string) ([]string, error) {
	return []string{"users"}, nil
}
func (f *fakeBackend) FetchTable(ns, table string, limit int) (*tuidata.Frame, error) {
	f.fetchNS, f.fetchTable, f.fetchLimit = ns, table, limit
	return f.frame, f.fetchErr
}
func (f *fakeBackend) PrimaryKeys(string, string) ([]string, error)               { return nil, nil }
func (f *fakeBackend) ColumnsMeta(string, string) ([]backend.ColumnMeta, error)        { return nil, nil }
func (f *fakeBackend) ColumnIndexTypes(string, string) (map[string]string, error) { return nil, nil }
func (f *fakeBackend) Close() error                                               { return nil }

// fakeKV is a backend.KVBackend stub for redis-mode tests.
type fakeKV struct {
	doArgs  []string
	doOut   string
	doErr   error
	keys    map[string][]string
	scanErr error
	value   string
	valErr  error
}

func (f *fakeKV) Title() string { return "redis://test/0" }
func (f *fakeKV) Do(args []string) (string, error) {
	f.doArgs = args
	return f.doOut, f.doErr
}
func (f *fakeKV) ScanKeys(keyType string) ([]string, error) {
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return f.keys[keyType], nil
}
func (f *fakeKV) Value(string) (string, error) { return f.value, f.valErr }
func (f *fakeKV) Close() error                 { return nil }

// fakeCtx is a minimal tui.AppContext for factory construction.
type fakeCtx struct {
	be  backend.Backend
	kv  backend.KVBackend
	cli *fake.Client
}

func (c *fakeCtx) CurrentFrame() *tuidata.Frame        { return nil }
func (c *fakeCtx) CurrentRow() int                  { return 0 }
func (c *fakeCtx) SheetFieldCursor() int            { return 0 }
func (c *fakeCtx) CurrentTableNamespace() string    { return "" }
func (c *fakeCtx) BaseCrumb() string                { return "" }
func (c *fakeCtx) Crumbs() []string                 { return nil }
func (c *fakeCtx) ColumnNames() []string            { return nil }
func (c *fakeCtx) Client() transport.Client        { return c.cli }
func (c *fakeCtx) TableNames() []string             { return nil }
func (c *fakeCtx) Backend() backend.Backend              { return c.be }
func (c *fakeCtx) KV() backend.KVBackend                 { return c.kv }
func (c *fakeCtx) Theme() *theme.Theme              { return testTheme() }
func (c *fakeCtx) ThemeName() string                { return "test" }
func (c *fakeCtx) ShowBorders() bool                { return false }
func (c *fakeCtx) ShowRowNumbers() bool             { return false }
func (c *fakeCtx) Tabs() []tui.TabInfo               { return nil }
func (c *fakeCtx) ActiveTab() int                   { return 0 }
func (c *fakeCtx) ActivePaneID() int                { return 0 }
func (c *fakeCtx) PendingEdit() *tui.PendingEdit     { return nil }
func (c *fakeCtx) PendingDelete() *tui.PendingDelete { return nil }

var _ tui.AppContext = (*fakeCtx)(nil)

// runCmd executes a tea.Cmd and returns the produced message (nil-safe).
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// stringFrame builds a small all-string test frame.
func stringFrame(cols []string, rows [][]string) *tuidata.Frame {
	f := tuidata.New(cols...)
	for _, r := range rows {
		row := make([]any, len(cols))
		for i := range cols {
			row[i] = r[i]
		}
		f.AppendRow(row)
	}
	return f
}

// TestSplitTableArg covers the schema-browser arg encoding ("ns\ttable").
func TestSplitTableArg(t *testing.T) {
	for _, tc := range []struct{ arg, ns, table string }{
		{"public\tusers", "public", "users"},
		{"\tusers", "", "users"},
		{"users", "", "users"},
		{"a\tb\tc", "a", "b\tc"},
	} {
		ns, table := splitTableArg(tc.arg)
		if ns != tc.ns || table != tc.table {
			t.Errorf("splitTableArg(%q) = (%q, %q), want (%q, %q)",
				tc.arg, ns, table, tc.ns, tc.table)
		}
	}
}

// TestRegisterFactoriesWrapsRedisOnce ensures repeated registration never
// nests the redis wrappers and that delegation to the originals works.
func TestRegisterFactoriesWrapsRedisOnce(t *testing.T) {
	origQueryFn := tui.Factories["query"]
	origSchemaFn := tui.Factories["schema"]
	t.Cleanup(func() {
		tui.Factories["query"] = origQueryFn
		tui.Factories["schema"] = origSchemaFn
	})

	RegisterFactories()
	RegisterFactories() // must be harmless

	if tui.Factories["opentable"] == nil {
		t.Fatal("opentable factory not registered")
	}
	if tui.Factories["connect"] == nil {
		t.Fatal("connect factory not registered")
	}

	// With a live KV connection the query factory yields the redis prompt.
	ov, err := tui.Factories["query"](&fakeCtx{kv: &fakeKV{}}, "")
	if err != nil {
		t.Fatalf("query factory: %v", err)
	}
	if _, ok := ov.(*redisPrompt); !ok {
		t.Fatalf("query factory returned %T, want *redisPrompt", ov)
	}

	// And the schema factory yields the key browser.
	ov, err = tui.Factories["schema"](&fakeCtx{kv: &fakeKV{}}, "")
	if err != nil {
		t.Fatalf("schema factory: %v", err)
	}
	if _, ok := ov.(*keyBrowser); !ok {
		t.Fatalf("schema factory returned %T, want *keyBrowser", ov)
	}

	// Without a KV connection both delegate to the originals (which exist,
	// since the popup package is imported).
	ov, err = tui.Factories["schema"](&fakeCtx{}, "")
	if err != nil {
		t.Fatalf("schema delegate: %v", err)
	}
	if _, ok := ov.(*keyBrowser); ok {
		t.Fatal("schema factory returned the key browser without a KV connection")
	}
}
