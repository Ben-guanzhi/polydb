package tui

import (
	"testing"

	"github.com/polydb/polydb/internal/theme"
)

// TestThemeEnvOverride 验证 POLYDB_TUI_THEME 环境变量可覆盖默认主题
// （polydb 发行版的兼容性桥接，sqltui 上游不含此逻辑）。
func TestThemeEnvOverride(t *testing.T) {
	const envKey = "POLYDB_TUI_THEME"

	// 无环境变量：沿用默认主题。
	t.Setenv(envKey, "")
	a := New(Options{})
	if a.ThemeName() == "" {
		t.Fatal("expected a non-empty default theme name")
	}

	// 环境变量指向一个已注册主题：应以它为生效主题。
	if _, ok := theme.Builtin("solarized-dark"); !ok {
		t.Skip("solarized-dark not registered in this build; skipping override assertion")
	}
	t.Setenv(envKey, "solarized-dark")
	a = New(Options{})
	if a.ThemeName() != "solarized-dark" {
		t.Fatalf("theme from env = %q, want solarized-dark", a.ThemeName())
	}

	// 环境变量优先于显式传入。
	t.Setenv(envKey, "solarized-dark")
	a = New(Options{ThemeName: "catppuccin-mocha"})
	if a.ThemeName() != "solarized-dark" {
		t.Fatalf("env should override explicit: got %q", a.ThemeName())
	}
}
