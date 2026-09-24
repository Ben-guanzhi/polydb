package tui

// TUI 主题 token 化：把散落的 raw ANSI 收进此处（借 lipgloss）。
// 借 lipgloss 保持可见行为等价（反显选中行、绿/红状态、语句类型徽标），
// 并统一支持 NO_COLOR 与 dark/light。AGPL 红线：仅自研，未移植任何外部界面代码。

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/polydb/polydb/pkg/protocol"
)

// profileFromEnv 决定颜色输出档位：
//   - NO_COLOR（或 POLYDB_TUI_NO_COLOR=1）→ Ascii，剥离所有样式；
//   - POLYDB_TUI_COLOR=ansi|256|truecolor|ascii 强制档位（测试用 ANSI 保证确定性）；
//   - 否则按 tty 自动探测。
func profileFromEnv() termenv.Profile {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("POLYDB_TUI_NO_COLOR") == "1" {
		return termenv.Ascii
	}
	switch strings.ToLower(os.Getenv("POLYDB_TUI_COLOR")) {
	case "ascii":
		return termenv.Ascii
	case "ansi":
		return termenv.ANSI
	case "ansi256", "256":
		return termenv.ANSI256
	case "truecolor":
		return termenv.TrueColor
	}
	return termenv.ColorProfile()
}

// hasDarkBackground 报告是否为深色底（默认深色）；POLYDB_TUI_THEME=light 强制浅色。
func hasDarkBackground() bool {
	switch strings.ToLower(os.Getenv("POLYDB_TUI_THEME")) {
	case "light":
		return false
	case "dark":
		return true
	}
	return termenv.NewOutput(os.Stdout).HasDarkBackground()
}

// styles 汇总主题样式。用结构体封装以便 TestMain 设定 POLYDB_TUI_COLOR 后重建，
// 使样式在测试（非 tty）环境下也能确定地产出 ANSI。
type styles struct {
	selected    lipgloss.Style
	fail, ok    lipgloss.Style
	warn        lipgloss.Style
	badgeSelect lipgloss.Style
	badgeWrite  lipgloss.Style
	badgeDDL    lipgloss.Style
	badgeOther  lipgloss.Style
}

// buildStyles 依据当前环境（NO_COLOR / POLYDB_TUI_COLOR / POLYDB_TUI_THEME）构造样式集。
// 底色深浅决定各角色取深色还是亮色变体（ANSI 基本色 0-7 / 亮色 8-15）。
func buildStyles() styles {
	r := lipgloss.NewRenderer(os.Stdout)
	// lipgloss 默认走 EnvColorProfile()（非 tty 会降为 Ascii），WithProfile 不生效；
	// 必须显式 SetColorProfile 才能固定档位（测试里用 ANSI 保证确定性）。
	r.SetColorProfile(profileFromEnv())
	fg := func(dark, light string) lipgloss.TerminalColor {
		if hasDarkBackground() {
			return lipgloss.Color(dark)
		}
		return lipgloss.Color(light)
	}
	return styles{
		selected: r.NewStyle().Reverse(true),
		// 深底用标准色（1/2/3），浅底用与之对应、对比足够的色号（此处 ANSI 基本色两种底色皆可辨）。
		fail:        r.NewStyle().Foreground(fg("1", "1")),
		ok:          r.NewStyle().Foreground(fg("2", "2")),
		warn:        r.NewStyle().Foreground(fg("3", "3")),
		badgeSelect: r.NewStyle().Foreground(fg("2", "2")),
		badgeWrite:  r.NewStyle().Foreground(fg("6", "6")),
		badgeDDL:    r.NewStyle().Foreground(fg("3", "3")),
		// 灰徽标：深底用亮黑（90），浅底用黑（30）以保证可见。
		badgeOther: r.NewStyle().Foreground(fg("8", "0")),
	}
}

var th = buildStyles()

// initTheme 重建全局样式集：环境变更后（如测试设 POLYDB_TUI_COLOR）调用以生效。
func initTheme() { th = buildStyles() }

func selected(s string) string { return th.selected.Render(s) }
func okText(s string) string   { return th.ok.Render(s) }
func failText(s string) string { return th.fail.Render(s) }
func warnText(s string) string { return th.warn.Render(s) }

// badgeStyle 返回语句类型对应的样式（空类型回落 other）。
func badgeStyle(t protocol.StatementType) lipgloss.Style {
	switch t {
	case protocol.StatementTypeSelect:
		return th.badgeSelect
	case protocol.StatementTypeInsert, protocol.StatementTypeUpdate, protocol.StatementTypeDelete:
		return th.badgeWrite
	case protocol.StatementTypeDDL:
		return th.badgeDDL
	default:
		return th.badgeOther
	}
}
