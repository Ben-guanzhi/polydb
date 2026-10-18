package popup

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
)

// qxKey builds a KeyPressMsg from a key name or a single printable string
//（移植自 sqltui popup/queryexec_test.go）。
func qxKey(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "delete":
		return tea.KeyPressMsg{Code: tea.KeyDelete}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// qxTestTheme 移植自 sqltui popup/queryexec_test.go。
func qxTestTheme() *theme.Theme {
	return theme.New(theme.Palette{
		Name: "test", Bg: "#000000", Fg: "#ffffff", BgSoft: "#111111",
		FgDim: "#888888", Header: "#ffffff", Accent: "#5555ff",
		AccentFg: "#000000", Highlight: "#ff00ff", Error: "#ff0000",
		Warning: "#ffff00", Success: "#00ff00",
		Series: []string{"#111111", "#222222", "#333333", "#444444"},
	})
}

// infoTestTheme 移植自 sqltui popup/info_test.go。
func infoTestTheme(t *testing.T) *theme.Theme {
	t.Helper()
	return qxTestTheme()
}
