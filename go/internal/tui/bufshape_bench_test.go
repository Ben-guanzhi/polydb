package tui

// 第七轮 ①量：宽度缓存的 **buf 分配尺度**选型（§11.6-1 ＋ §11.6-3 合起来看）。
//
// 写这个文件时**没有改产品码**：当时那份 `widthCache`（整帧分配、键＝帧指针＋行数＋列数、
// 键失配就 make）原样摆在这里当参照物，候选形状（窗口跨度分配 ＋ 分块 slack ＋ 出窗时
// 清/移 ＋ 换帧时复用）只在本文件里实现。判据全部落在"腿"这一层与分配这一层，端到端留给 ②。
//
// ②落地之后（本轮 §12.3）：产品的界内形状＝本文件的 `modeWholeClear`，越限形状＝窗口跨度
// 但跨度预算是常量（不是窗高＋slack），所以只有 `modeWholeClear` 还继续当对照臂，
// 由 TestWholeClearMirrorMatchesProductCache 逐计数器对表；余下的臂是本轮的**读数存档**。
//
// 为什么窗口跨度值得试：
//   - §11.2(f) 的分配账：C 档（5000×20）整帧 100,000 格＝400 KB，`bufAlloc` 203.6 µs；
//   - 量宽那一遍每帧只碰 [lo, lo+sampleRows) ＝ ≤200 行（tableview.go 的 `sampleRows` 常量、
//     Render 里 `lo := tv.rowOff` / `hi := min(nrows, lo+sampleRows)` 那两行）；
//   - 第六轮的 `widthCacheMaxCells` 是按**整帧格数**判的 ⇒ 50000×20 这类大帧**根本没进缓存**
//     （§11 只在 E 档测过它的限定列分支，全列大帧这一档 §11 的表里没有）。

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
)

// ---------- 循环形状：逐字照抄产品 naturalWidthsCached 的结构 ----------

// widthsVia 是产品 `naturalWidthsCached` 的**同形状副本**，只把"每格宽度从哪
// 来"抽成 get ⇒ 四种 buf 形状共用同一遍扫描、同一个早停、同一个 clamp。参照物是产品
// `naturalWidths`，两者必须逐列相等。
func widthsVia(f *tuidata.Frame, lo, hi, capW int, get func(r, col int) int) []int {
	lo = clamp(lo, 0, f.NumRows())
	hi = clamp(hi, lo, f.NumRows())
	out := make([]int, f.NumCols())
	for col := range f.Columns {
		w := ansi.StringWidth(f.Columns[col].Name)
		for r := lo; r < hi; r++ {
			if cw := get(r, col); cw > w {
				w = cw
			}
			if w >= capW {
				w = capW
				break
			}
		}
		out[col] = clamp(w, 1, capW)
	}
	return out
}

// ---------- 候选 ----------

type bufMode int

const (
	modeWhole       bufMode = iota // ＝产品现状：整帧 rows×cols，键失配就 make
	modeWholeClear                 // 整帧跨度，但**换帧复用同一块**（＝只动 §11.6-3 这一半）
	modeWindowClear                // 窗口跨度＋slack：出窗就清掉整块复用（零 make）
	modeWindowShift                // 窗口跨度＋slack：出窗优先平移，跨得远才清
	modeWindowExact                // 窗口跨度、slack＝0（每个新窗口都重建＝设计来难看的对照）
)

// wholeFamily 报告"buf 按整帧分配"这一族（跨度不参与，索引仍是 col*rows+r）。
func wholeFamily(m bufMode) bool { return m == modeWhole || m == modeWholeClear }

// wholeSpanFits 是**第六轮产品判据**的本地镜像（rows ≤ 上限/列数）。本轮之后产品侧的
// tooBigForWidthCache 问的是"连窗口档都放不下"，两个问题不同名 ⇒ ①的仪表自己带一把尺，
// 不去借产品那把会变形的。
func wholeSpanFits(f *tuidata.Frame) bool {
	rows, ncols := f.NumRows(), f.NumCols()
	return rows > 0 && ncols > 0 && rows <= widthCacheMaxCells/ncols
}

// candCache 实现与产品 `widthCache.cellWidth` 相同的契约：返回这格的原始显示宽度
// （不含 capW 截断），并且**内部状态**要能报出建了几次、算了多少格。
type candCache struct {
	mode  bufMode
	slack int

	f     *tuidata.Frame
	rows  int
	ncols int

	base int // buf 覆盖的第一行
	span int // buf 覆盖的行数（列主序的步幅）
	buf  []uint32

	makes     int64 // make 次数
	clears    int64 // 复用同一块内存清零的次数
	shifts    int64 // 平移次数
	bytesMade int64 // 累计 make 出来的字节
	reanchors int64 // 窗口跑出覆盖范围的次数
	computed  int64 // 真付 StringWidth 的格数
	hits      int64 // 命中数组读的格数
	fallback  int64 // 越界回退（不缓存、直接量）的帧次数
}

func newCand(mode bufMode, slack int) *candCache {
	return &candCache{mode: mode, slack: slack}
}

// begin 在产品 naturalWidthsCached 的入口位置被调用：产品那里有 (lo,hi)，缓存自己不知道
// 窗口，所以锚要在这里对账。
func (c *candCache) begin(f *tuidata.Frame, lo, hi int) {
	if wholeFamily(c.mode) {
		// 与产品 `widthCache.align` 的界内那一支同：键失配就地换料，窗口不参与键。
		if c.f != f || c.rows != f.NumRows() || c.ncols != f.NumCols() {
			c.resetWhole(f)
		}
		return
	}
	// 窗口跨度那一支：锚的覆盖必须与"实际会被碰到的行"同一套夹逼——widthsVia 里 lo/hi 会
	// 被 clamp 到 [0, rows]（照抄产品 naturalWidthsCached 入口那两行），这里不夹就会算出错误的 span/base。
	lo = clamp(lo, 0, f.NumRows())
	hi = clamp(hi, lo, f.NumRows())
	// 帧变了必须换料（内容无关的槽位复用＝脏值命中）。
	if c.f != f || c.rows != f.NumRows() || c.ncols != f.NumCols() {
		c.base, c.span = 0, 0
		c.resetWindow(f, lo, hi)
		return
	}
	if lo >= c.base && hi <= c.base+c.span {
		return // 还在覆盖范围里
	}
	c.reanchors++
	if c.mode == modeWindowShift && c.buf != nil {
		if d := lo - c.base; d > 0 && d < c.span/2 && hi <= c.base+d+c.span {
			// 只往下走一点点：每列整块往前挪 d 格，挪空的尾部清零。
			for col := 0; col < c.ncols; col++ {
				s := c.buf[col*c.span : col*c.span+c.span]
				copy(s[:c.span-d], s[d:])
				for i := c.span - d; i < c.span; i++ {
					s[i] = 0
				}
			}
			c.base += d
			c.shifts++
			return
		}
	}
	c.resetWindow(f, lo, hi)
}

func (c *candCache) resetWhole(f *tuidata.Frame) {
	rows, ncols := f.NumRows(), f.NumCols()
	c.f, c.rows, c.ncols, c.computed = f, rows, ncols, 0
	if !wholeSpanFits(f) {
		c.buf = nil
		c.fallback++
		return
	}
	// §11.6-3 那一半：换帧不重新 make，而是把同一块清零复用。槽位映射跟着 rows 走，
	// 但既然整块都清了，"同样的格数、不同的 rows"不会给出脏值。
	if c.mode == modeWholeClear && len(c.buf) == rows*ncols {
		clear(c.buf)
		c.clears++
		return
	}
	c.buf = make([]uint32, rows*ncols)
	c.makes++
	c.bytesMade += int64(4 * rows * ncols)
}

// resetWindow 按窗口跨度放料：span ＝ 本帧窗高 ＋ slack，夹在 [1, rows]。
func (c *candCache) resetWindow(f *tuidata.Frame, lo, hi int) {
	rows, ncols := f.NumRows(), f.NumCols()
	c.f, c.rows, c.ncols, c.computed = f, rows, ncols, 0
	if rows <= 0 || ncols <= 0 {
		c.buf, c.base, c.span = nil, 0, 0
		c.fallback++
		return
	}
	want := hi - lo
	if c.mode != modeWindowExact {
		want += c.slack
	}
	span := min(want, rows)
	span = max(span, 1)
	// 居中锚：base ＝ lo - slack/2，两端各留一半余量 ⇒ 上下翻页的免费步数对称。
	base := lo
	if c.mode != modeWindowExact {
		base = lo - c.slack/2
	}
	if span > rows {
		span = rows
	}
	if base < 0 {
		base = 0
	}
	if base+span > rows {
		base = rows - span
	}
	c.base, c.span = base, span
	if c.buf != nil && len(c.buf) == span*ncols {
		clear(c.buf) // 复用同一块，零 make
		c.clears++
		return
	}
	c.buf = make([]uint32, span*ncols)
	c.makes++
	c.bytesMade += int64(4 * span * ncols)
}

func (c *candCache) cellWidth(f *tuidata.Frame, r, col int) int {
	if wholeFamily(c.mode) {
		// 逐字镜像产品 align+cellWidth 的界内形状（键检查在 align 里，窗口不参与锚）
		if c.f != f || c.rows != f.NumRows() || c.ncols != f.NumCols() {
			c.resetWhole(f)
		}
		if c.buf == nil {
			c.fallback++
			return ansi.StringWidth(f.CellString(r, col))
		}
		i := col*c.rows + r
		if v := c.buf[i]; v != 0 {
			c.hits++
			return int(v) - 1
		}
		w := ansi.StringWidth(f.CellString(r, col))
		c.buf[i] = uint32(w) + 1
		c.computed++
		return w
	}
	if c.buf == nil {
		return ansi.StringWidth(f.CellString(r, col))
	}
	if r < c.base || r >= c.base+c.span {
		// 结构性不该发生（begin 已经把过锚），留这里是**诊断**不是防线：计数器非 0 就说明
		// begin 的判据与 cellWidth 的取数不一致。测试要钉它为 0。
		c.fallback++
		return ansi.StringWidth(f.CellString(r, col))
	}
	i := col*c.span + (r - c.base)
	if v := c.buf[i]; v != 0 {
		c.hits++
		return int(v) - 1
	}
	w := ansi.StringWidth(f.CellString(r, col))
	c.buf[i] = uint32(w) + 1
	c.computed++
	return w
}

// widthsWith 把 begin＋循环＋cellWidth 绑成一次"腿"，等价于产品的一帧量宽。
func widthsWith(f *tuidata.Frame, lo, hi, capW int, c *candCache) []int {
	c.begin(f, lo, hi)
	return widthsVia(f, lo, hi, capW, func(r, col int) int { return c.cellWidth(f, r, col) })
}

// ---------- 窗口序列：一条可复算的滚动剧本 ----------

type window struct{ lo, hi int }

// scrollScript ＝ 20 步下滚 1 行 ＋ 3 次整页下翻 ＋ 1 次远跳 ＋ 20 步上滚 1 行 ＋ 1 次整页
// 上翻 ＋ 回到起点。整条序列只依赖 (rows, body)，四档形状共用同一剧本 ⇒ computed 的差
// 只能来自 buf 形状本身。
func scrollScript(rows, body int) []window {
	hi := func(lo int) int { return min(rows, lo+sampleRows) }
	var ws []window
	lo := 0
	push := func(l int) { lo = l; ws = append(ws, window{l, hi(l)}) }
	for i := 0; i < 20; i++ {
		push(min(lo, max(0, rows-body)))
		lo++
	}
	for i := 0; i < 3; i++ {
		lo = min(lo+body, max(0, rows-body))
		push(lo)
	}
	lo = rows / 2
	push(lo)
	for i := 0; i < 20; i++ {
		lo = max(0, lo-1)
		push(lo)
	}
	lo = max(0, lo-body)
	push(lo)
	lo = 0
	push(lo)
	return ws
}

// ---------- 等价性与诊断 ----------

// candEqualsReference：每种模式、每个 slack，剧本里每一帧的宽度向量必须与产品
// naturalWidths 逐列相等；且 begin 之后 cellWidth 的越界诊断计数必须是 0。
func TestCandShapesEqualReference(t *testing.T) {
	shapes := []struct {
		rows, cols, body int
	}{
		{200, 20, 35}, {200, 60, 35}, {5000, 20, 35}, {200, 20, 19}, {50000, 20, 35}, {2000, 3, 35},
	}
	modes := []struct {
		name   string
		mode   bufMode
		slacks []int
	}{
		{"whole", modeWhole, []int{0}},
		{"whole_clear", modeWholeClear, []int{0}},
		{"window_clear", modeWindowClear, []int{0, 1, 8, 35}},
		{"window_shift", modeWindowShift, []int{0, 1, 8, 35}},
		{"window_exact", modeWindowExact, []int{0}},
	}
	for _, sh := range shapes {
		f := benchFrame(sh.rows, sh.cols, "ascii")
		for _, m := range modes {
			for _, slack := range m.slacks {
				c := newCand(m.mode, slack)
				ws := scrollScript(sh.rows, sh.body)
				for k, w := range ws {
					got := widthsWith(f, w.lo, w.hi, compactColCap, c)
					want := naturalWidths(f, w.lo, w.hi, compactColCap)
					for col := range want {
						if got[col] != want[col] {
							t.Fatalf("shape=%dx%d mode=%s slack=%d 帧 %d（lo=%d hi=%d）列 %d：得 %d 期望 %d",
								sh.rows, sh.cols, m.name, slack, k, w.lo, w.hi, col, got[col], want[col])
						}
					}
				}
				// 越界诊断只对**窗口跨度**那一族成立：整帧族（whole / whole_clear）的 fallback
				// 计数是"大帧按第六轮那条规模上限不缓存"（wholeSpanFits），那正是本轮要量的东西，不是锚的破绽。
				if !wholeFamily(m.mode) && c.fallback != 0 {
					t.Fatalf("mode=%s slack=%d：begin 之后仍有 %d 次越界取数，锚的判据与取数不一致", m.name, slack, c.fallback)
				}
			}
		}
	}
}

// TestCandNeverReanchorIsCaught：负对照要有牙齿——把"出窗不清也不移"这一档（把 slack 当成
// 覆盖上限、永不 re-anchor）摆进来，剧本里必然读到窗口外的行 ⇒ 参考实现必须与它不一致。
// 这一格同时证明上面那条相等测试**不是**恒真。
func TestCandNeverReanchorIsCaught(t *testing.T) {
	f := benchFrame(2000, 5, "ascii")
	c := newCand(modeWindowClear, 8)
	c.begin(f, 0, min(2000, sampleRows))
	// 手动把窗口推到覆盖范围之外，并**跳过** begin 的对账（＝漏掉 re-anchor 的那类写法）。
	lo := 900
	got := widthsVia(f, lo, min(2000, lo+sampleRows), compactColCap, func(r, col int) int {
		return c.cellWidth(f, r, col)
	})
	want := naturalWidths(f, lo, min(2000, lo+sampleRows), compactColCap)
	if c.fallback == 0 {
		t.Fatal("漏掉 re-anchor 却没触发越界诊断 ⇒ 这一格没有牙")
	}
	// 越界那一支回退到直接量宽 ⇒ 结果仍然正确，**这正是为什么正确性靠回退而不是靠锚**；
	// 但 reanchors/命中数会露出破绽，见下。
	if !equalInts(got, want) {
		t.Fatalf("回退那一支本应仍给出正确向量：%v vs %v", got, want)
	}
	before := c.hits
	c2 := newCand(modeWindowClear, 8)
	c2.begin(f, 0, min(2000, sampleRows))
	c2.begin(f, lo, min(2000, lo+sampleRows)) // 走正路：begin 对账
	widthsVia(f, lo, min(2000, lo+sampleRows), compactColCap, func(r, col int) int { return c2.cellWidth(f, r, col) })
	if c2.reanchors == 0 {
		t.Fatal("窗口跑到覆盖范围之外却没有 re-anchor ⇒ 锚没动")
	}
	if c2.hits != 0 && before == c2.hits {
		t.Fatalf("漏对账与正路的命中数应当不同（漏对账那一次窗口外的格全在回退支）：%d vs %d", before, c2.hits)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCandGuardExcludesBigFrameOnlyForWhole：把 §11 的规模上限这条差异**量出来**而不是
// 推断——整帧那一支对 50000×20 直接放弃缓存，窗口跨度那一支建得起。
func TestCandGuardExcludesBigFrameOnlyForWhole(t *testing.T) {
	f := benchFrame(50000, 20, "ascii")
	if wholeSpanFits(f) {
		t.Fatal("50000×20 按第六轮的整帧判据应当越限")
	}
	cw := newCand(modeWhole, 0)
	widthsWith(f, 0, sampleRows, compactColCap, cw)
	if cw.makes != 0 || cw.fallback == 0 {
		t.Fatalf("整帧那一支应当 0 次 make、全部走回退：%d makes, %d fallback", cw.makes, cw.fallback)
	}
	cn := newCand(modeWindowClear, 35)
	widthsWith(f, 0, sampleRows, compactColCap, cn)
	if cn.makes != 1 || cn.fallback != 0 {
		t.Fatalf("窗口跨度那一支应当恰好建一次、不回退：%d makes, %d fallback", cn.makes, cn.fallback)
	}
	if len(cn.buf) >= f.NumRows()*f.NumCols() {
		t.Fatalf("窗口跨度 buf 不该有整帧那么大：%d vs %d", len(cn.buf), f.NumRows()*f.NumCols())
	}
	t.Logf("整帧＝%d 格（放弃）；窗口跨度＝%d 格（界内）", f.NumRows()*f.NumCols(), len(cn.buf))
}

// ---------- ①量的主表：建料价 / 分配字节 / 滚动剧本的总付账 ----------

type bufCase struct {
	name       string
	rows, cols int
	kind       string
	body       int
}

var bufCases = []bufCase{
	{"A_200x20_ascii", 200, 20, "ascii", 35},
	{"B_200x60_ascii", 200, 60, "ascii", 35},
	{"C_5000x20_cjk", 5000, 20, "cjk", 35},
	{"F_200x20_ascii_80x24", 200, 20, "ascii", 19},
	{"G_50000x20_ascii_bigframe", 50000, 20, "ascii", 35},
	{"H_100000x8_ascii_bigframe", 100000, 8, "ascii", 35},
}

// BenchmarkBufBuild：一次"换帧建料"的价——臂里**只有 begin**，不含量宽那一遍（＝打字那一支
// 每键都要付的那笔，外加 §11 登记的分配账）。arms：整帧 make / 窗口跨度 make / 复用同块 clear。
func BenchmarkBufBuild(b *testing.B) {
	for _, c := range bufCases {
		f := benchFrame(c.rows, c.cols, c.kind)
		// 只在两帧之间来回切：键必失配 ⇒ 每轮都走"建料"；不为大帧再造 8 份副本。
		alt := f.WithCell(0, 0, "x")
		frames := [2]*tuidata.Frame{f, alt}
		for _, mode := range []bufMode{modeWhole, modeWholeClear, modeWindowClear, modeWindowShift, modeWindowExact} {
			mode := mode
			b.Run(c.name+"/"+modeName(mode), func(b *testing.B) {
				cache := newCand(mode, c.body)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					fr := frames[i%2]
					cache.begin(fr, 0, min(fr.NumRows(), sampleRows))
				}
				b.StopTimer()
				b.ReportMetric(float64(cache.makes)/float64(b.N), "makes/op")
				b.ReportMetric(float64(cache.clears)/float64(b.N), "clears/op")
				b.ReportMetric(float64(cache.shifts)/float64(b.N), "shifts/op")
				b.ReportMetric(float64(cache.bytesMade)/float64(b.N), "Bmade/op")
				b.ReportMetric(float64(cache.fallback)/float64(b.N), "fallback/op")
			})
		}
	}
}

// BenchmarkBufScroll：同一条滚动剧本、同一个循环形状，四档 buf 形状各跑一遍。报的是
// **每帧**的腿价与每帧真付 StringWidth 的格数（computed）⇒ 建料便宜但要多算格的那些档，
// 会在这里露出来。
func BenchmarkBufScroll(b *testing.B) {
	for _, c := range bufCases {
		f := benchFrame(c.rows, c.cols, c.kind)
		ws := scrollScript(c.rows, c.body)
		// slack 三档：body(35/19) 与 sampleRows(200)——后者是**产品不用改签名就能拿到的**那个
		// 上界（窗口恒 ≤ sampleRows，见 Render 的 hi 那一行），所以它是真正的候选；1/8 两档留作"slack 太小"的
		// 下界样本。
		for _, sl := range []int{8, 35, 200} {
			for _, mode := range []bufMode{modeWhole, modeWholeClear, modeWindowClear, modeWindowShift} {
				mode, sl := mode, sl
				b.Run(c.name+"/"+modeName(mode)+"/slack"+itoa(sl), func(b *testing.B) {
					cache := newCand(mode, sl)
					perRun := len(ws)
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						w := ws[i%perRun]
						// begin 必须在计时循环**内**：窗口对账就是这套机制的开销所在，
						// 挪到外面就等于没量它（产品里也是每帧调一次 naturalWidthsCached）。
						cache.begin(f, w.lo, w.hi)
						widthsVia(f, w.lo, w.hi, compactColCap, func(r, col int) int { return cache.cellWidth(f, r, col) })
					}
					b.StopTimer()
					b.ReportMetric(float64(cache.computed)/float64(b.N), "computed/op")
					b.ReportMetric(float64(cache.hits)/float64(b.N), "hits/op")
					b.ReportMetric(float64(cache.reanchors)/float64(b.N), "reanchor/op")
					b.ReportMetric(float64(cache.makes+cache.clears+cache.shifts)/float64(b.N), "rebuild/op")
					b.ReportMetric(float64(cache.bytesMade)/float64(b.N), "Bmade/op")
					// 整帧族在越限档的 fallback 是要量的东西；窗口跨度族出现 fallback 就是锚
					// 的判据与取数不一致，必须红。
					if !wholeFamily(mode) && cache.fallback != 0 {
						b.Fatalf("剧本里出现越界取数 %d 次 ⇒ 锚与取数不一致", cache.fallback)
					}
					b.ReportMetric(float64(cache.fallback)/float64(b.N), "fallback/op")
				})
			}
		}
	}
}

// TestWholeClearMirrorMatchesProductCache：对照臂的**忠实性**钉。产品侧界内帧现在的形状
// ＝"整帧跨度＋换帧 clear 复用"，逐条对应本文件的 `modeWholeClear`。如果镜像抄错了，
// 本轮所有"窗口跨度 vs 现状"的比较就都失去意义 ⇒ 用同一条剧本、同一份帧，要求宽度向量
// 逐列相等**并且**计数器逐步相等（builds↔makes、clears↔clears、computed↔computed）。
//
// 越限那一档**故意不在这里对表**：产品用的跨度预算是常量 2*sampleRows（与每帧窗高无关），
// 镜像的 window 族用的是"本帧窗高＋slack"，两者块长不同 ⇒ 那一档的性质由产品测试
// （TestWidthCacheWindowSpanEqualsReference / …Indexing）直接钉，不借镜像的读数。
func TestWholeClearMirrorMatchesProductCache(t *testing.T) {
	for _, sh := range []struct {
		rows, cols, body int
	}{{200, 20, 35}, {200, 60, 35}, {5000, 20, 35}} {
		f := benchFrame(sh.rows, sh.cols, "ascii")
		if !wholeSpanFits(f) {
			t.Fatalf("%dx%d 应当是界内形状", sh.rows, sh.cols)
		}
		var prod widthCache
		mirror := newCand(modeWholeClear, 0)
		for _, w := range scrollScript(sh.rows, sh.body) {
			gotP := naturalWidthsCached(f, w.lo, w.hi, compactColCap, &prod)
			gotM := widthsWith(f, w.lo, w.hi, compactColCap, mirror)
			if !equalInts(gotP, gotM) {
				t.Fatalf("%dx%d lo=%d：镜像与产品给出不同向量 %v vs %v", sh.rows, sh.cols, w.lo, gotP, gotM)
			}
			if int64(prod.builds) != mirror.makes || int64(prod.computed) != mirror.computed {
				t.Fatalf("%dx%d lo=%d：计数器失配 builds=%d makes=%d / computed=%d %d",
					sh.rows, sh.cols, w.lo, prod.builds, mirror.makes, prod.computed, mirror.computed)
			}
			if int64(prod.clears) != mirror.clears {
				t.Fatalf("%dx%d lo=%d：复用计数失配 product clears=%d mirror clears=%d ⇒ §12 的复用与对照臂分叉了",
					sh.rows, sh.cols, w.lo, prod.clears, mirror.clears)
			}
		}
	}
}

func itoa(v int) string { return strconv.Itoa(v) }

// TestReuseTurnsMakeIntoClear：§11.6-3 那一半的结构主张——连换 4 帧，整帧那一支付 4 次
// make ＋ 4 份字节，复用那一支只付 1 次 make ＋ 3 次清零。**并且**这一条只在跨度受限时才
// 划算：用 5000×20 这一档（C 档）量"复用后滞留多少"，整帧族滞留 400 KB、窗口跨度族滞留
// 18.8 KB ⇒ 这就是候选 1 与候选 3 必须合起来做的理由，量在这里而不是写在嘴上。
func TestReuseTurnsMakeIntoClear(t *testing.T) {
	rows, cols := 5000, 20
	frames := []*tuidata.Frame{
		benchFrame(rows, cols, "cjk"), benchFrame(rows, cols, "cjk"),
		benchFrame(rows, cols, "cjk"), benchFrame(rows, cols, "cjk"),
	}
	const window = 200 // hi-lo ＝ min(rows, sampleRows)
	for _, tc := range []struct {
		mode        bufMode
		slack       int
		wantMakes   int64
		wantClears  int64
		wantBytes   int64 // 累计 make 出来的字节
		wantCells   int   // 终态滞留的格数
		retentionOK bool
	}{
		{mode: modeWhole, wantMakes: 4, wantBytes: int64(4 * 4 * rows * cols), wantCells: rows * cols},
		{mode: modeWholeClear, wantMakes: 1, wantClears: 3, wantBytes: int64(4 * rows * cols), wantCells: rows * cols},
		{mode: modeWindowClear, slack: 35, wantMakes: 1, wantClears: 3, wantBytes: int64(4 * (window + 35) * cols), wantCells: (window + 35) * cols, retentionOK: true},
	} {
		c := newCand(tc.mode, tc.slack)
		for _, fr := range frames {
			c.begin(fr, 0, min(fr.NumRows(), sampleRows))
		}
		if c.makes != tc.wantMakes || c.clears != tc.wantClears || c.bytesMade != tc.wantBytes {
			t.Fatalf("mode=%s：makes=%d clears=%d Bmade=%d，期望 %d/%d/%d",
				modeName(tc.mode), c.makes, c.clears, c.bytesMade, tc.wantMakes, tc.wantClears, tc.wantBytes)
		}
		if len(c.buf) != tc.wantCells {
			t.Fatalf("mode=%s：终态滞留 %d 格，期望 %d 格", modeName(tc.mode), len(c.buf), tc.wantCells)
		}
		if tc.retentionOK && len(c.buf)*4 > 4*(window+35)*cols {
			t.Fatalf("mode=%s：窗口跨度复用后滞留 %d B 超过上界", modeName(tc.mode), len(c.buf)*4)
		}
	}
	// 大帧那一格：整帧族**根本不复用**（判据不给建），窗口跨度族建得起 ⇒ 复用的滞留可控。
	cw := newCand(modeWholeClear, 0)
	for _, fr := range []*tuidata.Frame{benchFrame(50000, 20, "ascii"), benchFrame(50000, 20, "ascii")} {
		cw.begin(fr, 0, sampleRows)
	}
	if cw.makes != 0 || cw.clears != 0 || len(cw.buf) != 0 {
		t.Fatalf("越限大帧上整帧复用族不该有任何 buf：makes=%d clears=%d len=%d", cw.makes, cw.clears, len(cw.buf))
	}
}

func modeName(m bufMode) string {
	switch m {
	case modeWhole:
		return "whole"
	case modeWholeClear:
		return "wholeClear"
	case modeWindowClear:
		return "windowClear"
	case modeWindowShift:
		return "windowShift"
	case modeWindowExact:
		return "windowExact"
	default:
		panic("未知的 buf 形状")
	}
}

// TestBufShapeLedger：把"建料字节"写成可判的结构等式而不是一句形容词。
//
// 窗口跨度那一支的 span ＝ min(rows, 本帧窗高 + slack) ⇒ **收益只在 rows > sampleRows+slack
// 的档上存在**；rows ≤ 200+slack 的档（A/B/F 都是 200 行）两种形状的格数**逐字相等**，
// 这一格就是把"本轮对小帧没有分配收益"这件事钉在盘上，免得读报告的人以为 400 KB 那笔
// 到处都在。越限档（G/H）反过来：整帧那一支根本不建 ⇒ 窗口跨度是"从不缓存到缓存"。
func TestBufShapeLedger(t *testing.T) {
	for _, c := range bufCases {
		f := benchFrame(c.rows, c.cols, c.kind)
		whole := newCand(modeWhole, 0)
		whole.begin(f, 0, min(c.rows, sampleRows))
		win := newCand(modeWindowClear, c.body)
		win.begin(f, 0, min(c.rows, sampleRows))
		wantSpan := min(c.rows, min(c.rows, sampleRows)+c.body)
		if win.span != wantSpan || len(win.buf) != wantSpan*c.cols {
			t.Fatalf("%s：span 结构等式破了 得 span=%d len=%d，期望 span=%d len=%d",
				c.name, win.span, len(win.buf), wantSpan, wantSpan*c.cols)
		}
		winB, wholeB := int64(len(win.buf))*4, int64(len(whole.buf))*4
		t.Logf("%s：整帧 buf=%d B（makes=%d fallback=%d）｜窗口跨度 buf=%d B span=%d（makes=%d）｜比=%s",
			c.name, wholeB, whole.makes, whole.fallback, winB, win.span, win.makes, ratioText(winB, wholeB))
		if len(win.buf) == 0 || win.makes != 1 {
			t.Fatalf("%s：窗口跨度档应当建一次", c.name)
		}
		if !wholeSpanFits(f) {
			if len(whole.buf) != 0 {
				t.Fatalf("%s：越限档整帧不该有 buf（第六轮判据 rows ≤ 上限/列数）", c.name)
			}
			continue
		}
		if c.rows > sampleRows+c.body {
			if wholeB/winB < 4 {
				t.Fatalf("%s：大帧档窗口跨度没比整帧小 4 倍以上：%d vs %d", c.name, winB, wholeB)
			}
		} else if winB != wholeB {
			t.Fatalf("%s：rows=%d ≤ 200+slack=%d ⇒ 两种形状格数必须相等：%d vs %d", c.name, c.rows, sampleRows+c.body, winB, wholeB)
		}
	}
}

func ratioText(winB, wholeB int64) string {
	if wholeB == 0 {
		return "整帧不建（新界内）"
	}
	return strconv.FormatFloat(float64(wholeB)/float64(winB), 'f', 1, 64) + "x"
}
