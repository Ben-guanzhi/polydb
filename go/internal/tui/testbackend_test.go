package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
)

// fakeBackend is a minimal backend.Backend for framework tests
// （移植自 sqltui app_test.go 的同名助手，去掉引擎相关部分）。
type fakeBackend struct{}

func (fakeBackend) Kind() string                                           { return "mysql" }
func (fakeBackend) Title() string                                          { return "mysql://h/db" }
func (fakeBackend) Run(string) (backend.Result, error)                     { return backend.Result{}, nil }
func (fakeBackend) Namespaces() ([]string, error)                          { return nil, nil }
func (fakeBackend) Tables(string) ([]string, error)                        { return nil, nil }
func (fakeBackend) FetchTable(string, string, int) (*tuidata.Frame, error) { return nil, nil }
func (fakeBackend) PrimaryKeys(string, string) ([]string, error)           { return nil, nil }
func (fakeBackend) ColumnsMeta(string, string) ([]backend.ColumnMeta, error) {
	return nil, nil
}
func (fakeBackend) ColumnIndexTypes(string, string) (map[string]string, error) {
	return nil, nil
}
func (fakeBackend) Close() error { return nil }

// key builds a KeyPressMsg from a key name or single printable string
// （移植自 sqltui app_test.go）。
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// newTestApp builds an app seeded with two tabs（移植自 sqltui app_test.go）。
func newTestApp(t *testing.T) *App {
	t.Helper()
	a := New(Options{
		Frames: []NamedFrame{
			{Name: "one", Frame: testFrame(20)},
			{Name: "two", Frame: testFrame(5)},
		},
		ThemeName:      "catppuccin-mocha",
		ShowBorders:    true,
		ShowRowNumbers: true,
	})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return a
}
