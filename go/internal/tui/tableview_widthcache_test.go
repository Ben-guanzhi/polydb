package tui

// widthCache（每格显示宽度记忆化）的契约测试。四条腿，缺一不可：
//   1) 与改前逐字同形的参考实现差分等值（naturalWidths 原样留在产品码里当参照）；
//   2) "缓存必须命中"这条**性质**（builds / computed 两个计数器）——差分只证明"改后＝改前"，
//      证明不了"优化真发生了"；
//   3) 键的每个维度都能失效（换帧指针 / 换行数 / 超限大帧回退）；
//   4) 负对照要有牙齿：把"存向量"那个错误形状实现出来，确认翻页差分**真的**能抓住它。
//
// 第七轮（§12）在此之上加了三条：换帧复用同一块（clears 而不是 builds）、越限大帧改走
// 窗口跨度（span/base 这一档的差分＋索引布局）、以及对账漏一次必须被 outside 计数器露出
// ——最后这条同时是"覆盖检查不是冗余防线"的证据：少了它下标会指向别的槽位。

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/internal/theme"
)

// --- 造帧助手 ---------------------------------------------------------------

// wcFrame 按 cells[r][c] 造一帧；names 与列数不符时改用默认的 "c%d"。
func wcFrame(names []string, cells [][]any) *tuidata.Frame {
	ncols := 0
	for _, row := range cells {
		if len(row) > ncols {
			ncols = len(row)
		}
	}
	if len(names) != ncols {
		names = make([]string, ncols)
		for i := range names {
			names[i] = fmt.Sprintf("c%d", i)
		}
	}
	f := &tuidata.Frame{Columns: make([]tuidata.Column, ncols)}
	for c := 0; c < ncols; c++ {
		col := make([]any, len(cells))
		for r, row := range cells {
			if c < len(row) {
				col[r] = row[c]
			}
		}
		f.Columns[c] = tuidata.NewColumn(names[c], tuidata.TypeString, col)
	}
	return f
}

// wcShapes 是差分电池的形状集：宽窄、早停、宽度非平凡的每一型都各占一列。
// 尤其注意 "ansi-escape" 与 "empty-cells" 两列——前者的显示宽度**不等于**字节数/字符数，
// 后者每一格的宽度都是 **0**，而 0 正是"没算过"的编码邻域（缓存存的是 width+1，
// 见 TestWidthCacheEmptyCellIsNotAMiss）。
var wcShapes = []struct {
	name  string
	cells [][]any
}{
	{"ascii", [][]any{
		{"a", "hello"},
		{"bb", "world!"},
		{"ccc", "x"},
	}},
	{"wide-first-then-narrow", [][]any{ // 第一格就顶到 cap，考验早停后剩下的格不该被算
		{strings.Repeat("w", 90), "s"},
		{"s", strings.Repeat("w", 90)},
		{"s", "s"},
	}},
	{"cjk", [][]any{
		{"客户名称", "地址"},
		{"简短", "客户名称测试数据一二三四五"},
		{"", "〇"},
	}},
	{"ansi-escape", [][]any{ // ansi.StringWidth 把转义序列算成 0 宽
		{"\x1b[31mred\x1b[0m", "plain"},
		{"\x1b[1;33mWARN\x1b[0m", "\x1b[0m"},
	}},
	{"emoji-combining", [][]any{
		{"🙂", "é"},
		{"🇨🇳", "àb"},
	}},
	{"types", [][]any{
		{int64(-12345), 3.14159, true, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
		{int64(0), 0.0, false, time.Date(2026, 10, 8, 13, 45, 12, 0, time.UTC)},
		{nil, nil, nil, nil},
	}},
	{"empty-cells", [][]any{
		{"", ""},
		{nil, ""},
		{"", nil},
	}},
	{"single-row", [][]any{{"only"}}},
}

// wcWindows 覆盖 lo 的每一侧（0 / 中间 / 越界钳制）与各档 cap。
var wcWindows = []struct {
	lo, hi, capW int
	tag          string
}{
	{0, 3, compactColCap, "head/40"},
	{0, 3, expandedColCap, "head/80"},
	{1, 3, compactColCap, "mid/40"},
	{2, 3, expandedColCap, "tail/80"},
	{1, 2, 4, "tiny-cap"},
	{0, 0, compactColCap, "empty-window"},
	{-5, 99, compactColCap, "clamped"},
	{0, 3, 1, "cap-1"},
	{0, 3, 0, "cap-0"},
}

// --- 1) 差分等值 -------------------------------------------------------------

func TestWidthCacheEqualsUncachedBattery(t *testing.T) {
	for _, sh := range wcShapes {
		sh := sh
		t.Run(sh.name, func(t *testing.T) {
			f := wcFrame(nil, sh.cells)
			var c widthCache
			// 每档跑三遍：第一遍建缓存，后两遍走命中路径。
			for rep := 0; rep < 3; rep++ {
				for _, w := range wcWindows {
					want := naturalWidths(f, w.lo, w.hi, w.capW)
					got := naturalWidthsCached(f, w.lo, w.hi, w.capW, &c)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("rep=%d win=%s shape=%s\n cached=%v\n uncached=%v", rep, w.tag, sh.name, got, want)
					}
				}
			}
			// nil 缓存必须与参考实现逐字同路（这是 Render 里限定列那一支拿到的形状）。
			for _, w := range wcWindows {
				got := naturalWidthsCached(f, w.lo, w.hi, w.capW, nil)
				want := naturalWidths(f, w.lo, w.hi, w.capW)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("nil 缓存那一支与参考实现不一致 win=%s shape=%s", w.tag, sh.name)
				}
			}
		})
	}
}

// 随机化差分：帧形状随机、窗口随机、cap 随机，且**每个窗口先用参考实现算一遍、
// 再用缓存算**——差分要打在缓存已经 warmed 的状态上，只打冷帧等于没测命中路径。
func TestWidthCacheEqualsUncachedRandomized(t *testing.T) {
	alphabet := []any{
		"", "a", "ab", "ABC", "客户", "客户名称测试", "🙂", "\x1b[31mr\x1b[0m",
		int64(-987654321), 3.5, true, nil, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC),
		strings.Repeat("q", 55), strings.Repeat("中", 25),
	}
	rnd := uint64(20261008)
	next := func(n int) int { // xorshift，避免依赖 math/rand 的全局态
		rnd ^= rnd << 13
		rnd ^= rnd >> 7
		rnd ^= rnd << 17
		return int(rnd % uint64(n))
	}
	caps := []int{1, 4, compactColCap, expandedColCap, 120}
	for trial := 0; trial < 40; trial++ {
		ncols := 1 + next(9)
		nrows := 1 + next(60)
		cells := make([][]any, nrows)
		for r := range cells {
			row := make([]any, ncols)
			for c := range row {
				row[c] = alphabet[next(len(alphabet))]
			}
			cells[r] = row
		}
		f := wcFrame(nil, cells)
		var c widthCache
		for pass := 0; pass < 3; pass++ {
			lo := next(nrows + 2)
			hi := lo + next(nrows+1)
			capW := caps[next(5)]
			want := naturalWidths(f, lo, hi, capW)
			got := naturalWidthsCached(f, lo, hi, capW, &c)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial=%d pass=%d lo=%d hi=%d capW=%d\n cached=%v\n uncached=%v", trial, pass, lo, hi, capW, got, want)
			}
		}
	}
}

// --- 2) "必须命中"这条性质 ----------------------------------------------------

func TestWidthCacheMustHitAndStaysLazy(t *testing.T) {
	f := wcFrame([]string{"h1", "h2"}, [][]any{
		{"aa", "bbbb"},
		{"ccc", "d"},
		{"dddddd", "e"},
		{"f", "gggggggg"},
	})
	var c widthCache
	if got := naturalWidthsCached(f, 0, 4, compactColCap, &c); len(got) != 2 {
		t.Fatalf("向量长度 %d，应为 2 列", len(got))
	}
	if c.builds != 1 {
		t.Fatalf("首帧 builds=%d，应为 1", c.builds)
	}
	if c.computed != 8 {
		t.Fatalf("首帧 computed=%d，应为 4 行 × 2 列 = 8", c.computed)
	}

	// 同一帧第二帧：一个格都不许再付钱；换 cap（40→80）也不许重建，
	// 因为缓存存的是**原始宽度**、截断发生在向量层。
	before := c.computed
	naturalWidthsCached(f, 0, 4, compactColCap, &c)
	naturalWidthsCached(f, 0, 4, expandedColCap, &c)
	if c.builds != 1 {
		t.Fatalf("同帧两遍后 builds=%d，应为 1（换 cap 不许重建）", c.builds)
	}
	if c.computed != before {
		t.Fatalf("同帧两遍后 computed 从 %d 涨到 %d，缓存没命中", before, c.computed)
	}

	// 窗口只碰前两行 ⇒ 只有那 4 格付钱；窗口外的格永不被计算。
	var lazy widthCache
	naturalWidthsCached(f, 0, 2, compactColCap, &lazy)
	if lazy.computed != 4 {
		t.Fatalf("只看前两行时 computed=%d，应为 4（窗口外的格不该付钱）", lazy.computed)
	}

	// 早停：capW=2 时两列都在第一格就到门 ⇒ 全帧只付 2 格。
	// 参考实现在同一处 break，所以两边的"付钱格数"必须一致。
	var es widthCache
	want := naturalWidths(f, 0, 4, 2)
	got := naturalWidthsCached(f, 0, 4, 2, &es)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("早停档差分不等 cached=%v uncached=%v", got, want)
	}
	if es.computed != 2 {
		t.Fatalf("早停档 computed=%d，应为 2（每列只付第一格）", es.computed)
	}
}

// 0 宽度是一格的**合法**值（空串、nil），而 make 的零值也是 0——缓存把 width+1 存进 buf
// 正是为了区分这两件事。这一格若写成存 width，第二次调用会把每个空格重新算一遍：
// 差分照样通过（结果对），但"必须命中"这条性质被悄悄打破。
func TestWidthCacheEmptyCellIsNotAMiss(t *testing.T) {
	f := wcFrame([]string{"h"}, [][]any{{nil}, {""}, {nil}, {""}})
	var c widthCache
	naturalWidthsCached(f, 0, 4, compactColCap, &c)
	first := c.computed
	if first != 4 {
		t.Fatalf("全空格一帧 computed=%d，应为 4（参考实现每帧也要量 4 格）", first)
	}
	naturalWidthsCached(f, 0, 4, compactColCap, &c)
	if c.computed != first {
		t.Fatalf("全空格帧第二遍 computed 从 %d 涨到 %d ⇒ 0 宽度被当成\"没算过\"（width+1 编码失效）", first, c.computed)
	}
	if got := naturalWidthsCached(f, 0, 4, compactColCap, &c); got[0] != 1 {
		t.Fatalf("全空格列宽应为 clamp(0,1,cap)=1，实得 %d", got[0])
	}
}

// --- 3) 键的每个维度都能失效 ---------------------------------------------------

func TestWidthCacheKeyInvalidation(t *testing.T) {
	base := wcFrame([]string{"h1", "h2"}, [][]any{{"aa", "bb"}, {"cc", "dd"}})

	t.Run("WithCell 换新帧指针 ⇒ 块长相同就只清零", func(t *testing.T) {
		var c widthCache
		naturalWidthsCached(base, 0, 2, compactColCap, &c)
		b0, cl0 := c.builds, c.clears
		edited := base.WithCell(0, 0, strings.Repeat("z", 30))
		got := naturalWidthsCached(edited, 0, 2, compactColCap, &c)
		if c.builds != b0 {
			t.Fatalf("builds=%d，应为 %d（同形状换帧走 §12 的复用，不该再 make）", c.builds, b0)
		}
		if c.clears != cl0+1 {
			t.Fatalf("clears=%d，应为 %d（换帧必须换料，只是不清零而是复用同一块）", c.clears, cl0+1)
		}
		if want := naturalWidths(edited, 0, 2, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("新帧差分不等 cached=%v uncached=%v", got, want)
		}
		if got[0] != 30 {
			t.Fatalf("WithCell 后的宽度没生效：%v", got)
		}
	})

	t.Run("AppendRow 改行数 ⇒ 块长变了才 make", func(t *testing.T) {
		f := wcFrame([]string{"h1", "h2"}, [][]any{{"aa", "bb"}})
		var c widthCache
		naturalWidthsCached(f, 0, 1, compactColCap, &c)
		b0 := c.builds
		f.AppendRow([]any{strings.Repeat("q", 20), "x"}) // 流式追加：行数变了
		got := naturalWidthsCached(f, 0, 2, compactColCap, &c)
		if c.builds != b0+1 {
			t.Fatalf("builds=%d，应为 %d（行数变了、块长对不上 ⇒ 只能重建）", c.builds, b0+1)
		}
		if want := naturalWidths(f, 0, 2, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("追加后差分不等 cached=%v uncached=%v", got, want)
		}
		if got[0] != 20 {
			t.Fatalf("追加行的宽度没进入计算：%v", got)
		}
	})

	t.Run("Select 换指针 ⇒ 重建", func(t *testing.T) {
		f := wcFrame([]string{"h1", "h2"}, [][]any{{"aa", "bb"}, {"cccc", "dd"}, {"e", "fffff"}})
		var c widthCache
		naturalWidthsCached(f, 0, 3, compactColCap, &c)
		b0 := c.builds
		sel := f.Select([]int{2})
		got := naturalWidthsCached(sel, 0, 1, compactColCap, &c)
		if c.builds != b0+1 {
			t.Fatalf("builds=%d，应为 %d", c.builds, b0+1)
		}
		if want := naturalWidths(sel, 0, 1, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("Select 后差分不等 cached=%v uncached=%v", got, want)
		}
	})

	t.Run("超限大帧改走窗口跨度 ⇒ 只建一块预算那么大的料", func(t *testing.T) {
		ncols := 10
		nrows := widthCacheMaxCells/ncols + 1 // 刚好越过整帧上限
		f := wcFrame(nil, wcRectCellTable(nrows, ncols, "x"))
		var wc widthCache
		got := naturalWidthsCached(f, 0, 3, compactColCap, &wc)
		if wc.span != widthCacheSpanRows || wc.base != 0 || wc.buf == nil {
			t.Fatalf("超限帧应当走窗口档：span=%d base=%d buf==nil? %v", wc.span, wc.base, wc.buf == nil)
		}
		if len(wc.buf) != widthCacheSpanRows*ncols {
			t.Fatalf("窗口档的料该是 %d 格，实得 %d", widthCacheSpanRows*ncols, len(wc.buf))
		}
		if len(wc.buf)*4 >= nrows*ncols*4/2 {
			t.Fatalf("窗口档没省下一半以上：%d 格 vs 整帧 %d 格", len(wc.buf), nrows*ncols)
		}
		if want := naturalWidths(f, 0, 3, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("超限帧差分不等")
		}
		if wc.outside != 0 {
			t.Fatalf("超限帧第一遍就出现越界取数 %d ⇒ 锚与取数不一致", wc.outside)
		}
		// 同一超限帧第二遍不许再 make（键相符、窗口仍在覆盖内）。
		second := naturalWidthsCached(f, 0, 3, expandedColCap, &wc)
		if wc.builds != 1 || wc.clears != 0 || wc.reanchors != 0 {
			t.Fatalf("同帧第二遍不该动料：builds=%d clears=%d reanchors=%d", wc.builds, wc.clears, wc.reanchors)
		}
		if want := naturalWidths(f, 0, 3, expandedColCap); !reflect.DeepEqual(second, want) {
			t.Fatalf("超限帧第二遍差分不等")
		}
	})

	t.Run("列极多的宽表：连窗口档都放不下 ⇒ 一块都不建", func(t *testing.T) {
		ncols := widthCacheMaxCells/widthCacheSpanRows + 1 // 501 ⇒ 上限只允许 399 行 < 窗口预算
		nrows := widthCacheMaxCells/ncols + 1
		f := wcFrame(nil, wcRectCellTable(nrows, ncols, "x"))
		var wc widthCache
		got := naturalWidthsCached(f, 0, 3, compactColCap, &wc)
		if wc.buf != nil || wc.builds != 0 || wc.span != 0 {
			t.Fatalf("放不下的一档竟然建了料：buf==nil? %v builds=%d span=%d", wc.buf != nil, wc.builds, wc.span)
		}
		if want := naturalWidths(f, 0, 3, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("无料可建时差分不等")
		}
	})

	t.Run("恰好在上限内 ⇒ 建一次", func(t *testing.T) {
		ncols := 10
		nrows := widthCacheMaxCells / ncols
		f := wcFrame(nil, wcRectCellTable(nrows, ncols, "x"))
		var wc widthCache
		naturalWidthsCached(f, 0, 2, compactColCap, &wc)
		if wc.builds != 1 || len(wc.buf) != nrows*ncols {
			t.Fatalf("上限内应当建一次：builds=%d len(buf)=%d want %d", wc.builds, len(wc.buf), nrows*ncols)
		}
	})
}

func wcRectCellTable(rows, cols int, v string) [][]any {
	cells := make([][]any, rows)
	for r := range cells {
		row := make([]any, cols)
		for c := range row {
			row[c] = v
		}
		cells[r] = row
	}
	return cells
}

// --- 4) 负对照：向量缓存必须被翻页差分抓住 ---------------------------------------

// vecCacheWrong 是**故意写错**的形状：缓存 naturalWidths 的输出向量，键里没有 lo/hi。
// 它就是候选 10.6-1 警告的那一型（"lo＝tv.rowOff 参与，:288"）。
type vecCacheWrong struct {
	f    *tuidata.Frame
	capW int
	vec  []int
	seen bool
}

func naturalWidthsVecCached(f *tuidata.Frame, lo, hi, capW int, c *vecCacheWrong) []int {
	if c.seen && c.f == f && c.capW == capW {
		return c.vec // 错：向量里混着 lo 的信息，键却没带它
	}
	v := naturalWidths(f, lo, hi, capW)
	c.f, c.capW, c.vec, c.seen = f, capW, v, true
	return v
}

func TestNegativeControlVectorCacheIsCaughtByScroll(t *testing.T) {
	// 每列的宽度随窗口移动而变 ⇒ 这是"存向量"必然出错的那类帧。
	f := wcFrame([]string{"h"}, [][]any{
		{"a"}, {"bb"}, {"cccc"}, {"dddddd"}, {"eeeeeeee"}, {"WWWWWWWWWW"},
	})
	var wrong vecCacheWrong
	naturalWidthsVecCached(f, 0, 3, compactColCap, &wrong) // 只看前 3 行
	got := naturalWidthsVecCached(f, 3, 6, compactColCap, &wrong)
	want := naturalWidths(f, 3, 6, compactColCap)
	if reflect.DeepEqual(got, want) {
		t.Fatal("负对照失效：向量缓存竟然没被翻页差分抓住 ⇒ 这一格没有牙齿，后面的结论都不作数")
	}
	if got[0] != 4 {
		t.Fatalf("负对照应当给出\"停在上一帧宽度 4\"这个可辨认的错值，实得 %v", got)
	}

	// 同一格上真正的按格缓存必须与参考实现相等（对照的对照）。
	var right widthCache
	naturalWidthsCached(f, 0, 3, compactColCap, &right)
	mid := right.computed
	if !reflect.DeepEqual(naturalWidthsCached(f, 3, 6, compactColCap, &right), want) {
		t.Fatalf("按格缓存在翻页上失守：want=%v", want)
	}
	if right.builds != 1 {
		t.Fatalf("翻页不该重建缓存：builds=%d", right.builds)
	}
	if right.computed-mid != 3 {
		t.Fatalf("翻页只该为新进入窗口的 3 格付钱，实得增量 %d", right.computed-mid)
	}
}

// --- 5) 产品边界：Render 的渲染结果不许因缓存而变 -------------------------------

// 热缓存（同一个 TableView 逐页翻到第 k 页）必须与冷缓存（每次新开一个 TableView 直接跳页）
// 逐字节相同。这一条打在 Render 上，是把上面的单元差分接到"用户看得见的那张表"上。
func TestRenderCachedMatchesColdRender(t *testing.T) {
	th := theme.Default()
	rowCycle := func(n int) [][]any {
		cells := make([][]any, n)
		cycle := [][]any{
			{"aa", "bb", strings.Repeat("客", 3)},
			{strings.Repeat("d", 12), "e", "f"},
			{"x", strings.Repeat("宽", 9), "y"},
			{int64(-901234), "", nil},
			{"\x1b[31mWARN\x1b[0m", "z", strings.Repeat("q", 21)},
		}
		for r := range cells {
			row := cycle[r%len(cycle)]
			cells[r] = append([]any(nil), row...)
		}
		return cells
	}
	views := []struct {
		name  string
		cells [][]any
		opts  RenderOpts
	}{
		{"compact-noborder", rowCycle(24),
			RenderOpts{Width: 46, Height: 8, Theme: th}},
		{"borders-rownb", rowCycle(24),
			RenderOpts{Width: 60, Height: 10, Theme: th, ShowBorders: true, ShowRowNumbers: true}},
		{"severity", rowCycle(24),
			RenderOpts{Width: 46, Height: 7, Theme: th, ShowRowNumbers: true, SeverityCol: "c1"}},
	}
	for _, v := range views {
		v := v
		t.Run(v.name, func(t *testing.T) {
			f := wcFrame(nil, v.cells)
			warm := &TableView{}
			scrolled := 0
			for off := 0; off < f.NumRows(); off++ {
				warm.rowOff, warm.sel = off, off
				got := warm.Render(f, v.opts)
				cold := &TableView{}
				cold.rowOff, cold.sel = off, off
				want := cold.Render(f, v.opts)
				if got != want {
					t.Fatalf("rowOff=%d 热缓存与冷渲染不一致\n--- warm ---\n%s\n--- cold ---\n%s", off, got, want)
				}
				if warm.rowOff != off {
					scrolled++ // 页尾钳制会让实际窗口小于 off，计数即可
				}
				if warm.widths.builds > 1 {
					t.Fatalf("rowOff=%d 时宽度缓存重建了 %d 次（同一帧翻页只该建一次）", off, warm.widths.builds)
				}
			}
			if scrolled == 0 {
				t.Fatal("量尺没真正翻页（rowOff 从未被钳制）⇒ 这一格是空转")
			}
			if warm.widths.builds != 1 {
				t.Fatalf("整轮下来缓存建了 %d 次，应为 1", warm.widths.builds)
			}
		})
	}
}

// 限定列那一支显式不接缓存：每帧新建子帧 ⇒ 键必失配，接上只会每帧多一份永不命中的分配。
// 这一格钉住"没接"这个事实，否则将来有人把 wc 的条件写反就无人报警。
func TestRenderTableColsDoesNotBuildWidthCache(t *testing.T) {
	f := wcFrame([]string{"ts", "body"}, wcRectCellTable(40, 2, "x"))
	th := theme.Default()
	tv := &TableView{}
	o := RenderOpts{Width: 60, Height: 12, Theme: th, TableCols: []int{0, 1}, NoEllipsis: true}
	for i := 0; i < 3; i++ {
		tv.Render(f, o)
	}
	if tv.widths.builds != 0 {
		t.Fatalf("限定列那一支建了宽度缓存（builds=%d），应当完全不接", tv.widths.builds)
	}
	// 同一帧关掉限定列再渲染：这次必须建、且只建一次。
	tv2 := &TableView{}
	for i := 0; i < 3; i++ {
		tv2.Render(f, RenderOpts{Width: 60, Height: 12, Theme: th})
	}
	if tv2.widths.builds != 1 {
		t.Fatalf("非限定列渲染三帧只该建一次缓存，实得 builds=%d", tv2.widths.builds)
	}
}

// --- 6) 原地改格值已被类型系统消灭；钉住受支持的换帧路径 --------------------------------------

// tuidata.Column 的 cells 字段非导出后，包外直写格值不再能编译（4.3 的结构强制）。
// 这里改为钉受支持的变更路径：改一格走 WithCell 换出新帧 ⇒ 缓存按帧指针判 miss、
// 重建并给出新宽度；同帧重复读仍是命中。
func TestWidthCacheRebuildsOnFrameReplace(t *testing.T) {
	f := wcFrame([]string{"h"}, [][]any{{"a"}, {"b"}, {"c"}})
	var c widthCache
	first := naturalWidthsCached(f, 0, 3, compactColCap, &c)
	again := naturalWidthsCached(f, 0, 3, compactColCap, &c)
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("同帧重复读没有命中缓存（%v vs %v）", first, again)
	}
	if c.builds != 1 {
		t.Fatalf("同帧读不该重建缓存：builds=%d", c.builds)
	}

	g := f.WithCell(1, 0, strings.Repeat("Z", 25))
	replaced := naturalWidthsCached(g, 0, 3, compactColCap, &c)
	fresh := naturalWidths(g, 0, 3, compactColCap)
	if !reflect.DeepEqual(replaced, fresh) {
		t.Fatalf("换新帧后宽度 %v != 现算 %v ⇒ 缓存没按新帧重建", replaced, fresh)
	}
	if c.clears != 1 {
		t.Fatalf("换帧应复用缓冲并清零一次：clears=%d", c.clears)
	}
	if c.builds != 1 {
		t.Fatalf("同尺寸换帧不该重新 make：builds=%d", c.builds)
	}
	if reflect.DeepEqual(replaced, first) {
		t.Fatal("25 字符宽的改写后宽度与旧值相同 ⇒ 新帧没被量宽")
	}
}

// --- 7) 缓冲区布局钉：界内帧仍是列主序 col*rows+r ----------------------------------

// 转置下标（写成 r*ncols+col）在**行为**上是个等价变异：读写用同一个式子 ⇒ 每格仍然
// 映射到自己的槽，差分、builds、computed 全绿（§11.4 的 M5 就是这个情况，实测 0 红）。
// 所以注释里"列主序＝量宽那一遍按列连续访问"这句主张，只能由一个直接看 buf 的断言钉住。
// 它顺带钉第二件事：窗口外的槽必须还是 0，即惰性落在缓冲区的**坐标**上而不是只在计数上。
func TestWidthCacheCellMajorLayout(t *testing.T) {
	rows, cols, win := 5, 3, 3
	f := wcFrame(nil, [][]any{
		{"a", "bb", "ccc"},
		{"dddd", "e", "ff"},
		{"g", "hhh", "i"},
		{"jj", "k", "llll"},
		{"m", "nnn", "o"},
	})
	var c widthCache
	naturalWidthsCached(f, 0, win, compactColCap, &c)
	if len(c.buf) != rows*cols {
		t.Fatalf("buf 长度 %d ≠ rows*cols=%d", len(c.buf), rows*cols)
	}
	// 界内帧必须保持整帧跨度：span==rows、base==0 ⇒ §12 的 col*span+(r-base) 与第六轮
	// 的 col*rows+r 逐位相同，上面/下面的槽位断言因此同时是"两轮没分叉"的钉。
	if c.span != rows || c.base != 0 {
		t.Fatalf("界内帧应当是整帧跨度：span=%d base=%d（应为 %d/0）", c.span, c.base, rows)
	}
	for col := 0; col < cols; col++ {
		for r := 0; r < win; r++ {
			i := col*rows + r
			want := ansi.StringWidth(f.CellString(r, col)) + 1
			if int(c.buf[i]) != want {
				t.Fatalf("槽 col*rows+r=%d 存的是 %d，应为 宽度+1=%d（行 r=%d 列 col=%d）⇒ 布局不是列主序",
					i, c.buf[i], want, r, col)
			}
		}
		for r := win; r < rows; r++ {
			if v := c.buf[col*rows+r]; v != 0 {
				t.Fatalf("窗口外槽 col*rows+r=%d 非 0（%d）⇒ 越窗的格被算过了", col*rows+r, v)
			}
		}
	}
	// 牙齿自检：这个形状下两种布局必须给出不同下标，否则上面那条断言抓不到转置。
	distinguishable := false
	for col := 0; col < cols && !distinguishable; col++ {
		for r := 0; r < win; r++ {
			if col*rows+r != r*cols+col {
				distinguishable = true
				break
			}
		}
	}
	if !distinguishable {
		t.Fatalf("rows=%d/cols=%d 让列主序与行主序不可区分 ⇒ 这条布局钉没有牙齿", rows, cols)
	}
}

// --- 8) 量尺侧 Skip 判据必须与产品侧建缓存判据同源 --------------------------------

// tooBigForWidthCache / modelBlockCells 住在 view_frame_bench_test.go：前者决定
// measureCached / measureCold / bufAlloc / bufReuse 四臂是否 Skip，后者给出 bufReuse
// 那条臂建模的块长。判据与产品码不一致时，症状不是报错而是"量出一个没意义的倍数"，
// 所以这里用产品的**实际行为**（到底分配了没有、分了多长）来对表。
//
// §12 之后第一个词的意思变了：不再是"整帧放不下 ⇒ 整档不缓存"，而是"整帧档与窗口档都放不下
// ⇒ 一块料都不建"。所以表里既有"整帧界内"的、也有"整帧越限但窗口档建得起"的（它们都
// 应当 builds=true），只有列极多的那一型才是 false。第二个词是本轮新加的，它必须同时
// 覆盖界内（rows*ncols）与越限（恒定 widthCacheSpanRows*ncols）两档，否则量尺 G 档的
// bufReuse 就是在量一块产品根本不放的料。
func TestWidthCacheSkipPredicateMatchesProduct(t *testing.T) {
	for _, tc := range []struct{ rows, cols int }{
		{0, 3}, {5, 0}, {1, 1}, {200, 20}, {5000, 20}, {10000, 20}, {10001, 20}, {666, 300},
		{200000, 20}, {4001, 50}, {400, 501},
	} {
		f := wcFrame(nil, wcRectCellTable(tc.rows, tc.cols, "x"))
		var c widthCache
		naturalWidthsCached(f, 0, min(2, f.NumRows()), compactColCap, &c)
		builds := c.buf != nil
		if got, want := tooBigForWidthCache(f), !builds; got != want {
			t.Fatalf("rows=%d cols=%d：量尺判\"无料\"=%v，产品侧实际建缓存=%v ⇒ 两边不同源",
				tc.rows, tc.cols, got, builds)
		}
		if got, want := modelBlockCells(f), len(c.buf); got != want {
			t.Fatalf("rows=%d cols=%d：模型块长 %d ≠ 产品块长 %d ⇒ bufReuse 量的不是产品那块料",
				tc.rows, tc.cols, got, want)
		}
	}
	// 牙齿：越限那一型必须真的给出"比整帧小一块"的常数长度，否则这条对表只是同义反复。
	f := wcFrame(nil, wcRectCellTable(4001, 50, "x"))
	if got, want := modelBlockCells(f), 4001*50; got >= want {
		t.Fatalf("越限档模型块长 %d ≥ 整帧 %d ⇒ 窗口跨度那一档没被量到", got, want)
	}
}

// --- 9) 打字那一支：缓存结构上拿不到收益（§11.0 的头号诚实结论） --------------------

// 搜索态渲染的是 searchbar.go:111 `s.preview = s.base.Select(rows)` 造出来的**新帧**，
// 而 Select 每次都重新分配（frame.go:120-130，没有"全保留就返回自己"的短路）⇒ 键里的
// 帧指针逐键失配。这一格锁的是**当前机制**：如果将来有人让预览帧复用（或把宽度塞进
// Select 的拷贝里），这里会变红，那才是 §11 的结论需要重写的时刻。
//
// §12 改的是"失配之后付什么"：预览帧行数逐键变化 ⇒ 块长对不上 ⇒ 仍然 make；只有相邻两键
// 命中数相同（rows 相等）那一支降级成 clear。所以钉的是"每键必换料"这条弱不变量
// （builds+clears），并且把两笔的分配如实报出来——§11 那句"每键一次 make"自此不再全称成立。
func TestTypingPathReplacesWidthBlockEachKeystroke(t *testing.T) {
	f := benchSessionFrame(200, 12, "ascii")
	a := benchApp(f, 120, 40, nil)
	a.View()
	p := a.pane()
	if p.Table.widths.builds != 1 {
		t.Fatalf("浏览态首帧只该建一次缓存，实得 builds=%d", p.Table.widths.builds)
	}
	a.View()
	a.View()
	if p.Table.widths.builds != 1 {
		t.Fatalf("同一帧渲三遍 builds=%d，应为 1 ⇒ 浏览态没命中，下面的对照没意义", p.Table.widths.builds)
	}

	a.search.StartCols(f, false, allCols(12))
	beforeB, beforeC := p.Table.widths.builds, p.Table.widths.clears
	var ptrs []*tuidata.Frame
	for _, d := range deltas(keysFor("ascii")) {
		a.search.Type(d)
		a.View()
		pv := a.search.Preview()
		if pv == nil || pv == f || pv.NumRows() >= f.NumRows() {
			t.Fatalf("query=%q 的预览帧不是筛出来的新帧（pv==f? %v, rows=%d/%d）⇒ 打字腿空转",
				d, pv == f, pv.NumRows(), f.NumRows())
		}
		for _, prev := range ptrs {
			if prev == pv {
				t.Fatal("两键共用一个预览帧指针 ⇒ Select 有短路了，本格的结论要重写")
			}
		}
		ptrs = append(ptrs, pv)
	}
	made, cleared := p.Table.widths.builds-beforeB, p.Table.widths.clears-beforeC
	t.Logf("打字 %d 键：make=%d clear=%d（§11 的口径是每键一次 make；现在块长对上那几键降级成 clear）",
		len(ptrs), made, cleared)
	if got := made + cleared; got < len(ptrs) {
		t.Fatalf("连打 %d 键只换料 %d 次 ⇒ 打字那一支居然命中了（与 §11 的记录不符）",
			len(ptrs), got)
	}
}

// --- 10) §12：越限大帧的窗口跨度档 ------------------------------------------------

// wcOverLimitFrame 造一帧"整帧放不下、窗口档放得下"的表：ncols＝50 ⇒ 上限只允许 4,000 行，
// rows＝4,001 刚好越过；整帧要 200,050 格，窗口档只要 400×50＝20,000 格。
// 取值用 poolASCII[(r+c)%L]（benchFrame），相邻行的宽度不同 ⇒ 下面的"别名槽位"那一格有牙。
func wcOverLimitFrame() *tuidata.Frame { return benchFrame(4001, 50, "ascii") }

// 差分等值：剧本里每一帧的向量与 naturalWidths 逐列相等，并且这条道上
// 一次越界取数都不许有（outside==0 是锚与取数一致性的性质钉）。
func TestWidthCacheWindowSpanEqualsReference(t *testing.T) {
	f := wcOverLimitFrame()
	rows, ncols := f.NumRows(), f.NumCols()
	var c widthCache
	ws := scrollScript(rows, 35)
	for k, w := range ws {
		hi := min(rows, w.lo+sampleRows)
		got := naturalWidthsCached(f, w.lo, hi, compactColCap, &c)
		if want := naturalWidths(f, w.lo, hi, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 帧 lo=%d：窗口档差分不等 cached=%v want=%v", k, w.lo, got[:4], want[:4])
		}
		if c.base < 0 || c.base+c.span > rows {
			t.Fatalf("第 %d 帧：锚越界 base=%d span=%d rows=%d", k, c.base, c.span, rows)
		}
		if w.lo < c.base || hi > c.base+c.span {
			t.Fatalf("第 %d 帧：窗口 [%d,%d) 不在覆盖 [%d,%d) 内 ⇒ 下一格必然出现越界取数",
				k, w.lo, hi, c.base, c.base+c.span)
		}
	}
	if c.outside != 0 {
		t.Fatalf("剧本里出现越界取数 %d 次 ⇒ 对账与取数不一致", c.outside)
	}
	if c.builds != 1 {
		t.Fatalf("窗口档的块长恒定（预算来自常量不是窗高）⇒ 整条剧本只该 make 一次，实得 %d", c.builds)
	}
	if c.reanchors == 0 || c.clears == 0 {
		t.Fatalf("剧本跑了 47 个窗口却没有重放锚（reanchors=%d clears=%d）⇒ 这条腿空转", c.reanchors, c.clears)
	}
	if len(c.buf) != widthCacheSpanRows*ncols {
		t.Fatalf("窗口档的料应是 %d 格，实得 %d", widthCacheSpanRows*ncols, len(c.buf))
	}
	t.Logf("窗口档：buf=%d 格 vs 整帧 %d 格｜make=%d clear=%d reanchor=%d computed=%d outside=%d",
		len(c.buf), rows*ncols, c.builds, c.clears, c.reanchors, c.computed, c.outside)
}

// 索引布局钉（§11.4 的 M5 教训：行为等价的变异抓不到"表示不同"，必须直接看 buf）。
// 这里同时钉三件事：①槽位＝col*span+(r-base)；②覆盖之外的格还是 0（惰性落在坐标上）；
// ③少了 cellWidth 的覆盖检查就会读到**别格的宽度**——那才是它不是冗余防线的证据。
func TestWidthCacheWindowIndexing(t *testing.T) {
	f := wcOverLimitFrame()
	var c widthCache
	lo, hi := 0, sampleRows
	naturalWidthsCached(f, lo, hi, compactColCap, &c)
	if c.span != widthCacheSpanRows || c.base != 0 {
		t.Fatalf("第一遍应当是 base=0/span=%d，实得 base=%d span=%d", widthCacheSpanRows, c.base, c.span)
	}
	// ① 窗口里被算过的槽必须存着自己的宽度+1；早停没碰到的槽是 0，所以判据写成
	//    "非 0 就必须对上自己"——转置/错跨度都会让它对上别格的值。
	for col := 0; col < f.NumCols(); col++ {
		for r := lo; r < hi; r++ {
			v := c.buf[col*c.span+(r-c.base)]
			if v == 0 {
				continue
			}
			if want := ansi.StringWidth(f.CellString(r, col)) + 1; int(v) != want {
				t.Fatalf("槽 col*span+(r-base)=%d 存的是 %d，应为宽度+1=%d（r=%d col=%d）⇒ 布局不是列主序的跨度",
					col*c.span+(r-c.base), v, want, r, col)
			}
		}
	}
	// ② 覆盖内、窗口下方那一段（hi..base+span）压根没被扫到 ⇒ 必须全 0：惰性落在
	//    buf 的**坐标**上，不只是落在 computed 计数上。
	for col := 0; col < f.NumCols(); col++ {
		for r := hi; r < c.base+c.span; r++ {
			if v := c.buf[col*c.span+(r-c.base)]; v != 0 {
				t.Fatalf("窗口下方 r=%d col=%d 的槽被写了（%d）⇒ 扫描越过了 hi", r, col, v)
			}
		}
	}
	// 牙齿自检：span 与 rows 必须不同，否则 col*span+(r-base) 与 col*rows+r 不可区分。
	if c.span == f.NumRows() {
		t.Fatal("span==rows ⇒ 这一格抓不到跨度用错（4,001 行应当越限）")
	}
	// ③ 别名演示：r 落在覆盖之外时，col*span+(r-base) 会指到下一列的地盘。
	//    若哪天有人删掉 cellWidth 的覆盖检查，读到的就是别格的宽度。
	out := c.base + c.span // 第一个出覆盖的行
	alias := c.buf[out-c.base]
	if alias == 0 {
		t.Fatalf("别名槽位 %d 还没被算过，这一格没有牙齿（换个窗口再试）", out-c.base)
	}
	if wantNext := ansi.StringWidth(f.CellString(0, 1)) + 1; int(alias) != wantNext {
		t.Fatalf("别名槽位存的不是 (r=0,col=1) 的宽度＋1（%d vs %d）⇒ 本格的形状假设变了", alias, wantNext)
	}
	distinguishable := false
	for r := out; r < out+30 && r < f.NumRows(); r++ {
		if ansi.StringWidth(f.CellString(r, 0))+1 != int(alias) {
			distinguishable = true
			break
		}
	}
	if !distinguishable {
		t.Fatal("覆盖外 30 行的第 0 列宽度全等于别格值 ⇒ 这一格没有牙齿，换帧形状")
	}
	// 正路：窗口跳到覆盖之外（lo=2,000）⇒ 重放锚，差分仍等、outside 仍为 0。
	lo2, hi2 := 2000, 2000+sampleRows
	got := naturalWidthsCached(f, lo2, hi2, compactColCap, &c)
	if want := naturalWidths(f, lo2, hi2, compactColCap); !reflect.DeepEqual(got, want) {
		t.Fatal("重放锚之后差分不等")
	}
	// 看内部表示的那一条（M13 的牙）：重放锚之后，槽里**非 0 的每一个**都必须等于它现在
	// 所指那一行的宽度。差分抓不到"忘了 clear"：poolASCII 是 8 元的循环池，而每一列的
	// 向量取的是整窗最大值，窗口跨了一整个周期 ⇒ 平移 1,900 行（≡4 mod 8）取到的还是
	// 同一组宽度，max 把偏移抹平了。只有直接看 buf 才抓得住。
	for col := 0; col < f.NumCols(); col++ {
		for r := c.base; r < c.base+c.span; r++ {
			v := c.buf[col*c.span+(r-c.base)]
			if v == 0 {
				continue
			}
			if want := ansi.StringWidth(f.CellString(r, col)) + 1; int(v) != want {
				t.Fatalf("重放锚后槽 (r-base=%d, col=%d) 存的是 %d，应为宽度+1=%d（r=%d）⇒ 忘了清零，旧行的值留在那里",
					r-c.base, col, v, want, r)
			}
		}
	}
	if c.outside != 0 {
		t.Fatalf("重放锚后出现越界取数 %d", c.outside)
	}
	if c.reanchors != 1 || c.builds != 1 {
		t.Fatalf("重放锚应当只清不建：reanchors=%d builds=%d", c.reanchors, c.builds)
	}
}

// 负对照：把"align 漏了对账"这一支实现出来（绕开 naturalWidthsCached、拿旧锚直接调
// cellWidth）。症状必须是**能被看见**的：
//   - outside 等于这一遍读过的格数（一格没少报）；
//   - computed 一个都不涨（回退那一支根本不写槽 ⇒ 这一遍的省量是 0）；
//   - 但值仍然正确（正确性靠回退，不靠锚）。
//
// 反过来，如果 outside 恒 0，就说明覆盖检查根本没被走到 ⇒ 这一格没有牙。
func TestWidthCacheStaleAnchorIsObservable(t *testing.T) {
	f := wcOverLimitFrame()
	var c widthCache
	lo, hi := 0, sampleRows
	naturalWidthsCached(f, lo, hi, compactColCap, &c)
	before := c.computed

	lo2 := 2000
	lo, hi = lo2, lo2+sampleRows
	want := naturalWidths(f, lo, hi, compactColCap) // 参照值
	reads := 0
	var viaCache []int
	for col := 0; col < f.NumCols(); col++ {
		w := ansi.StringWidth(f.Columns[col].Name)
		for r := lo; r < hi; r++ {
			reads++
			if cw := c.cellWidth(f, r, col); cw > w {
				w = cw
			}
			if w >= compactColCap {
				w = compactColCap
				break
			}
		}
		viaCache = append(viaCache, clamp(w, 1, compactColCap))
	}
	if c.outside == 0 {
		t.Fatal("旧锚下跑了 200 行窗口却一次越界都没报 ⇒ 覆盖检查没牙")
	}
	if c.outside != reads {
		t.Fatalf("越界计数 %d ≠ 这一遍读过的格数 %d ⇒ outside 不是可用的诊断量", c.outside, reads)
	}
	if c.computed != before {
		t.Fatalf("漏对账那一遍的 computed 从 %d 涨到 %d ⇒ 回退支不该写槽", before, c.computed)
	}
	if !reflect.DeepEqual(want, viaCache) {
		t.Fatalf("回退那一支本应仍给出正确向量：%v vs %v", want[:4], viaCache[:4])
	}
	// 正路对照：走 naturalWidthsCached 时同一遍的 outside 增量为 0，且第二遍一格都不重付。
	o0 := c.outside
	naturalWidthsCached(f, lo, hi, compactColCap, &c)
	if c.outside != o0 {
		t.Fatalf("正路上又出现越界取数 %d 次 ⇒ 对账没起作用", c.outside-o0)
	}
	mid := c.computed
	naturalWidthsCached(f, lo, hi, compactColCap, &c)
	if c.computed != mid {
		t.Fatalf("同帧同窗口第二遍 computed 从 %d 涨到 %d ⇒ 窗口档没命中", mid, c.computed)
	}
}

// 复用档的"必须命中"性质：同形状的帧换 N 次指针 ⇒ 只 make 一次、clear N 次，
// 而每一遍的值都跟着最新帧走（脏值不许留住）。
func TestWidthCacheReuseForgetsOldFrame(t *testing.T) {
	f := wcFrame([]string{"h1", "h2"}, [][]any{{"aa", "bb"}, {"cc", "dd"}})
	var c widthCache
	naturalWidthsCached(f, 0, 2, compactColCap, &c)
	base := f
	for i := 0; i < 6; i++ {
		// 第 3+i 个 z：宽度逐键严格变大，且始终高于表头 "h1" 的 2 ⇒ 读到旧值一眼可见。
		base = base.WithCell(0, 0, strings.Repeat("z", 3+i))
		got := naturalWidthsCached(base, 0, 2, compactColCap, &c)
		if want := naturalWidths(base, 0, 2, compactColCap); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次换帧差分不等：%v vs %v", i, got, want)
		}
		if got[0] != 3+i {
			t.Fatalf("第 %d 次换帧读到旧宽度 %d ⇒ clear 复用留住了脏值", i, got[0])
		}
	}
	if c.builds != 1 {
		t.Fatalf("7 次同形状换帧之后 makes=%d，应为 1（§12 的复用没生效）", c.builds)
	}
	if c.clears != 6 {
		t.Fatalf("clears=%d，应为 6（换帧 6 次全部复用）", c.clears)
	}
}
