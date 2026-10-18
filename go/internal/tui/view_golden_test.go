package tui

// 绘制腿（App.View -> renderBody/renderBottom/Composite）的 SGR 金样。
//
// 为什么只有这条路需要金样：它是每帧全量重建、且每格都付一次 lipgloss Render
// 样式增量的唯一路径（选行底色、列光标、匹配下划线、斑马纹、严重级别着色）。
// 它输出的字节就是契约——少一个字节终端就会硬回绕、多一个字节列就错位——而
// SGR 字节没有别的等价表达，只能把字节本身钉住。其它被测函数都有语义返回值
// （行号、列宽、缓存命中率）可用等值断言，唯独渲染输出没有。
//
// 金样纪律：产品码改动后若输出真的应该变化（换主题、增删样式），先人工核对
// 差异，再用 `UPDATE_GOLDEN=1 go test ./internal/ui/` 重钉；不许为了让测试变绿
// 而静默重钉。
//
// 金样之外还叠几何不变量（每行显示宽度、总行数），由 checkGeometry 独立断言，
// 这样即使金样被重钉，"宽度/高度正确性"仍被单独钉住、不会随金样一起漂移。
//
// 金样不依赖终端尺寸：绘制路径上的样式只用 Foreground/Background/Bold/
// Underline，没有 Width/MaxWidth/WordWrap/Align，Style.Render 因此不做基于
// 渲染器宽度的换行，输出与运行测试的终端无关。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
)

const goldenDir = "testdata/golden"

func goldenPath(name string) string {
	return filepath.Join(goldenDir, name+".golden.txt")
}

// wantGolden 把 got 与 name 对应的金样文件逐字节比较；UPDATE_GOLDEN=1 时改为重钉。
func wantGolden(t *testing.T, name, got string) {
	t.Helper()
	p := goldenPath(name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: 金样已更新（%d 字节）", p, len(got))
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("金样 %s 缺失：%v — 人工核对后请跑 `UPDATE_GOLDEN=1 go test` 重钉", p, err)
	}
	want := strings.TrimSuffix(string(raw), "\n")
	if got != want {
		t.Fatalf("金样 %s 漂移（got %d 字节 / want %d 字节）\n=== got ===\n%s\n=== want ===\n%s",
			name, len(got), len(want), got, want)
	}
}

// checkGeometry 用 ansi 宽度独立验证布局：每行恰宽 w 列、恰 h 行。
// 少一列＝右侧缺字符，多一列＝终端硬回绕出额外一屏。
func checkGeometry(t *testing.T, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if h > 0 && len(lines) != h {
		t.Fatalf("行数 = %d，想要 %d", len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("第 %d 行显示宽度 = %d，想要 %d（文本 %q）", i, got, w, ansi.Strip(l))
		}
	}
}

// goldenFrame 是全部金样共用的确定数据：带类型的 int/float/bool 列、null 格、
// 中文（双宽）文本、空串，以及命中严重级别着色的 WARN/ERROR/DEBUG/FATAL。
func goldenFrame() *tuidata.Frame {
	return &tuidata.Frame{Columns: []tuidata.Column{
		tuidata.NewColumn("id", tuidata.TypeInt, []any{
			int64(101), int64(102), nil, int64(104), int64(105), int64(106),
			int64(107), int64(108), int64(109), int64(110), int64(111), int64(112),
		}),
		tuidata.NewColumn("name", tuidata.TypeString, []any{
			"alice", "bob", "客户", "dave", "eve", "grace",
			"heidi", "ivan", "judy", "mallory", "oscar", "peggy",
		}),
		tuidata.NewColumn("客户", tuidata.TypeString, []any{
			"created", "updated", "客户备注", "archived", "", "note",
			"WARN", "ERROR", "DEBUG", "NULL", "INFO", "FATAL",
		}),
		tuidata.NewColumn("score", tuidata.TypeFloat, []any{
			float64(3.14), 0.0, 123456.789, 1.0, -7.0, 42.5,
			0.0001, 999999.5, 2.0, 3.0, 4.0, 5.25,
		}),
		tuidata.NewColumn("ok", tuidata.TypeBool, []any{
			true, false, nil, true, false, true,
			true, false, false, true, nil, true,
		}),
	}}
}

func goldenApp(t *testing.T, f *tuidata.Frame, w, h int) *App {
	t.Helper()
	a := New(Options{
		Frames:         []NamedFrame{{Name: "gold", Frame: f}},
		ThemeName:      "catppuccin-mocha",
		ShowBorders:    true,
		ShowRowNumbers: true,
	})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if a.pane() == nil {
		t.Fatal("golden app has no pane")
	}
	return a
}

// --- 整屏金样（App.View 端到端）--------------------------------------------

func TestGoldenViewDefault(t *testing.T) {
	a := goldenApp(t, goldenFrame(), 80, 24)
	got := a.View().Content
	wantGolden(t, "view-default", got)
	checkGeometry(t, got, 80, 24)
}

func TestGoldenViewSelected(t *testing.T) {
	// 选行 + 列光标 + 宽模式：一次钉住 RowSelected 底色、ColCursor 头高亮与
	// 展开模式下的列定位三种样式增量。
	a := goldenApp(t, goldenFrame(), 80, 24)
	p := a.pane()
	p.Table.sel = 5
	p.Table.rowOff = 3
	p.Table.expanded = true
	p.Table.modeSet = true
	p.Table.colCursor = 2
	got := a.View().Content
	wantGolden(t, "view-selected", got)
	checkGeometry(t, got, 80, 24)
}

func TestGoldenViewSearch(t *testing.T) {
	// 搜索激活：顶部多出搜索条（bodyH 减一），正文用预览帧并高亮命中片段。
	a := goldenApp(t, goldenFrame(), 80, 24)
	p := a.pane()
	p.Table.sel = 0
	p.Table.rowOff = 0
	a.search.Start(p.Current(), false)
	a.search.Type("客户")
	got := a.View().Content
	wantGolden(t, "view-search", got)
	checkGeometry(t, got, 80, 24)
}

func TestGoldenViewNarrow(t *testing.T) {
	// 窄视口：列被压缩、值被截断，钉住紧凑模式与省略号的输出形态。
	a := goldenApp(t, goldenFrame(), 48, 12)
	got := a.View().Content
	wantGolden(t, "view-narrow", got)
	checkGeometry(t, got, 48, 12)
}

func TestGoldenViewNoBorders(t *testing.T) {
	a := goldenApp(t, goldenFrame(), 80, 24)
	a.showBorders = false
	a.showRowNumbers = false
	got := a.View().Content
	wantGolden(t, "view-noborders", got)
	checkGeometry(t, got, 80, 24)
}

func TestGoldenViewToast(t *testing.T) {
	a := goldenApp(t, goldenFrame(), 80, 24)
	a.toastText = "toast: 3 rows affected"
	got := a.View().Content
	wantGolden(t, "view-toast", got)
	checkGeometry(t, got, 80, 24)
}

// --- 分支渲染金样（renderBody 的各条路径）-----------------------------------

func TestGoldenBodyJson(t *testing.T) {
	a := goldenApp(t, goldenFrame(), 80, 24)
	p := a.pane()
	p.Mode = ModeJsonView
	p.JsonViewOff = 2
	wantGolden(t, "body-json", a.renderBody(80, 23))
}

func TestGoldenBodySheet(t *testing.T) {
	a := goldenApp(t, goldenFrame(), 80, 24)
	p := a.pane()
	p.Mode = ModeSheet
	p.SheetField = 2
	p.SheetOff = 1
	wantGolden(t, "body-sheet", a.renderBody(80, 23))
}

func TestGoldenBodyEmpty(t *testing.T) {
	// 空数据：pane 为 nil，走 renderBody 的 "no data loaded" 分支。
	a := New(Options{ThemeName: "catppuccin-mocha"})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	wantGolden(t, "body-empty", a.renderBody(80, 23))
}

// --- 表格渲染的样式全开金样 ------------------------------------------------

func TestGoldenTableStyled(t *testing.T) {
	// 一次开齐所有样式开关：选行、匹配行、表头光标、严重级别着色、命中高亮、
	// 边框与行号。这条金样钉的就是优化目标——样式增量的最终字节形态。
	a := goldenApp(t, goldenFrame(), 80, 24)
	p := a.pane()
	p.Table.sel = 4
	out := p.Table.Render(p.Current(), RenderOpts{
		Width:          80,
		Height:         23,
		Theme:          a.th,
		ShowBorders:    true,
		ShowRowNumbers: true,
		MatchRows:      map[int]bool{1: true, 4: true, 7: true},
		HeaderCur:      1,
		SeverityCol:    "客户",
		HighlightQuery: "客户",
		HighlightCols:  []int{1, 2},
	})
	wantGolden(t, "table-styled", out)
	checkGeometry(t, out, 80, 23)
}
