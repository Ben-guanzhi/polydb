package tui

// 第十轮 ① 段：限定列支"残余帧价"分解探针。**只新建、不改任何既有 .go**
// （产品码 tableview.go 与全部既有量尺/测试指纹必须跑前跑后逐字不变）。
//
// 形状 ＝ §12/§13 的 E 档：父帧 50000×20 ascii、TableCols＝{0,1}、120×40、
// ShowBorders/ShowRowNumbers 走 benchApp（view_frame_bench_test.go:139）同一条路。
// §13 之后子帧只物化渲染行窗口（win=200），view 中位 1.55–1.72 ms，子帧那份只剩 ~0.3%
// ⇒ 本探针给"帧内其余工作"定价：
//
//	widthsNil   限定列支每帧重算的自然宽（naturalWidthsCached(f,…,nil)，400 次格读＋格宽）
//	widthsHit   假想"接住缓存且全命中"的下界参照（同一 fw、暖 widthCache）
//	cellStr     400 次 CellString（FormatValue 层，剥掉 StringWidth）
//	strWidthOnly 400 次纯 ansi.StringWidth（21 格宽 ascii 料）
//	fitOnly     每帧的 fitWidths（compact 一支不调 visibleColumns）
//	renderWin   Render(fw, opts) 全体（生产形状：窗口子帧）
//	drawBody    Render 里 35 行×2 列的绘制装配段（影子代码，与真 Render 逐字节对拍钉住）
//	winBuild    tableColsSubFrameWindow 每帧那份（候选 A 的记忆化目标）
//	drawBody    Render 里 35 行×2 列的绘制装配段（影子代码，与真 Render 逐字节对拍钉住）
//	drawNoStyle drawBody 的反事实臂：只去掉每行两次 lipgloss Render ⇒ 给行样式引擎定价
//	runeCountOnly 400 次 utf8.RuneCountInString（宽度函数"只要不多付"的下界参照）
//	view        a.View() 整帧（同进程参照）
//
// 影子臂不许静默跑偏：drawBody 的每一行都从产品 Render 体复制，且必须先通过与真
// Render 输出（同一 fw/opts）的行级对拍才允许计时；对拍在 bench 与 test 里各钉一次。

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
)

const (
	p14Rows       = 50000 // E 档父帧行数（view_frame_bench_test.go:262）
	p14ParentCols = 20
	p14WinW       = 120
	p14WinH       = 40
	p14Kind       = "ascii"
)

var (
	p14Cols = []int{0, 1}
	keep14  any // 防编译器杀臂
)

// p14Probe ＝ 一次 E 档摆位：父帧、App、窗口子帧、生产 opts、布局常数。
type p14Probe struct {
	f      *tuidata.Frame
	a      *App
	fw     *tuidata.Frame // 生产同形的窗口子帧（rowOff=0 ⇒ base=0，win=max(body,200)=200）
	opts   RenderOpts
	bodyH  int   // 喂给 Render 的高度（=bodyHFor）
	body   int   // Render 内的表体行数（bodyH-4，带边框）
	inner  int   // w-2
	gutW   int   // 行号槽宽
	avail  int   // inner-gutW
	mlo    int   // 测宽窗口坐标下界（=lo-rowBase）
	mhi    int   // 测宽窗口坐标上界（=hi-rowBase）
	nat    []int // 生产同参的一遍自然宽
	fitted []int // compact 一支的显示宽（含限定列拉伸段）
}

func p14New(tb testing.TB) *p14Probe {
	tb.Helper()
	f := benchFrame(p14Rows, p14ParentCols, p14Kind)
	a := benchApp(f, p14WinW, p14WinH, p14Cols)
	a.Update(tea.WindowSizeMsg{Width: p14WinW, Height: p14WinH})
	p := a.pane()
	if p == nil {
		tb.Fatal("pane 必须存在")
	}
	if p.Table.Sel() != 0 {
		tb.Fatalf("Sel()=%d，量尺假定光标在首行", p.Table.Sel())
	}
	if p.Table.Expanded() {
		tb.Fatal("E 档 wide 应为 0（compact 一支），实为 expanded ⇒ 摆位与日志不同形")
	}
	if q := a.searchQuery(); q != "" {
		tb.Fatalf("探针影子段不覆盖高亮支：searchQuery=%q 非空", q)
	}
	if p.HScroll != 0 {
		tb.Fatalf("探针影子段不覆盖 HScroll 支：HScroll=%d", p.HScroll)
	}
	bodyH := bodyHFor(p14WinH, false)              // 39
	body := bodyH - 4                              // Render:251-254，ShowBorders=true
	inner := p14WinW - 2                           // Render:299-300
	gutW := len(strconv.Itoa(max(1, p14Rows))) + 1 // Render:306-308
	avail := inner - gutW                          // Render:309
	if avail < 1 {
		avail = 1
	}
	win := min(p14Rows-0, max(body, sampleRows)) // Render:291
	fw := tableColsSubFrameWindow(f, p14Cols, 0, win)
	if fw.NumRows() != win || fw.NumCols() != len(p14Cols) {
		tb.Fatalf("窗口子帧 %d×%d ≠ %d×%d", fw.NumRows(), fw.NumCols(), win, len(p14Cols))
	}
	opts := RenderOpts{
		Width:          p14WinW,
		Height:         bodyH,
		Theme:          a.th,
		ShowBorders:    a.showBorders,
		ShowRowNumbers: a.showRowNumbers,
		HeaderCur:      p.Table.ColCursor(),
		SeverityCol:    "",
		TableCols:      p.TableCols,
		NoEllipsis:     len(p.TableCols) > 0,
		HScroll:        p.HScroll,
		HighlightQuery: a.searchQuery(),
		HighlightCols:  p.TableCols,
	}
	nat := naturalWidthsCached(fw, 0, win, compactColCap, nil) // mlo=0, mhi=win（rowOff=0）
	fit := fitWidths(nat, avail-(len(p14Cols)-1))
	// 限定列拉伸段（Render:365-373）逐字镜像，影子臂与真 Render 用同一张 widths。
	used := gutW + len(fit) - 1
	for _, w := range fit[:len(fit)-1] {
		used += w
	}
	if rem := avail - used; rem > fit[len(fit)-1] {
		fit[len(fit)-1] = rem
	}
	return &p14Probe{
		f: f, a: a, fw: fw, opts: opts,
		bodyH: bodyH, body: body, inner: inner, gutW: gutW, avail: avail,
		mlo: 0, mhi: win, nat: nat, fitted: fit,
	}
}

// p14CountedWidths ＝ naturalWidthsCached(c==nil) 的计数影子：返回同一张宽度表
// **并数出真实格读点**（早停在 ascii×cap40 下不触发 ⇒ 应恰好 400）。
func p14CountedWidths(f *tuidata.Frame, lo, hi, capW int) (out []int, cells, names int) {
	lo = clamp(lo, 0, f.NumRows())
	hi = clamp(hi, lo, f.NumRows())
	out = make([]int, f.NumCols())
	for col := range f.Columns {
		w := ansi.StringWidth(f.Columns[col].Name)
		names++
		for r := lo; r < hi; r++ {
			cells++
			if cw := ansi.StringWidth(f.CellString(r, col)); cw > w {
				w = cw
			}
			if w >= capW {
				w = capW
				break
			}
		}
		out[col] = clamp(w, 1, capW)
	}
	return out, cells, names
}

// p14DrawBody ＝ Render 体 :403-509 的表体段的影子（每行从产品码复制；
// sel=0 ⇒ 第 0 行走 RowSelected 支；无选中行、无高亮、无 severity、无 HScroll）。
// 对拍不过就返回 ok=false —— 调用方必须 Fail，不许拿跑偏的影子计时。
func p14DrawBody(pr *p14Probe) (string, bool) {
	tv := pr.a.pane().Table
	f, o := pr.fw, pr.opts
	th := o.Theme
	nrows := pr.f.NumRows() // 父帧行数（Render:248 的 nrows 语义）
	cols := make([]int, len(pr.fitted))
	for i := range cols {
		cols[i] = i
	}
	widths := pr.fitted
	inner, gutterW, body := pr.inner, pr.gutW, pr.body
	var b strings.Builder
	cells := make([]string, len(cols))
	for i := 0; i < body; i++ {
		r := tv.rowOff + i
		var line string
		if r >= nrows {
			line = th.Text.Render(strings.Repeat(" ", inner))
		} else {
			sel := tv.IsSelected(r)
			rowStyle := th.RowEven
			gstyle := th.Gutter
			switch {
			case r == tv.sel:
				rowStyle = th.RowSelected
				gstyle = th.RowSelected
			case sel:
				rowStyle = th.Match
				gstyle = th.Match
			case r%2 == 1:
				rowStyle = th.RowOdd
			}
			for j, c := range cols {
				val := f.CellString(r, c) // rowOff=0 ⇒ rowBase=0，窗口坐标即绝对坐标
				cells[j] = padCellOpts(val, widths[j], o.NoEllipsis)
			}
			rowText := strings.Join(cells, " ")
			gut := gstyle.Render(padLeft(strconv.Itoa(r+1), gutterW-1) + " ")
			line = gut + rowStyle.Render(padLine(rowText, inner-gutterW))
		}
		b.WriteString(wrapBorder(line, o.ShowBorders, th))
		if i < body-1 || o.ShowBorders {
			b.WriteString("\n")
		}
	}
	return b.String(), true
}

// p14DrawNoStyle ＝ drawBody 的**反事实臂**：同一循环、同样的 padCellOpts/padLine/
// wrapBorder/Builder，只把每行的 th.Row*/th.Gutter 两次 lipgloss Render 换成裸串。
// 它不对拍任何东西——存在的意义就是让 drawBody − drawNoStyle 给"行样式引擎"定价。
func p14DrawNoStyle(pr *p14Probe) string {
	tv := pr.a.pane().Table
	f, o := pr.fw, pr.opts
	th := o.Theme
	nrows := pr.f.NumRows()
	cols := make([]int, len(pr.fitted))
	for i := range cols {
		cols[i] = i
	}
	widths := pr.fitted
	inner, gutterW, body := pr.inner, pr.gutW, pr.body
	var b strings.Builder
	cells := make([]string, len(cols))
	for i := 0; i < body; i++ {
		r := tv.rowOff + i
		var line string
		if r >= nrows {
			line = strings.Repeat(" ", inner)
		} else {
			for j, c := range cols {
				cells[j] = padCellOpts(f.CellString(r, c), widths[j], o.NoEllipsis)
			}
			rowText := strings.Join(cells, " ")
			gut := padLeft(strconv.Itoa(r+1), gutterW-1) + " "
			line = gut + padLine(rowText, inner-gutterW)
		}
		b.WriteString(wrapBorder(line, o.ShowBorders, th))
		if i < body-1 || o.ShowBorders {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// p14VerifyDraw ＝ 钉影子：真 Render 的行 [3, 3+body) ＝ 影子表体（逐字节）。
func p14VerifyDraw(tb testing.TB, pr *p14Probe) {
	tb.Helper()
	if pr.a.pane().Table.Expanded() {
		tb.Fatal("影子臂只覆盖 compact 一支")
	}
	// 影子前提再钉一遍（任何一条破了都可能把对拍变成假绿）：IsSelected 全 false、
	// MatchRows 为 nil，Match 支在本形状不会触发，由对拍的输出相等兜底。
	// 生产形状：Render 收**父帧**，窗口子帧在其内部 :289-295 构建（gutter 按父帧 50000 行
	// ＝6 宽；既有 tableRender 量尺臂把子帧直接喂给 Render，gutter 会错按子帧行数算——
	// 本臂不用那条路，保证与 view 同形）。
	real := pr.a.pane().Table.Render(pr.f, pr.opts)
	rl := strings.Split(real, "\n")
	if len(rl) != pr.bodyH {
		tb.Fatalf("真 Render 行数 %d ≠ %d", len(rl), pr.bodyH)
	}
	// 带边框时 Render:498-500 在**每一条**表体行后写换行（最后一行也写，底边框另起一行）
	// ⇒ 影子段应以 "\n" 收尾，与 rl[3:3+body] 重新拼接（末尾 "\n" 手工补回）。
	want := strings.Join(rl[3:3+pr.body], "\n") + "\n"
	got, ok := p14DrawBody(pr)
	if !ok {
		tb.Fatal("影子臂自检失败")
	}
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for k := range min(len(gl), len(wl)) {
			if gl[k] != wl[k] {
				tb.Fatalf("影子表体第 %d 行与真 Render 不一致 ⇒ 计时臂量的是别的东西\n got=%q\nwant=%q", k, gl[k], wl[k])
			}
		}
		tb.Fatalf("影子表体行数/收尾与真 Render 不一致: len(got)=%d len(want)=%d", len(got), len(want))
	}
}

func BenchmarkDrawProbe14(b *testing.B) {
	pr := p14New(b)
	p := pr.a.pane()
	// 前提针：与 E 档日志同形（cellMeasure 预算 400、绘制 70、compact）。
	if m := pr.mhi * len(p14Cols); m != 400 {
		b.Fatalf("测宽格读预算 %d ≠ 400", m)
	}
	if d := pr.body * len(p14Cols); d != 70 {
		b.Fatalf("绘制格预算 %d ≠ 70", d)
	}
	// 计数影子针：真实格读必须恰好 400（早停不许在这档触发）。
	natC, cells, names := p14CountedWidths(pr.fw, pr.mlo, pr.mhi, compactColCap)
	if cells != 400 || names != 2 {
		b.Fatalf("计数影子 cells=%d names=%d，期望 400/2", cells, names)
	}
	for i := range natC {
		if natC[i] != pr.nat[i] {
			b.Fatalf("计数影子宽度表与产品路径不符: %v vs %v", natC, pr.nat)
		}
	}
	// 影子臂对拍（计时之前）。
	p14VerifyDraw(b, pr)
	// widthsHit 的"命中性质"针：暖缓存后必须 builds==1、computed 恒 400。
	var warm widthCache
	if got := naturalWidthsCached(pr.fw, pr.mlo, pr.mhi, compactColCap, &warm); len(got) == 0 {
		b.Fatal("warm 首建返回空")
	}
	if warm.builds != 1 || warm.computed != 400 {
		b.Fatalf("warm 首帧 builds=%d computed=%d，期望 1/400", warm.builds, warm.computed)
	}

	strs := make([]string, 400)
	expStrW := 0 // 池料宽度 21–26 不等（两列各轮转全部 8 池料 25 周 ⇒ 期望 9600）
	{
		i := 0
		for c := range pr.fw.Columns {
			for r := pr.mlo; r < pr.mhi; r++ {
				strs[i] = pr.fw.CellString(r, c)
				expStrW += ansi.StringWidth(strs[i])
				i++
			}
		}
		if i != 400 {
			b.Fatalf("strs 铺料 %d ≠ 400", i)
		}
		if expStrW != 9600 {
			b.Fatalf("铺料总宽 %d ≠ 9600（池料形状漂移，E 档 8 串宽 21..26）", expStrW)
		}
	}

	arms := []string{"view", "renderWin", "widthsNil", "widthsHit", "cellStr",
		"strWidthOnly", "runeCountOnly", "fitOnly", "winBuild", "drawBody", "drawNoStyle"}
	for _, arm := range arms {
		arm := arm
		b.Run(arm, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				switch arm {
				case "view":
					v := pr.a.View()
					assertFrameContent(b, v.Content, p14WinW, p14WinH)
				case "renderWin":
					s := p.Table.Render(pr.f, pr.opts) // 生产形状：传父帧，内部建窗口子帧
					if n := strings.Count(s, "\n") + 1; n != pr.bodyH {
						b.Fatalf("Render 行数 %d ≠ %d", n, pr.bodyH)
					}
					keep14 = s[0]
				case "widthsNil":
					got := naturalWidthsCached(pr.fw, pr.mlo, pr.mhi, compactColCap, nil)
					if got[0] != pr.nat[0] || got[1] != pr.nat[1] {
						b.Fatal("widthsNil 宽度表与首摆不符")
					}
					keep14 = got[0]
				case "widthsHit":
					got := naturalWidthsCached(pr.fw, pr.mlo, pr.mhi, compactColCap, &warm)
					if warm.builds != 1 || warm.computed != 400 {
						b.Fatalf("命中性质破了: builds=%d computed=%d（臂 %d）", warm.builds, warm.computed, i)
					}
					keep14 = got[0]
				case "cellStr":
					n := 0
					for c := range pr.fw.Columns {
						for r := pr.mlo; r < pr.mhi; r++ {
							n += len(pr.fw.CellString(r, c))
						}
					}
					if n != 9600 {
						b.Fatalf("cellStr 累宽 %d ≠ 9600（料不对）", n)
					}
					keep14 = n
				case "strWidthOnly":
					n := 0
					for _, s := range strs {
						n += ansi.StringWidth(s)
					}
					if n != 9600 {
						b.Fatalf("strWidthOnly 累宽 %d ≠ 9600", n)
					}
					keep14 = n
				case "runeCountOnly":
					// 宽度函数"只要不多付"的下界参照：ascii 池料 rune 数＝显示宽（21..26）。
					n := 0
					for _, s := range strs {
						n += utf8.RuneCountInString(s)
					}
					if n != 9600 {
						b.Fatalf("runeCountOnly 累宽 %d ≠ 9600", n)
					}
					keep14 = n
				case "fitOnly":
					got := fitWidths(pr.nat, pr.avail-(len(p14Cols)-1))
					keep14 = got[0]
				case "winBuild":
					got := tableColsSubFrameWindow(pr.f, p14Cols, 0, pr.mhi)
					if got.NumRows() != pr.mhi {
						b.Fatalf("winBuild 行数 %d ≠ %d", got.NumRows(), pr.mhi)
					}
					keep14 = got.NumCols()
				case "drawBody":
					s, ok := p14DrawBody(pr)
					if !ok {
						b.Fatal("影子臂自检失败")
					}
					// 带边框 ⇒ 影子段每条表体行都写尾换行 ⇒ 换行数＝body。
					if n := strings.Count(s, "\n"); n != pr.body {
						b.Fatalf("影子表体换行数 %d ≠ %d", n, pr.body)
					}
					keep14 = s[0]
				case "drawNoStyle":
					s := p14DrawNoStyle(pr)
					if n := strings.Count(s, "\n"); n != pr.body {
						b.Fatalf("反事实臂换行数 %d ≠ %d", n, pr.body)
					}
					keep14 = s[0]
				}
			}
		})
	}
}

// TestDrawProbe14Pins 独立重钉三条前提（不依赖 bench 跑过）：
// 影子对拍、400 计数、宽度表一致。
func TestDrawProbe14Pins(t *testing.T) {
	pr := p14New(t)
	p14VerifyDraw(t, pr)
	natC, cells, names := p14CountedWidths(pr.fw, pr.mlo, pr.mhi, compactColCap)
	if cells != 400 || names != 2 {
		t.Errorf("计数影子 cells=%d names=%d，期望 400/2", cells, names)
	}
	for i := range natC {
		if natC[i] != pr.nat[i] {
			t.Errorf("计数影子宽度表不符 %v vs %v", natC, pr.nat)
		}
	}
	// nil 缓存与计数影子、与 naturalWidths 三者必须同一张表。
	if a := naturalWidths(pr.fw, pr.mlo, pr.mhi, compactColCap); len(a) != 2 {
		t.Errorf("naturalWidths 长度 %d", len(a))
	}
}

// TestDrawProbe14RetainPrice 给候选 A 那条**从未量过**的代价定价：
// memo 持强引用 ⇒ 换帧后"父帧 50000×20 ＋ 子帧"整块留驻。用堆差分量一次。
func TestDrawProbe14RetainPrice(t *testing.T) {
	var m0, m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	f := benchFrame(p14Rows, p14ParentCols, p14Kind)
	runtime.ReadMemStats(&m1)
	sub := tableColsSubFrame(f, p14Cols) // 全行子帧（换帧前的旧形状，即 memo 若持旧帧的代价上界）
	runtime.ReadMemStats(&m2)
	t.Logf("RETAIN parent_50000x20_heapdelta=%d B  full_subframe_50000x2_delta=%d B  (合计=%d)",
		int64(m1.HeapAlloc)-int64(m0.HeapAlloc),
		int64(m2.HeapAlloc)-int64(m1.HeapAlloc),
		int64(m2.HeapAlloc)-int64(m0.HeapAlloc))
	win := tableColsSubFrameWindow(f, p14Cols, 0, 200)
	var m3 runtime.MemStats
	runtime.ReadMemStats(&m3)
	t.Logf("RETAIN win_subframe_200x2_delta=%d B", int64(m3.HeapAlloc)-int64(m2.HeapAlloc))
	keep14 = []any{f, sub, win}
}
