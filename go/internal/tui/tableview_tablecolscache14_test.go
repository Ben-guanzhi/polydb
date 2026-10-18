package tui

// 第十轮 ②a 段（报告 §14）：限定列（RenderOpts.TableCols）那一支的量宽腿接一块
// **按窗口坐标建键**的宽度表（机制 M1）。选型与预登记判据在
// D:\tmp\round10_draw\SELECT14.md（① 段，只量不改，81 行 / 11,585 B / e498dcda…）。
//
// 这一轮与 §13 的分工要读清楚：§13 把"子帧有多少行"从 50,000 降到窗口（win），
// 于是子帧那份只剩 ~0.3%，但**量宽腿本身**（每帧 400 格读 + 400 次 StringWidth）
// 一条没动 ⇒ ① 段实测它占整帧 view 的 18.3–20.1%（三轮合并 16–26%），命中下界
// widthsHit 只有 0.19–0.29% ⇒ 净上界 16.2–19.9%。M1 就是把这一段换成回表。
//
// 为什么这一支不能直接用 §11/§12 那块 widthCache：键是**帧指针**，而限定列那一支的
// 子帧每帧新建（:292 tableColsSubFrameWindow），指针必变 ⇒ 键必失配。所以 M1 的键
// 换成 **父帧指针 + 父帧 rows/ncols + cols 序列 + 窗口坐标 (base,win,mlo,mhi) + capW**。
// 键里少任何一维都不是"慢一点"而是**读到别的窗口的宽度 ⇒ 显示偏差**，所以本文件的
// 三条负对针（rowOff 移 1 / 换父帧指针 / cols 序列变）都同时钉两件事：
// 必须重算（计数器动）＋ 重算结果与"不接缓存的旧路"逐格相等。
//
// 金样纪律（与 §13 同）：期望串不写在源码里，写在盘上（testdata/golden-tc14，随仓库版本化）。
// 金样必须在**改 tableview.go 之前**用 pristine 渲染出来；目录空着 ⇒ 比对测试必须失败，
// 且失败原因由跑测试的人经环境变量递进 `go test` 输出（负对针要有牙齿）。
// 本轮金样尤其重要的一点：新机制**只在第二帧起生效**，所以序列形状（同一块 tv 连渲
// N 帧、其间换 rowOff / 换 cols 序列 / 换父帧）必须在改前就落盘，否则"命中后输出变了"
// 这件事没有任何东西能抓住。

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/internal/theme"
)

const (
	// 仓库内唯一权威，随代码一起版本化，CI 才跑得起来。仓库相对路径，不写 /tmp 或
	// %TEMP%（§13.1 更正块②那条死指针教训），也不写绝对路径——绝对路径只在作者本机
	// 存在，CI 的 Linux 上会让 32/32 份金样全部读不到。
	tc14GoldenDir = "testdata/golden-tc14"
	// 金样还没取时，跑测试的人把原因放进这个变量，compare 测试把它打进输出。
	tc14ReasonEnv = "TC14_GOLDEN_REASON"
	// 写了金样才允许为 1；平时它是关的，compare 才是常跑的那一个。
	tc14WriteEnv = "TC14_GOLDEN_WRITE"
)

// --- 用例矩阵
//
// id, rows, cols, kind, colsSel, w, height, borders, rowNumbers, hscroll, sel,
// expandedFirst, frames, moveAfter, colsAfter

type tc14Case struct {
	id            string
	rows, cols    int
	kind          string // ascii / cjk / int（料与 E 档量尺同源：benchFrame）
	colsSel       []int
	w, height     int // height == RenderOpts.Height（＝bodyHFor(h,false)，见 t14RenderCase）
	borders       bool
	rowNumbers    bool
	hscroll       int
	sel           int  // tv.JumpTo 目标；-1 ＝ 不动
	expandedFirst bool // 首帧强制 expanded ⇒ 走 :340 那一支（capW=expandedColCap）
	frames        int  // >1 ⇒ 同一块 tv 连渲 frames 次，逐帧拼接
	colsAfter     []int
	moveAfter     int // 最后一帧之前 JumpTo 到这里；-1 ＝ 不挪
}

func tc14Seq(n int) []int {
	a := make([]int, n)
	for i := range a {
		a[i] = i
	}
	return a
}

// tc14Cases 的**前七条**＝SELECT14 ⑤-5 点名的七形（a)-(g)，一条不缺；其余是本段补的
// 序列/边角形状（命中路径只有序列形状能钉）。
func tc14Cases() []tc14Case {
	return []tc14Case{
		// (a) E 档 120×40 ascii cols{0,1} rowOff=0
		{"a_E_ascii_120x40_base0", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, -1, false, 1, nil, -1},
		// (b) 同窗 80×24
		{"b_E_ascii_80x24_base0", 50000, 20, "ascii", []int{0, 1}, 80, 23, false, false, 0, -1, false, 1, nil, -1},
		// (c) CJK 限定列 120×40
		{"c_E_cjk_120x40_base0", 50000, 20, "cjk", []int{0, 1}, 120, 39, false, false, 0, -1, false, 1, nil, -1},
		// (d) rowOff=137 滚动中窗
		{"d_E_ascii_rowoff137", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, 137, false, 1, nil, -1},
		// (e) 高终端档：① 段写的是"300×60"，本段两种读法都落盘（宽 300／高 300），
		//     其中"高 300"那一档才是 win>sampleRows 的上沿档（body=296>200）。
		{"e1_E_ascii_h300_w200", 50000, 20, "ascii", []int{0, 1}, 200, 299, true, true, 0, -1, false, 1, nil, -1},
		{"e2_E_ascii_w300_h60", 50000, 20, "ascii", []int{0, 1}, 300, 59, false, false, 0, -1, false, 1, nil, -1},
		// (f) HScroll=40
		{"f_E_ascii_hscroll40", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 40, 500, false, 1, nil, -1},
		// (g) int 型限定列（FormatValue 支）
		{"g_E_int_120x40_base0", 50000, 20, "int", []int{0, 1}, 120, 39, false, false, 0, -1, false, 1, nil, -1},

		// --- 命中/失效序列（新机制唯一的行为面；改码前必须落盘）---
		{"h_seq4_same_window", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, -1, false, 4, nil, -1},
		{"i_seq_cols_change", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, -1, false, 3, []int{0, 2}, -1},
		{"j_seq_cols_reorder", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, -1, false, 3, []int{1, 0}, -1},
		{"k_seq_scroll_then_back", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, true, 0, -1, false, 3, nil, 137},
		{"l_seq_expanded_3", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, 33, true, 3, nil, -1},
		{"m_seq_caps_expanded_to_compact", 5000, 20, "ascii", []int{0, 1}, 120, 39, false, false, 0, -1, true, 2, nil, -1},
		{"n_seq_borders_rownum_3", 50000, 20, "ascii", []int{0, 1}, 120, 39, true, true, 0, 900, false, 3, nil, -1},
		{"o_seq_cjk_3", 50000, 20, "cjk", []int{0, 1}, 120, 39, false, false, 0, 222, false, 3, nil, -1},
		{"p_seq_int_3", 50000, 20, "int", []int{0, 1}, 120, 39, false, false, 0, 222, false, 3, nil, -1},
		{"q_seq_h300_scroll", 50000, 20, "ascii", []int{0, 1}, 200, 299, true, true, 0, -1, false, 3, nil, 700},

		// --- 边角形状 ---
		{"r_allcols20", 5000, 20, "ascii", tc14Seq(20), 120, 39, false, true, 0, -1, false, 2, nil, -1},
		{"s_cols3_reverse", 5000, 20, "ascii", []int{4, 2, 0}, 120, 39, false, false, 0, 250, false, 2, nil, -1},
		{"t_dup_cols", 5000, 20, "ascii", []int{0, 0, 1}, 120, 39, false, false, 0, 280, false, 2, nil, -1},
		{"u_single_col", 5000, 20, "ascii", []int{1}, 120, 39, false, true, 0, 270, false, 2, nil, -1},
		{"v_outofrange_col", 5000, 20, "ascii", []int{0, 1, 99}, 120, 39, false, false, 0, 260, false, 2, nil, -1},
		{"w_tail_base", 300, 6, "ascii", []int{0, 2}, 100, 39, true, true, 0, 299, false, 2, nil, -1},
		{"x_narrow_w8", 5000, 6, "ascii", []int{0, 3}, 8, 23, true, true, 0, 120, false, 2, nil, -1},
		{"y_rows201_base200", 201, 4, "ascii", []int{0, 2}, 100, 39, false, true, 0, 200, false, 2, nil, -1},
		{"z_body1_rows50000", 50000, 4, "ascii", []int{0, 2}, 100, 1, false, true, 0, 40000, false, 2, nil, -1},

		// --- 对照：非限定列那一支（§11/§12 的 widthCache）本轮不许被扰动 ---
		{"ctrl_nocols_seq3", 5000, 5, "ascii", nil, 120, 39, false, true, 0, 2, false, 3, nil, 400},
		{"ctrl_nocols_cjk_seq2", 5000, 5, "cjk", nil, 120, 39, true, true, 0, -1, false, 2, nil, -1},
	}
}

func tc14RenderCase(t *testing.T, c tc14Case) string {
	t.Helper()
	f := benchFrame(c.rows, c.cols, c.kind)
	tv := &TableView{}
	if c.expandedFirst {
		tv.expanded = true
	}
	if c.sel >= 0 {
		tv.JumpTo(c.sel, f.NumRows())
	}
	o := RenderOpts{
		Width:          c.w,
		Height:         c.height,
		Theme:          theme.Default(),
		ShowBorders:    c.borders,
		ShowRowNumbers: c.rowNumbers,
		TableCols:      c.colsSel,
		HScroll:        c.hscroll,
		HeaderCur:      -1,
		NoEllipsis:     len(c.colsSel) > 0,
	}
	// 生产同形：Render 收**父帧**，窗口子帧在 Render 内部 :289-295 建。
	out := tv.Render(f, o)
	for i := 1; i < c.frames; i++ {
		if c.colsAfter != nil && i == c.frames-1 {
			o.TableCols = c.colsAfter
			o.NoEllipsis = len(o.TableCols) > 0
			o.HighlightCols = o.TableCols
		}
		if c.moveAfter >= 0 && i == c.frames-1 {
			tv.JumpTo(c.moveAfter, f.NumRows())
		}
		out += "\n===\n" + tv.Render(f, o)
	}
	return out
}

// tc14RenderAppLevel 是 (a) 那一形的**整帧**版本：走 a.View()（app.go 那条路），
// 因为 §14 的时间判据量的是 view，不是 Render。连渲三帧 ⇒ 第二、三帧是命中帧。
func tc14RenderAppLevel(t *testing.T, c tc14Case) string {
	t.Helper()
	f := benchFrame(c.rows, c.cols, c.kind)
	a := benchApp(f, c.w, c.height+1, c.colsSel)
	a.Update(tea.WindowSizeMsg{Width: c.w, Height: c.height + 1})
	if c.sel >= 0 {
		a.pane().Table.JumpTo(c.sel, f.NumRows())
	}
	var b strings.Builder
	for i := 0; i < c.frames; i++ {
		v := a.View()
		assertFrameContentT(t, v.Content, c.w, c.height+1)
		b.WriteString(v.Content)
		b.WriteString("\n===\n")
	}
	return b.String()
}

// assertFrameContent 的 testing.TB 版（量尺里那一份签名收 *testing.B，不许动它）：
// 行数与行宽都按**显示宽度**算（ansi.StringWidth），照抄量尺 view_frame_bench_test.go:228 的口径。
func assertFrameContentT(tb testing.TB, content string, w, wantLines int) {
	tb.Helper()
	if n := strings.Count(content, "\n") + 1; n != wantLines {
		tb.Fatalf("帧行数 %d ≠ 期望 %d（渲染路径接错了）", n, wantLines)
	}
	for i, l := range strings.Split(content, "\n") {
		if got := ansi.StringWidth(l); got > w {
			tb.Fatalf("第 %d 行显示宽 %d > %d", i, got, w)
		}
	}
}

func tc14GoldenPath(id string) string {
	return filepath.Join(tc14GoldenDir, id+".golden")
}

// tc14IsAppLevel 标出唯一那一条整帧形状。
func tc14IsAppLevel(id string) bool { return strings.HasPrefix(id, "app_") }

func init() { tc14CasesApp = append(tc14CasesApp, tc14AppCases()...) }

var tc14CasesApp []tc14Case

func tc14AppCases() []tc14Case {
	return []tc14Case{
		{"app_a_E_ascii_120x40_seq3", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, true, 0, -1, false, 3, nil, -1},
		{"app_d_E_ascii_rowoff137_seq3", 50000, 20, "ascii", []int{0, 1}, 120, 39, false, true, 0, 137, false, 3, nil, -1},
		{"app_ctrl_nocols_120x40_seq3", 200, 20, "ascii", nil, 120, 39, false, true, 0, -1, false, 3, nil, -1},
	}
}

func tc14AllCases() []tc14Case {
	out := make([]tc14Case, 0, len(tc14Cases())+len(tc14CasesApp))
	out = append(out, tc14Cases()...)
	out = append(out, tc14CasesApp...)
	return out
}

// TestTC14GoldenWrite 只在显式开启时跑，且**必须**跑在改 tableview.go 之前。
func TestTC14GoldenWrite(t *testing.T) {
	if os.Getenv(tc14WriteEnv) != "1" {
		t.Skip("金样写入需 TC14_GOLDEN_WRITE=1（必须在改 tableview.go 之前跑一次）")
	}
	if err := os.MkdirAll(tc14GoldenDir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	n := 0
	for _, c := range tc14Cases() {
		out := tc14RenderCase(t, c)
		if out == "" {
			t.Fatalf("%s：渲染输出为空 ⇒ 形状没跑起来，金样不许是空串", c.id)
		}
		if err := os.WriteFile(tc14GoldenPath(c.id), []byte(out), 0o644); err != nil {
			t.Fatalf("写 %s: %v", c.id, err)
		}
		n++
	}
	for _, c := range tc14AppCases() {
		out := tc14RenderAppLevel(t, c)
		if err := os.WriteFile(tc14GoldenPath(c.id), []byte(out), 0o644); err != nil {
			t.Fatalf("写 %s: %v", c.id, err)
		}
		n++
	}
	t.Logf("TC14 写了 %d 份金样到 %s", n, tc14GoldenDir)
}

// TestTC14GoldenCompare 逐字节比对。金样缺失 ⇒ 失败，并把递进来的原因原样打进输出。
func TestTC14GoldenCompare(t *testing.T) {
	cases := tc14AllCases()
	missing := 0
	for _, c := range cases {
		if _, err := os.Stat(tc14GoldenPath(c.id)); err != nil {
			missing++
		}
	}
	if missing > 0 {
		reason := os.Getenv(tc14ReasonEnv)
		if reason == "" {
			reason = "未提供原因（先跑 TestTC14GoldenWrite，且必须在改 tableview.go 之前）"
		}
		t.Errorf("TC14-GOLDEN-MISSING-REASON: 缺 %d/%d 份金样，原因=%s", missing, len(cases), reason)
		return
	}
	for _, c := range cases {
		c := c
		t.Run(c.id, func(t *testing.T) {
			want, err := os.ReadFile(tc14GoldenPath(c.id))
			if err != nil {
				t.Fatalf("读金样: %v", err)
			}
			var got string
			if tc14IsAppLevel(c.id) {
				got = tc14RenderAppLevel(t, c)
			} else {
				got = tc14RenderCase(t, c)
			}
			if got != string(want) {
				t.Fatalf("输出与金样不逐字节相等：len(got)=%d len(golden)=%d md5(got)=%s md5(golden)=%s\n--- got 前 400 字节 ---\n%s\n--- want 前 400 字节 ---\n%s",
					len(got), len(want), md5hex(got), md5hex(string(want)), firstN(got, 400), firstN(string(want), 400))
			}
		})
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// TestTC14GoldenManifest 把盘上金样的三元组打进测试输出（给 GOLDEN.txt 用，不写死数字）。
func TestTC14GoldenManifest(t *testing.T) {
	if os.Getenv(tc14WriteEnv) != "manifest" {
		t.Skip("清单需要 TC14_GOLDEN_WRITE=manifest")
	}
	entries, err := os.ReadDir(tc14GoldenDir)
	if err != nil {
		t.Fatalf("读目录: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".golden") {
			names = append(names, strings.TrimSuffix(e.Name(), ".golden"))
		}
	}
	total := 0
	var b strings.Builder
	for _, nm := range names {
		bs, err := os.ReadFile(filepath.Join(tc14GoldenDir, nm+".golden"))
		if err != nil {
			t.Fatalf("读 %s: %v", nm, err)
		}
		total += len(bs)
		b.WriteString(nm + "\t" + strconv.Itoa(len(bs)) + "\t" + md5hex(string(bs)) + "\n")
	}
	t.Logf("TC14-GOLDEN-MANIFEST-START\n%sTC14-GOLDEN-MANIFEST-END: %d files, %d bytes total", b.String(), len(names), total)
}

// TestTC14GoldenNonEmptyShape 钉"金样不是空串"（空文件 md5 是 d41d8cd9…，
// 逐字节比对会假绿）＋ 钉份数与本文件的用例表一致。
func TestTC14GoldenNonEmptyShape(t *testing.T) {
	n := 0
	for _, c := range tc14AllCases() {
		bs, err := os.ReadFile(tc14GoldenPath(c.id))
		if err != nil {
			t.Logf("金样缺席：%s（改码前还没跑 TestTC14GoldenWrite）", c.id)
			continue
		}
		if len(bs) == 0 || md5hex(string(bs)) == "d41d8cd98f00b204e9800998ecf8427e" {
			t.Errorf("%s：金样是空串 ⇒ 比对无意义", c.id)
		}
		n++
	}
	if n == 0 {
		t.Skip("盘上还没有金样（本条只在金样已落盘后有义）")
	}
	t.Logf("TC14-GOLDEN-ASSERTED: %d 份金样非空", n)
	if want := len(tc14AllCases()); n != want {
		t.Errorf("TC14-GOLDEN-ASSERTED: %d != %d（用例表与盘上对不上）", n, want)
	}
}

// ============================================================================
// 机制侧（M1）：命中性质针＋三条负对针＋越界 fail-loud＋与旧路差分＋对照档不动
// ============================================================================

// tc14BodyFor 镜像 Render :251-257 的纵向预算（本测试文件里的"复制来的式子"，
// 产品码不动；§13 的 p13b 侧同规矩）。
func tc14BodyFor(o RenderOpts) int {
	b := o.Height - 1
	if o.ShowBorders {
		b = o.Height - 4
	}
	return max(1, b)
}

// tc14Window 镜像 Render :289-295（窗口子帧）与 :315-320（测宽窗口坐标）。
// 必须在 tv.Render **之后**调用：Render 会先钳 tv.rowOff。
func tc14Window(o RenderOpts, tv *TableView, orig *tuidata.Frame, cols []int) (*tuidata.Frame, int, int, int) {
	nrows := orig.NumRows()
	base := tv.rowOff
	win := min(nrows-base, max(tc14BodyFor(o), sampleRows))
	sub := tableColsSubFrameWindow(orig, cols, base, win)
	lo := tv.rowOff
	hi := min(nrows, lo+sampleRows)
	return sub, win, lo - base, hi - base
}

// tc14OldRoute ＝ 本轮被替换掉的那条**每帧重算**的老路（wc==nil 的 naturalWidthsCached）。
// 它仍然是 get() 未命中时跑的那一个函数 ⇒ 本文件里的差分参照与产品未命中路径同源，
// 不是第二条实现（这一点是 §12/§13 一直守的"不许有第二份量宽循环"）。
func tc14OldRoute(sub *tuidata.Frame, mlo, mhi, capW int) []int {
	return naturalWidthsCached(sub, mlo, mhi, capW, nil)
}

func tc14Opts(c tc14Case) RenderOpts {
	return RenderOpts{
		Width:          c.w,
		Height:         c.height,
		Theme:          theme.Default(),
		ShowBorders:    c.borders,
		ShowRowNumbers: c.rowNumbers,
		TableCols:      c.colsSel,
		HScroll:        c.hscroll,
		HeaderCur:      -1,
		NoEllipsis:     len(c.colsSel) > 0,
	}
}

// TestTC14CacheHitProperty ＝ ⑤-1 的命中性质针：同一父帧、同一 (rowOff, body, cols, capW)
// 连续多帧 ⇒ computed 增量＝0、宽度表与首帧全等、builds ≤ 1。
func TestTC14CacheHitProperty(t *testing.T) {
	c := tc14Case{id: "hit", rows: 50000, cols: 20, kind: "ascii", colsSel: []int{0, 1}, w: 120, height: 39}
	f := benchFrame(c.rows, c.cols, c.kind)
	tv := &TableView{}
	o := tc14Opts(c)

	_ = tv.Render(f, o)
	sub, win, mlo, mhi := tc14Window(o, tv, f, c.colsSel)
	if win != sampleRows || mlo != 0 || mhi != sampleRows {
		t.Fatalf("窗口 %d/%d/%d ≠ %d/0/%d ⇒ 摆位与判据不同形", win, mlo, mhi, sampleRows, sampleRows)
	}
	first := append([]int(nil), tv.tcWidths.val...) // 首帧那张表的快照（下面逐帧比"宽度表与首帧全等"）
	// 首帧：modeSet 那一遍建表（:333），随后 compact 那一遍（:353）同键 ⇒ 命中。
	if got := tv.tcWidths.builds; got != 1 {
		t.Errorf("首帧 builds=%d，期望 1（buf 常驻）", got)
	}
	if got := tv.tcWidths.computed; got != (mhi-mlo)*len(c.colsSel) {
		t.Errorf("首帧 computed=%d，期望 %d（预算口径＝(mhi-mlo)*len(cols)）", got, (mhi-mlo)*len(c.colsSel))
	}
	if got := tv.tcWidths.hits; got < 1 {
		t.Errorf("首帧 hits=%d，期望 ≥1（同一帧内两处同键调用）", got)
	}
	hits0, comp0, builds0 := tv.tcWidths.hits, tv.tcWidths.computed, tv.tcWidths.builds

	want := tc14OldRoute(sub, mlo, mhi, compactColCap)
	for frame := 2; frame <= 4; frame++ {
		out := tv.naturalWidthsFor(sub, f, c.colsSel, tv.rowOff, mlo, mhi, compactColCap, nil)
		for i := range want {
			if out[i] != want[i] {
				t.Fatalf("第 %d 帧宽度表第 %d 列 %d ≠ 首帧 %d ⇒ 命中回表给了别的值", frame, i, out[i], want[i])
			}
		}
		if d := tv.tcWidths.computed - comp0; d != 0 {
			t.Fatalf("第 %d 帧 computed 增量=%d，期望 0 ⇒ 命中仍在量宽", frame, d)
		}
		if b := tv.tcWidths.builds; b != builds0 {
			t.Fatalf("第 %d 帧 builds=%d > %d ⇒ 命中性质破了", frame, b, builds0)
		}
		h := tv.tcWidths.hits
		if h != hits0+1 {
			t.Fatalf("第 %d 帧 hits=%d，期望 %d", frame, h, hits0+1)
		}
		hits0 = h
		if len(out) != len(first) {
			t.Fatalf("第 %d 帧向量长度 %d ≠ 首帧表长 %d", frame, len(out), len(first))
		}
		for i := range first {
			if out[i] != first[i] {
				t.Fatalf("第 %d 帧宽度表与首帧不全等: %d vs %d", frame, out[i], first[i])
			}
		}
	}
	// 整帧串：连渲三帧后计数器仍只有一份表。
	for i := 0; i < 3; i++ {
		_ = tv.Render(f, o)
	}
	if tv.tcWidths.builds != 1 {
		t.Errorf("连渲后 builds=%d，期望恒 1", tv.tcWidths.builds)
	}
	t.Logf("TC14-HIT: builds=%d hits=%d computed=%d（预算口径，E 档 400 格不触发早停）", tv.tcWidths.builds, tv.tcWidths.hits, tv.tcWidths.computed)
}

// TestTC14CacheNegativeRowOff ＝ 负对针 (i)：滚动 ⇒ 必须重算，且与"不接缓存的旧路
// 在新窗口上的结果"**逐格相等**（键里去掉窗口坐标的变异臂就栽在这里）。
//
// 形状事实（本段实测，别当成"mlo/mhi 会变"）：滚动时**子帧坐标**下的窗口恒是 [0, sampleRows)，
// 变的是 base（子帧第 0 行对应的父帧绝对行号）与 win（尾窗）。所以 base 单独进键并被这条针钉；
// mlo/mhi 那一维再用直接调 get 的方式各钉一次（窄窗 mhi-1、base 挪一格）。
func TestTC14CacheNegativeRowOff(t *testing.T) {
	c := tc14Case{rows: 50000, cols: 20, kind: "ascii", colsSel: []int{0, 1}, w: 120, height: 39}
	f := benchFrame(c.rows, c.cols, c.kind)
	o := tc14Opts(c)
	tv := &TableView{}
	_ = tv.Render(f, o)
	sub0, win0, mlo0, mhi0 := tc14Window(o, tv, f, c.colsSel)
	base0 := tv.rowOff
	b0, c0 := tv.tcWidths.builds, tv.tcWidths.computed
	if base0 != 0 || win0 != sampleRows || mlo0 != 0 || mhi0 != sampleRows {
		t.Fatalf("首帧窗口 base=%d win=%d mlo=%d mhi=%d，期望 0/%d/0/%d", base0, win0, mlo0, mhi0, sampleRows, sampleRows)
	}
	if sub0.NumRows() != win0 || sub0.NumCols() != len(c.colsSel) {
		t.Fatalf("首帧子帧 %d×%d ≠ %d×%d", sub0.NumRows(), sub0.NumCols(), win0, len(c.colsSel))
	}

	tv.JumpTo(40, f.NumRows()) // 真实滚动：body=38 ⇒ Render 把 rowOff 钳到 40-38+1 = 3
	_ = tv.Render(f, o)
	base1 := tv.rowOff
	if base1 == base0 {
		t.Fatalf("JumpTo(40) 后 rowOff 仍是 %d ⇒ 负对针没有牙齿", base1)
	}
	sub1, win1, mlo1, mhi1 := tc14Window(o, tv, f, c.colsSel)
	if mlo1 != mlo0 || mhi1 != mhi0 || win1 != win0 {
		t.Logf("子帧坐标下的窗口也变了 win %d→%d mlo %d→%d mhi %d→%d（合法，键仍须失配）", win0, win1, mlo0, mlo1, mhi0, mhi1)
	}
	if tv.tcWidths.builds != b0+1 {
		t.Fatalf("base 移 %d→%d 后 builds=%d（原 %d），期望 %d ⇒ base 没进键", base0, base1, tv.tcWidths.builds, b0, b0+1)
	}
	want := tc14OldRoute(sub1, mlo1, mhi1, compactColCap)
	got := tv.naturalWidthsFor(sub1, f, c.colsSel, base1, mlo1, mhi1, compactColCap, nil)
	if len(got) != len(want) {
		t.Fatalf("长度 %d ≠ %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("新窗口第 %d 列 %d ≠ 旧路 %d ⇒ 重算给了错值", i, got[i], want[i])
		}
	}
	if d := tv.tcWidths.computed - c0; d == 0 {
		t.Errorf("computed 增量=0 ⇒ 这次没真量（builds 却动了？）")
	}
	// 整串硬对拍：热缓存 tv 与一块**同状态**的新 tv 渲染这一帧必须逐字节相等。
	fresh := &TableView{}
	fresh.JumpTo(40, f.NumRows())
	if a, b := tv.Render(f, o), fresh.Render(f, o); a != b {
		t.Errorf("滚动帧上，热缓存 tv 与同状态冷 tv 的渲染输出不逐字节相等（len %d vs %d）", len(a), len(b))
	}
	// mhi 那一维单独钉一次：同 base、同子帧，只把上界挪一格 ⇒ 必须失配。
	mb := tv.tcWidths.builds
	narrow := tv.naturalWidthsFor(sub1, f, c.colsSel, base1, mlo1, mhi1-1, compactColCap, nil)
	if tv.tcWidths.builds != mb+1 {
		t.Errorf("mhi %d→%d 后 builds 没动（=%d）⇒ mhi 没进键", mhi1, mhi1-1, tv.tcWidths.builds)
	}
	refNarrow := tc14OldRoute(sub1, mlo1, mhi1-1, compactColCap)
	for i := range refNarrow {
		if narrow[i] != refNarrow[i] {
			t.Fatalf("窄窗第 %d 列 %d ≠ 旧路 %d", i, narrow[i], refNarrow[i])
		}
	}
	// base 挪**一格**（不是挪整页）也必须失配：这是"键只按页对齐"那类变异的牙。
	mm := tv.tcWidths.builds
	_ = tv.naturalWidthsFor(sub1, f, c.colsSel, base1+1, mlo1, mhi1, compactColCap, nil)
	if tv.tcWidths.builds != mm+1 {
		t.Errorf("base 挪一格后 builds 没动（=%d）⇒ base 只按页对齐", tv.tcWidths.builds)
	}
}

// TestTC14CacheNegativeParentPtr ＝ 负对针 (ii)：换父帧指针 ⇒ 必须全失效。
// 两帧**同 rows/ncols、同窗口坐标**，只有内容不同 ⇒ 少了父帧身份就会静默留旧值。
func TestTC14CacheNegativeParentPtr(t *testing.T) {
	o := RenderOpts{Width: 120, Height: 39, Theme: theme.Default(), TableCols: []int{0, 1}, NoEllipsis: true, HeaderCur: -1}
	cols := []int{0, 1}
	fA := benchFrame(500, 4, "ascii")
	fB := benchFrame(500, 4, "cjk") // 同 rows/ncols，内容不同
	tv := &TableView{}
	_ = tv.Render(fA, o)
	b0 := tv.tcWidths.builds
	_ = tv.Render(fB, o)
	if tv.tcWidths.builds != b0+1 {
		t.Fatalf("换父帧后 builds=%d（原 %d）⇒ 父帧身份没进键", tv.tcWidths.builds, b0)
	}
	subB, _, mloB, mhiB := tc14Window(o, tv, fB, cols)
	want := tc14OldRoute(subB, mloB, mhiB, compactColCap)
	got := tv.naturalWidthsFor(subB, fB, cols, tv.rowOff, mloB, mhiB, compactColCap, nil)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("新父帧第 %d 列 %d ≠ 旧路 %d", i, got[i], want[i])
		}
	}
	if a, b := tv.Render(fB, o), (&TableView{}).Render(fB, o); a != b {
		t.Errorf("换父帧那一帧上，热缓存 tv 与冷 tv 输出不等（len %d vs %d）", len(a), len(b))
	}
}

// TestTC14CacheNegativeColsSeq ＝ 负对针 (iii)：**父帧不变**只换 cols 序列 ⇒ 必须失效。
// 这条不是假想敌：app.go:247/950-971/1306、tablefetch.go:92 就是这种写法（① 段 ⑤-1 点名）。
func TestTC14CacheNegativeColsSeq(t *testing.T) {
	f := benchFrame(500, 6, "ascii")
	o := RenderOpts{Width: 120, Height: 39, Theme: theme.Default(), NoEllipsis: true, HeaderCur: -1}
	tv := &TableView{}
	o.TableCols = []int{0, 1}
	o.NoEllipsis = true
	_ = tv.Render(f, o)
	b0 := tv.tcWidths.builds
	subA, _, mloA, mhiA := tc14Window(o, tv, f, o.TableCols)
	wantA := tc14OldRoute(subA, mloA, mhiA, compactColCap)

	o2 := o
	o2.TableCols = []int{0, 2}
	o2.NoEllipsis = true
	_ = tv.Render(f, o2)
	if tv.tcWidths.builds != b0+1 {
		t.Fatalf("cols 序列变了但 builds 没动（=%d）⇒ 序列没进键，会把 {0,1} 的表给 {0,2} 用", tv.tcWidths.builds)
	}
	subB, _, mloB, mhiB := tc14Window(o2, tv, f, o2.TableCols)
	gotB := tv.naturalWidthsFor(subB, f, o2.TableCols, tv.rowOff, mloB, mhiB, compactColCap, nil)
	wantB := tc14OldRoute(subB, mloB, mhiB, compactColCap)
	for i := range wantB {
		if gotB[i] != wantB[i] {
			t.Fatalf("cols{0,2} 第 %d 列 %d ≠ 旧路 %d", i, gotB[i], wantB[i])
		}
	}
	// 序列只换**顺序**也必须失配（{1,0} vs {0,1}：len 相同、元素相同、顺序不同）。
	o3 := o2
	o3.TableCols = []int{2, 0}
	b1 := tv.tcWidths.builds
	_ = tv.Render(f, o3)
	if tv.tcWidths.builds != b1+1 {
		t.Errorf("cols 只换顺序后 builds=%d（原 %d）⇒ 键只比了集合没比序列", tv.tcWidths.builds, b1)
	}
	// 长度相同、元素不同（{0,1} vs {0,3}）
	o4 := o2
	o4.TableCols = []int{0, 3}
	_ = tv.Render(f, o4)
	if want := len(wantA); len(o4.TableCols) != want {
		t.Fatalf("用例形状破了：期望同长 %d", want)
	}
	t.Logf("TC14-NEG-COLS: builds=%d（cols 换过三次，每次都必须重建）", tv.tcWidths.builds)
}

// TestTC14CacheOutOfRangePanics ＝ 越界读 fail-loud：窗口坐标越出子帧行数时**必须 panic**，
// 不许让 naturalWidthsCached 的静默 clamp 把一张截短过的表存进写着完整 mhi 的键里。
func TestTC14CacheOutOfRangePanics(t *testing.T) {
	f := benchFrame(500, 4, "ascii")
	cols := []int{0, 1}
	sub := tableColsSubFrameWindow(f, cols, 0, 200)
	var cw tableColsWidthCache
	// 反面对照：合法窗口读得出来。
	got := cw.get(sub, f, cols, 0, 0, 200, compactColCap)
	if len(got) != 2 {
		t.Fatalf("合法窗口的宽度表长度 %d ≠ 2", len(got))
	}
	// 正面对撞：mhi 越出一格 ⇒ 必须炸。
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("mhi=win+1 没有 panic ⇒ 静默错列的门开了")
			} else {
				t.Logf("TC14-OUT-OF-RANGE panic 文案：%v", r)
			}
		}()
		cw.get(sub, f, cols, 0, 0, 201, compactColCap)
	}()
	// mlo 为负 ⇒ 同样炸。
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("mlo=-1 没有 panic")
			}
		}()
		cw.get(sub, f, cols, 0, -1, 200, compactColCap)
	}()
	// 炸过之后表没被污染：合法读仍给出同一张表。
	again := cw.get(sub, f, cols, 0, 0, 200, compactColCap)
	for i := range got {
		if again[i] != got[i] {
			t.Errorf("越界尝试之后合法表被改了: %d vs %d", again[i], got[i])
		}
	}
	// 空窗口子帧（win==0）上 mhi==0 合法、mhi==1 炸。
	empty := tableColsSubFrameWindow(f, cols, 500, 200)
	if empty.NumRows() != 0 {
		t.Fatalf("base=rows 的窗口应有 0 行，实为 %d", empty.NumRows())
	}
	var cw2 tableColsWidthCache
	if out := cw2.get(empty, f, cols, 500, 0, 0, compactColCap); len(out) != 2 {
		t.Errorf("空窗口也应给出每列的表头宽，实得 %d 项", len(out))
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("空窗口上 mhi=1 没有 panic")
			}
		}()
		cw2.get(empty, f, cols, 500, 0, 1, compactColCap)
	}()
}

// TestTC14CacheDiffVsOldRoute ＝ 一批形状上把"命中回表"与"每帧重算的旧路"逐格对拍。
// 覆盖滚动窗、cols 变、capW 变、CJK/int、高终端（win>sampleRows 上沿档）。
func TestTC14CacheDiffVsOldRoute(t *testing.T) {
	shapes := []struct {
		id         string
		rows, cols int
		kind       string
		colsSel    []int
		height     int
		borders    bool
		jump       int
		capW       int
	}{
		{"diff_E_base0", 50000, 20, "ascii", []int{0, 1}, 39, false, 0, compactColCap},
		{"diff_E_rowoff137", 50000, 20, "ascii", []int{0, 1}, 39, false, 137, compactColCap},
		{"diff_E_cjk", 50000, 20, "cjk", []int{0, 1}, 39, false, 0, compactColCap},
		{"diff_E_int", 50000, 20, "int", []int{0, 1}, 39, false, 0, compactColCap},
		{"diff_E_h300", 50000, 20, "ascii", []int{0, 1}, 299, true, 0, compactColCap},
		{"diff_E_allcols", 5000, 20, "ascii", tc14Seq(20), 39, false, 0, compactColCap},
		{"diff_E_expanded", 5000, 20, "ascii", []int{0, 1}, 39, false, 0, expandedColCap},
		{"diff_E_dup", 5000, 20, "ascii", []int{0, 0, 1}, 39, false, 250, compactColCap},
		{"diff_outofrange_col", 5000, 20, "ascii", []int{0, 1, 99}, 39, false, 0, compactColCap},
		{"diff_tail", 300, 6, "ascii", []int{0, 2}, 39, true, 299, compactColCap},
	}
	for _, s := range shapes {
		s := s
		t.Run(s.id, func(t *testing.T) {
			f := benchFrame(s.rows, s.cols, s.kind)
			o := RenderOpts{Width: 120, Height: s.height, Theme: theme.Default(),
				ShowBorders: s.borders, ShowRowNumbers: s.borders, TableCols: s.colsSel,
				NoEllipsis: len(s.colsSel) > 0, HeaderCur: -1}
			tv := &TableView{}
			if s.jump > 0 {
				tv.JumpTo(s.jump, f.NumRows())
			}
			_ = tv.Render(f, o) // 第一帧：填 modeSet/rowOff 钳位后的状态
			sub, _, mlo, mhi := tc14Window(o, tv, f, s.colsSel)
			want := tc14OldRoute(sub, mlo, mhi, s.capW)
			got := tv.naturalWidthsFor(sub, f, s.colsSel, tv.rowOff, mlo, mhi, s.capW, nil)
			if len(got) != len(want) {
				t.Fatalf("长度 %d ≠ %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("第 %d 列 %d ≠ 旧路 %d", i, got[i], want[i])
				}
			}
			// 再要一次（命中路）仍须逐格相等。
			again := tv.naturalWidthsFor(sub, f, s.colsSel, tv.rowOff, mlo, mhi, s.capW, nil)
			for i := range want {
				if again[i] != want[i] {
					t.Fatalf("命中路第 %d 列 %d ≠ 旧路 %d", i, again[i], want[i])
				}
			}
			// 整帧输出也必须与**同状态**的冷 tv 逐字节相等（冷 tv 要跳到同一行，
			// 否则比的是两个不同窗口——本段第一版就错在这里，登记 §14.7）。
			fresh := &TableView{}
			if s.jump > 0 {
				fresh.JumpTo(s.jump, f.NumRows())
			}
			if a, b := tv.Render(f, o), fresh.Render(f, o); a != b {
				t.Fatalf("渲染输出不等（len %d vs %d）", len(a), len(b))
			}
		})
	}
}

// TestTC14CacheCopyNotAliased ＝ 表内那份从不出门：调用方改了返回切片不能污染缓存。
func TestTC14CacheCopyNotAliased(t *testing.T) {
	f := benchFrame(1000, 6, "ascii")
	cols := []int{0, 2}
	o := RenderOpts{Width: 120, Height: 39, Theme: theme.Default(), TableCols: cols, NoEllipsis: true, HeaderCur: -1}
	tv := &TableView{}
	_ = tv.Render(f, o)
	sub, _, mlo, mhi := tc14Window(o, tv, f, cols)
	want := tc14OldRoute(sub, mlo, mhi, compactColCap)
	got := tv.naturalWidthsFor(sub, f, cols, tv.rowOff, mlo, mhi, compactColCap, nil)
	for i := range got {
		got[i] = 12345
	}
	after := tv.naturalWidthsFor(sub, f, cols, tv.rowOff, mlo, mhi, compactColCap, nil)
	for i := range want {
		if after[i] != want[i] {
			t.Fatalf("下游改写返回切片把缓存打了（第 %d 列 %d ≠ %d）", i, after[i], want[i])
		}
	}
	if tv.naturalWidthsFor(sub, f, cols, tv.rowOff, mlo, mhi, compactColCap, nil) == nil {
		t.Fatal("命中路返回了 nil")
	}
}

// TestTC14NonLimitedBranchUntouched ＝ 对照档的测试面：非限定列那一支一条没动。
// §11/§12 那块 widthCache 必须照常工作，新那块必须一次没被碰。
func TestTC14NonLimitedBranchUntouched(t *testing.T) {
	f := benchFrame(200, 20, "ascii")
	o := RenderOpts{Width: 120, Height: 39, Theme: theme.Default(), ShowRowNumbers: true, HeaderCur: -1}
	tv := &TableView{}
	for i := 0; i < 4; i++ {
		_ = tv.Render(f, o)
	}
	if tv.tcWidths.valid {
		t.Errorf("非限定列那一支把窗口表建起来了（builds=%d hits=%d）⇒ 分流条件写错了", tv.tcWidths.builds, tv.tcWidths.hits)
	}
	if tv.tcWidths.builds != 0 || tv.tcWidths.hits != 0 || tv.tcWidths.computed != 0 {
		t.Errorf("tcWidths 计数器非零: builds=%d hits=%d computed=%d", tv.tcWidths.builds, tv.tcWidths.hits, tv.tcWidths.computed)
	}
	if tv.widths.f != f {
		t.Errorf("非限定列那一支的 widthCache 键不是父帧（%v）⇒ §11/§12 那条路被动了", tv.widths.f)
	}
	if tv.widths.builds < 1 {
		t.Errorf("widthCache builds=%d，期望 ≥1", tv.widths.builds)
	}
	if b := tv.widths.builds; b > 1 {
		t.Errorf("同一父帧连渲四帧建了 %d 次缓存，期望 1（§12 的 reset 复用没生效）", b)
	}
}

// TestTC14LimitedBranchDoesNotUseFrameWidthCache ＝ 那一支**不碰** §11/§12 那块表的针：
// 键是父帧，而窗口子帧每帧新建 ⇒ 这一支只该动 tcWidths，widths 保持零值。
func TestTC14LimitedBranchDoesNotUseFrameWidthCache(t *testing.T) {
	f := benchFrame(50000, 20, "ascii")
	o := RenderOpts{Width: 120, Height: 39, Theme: theme.Default(), TableCols: []int{0, 1}, NoEllipsis: true, HeaderCur: -1}
	tv := &TableView{}
	for i := 0; i < 3; i++ {
		_ = tv.Render(f, o)
	}
	if tv.widths.f != nil || tv.widths.builds != 0 || tv.widths.computed != 0 {
		t.Errorf("限定列那一支把 §11/§12 那块表也用了（f=%v builds=%d computed=%d）", tv.widths.f, tv.widths.builds, tv.widths.computed)
	}
	if tv.tcWidths.builds != 1 {
		t.Errorf("限定列那一支三帧建了 %d 次表，期望 1", tv.tcWidths.builds)
	}
}

// TestTC14CapWSwitchRebuilds ＝ capW 那一维：compact/expanded 两条道互不串值。
func TestTC14CapWSwitchRebuilds(t *testing.T) {
	f := benchFrame(2000, 4, "cjk")
	cols := []int{0, 1}
	var cw tableColsWidthCache
	sub := tableColsSubFrameWindow(f, cols, 0, 200)
	cNat := cw.get(sub, f, cols, 0, 0, 200, compactColCap)
	eNat := cw.get(sub, f, cols, 0, 0, 200, expandedColCap)
	if cw.builds != 2 {
		t.Errorf("两种 capW 建了 %d 次，期望 2（capW 必须进键）", cw.builds)
	}
	if refC := tc14OldRoute(sub, 0, 200, compactColCap); len(cNat) == len(refC) {
		// expanded 之后再来一次 compact ⇒ 仍须等于 compact 的旧路值
		got := cw.get(sub, f, cols, 0, 0, 200, compactColCap)
		for i := range refC {
			if got[i] != refC[i] {
				t.Fatalf("切回 compact 后第 %d 列 %d ≠ 旧路 %d ⇒ 两条道串了", i, got[i], refC[i])
			}
		}
	}
	for i := range eNat {
		if eNat[i] < cNat[i] && eNat[i] != cNat[i] {
			t.Errorf("expanded 宽 %d < compact 宽 %d ⇒ 两条道的截断反了", eNat[i], cNat[i])
		}
	}
}

// --- 量尺：widthsNil → 命中 这一路的实测节省（§14.2 的另一条腿）---------------

var tc14Keep any // 防编译器杀臂（§13 的 keep13 同规矩）

// BenchmarkTC14CachePrice 把 M1 换掉的那一段单独定价：
//
//	nil    老路（naturalWidthsCached(…, nil)，每帧 400 格读＋400 次 StringWidth）
//	hit    新表命中（回一份长度 2 的副本，0 次格读）
//	render 生产整帧 Render(父帧)（改后含命中路）
//
// ① 段给的净上界是 351.7–452.6 µs/帧（widthsNil − widthsHit，同进程），本臂直接量
// nil − hit，两数应当同量级；口径与 SELECT14 ⑤-3 一致（同进程相邻臂）。
func BenchmarkTC14CachePrice(b *testing.B) {
	f := benchFrame(p14Rows, p14ParentCols, p14Kind)
	cols := []int{0, 1}
	base, win := 0, sampleRows
	sub := tableColsSubFrameWindow(f, cols, base, win)
	mlo, mhi := 0, win
	if mhi*len(cols) != 400 {
		b.Fatalf("格读预算 %d ≠ 400", mhi*len(cols))
	}
	want := tc14OldRoute(sub, mlo, mhi, compactColCap)
	var cw tableColsWidthCache
	_ = cw.get(sub, f, cols, base, mlo, mhi, compactColCap) // 暖表
	if cw.builds != 1 || cw.computed != 400 {
		b.Fatalf("暖表 builds=%d computed=%d，期望 1/400", cw.builds, cw.computed)
	}
	a := benchApp(f, p14WinW, p14WinH, cols)
	p := a.pane()
	if p == nil {
		b.Fatal("pane 必须存在")
	}
	o := RenderOpts{
		Width:          p14WinW,
		Height:         bodyHFor(p14WinH, false),
		Theme:          a.th,
		ShowBorders:    a.showBorders,
		ShowRowNumbers: a.showRowNumbers,
		HeaderCur:      p.Table.ColCursor(),
		TableCols:      p.TableCols,
		NoEllipsis:     len(p.TableCols) > 0,
		HScroll:        p.HScroll,
		HighlightQuery: a.searchQuery(),
		HighlightCols:  p.TableCols,
	}
	if p.Table.Expanded() {
		b.Fatal("E 档 wide 应为 0（compact 一支）")
	}
	for _, arm := range []string{"nil", "hit", "render"} {
		arm := arm
		b.Run(arm, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				switch arm {
				case "nil":
					got := tc14OldRoute(sub, mlo, mhi, compactColCap)
					if got[0] != want[0] || got[1] != want[1] {
						b.Fatal("老路宽度表与首摆不符")
					}
					tc14Keep = got[0]
				case "hit":
					got := cw.get(sub, f, cols, base, mlo, mhi, compactColCap)
					if cw.builds != 1 {
						b.Fatalf("命中路建了表（builds=%d, i=%d）", cw.builds, i)
					}
					if got[0] != want[0] || got[1] != want[1] {
						b.Fatal("命中回表与首摆不符")
					}
					tc14Keep = got[0]
				case "render":
					s := p.Table.Render(f, o)
					if n := strings.Count(s, "\n") + 1; n != o.Height {
						b.Fatalf("Render 行数 %d ≠ %d", n, o.Height)
					}
					tc14Keep = s[0]
				}
			}
		})
	}
}
