package tui

import "testing"

// 主题名解析的回归（含上游旧名别名映射）。

// envOf 造一个只认给定键值对的 getenv，便于不碰真实环境地测优先级。
func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestResolveThemeNameEnvWinsOverExplicit(t *testing.T) {
	got := resolveThemeName("catppuccin-mocha", envOf(map[string]string{envThemeName: "solarized-dark"}))
	if got != "solarized-dark" {
		t.Fatalf("环境变量应优先，got %q", got)
	}
}

func TestResolveThemeNameFallsBackToExplicit(t *testing.T) {
	got := resolveThemeName("dracula", envOf(nil))
	if got != "dracula" {
		t.Fatalf("无环境变量时应沿用显式传入, got %q", got)
	}
}

func TestResolveThemeNameTrimsEnv(t *testing.T) {
	got := resolveThemeName("", envOf(map[string]string{envThemeName: "  nord  "}))
	if got != "nord" {
		t.Fatalf("环境变量应去空白, got %q", got)
	}
}

// TestResolveThemeNameNilGetenv pins that a nil getenv degrades to the real
// environment rather than panicking.
func TestResolveThemeNameNilGetenv(t *testing.T) {
	if got := resolveThemeName("dracula", nil); got != "dracula" {
		t.Fatalf("nil getenv 应能正常工作, got %q", got)
	}
}

// TestThemeAliasLegacyNames pins the upstream (sqltui) names still resolve —
// otherwise POLYDB_TUI_THEME=dark silently falls back to the default and looks
// like the setting was ignored.
func TestThemeAliasLegacyNames(t *testing.T) {
	for in, want := range map[string]string{
		"dark":    "sorbet",
		"DARK":    "sorbet",
		" light ": "catppuccin-latte",
		"sorbet":  "sorbet",
		"dracula": "dracula",
	} {
		if got := themeAlias(in); got != want {
			t.Fatalf("themeAlias(%q) = %q, want %q", in, got, want)
		}
	}
}
