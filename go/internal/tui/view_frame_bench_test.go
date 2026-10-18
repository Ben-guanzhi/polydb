package tui

// 量尺（本轮＝端到端帧价，§9.6-3）：一次按键到底付多少钱，钱在哪几段。
// **只测不改产品码**。三段：过滤（§9 已缓存）、预览物化（searchbar.go:111 的
// base.Select(rows)）、整帧 View（App.View → renderBody → TableView.Render）。
// 每个臂都配"确实渲染了那张表 / 确实筛掉了行"的自证，防量尺静默空转。
//
// §11 在这把尺上追加了四个臂（viewCold / measureCached / measureCold / bufAlloc），
// 那一轮**改了产品码**（tableview.go 的 widthCache）；§10 的"只测不改"只对 §10 成立。
// 新臂的期望形态与旧臂不同处：measureCached 要"整个臂只建一次缓存"，viewCold 要
// "冷臂真的冷"，两者都在臂末用计数针复认，不让它们静默退化成 view 的别名。
//
// §12 又改了一次产品码（同一块 buf 的分配/复用），在这把尺上加了 G 档（4001×50＝越过
// 规模上限的那一档，第六轮只能 Skip，现在有了缓存可量）和一个 bufReuse 臂（bufAlloc 的
// 同宽对照：这块料留得下来 ⇒ 每帧只清零）。

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
)

// --- 合成料 ---
//
// 值取自小池子轮转（控内存：50,000×20 也只有 8 个不同串）——本轮的量对象是
// 单元格数与字符串宽度，不是堆占用。
var (
	poolASCII = []any{
		"customer name here 01", "invoice total amount 02", "payment method detail 03",
		"shipping address line 04", "account balance report 05", "warehouse location info 06",
		"supplier contact number 07", "contract expiry date 08",
	}
	poolCJK = []any{
		"客户名称测试数据一二三", "订单编号示例数据三四五", "发货地址补充说明六七",
		"应收账款明细表格八九", "客户联系人电话记录", "库存周转率分析报表",
		"客户结算方式说明", "采购合同扫描件索引",
	}
	poolMixed = []any{
		"Customer_Name 01", "Invoice-Total 02", "PAYMENT_METHOD 03", "ShippingAddr 04",
		"Account_Balance 05", "Warehouse-Loc 06", "Supplier_Phone 07", "Contract_Date 08",
	}
)

func benchFrame(rows, cols int, kind string) *tuidata.Frame {
	names := make([]string, cols)
	for i := range names {
		names[i] = "col_" + strconv.Itoa(i)
	}
	f := tuidata.New(names...)
	for r := 0; r < rows; r++ {
		vals := make([]any, cols)
		for c := 0; c < cols; c++ {
			switch kind {
			case "cjk":
				vals[c] = poolCJK[(r+c)%len(poolCJK)]
			case "mixed":
				vals[c] = poolMixed[(r+c)%len(poolMixed)]
			case "int":
				vals[c] = int64(r*97 + c*131)
			default:
				vals[c] = poolASCII[(r+c)%len(poolASCII)]
			}
		}
		f.AppendRow(vals)
	}
	return f
}

// 会话料：每一格都不含搜索词（干扰池），只有 r%8==0 的行在 0/1 列上带一个
// 目标词，且三个目标词按前缀深浅分层——这样每一键的命中集必须是基帧的真子集
// 且逐键不增，量尺不会静默量成"全命中"或"全零命中"。
var (
	distASCII = []any{
		"invoice total amount 02", "payment method detail 03",
		"shipping address line 04", "account balance report 05",
		"warehouse location info 06", "supplier contact number 07",
		"contract expiry date 08", "delivery region index 09",
	}
	tokASCII = []any{
		"customer name here 01", // 匹配全部 5 个前缀
		"cushion cover here 02", // 只匹配 cu / cus
		"customs form here 03",  // 匹配到 custom
	}
	distCJK = []any{
		"订单编号示例数据三四五", "发货地址补充说明六七",
		"应收账款明细表格八九", "库存周转率分析报表",
		"采购合同扫描件索引", "供应商联系电话记录",
		"仓储出入库流水台账", "发票打印归档清单",
	}
	tokCJK = []any{
		"客户名称测试数据一二三", // 匹配全部 5 键
		"客人签收记录表一份",   // 只匹配"客"
		"客户结算方式说明",    // 匹配"客""客户"
	}
	distMixed = []any{
		"Invoice-Total 02", "PAYMENT_METHOD 03", "ShippingAddr 04", "Account_Balance 05",
		"Warehouse-Loc 06", "Supplier_Phone 07", "Contract_Date 08", "Delivery-Region 09",
	}
	tokMixed = []any{"CUSTOMER_NAME 10", "Cushion_Cover 11", "Customs_Form 12"}
)

func benchSessionFrame(rows, cols int, kind string) *tuidata.Frame {
	names := make([]string, cols)
	for i := range names {
		names[i] = "col_" + strconv.Itoa(i)
	}
	f := tuidata.New(names...)
	var dist, tok []any
	switch kind {
	case "cjk":
		dist, tok = distCJK, tokCJK
	case "mixed":
		dist, tok = distMixed, tokMixed
	default:
		dist, tok = distASCII, tokASCII
	}
	for r := 0; r < rows; r++ {
		vals := make([]any, cols)
		for c := 0; c < cols; c++ {
			vals[c] = dist[(r+c)%len(dist)]
		}
		if r%8 == 0 {
			vals[(r/8)%2] = tok[(r/8)%len(tok)]
		}
		f.AppendRow(vals)
	}
	return f
}

// benchApp 造一个"表已加载、窗口已定尺寸"的 App（走 app.go:74 New 的同一条路）。
func benchApp(f *tuidata.Frame, w, h int, tableCols []int) *App {
	a := New(Options{
		Frames:         []NamedFrame{{Name: "bench", Frame: f}},
		ThemeName:      "catppuccin-mocha",
		ShowBorders:    true,
		ShowRowNumbers: true,
	})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if a.pane() == nil {
		panic("量尺前置：pane 必须存在")
	}
	a.pane().TableCols = tableCols
	return a
}

// 会话键序：累计查询串（真实打字的形状），每键的命中集必须逐键不增。
var (
	cumASCII = []string{"cu", "cus", "cust", "custom", "customer"}
	cumCJK   = []string{"客", "客户", "客户名", "客户名称", "客户名称测"}
)

func keysFor(kind string) []string {
	if kind == "cjk" {
		return cumCJK
	}
	return cumASCII
}

// deltas 把累计串切成 SearchBar.Type 该吃的增量（Type 是追加语义，searchbar.go:55）。
func deltas(cum []string) []string {
	out := make([]string, len(cum))
	for i, s := range cum {
		if i == 0 {
			out[i] = s
			continue
		}
		if !strings.HasPrefix(s, cum[i-1]) {
			panic("增量键序必须是前缀链")
		}
		out[i] = s[len(cum[i-1]):]
	}
	return out
}

// --- 从产品码复制来的式子（只用于算上报常数与可见列数；产品码一字未动） ---
//
// 对应 tableview.go:253-256（带边框 ⇒ body=Height-4）、:272-287（inner/gutter/avail）、
// :290-291（lo/hi）、:647（naturalWidths 扫的是这些列）。
type layout struct {
	bodyRows  int // 可见表体行数
	avail     int // 去掉边框与行号槽后可分给列的宽度
	ncolsView int // naturalWidths 这一遍要扫的列数（sub-frame 时＝选定列数）
	measureAt int // min(rows, sampleRows)
}

// bodyHFor 复制 View() 的纵向分配：无搜索框时 bodyH=h-1（app.go:377），
// 搜索框开着时再减一行（app.go:384）。
func bodyHFor(h int, searchActive bool) int {
	bh := max(1, h-1)
	if searchActive {
		bh = max(1, bh-1)
	}
	return bh
}

// layoutFor 的 h 参数是**喂给 Render 的那个高度**（＝bodyHFor 的结果），不是终端行数。
func layoutFor(w, h, rows, cols int, tableCols []int) layout {
	l := layout{bodyRows: h - 4, avail: w, measureAt: min(rows, sampleRows)} // ShowBorders=true
	if l.bodyRows < 1 {
		l.bodyRows = 1
	}
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	nrows := max(1, rows)
	gutterW := len(strconv.Itoa(nrows)) + 1
	l.avail = inner - gutterW
	if l.avail < 1 {
		l.avail = 1
	}
	l.ncolsView = cols
	if len(tableCols) > 0 {
		l.ncolsView = len(tableCols)
	}
	return l
}

// assertFrameContent 钉住"这一帧真的画了那张表"：行数、表头、tab 名、宽度。
func assertFrameContent(b *testing.B, content string, w, wantLines int) {
	b.Helper()
	if n := strings.Count(content, "\n") + 1; n != wantLines {
		b.Fatalf("帧行数 %d ≠ 期望 %d（量尺接错了渲染路径）", n, wantLines)
	}
	if !strings.Contains(content, "col_0") {
		b.Fatal("帧里没有表头 col_0——渲染的不是那张表")
	}
	if !strings.Contains(content, "bench") {
		b.Fatal("状态栏里没有 tab 名——底部那行没参与这一帧")
	}
	for _, ln := range strings.Split(content, "\n") {
		if n := ansi.StringWidth(ln); n > w+8 {
			b.Fatalf("行宽 %d 超出终端宽 %d——截断没生效，量的是别的东西", n, w)
		}
	}
}

// --- ① 一帧的分解（搜索框未打开） ---

type frameCase struct {
	name      string
	rows      int
	cols      int
	kind      string
	tableCols []int
	w, h      int
}

var frameCases = []frameCase{
	{"rows200_cols20_ascii_120x40", 200, 20, "ascii", nil, 120, 40},
	{"rows200_cols60_ascii_120x40", 200, 60, "ascii", nil, 120, 40},
	{"rows5000_cols20_cjk_120x40", 5000, 20, "cjk", nil, 120, 40},
	{"rows200_cols20_int_120x40", 200, 20, "int", nil, 120, 40},
	{"rows50000_cols20_ascii_cols2_120x40", 50000, 20, "ascii", []int{0, 1}, 120, 40},
	{"rows200_cols20_ascii_80x24", 200, 20, "ascii", nil, 80, 24},
	// G：4001×50 ＝ 200,050 格，刚好越过 widthCacheMaxCells ⇒ 第六轮里这档是"整档不缓存"，
	// measureCached / measureCold / bufAlloc 全得 Skip；第七轮它改走窗口跨度档 ⇒ 第一次能被量。
	{"rows4001_cols50_ascii_120x40", 4001, 50, "ascii", nil, 120, 40},
}

// tooBigForWidthCache 逐条件镜像产品侧 widthCache.reset 的"一块料都不建"判据
// （tableview.go:609-639：整帧档看 rows ≤ 上限/列数，越限后还有窗口档，只有连窗口档都
// 放不下才是真没有料）：那种帧根本不分配 ⇒ measureCached / measureCold / bufAlloc /
// bufReuse 这四臂对它是空话，只能 Skip 而不是量出一个"缓存没生效"的假倍数。
func tooBigForWidthCache(f *tuidata.Frame) bool {
	rows, ncols := f.NumRows(), f.NumCols()
	if rows <= 0 || ncols <= 0 {
		return true
	}
	limit := widthCacheMaxCells / ncols
	return rows > limit && limit < widthCacheSpanRows
}

// modelBlockCells 镜像产品侧那块 buf 的**格数**（tableview.go:610-639 的两档：界内＝整帧
// 跨度 rows*ncols，越限＝恒定块长 widthCacheSpanRows*ncols，连窗口档都放不下＝不建）。
// 臂末拿真实的 widthCache 复认一次长度，所以它不是"我以为产品是这么放的"。
func modelBlockCells(f *tuidata.Frame) int {
	rows, ncols := f.NumRows(), f.NumCols()
	if rows <= 0 || ncols <= 0 {
		return 0
	}
	limit := widthCacheMaxCells / ncols
	switch {
	case rows <= limit:
		return rows * ncols
	case widthCacheSpanRows <= limit:
		return widthCacheSpanRows * ncols
	}
	return 0
}

func BenchmarkFramePrice(b *testing.B) {
	for _, c := range frameCases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			f := benchFrame(c.rows, c.cols, c.kind)
			a := benchApp(f, c.w, c.h, c.tableCols)
			a.View() // 第一轮定下 expanded/colOff，之后各帧同路
			p := a.pane()
			if p.Table.Sel() != 0 {
				b.Fatalf("Sel()=%d，量尺假定光标在首行", p.Table.Sel())
			}
			bodyH := bodyHFor(c.h, false)
			l := layoutFor(c.w, bodyH, c.rows, c.cols, c.tableCols)

			// 可见列数：照产品码再算一遍 naturalWidths + visibleColumns。
			lo, hi := 0, l.measureAt
			capW := compactColCap
			if p.Table.Expanded() {
				capW = expandedColCap
			}
			rendered := f
			if len(c.tableCols) > 0 {
				rendered = tableColsSubFrame(f, c.tableCols)
			}
			nat := naturalWidths(rendered, lo, hi, capW)
			visCols, _ := visibleColumns(nat, p.Table.ColOff(), l.avail)
			if len(visCols) == 0 {
				b.Fatal("可见列数 0——量尺接错")
			}
			// 上报常数挂在**臂级** b 上（挂在父级 b 上不会打印出结果行）。
			mCellMeasure := float64(l.measureAt * l.ncolsView)
			mCellDraw := float64(l.bodyRows * len(visCols))
			mWide := boolMetric(p.Table.Expanded())

			opts := RenderOpts{
				Width:          c.w,
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

			arms := []string{"view", "body", "bottom", "tableRender", "measure", "measureCached", "measureCold", "bufAlloc", "bufReuse", "subframe", "viewCold", "viewRe"}
			for _, arm := range arms {
				b.Run(arm, func(b *testing.B) {
					b.ReportMetric(mCellMeasure, "cellMeasure")
					b.ReportMetric(mCellDraw, "cellDraw")
					b.ReportMetric(mWide, "wide")
					var mcCache widthCache // measureCached / measureCold 用；每个臂都是新闭包 ⇒ 冷启
					// bufReuse 的模型料：一块留得下来的 buf，外加"究竟 make 了几次"的针。
					// ruN 初值取 -1：nil buf 的长度也是 0，与"还没定长"撞车 ⇒ 首帧走不到 make。
					var ruBuf []uint32
					var ruMakes int
					ruN := -1
					// viewCold 每帧把整个结构清零，计数针也跟着归零 ⇒ 只能就地累加，
					// 不能在臂末读 p.Table.widths.builds（那永远是 1）。
					var coldBuilds, coldComputed int
					for i := 0; i < b.N; i++ {
						switch arm {
						case "view", "viewRe":
							v := a.View()
							assertFrameContent(b, v.Content, c.w, c.h)
						case "viewCold":
							// 与 view 同一条道，但每帧把宽度缓存清零 ⇒ §10 量到的那份
							// "帧帧付全价"的形状（外加本改动真正多付的那次 buf 分配）。
							p.Table.widths = widthCache{}
							v := a.View()
							coldBuilds += p.Table.widths.builds
							coldComputed += p.Table.widths.computed
							assertFrameContent(b, v.Content, c.w, c.h)
						case "body":
							s := a.renderBody(c.w, bodyH)
							if n := strings.Count(s, "\n") + 1; n != bodyH {
								b.Fatalf("renderBody 行数 %d ≠ %d", n, bodyH)
							}
						case "bottom":
							s := a.renderBottom(c.w)
							if !strings.Contains(s, "bench") {
								b.Fatal("renderBottom 里没有 tab 名")
							}
							if n := strings.Count(s, "\n") + 1; n != 1 {
								b.Fatalf("状态栏应当是一行，实得 %d", n)
							}
						case "tableRender":
							s := p.Table.Render(rendered, opts)
							if n := strings.Count(s, "\n") + 1; n != bodyH {
								b.Fatalf("Render 行数 %d ≠ %d", n, bodyH)
							}
						case "measure":
							if got := naturalWidths(rendered, lo, hi, capW); len(got) == 0 {
								b.Fatal("naturalWidths 返回空")
							}
						case "measureCached":
							if len(c.tableCols) > 0 || tooBigForWidthCache(rendered) {
								b.Skip("限定列那一支不接缓存；超限帧也不接 ⇒ 这两档没有命中路径可量")
							}
							if got := naturalWidthsCached(rendered, lo, hi, capW, &mcCache); len(got) == 0 {
								b.Fatal("naturalWidthsCached 返回空")
							}
						case "measureCold":
							if len(c.tableCols) > 0 || tooBigForWidthCache(rendered) {
								b.Skip("同 measureCached：这一档产品侧根本不建缓存")
							}
							var fresh widthCache
							if got := naturalWidthsCached(rendered, lo, hi, capW, &fresh); len(got) == 0 {
								b.Fatal("naturalWidthsCached 返回空")
							}
						case "bufAlloc":
							// §11 登记的那笔开销：每换一帧一次 make([]uint32, 行×列)（整帧长度）。
							// 第七轮之后这条臂的读法变了，两种都写在这里以免被当成"产品现在这样"：
							//   - 界内帧：块长仍是 rows*ncols ⇒ 相邻两帧形状不同时（打字那一支）付的还是这一笔；
							//   - 越限帧（G 档）：产品侧根本不放整帧料 ⇒ 这一笔是"若按整帧放"的反事实，
							//     真实长度看 bufReuse（恒定 widthCacheSpanRows*ncols）。
							if len(c.tableCols) > 0 || tooBigForWidthCache(rendered) {
								b.Skip("这一档产品侧一块料都不建 ⇒ 既没有这笔分配，也没有它的反事实可讲")
							}
							n := rendered.NumRows() * rendered.NumCols()
							buf := make([]uint32, n)
							buf[n-1] = 1
							if buf[0] != 0 || buf[n-1] != 1 {
								b.Fatal("buf 形状不对")
							}
						case "bufReuse":
							// §12 的分配账，与 bufAlloc 逐字同宽、只差"这块料留不留得下来"：
							// 块长对上就只清零（产品侧 tableview.go:632-635 那一支，镜像与它的等价性
							// 由 TestWholeClearMirrorMatchesProductCache 钉住）。块长按 modelBlockCells
							// （本文件 :282-294）取产品那一档的真实长度（界内整帧 / 越限恒定），
							// 臂末拿真缓存复认。
							// 口径：只建模**那一次分配决策**，不含量宽腿 ⇒ 与 bufAlloc 同尺度可比。
							// 它假定相邻两帧同形状（滚动/重渲那一支）；界内帧在打字那一支行数逐键变 ⇒
							// 块长变 ⇒ 仍然 make，那一支的收益不在这条臂上（见 §12.2 与 §12.6 的诚实账）。
							if len(c.tableCols) > 0 || tooBigForWidthCache(rendered) {
								b.Skip("这一档产品侧不建缓存 ⇒ 没有这块料可复用")
							}
							if len(ruBuf) != ruN {
								ruN = modelBlockCells(rendered)
								ruBuf = make([]uint32, ruN)
								ruMakes++
							}
							clear(ruBuf)
							ruBuf[ruN-1] = 1
							if ruBuf[0] != 0 || ruBuf[ruN-1] != 1 {
								b.Fatal("buf 形状不对")
							}
						case "subframe":
							if len(c.tableCols) == 0 {
								b.Skip("这一档没开限定列，subframe 不适用")
							}
							sub := tableColsSubFrame(f, c.tableCols)
							if sub.NumRows() != c.rows || sub.NumCols() != len(c.tableCols) {
								b.Fatalf("sub-frame %d×%d ≠ %d×%d",
									sub.NumRows(), sub.NumCols(), c.rows, len(c.tableCols))
							}
						}
					}
					// "必须命中"这条性质挂在量尺上，而不只挂在单元测试里：
					// view / measureCached 整条臂跑了 b.N 帧，缓存只许建一次、宽度只许被
					// 计算过**一帧**的量；viewCold 是它们的反面，每帧必须重建、每帧必须重量。
					switch arm {
					case "view":
						if len(c.tableCols) > 0 {
							if p.Table.widths.builds != 0 {
								b.Fatalf("限定列那一支竟然建了宽度缓存：builds=%d", p.Table.widths.builds)
							}
							break
						}
						if got := p.Table.widths.builds; got != 1 {
							b.Fatalf("view 臂 %d 帧建了 %d 次宽度缓存，应为 1（循环前那次 View 建的）⇒ 帧身份没保持住",
								b.N, got)
						}
						if got := p.Table.widths.computed; got > int(mCellMeasure) {
							b.Fatalf("view 臂 %d 帧里宽度被算了 %d 格，上界应是一帧的 %d 格 ⇒ 端到端没命中",
								b.N, got, int(mCellMeasure))
						}
					case "viewRe":
						// 与 view 逐字同码，但排在 viewCold 后面 ⇒ 进来时缓存已经是热的，
						// builds 可能为 0，只能钉上界。
						if got := p.Table.widths.builds; got > 1 {
							b.Fatalf("viewRe 建了 %d 次缓存，同一条帧身份最多 1 次 ⇒ 键在失配", got)
						}
						if got := p.Table.widths.computed; got > int(mCellMeasure) {
							b.Fatalf("viewRe %d 帧里宽度被算了 %d 格，上界 %d ⇒ 对照臂没走上同一条道",
								b.N, got, int(mCellMeasure))
						}
					case "viewCold":
						// 限定列那一支产品侧根本不接缓存 ⇒ 清不清零是同一条码，没有"冷"可钉；
						// 反过来钉它**一次都没建**，否则这条 Skip 就成了粉饰。
						if len(c.tableCols) > 0 {
							if coldBuilds != 0 || p.Table.widths.builds != 0 {
								b.Fatalf("限定列那一支 viewCold 建了 %d/%d 次缓存，应为 0", coldBuilds, p.Table.widths.builds)
							}
							break
						}
						if coldBuilds < b.N {
							b.Fatalf("viewCold %d 帧只重建了 %d 次缓存 ⇒ 冷臂没冷跑，对照失效",
								b.N, coldBuilds)
						}
						if coldComputed < b.N {
							b.Fatalf("viewCold %d 帧总共只量了 %d 格 ⇒ 每帧没付全价，对照失效",
								b.N, coldComputed)
						}
					case "measureCached":
						// 走到这里说明这一档没被 Skip（Skip 会直接终止子测试）。
						if got := mcCache.builds; got != 1 {
							b.Fatalf("measureCached 整个臂建了 %d 次缓存，应为 1 ⇒ 键在每帧失配，命中路径不存在", got)
						}
						if got := mcCache.computed; got > int(mCellMeasure) {
							b.Fatalf("measureCached %d 帧算了 %d 格，上界应是一帧的 %d 格 ⇒ 没命中",
								b.N, got, int(mCellMeasure))
						}
					case "bufReuse":
						// 这条臂的"必须命中"＝分配只发生一次；不钉这一条，臂末的 B/op≈0 可能只是
						// 因为循环压根没跑（bufAlloc 的假倍数就是这么来的）。
						if ruMakes != 1 {
							b.Fatalf("bufReuse %d 帧里 make 了 %d 次，应为 1 ⇒ 块长没接住，复用臂空转", b.N, ruMakes)
						}
						// 模型块长 vs 产品真实块长：就地喂一个**真的** widthCache 问它 buf 多长。
						// 这一条是 modelBlockCells 与产品同源性的复认（它镜像的判据本轮刚改过含义）。
						// 窗口只取一行：块长由帧形状决定（reset 只看 rows/ncols），把量宽腿扫到
						// 200×50 格会让这条复认自己变成被量的东西。
						var probe widthCache
						if got := naturalWidthsCached(rendered, lo, lo+1, capW, &probe); len(got) == 0 {
							b.Fatal("probe 返回空向量")
						}
						if len(probe.buf) != ruN {
							b.Fatalf("模型块长 %d ≠ 产品块长 %d ⇒ 这条臂量的不是产品那块料", ruN, len(probe.buf))
						}
						if probe.builds != 1 || probe.clears != 0 {
							b.Fatalf("probe 首帧应当是 1 次 make、0 次 clear，实得 builds=%d clears=%d", probe.builds, probe.clears)
						}
					}
				})
			}
		})
	}
}

func boolMetric(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// --- ② 一次按键的分解（搜索框开着，连打 5 键；一 op＝一个会话） ---

type keyCase struct {
	name     string
	rows     int
	cols     int
	kind     string
	restrict bool // 走 OpenObserve 式限定列（同时决定 TableCols 与搜索列集合）
	w, h     int
}

var keyCases = []keyCase{
	{"rows200_cols20_ascii_全列", 200, 20, "ascii", false, 120, 40},
	{"rows5000_cols20_cjk_全列", 5000, 20, "cjk", false, 120, 40},
	{"rows5000_cols20_mixed_限定2列", 5000, 20, "mixed", true, 120, 40},
}

func BenchmarkKeystrokePrice(b *testing.B) {
	for _, c := range keyCases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			f := benchSessionFrame(c.rows, c.cols, c.kind)
			cols := make([]int, c.cols)
			for i := range cols {
				cols[i] = i
			}
			var tableCols []int
			if c.restrict {
				tableCols = []int{0, 1}
				cols = tableCols
			}
			ks := keysFor(c.kind)
			ds := deltas(ks)

			// 料自检：逐键命中数必须是基帧的真子集，且随键增长不增。
			hits := make([]int, len(ks))
			var pre tuidata.FoldCache
			for i, needle := range ks {
				hits[i] = len(tuidata.SearchExactColsFold(f, cols, needle, &pre))
				if hits[i] == 0 || hits[i] >= f.NumRows() {
					b.Fatalf("needle=%q 命中 %d/%d——会话料失效", needle, hits[i], f.NumRows())
				}
				if i > 0 && hits[i] > hits[i-1] {
					b.Fatalf("needle=%q 命中数 %d 比上一键 %d 还多——前缀链断了", needle, hits[i], hits[i-1])
				}
			}
			// 上报常数挂在**臂级** b 上（挂在父级 b 上不会打印出结果行）。
			mKeys := float64(len(ks))
			mCellScan := float64(c.rows * len(cols))
			mHitFirst := float64(hits[0])
			mHitLast := float64(hits[len(hits)-1])

			// 每一臂都是"新建 App（含 StartCols）+ 5 键"，因为缓存必须冷启；
			// boot 那一臂就是同一件事但不按键，用作各臂的公共底座（相减才干净）。
			//
			//	scan         过滤腿（§9 缓存路径）
			//	scanNil      过滤腿、cache 传 nil ＝ §9 之前的走查价
			//	select       过滤腿 + 预览物化（base.Select）
			//	session      真实按键：Type+整帧 View
			//	sessionZero  session 但每键把缓存清零 ＝ §9 之前的整条会话
			//	doubleView   session 的每键再渲一次，(doubleView-session)/5 ＝ 稳态单帧价
			//	sessionRe    与 session 逐字同码，排最后当对照臂
			arms := []string{"boot", "scan", "scanNil", "select", "session", "sessionZero", "doubleView", "sessionRe"}
			for _, arm := range arms {
				b.Run(arm, func(b *testing.B) {
					b.ReportMetric(mKeys, "keys")
					b.ReportMetric(mCellScan, "cellScan")
					b.ReportMetric(mHitFirst, "hitFirst")
					b.ReportMetric(mHitLast, "hitLast")
					for i := 0; i < b.N; i++ {
						a := benchApp(f, c.w, c.h, tableCols)
						a.search.StartCols(f, false, cols)
						if !reflect.DeepEqual(a.search.fold, tuidata.FoldCache{}) {
							b.Fatal("StartCols 之后缓存必须是零值（§9 的接线）")
						}
						if arm == "boot" {
							continue
						}
						var last string
						for k, needle := range ks {
							switch arm {
							case "scan":
								if got := len(tuidata.SearchExactColsFold(f, cols, needle, &a.search.fold)); got != hits[k] {
									b.Fatalf("needle=%q 命中 %d ≠ 自检 %d", needle, got, hits[k])
								}
							case "scanNil":
								if got := len(tuidata.SearchExactColsFold(f, cols, needle, nil)); got != hits[k] {
									b.Fatalf("needle=%q 无缓存命中 %d ≠ 自检 %d", needle, got, hits[k])
								}
							case "select":
								got := tuidata.SearchExactColsFold(f, cols, needle, &a.search.fold)
								if pf := f.Select(got); pf.NumRows() != hits[k] {
									b.Fatalf("needle=%q 预览 %d ≠ 命中 %d", needle, pf.NumRows(), hits[k])
								}
							case "sessionZero":
								a.search.fold = tuidata.FoldCache{}
								fallthrough
							case "session", "sessionRe":
								a.search.Type(ds[k])
								last = a.View().Content
								if pv := a.search.Preview(); pv == nil || pv.NumRows() != hits[k] {
									b.Fatalf("needle=%q 预览与命中集不符（%v）", needle, pv)
								}
							case "doubleView":
								a.search.Type(ds[k])
								first := a.View().Content
								last = a.View().Content
								if k > 0 && first != last {
									n := firstDiffLine(first, last)
									b.Fatalf("同状态两次 View 输出不一致（needle=%q）首个差异行 #%d\n%s",
										needle, n, diffAround(lineAt(first, n), lineAt(last, n)))
								}
							}
						}
						if arm != "scanNil" && reflect.DeepEqual(a.search.fold, tuidata.FoldCache{}) {
							b.Fatal("折叠缓存没挂上——§9 那一段没参与这一会话")
						}
						armView := arm == "session" || arm == "sessionRe" || arm == "sessionZero" || arm == "doubleView"
						if !armView {
							continue
						}
						assertFrameContent(b, last, c.w, c.h)
						if !strings.Contains(last, "rows") {
							b.Fatal("帧里没有搜索条的行数计数——这一帧没走搜索态")
						}
					}
				})
			}
		})
	}
}

// --- ③ 每格的两笔：CellString（格式化）与 ansi.StringWidth（宽度走查） ---
//
// naturalWidths 每格做这两笔（tableview.go:649-650）。这一档要回答的是"哪一笔
// 是主导项"，不许靠断言。

var widthSink int

// firstDiffLine / lineAt / cut 只服务于 doubleView 失败时的诊断输出。
func firstDiffLine(a, b string) int {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range as {
		if i >= len(bs) || as[i] != bs[i] {
			return i
		}
	}
	return -1
}

func lineAt(s string, n int) string {
	if n < 0 {
		return ""
	}
	ls := strings.Split(s, "\n")
	if n >= len(ls) {
		return ""
	}
	return ls[n]
}

// diffAround 返回两行里第一处不同的 rune 偏移，以及各自前后 30 rune 的上下文。
func diffAround(a, b string) string {
	ra, rb := []rune(a), []rune(b)
	i := 0
	for i < len(ra) && i < len(rb) && ra[i] == rb[i] {
		i++
	}
	lo, hi := max(0, i-30), i+30
	slice := func(r []rune) string {
		l, h := lo, min(hi, len(r))
		if l > len(r) {
			l = len(r)
		}
		return string(r[l:h])
	}
	return fmt.Sprintf("首个差异 rune 偏移=%d lenA=%d lenB=%d\nA…%q…\nB…%q…", i, len(ra), len(rb), slice(ra), slice(rb))
}

func BenchmarkPerCellBreakdown(b *testing.B) {
	cases := []struct {
		name string
		rows int
		cols int
		kind string
	}{
		{"cells4000_ascii", 200, 20, "ascii"},
		{"cells4000_cjk", 200, 20, "cjk"},
		{"cells4000_mixed", 200, 20, "mixed"},
		{"cells4000_int", 200, 20, "int"},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			f := benchFrame(c.rows, c.cols, c.kind)
			// 预算好字符串：widthOnly 这一臂里只有走查，没有格式化。
			pre := make([]string, 0, c.rows*c.cols)
			for r := 0; r < c.rows; r++ {
				for col := 0; col < c.cols; col++ {
					pre = append(pre, f.CellString(r, col))
				}
			}
			if len(pre) != c.rows*c.cols {
				b.Fatalf("预取 %d 格 ≠ %d", len(pre), c.rows*c.cols)
			}
			arms := []string{"widthOnly", "cellString", "cellStringPlusWidth", "widthOnlyRe"}
			mCells := float64(c.rows * c.cols)
			for _, arm := range arms {
				b.Run(arm, func(b *testing.B) {
					b.ReportMetric(mCells, "cells")
					total, maxW := 0, 0
					for i := 0; i < b.N; i++ {
						total, maxW = 0, 0
						switch arm {
						case "widthOnly", "widthOnlyRe":
							for _, s := range pre {
								w := ansi.StringWidth(s)
								total += w
								if w > maxW {
									maxW = w
								}
							}
						case "cellString":
							for r := 0; r < c.rows; r++ {
								for col := 0; col < c.cols; col++ {
									s := f.CellString(r, col)
									total += len(s)
								}
							}
						case "cellStringPlusWidth":
							for r := 0; r < c.rows; r++ {
								for col := 0; col < c.cols; col++ {
									w := ansi.StringWidth(f.CellString(r, col))
									total += w
									if w > maxW {
										maxW = w
									}
								}
							}
						}
					}
					if total == 0 {
						b.Fatal("量尺空转：总宽 0")
					}
					widthSink += total
					// 走查宽度的三臂：每格宽度必须落在产品用的区间里（capW 之前）。
					// cellString 那一臂累加的是字节长，不参与这条钉子。
					if arm != "cellString" {
						if maxW < 1 || maxW > expandedColCap {
							b.Fatalf("maxW=%d 超出 [1,%d]", maxW, expandedColCap)
						}
						b.ReportMetric(float64(total)/float64(c.rows*c.cols), "meanCellW")
					}
				})
			}
		})
	}
}

// --- 量尺自身的钉子（不是基准，是"这些数字有意义"的前提） ---

func TestFramePriceSanity(t *testing.T) {
	f := benchFrame(50, 6, "ascii")
	a := benchApp(f, 100, 20, nil)
	v := a.View()
	if !v.AltScreen {
		t.Fatal("必须走 alt screen")
	}
	if n := strings.Count(v.Content, "\n") + 1; n != 20 {
		t.Fatalf("行数 %d，期望 20", n)
	}
	// 限定列档：sub-frame 必须是"整帧行数 × 选定列数"的形状。
	big := benchFrame(1000, 10, "ascii")
	sub := tableColsSubFrame(big, []int{2, 5})
	if sub.NumRows() != 1000 || sub.NumCols() != 2 {
		t.Fatalf("sub-frame %d×%d", sub.NumRows(), sub.NumCols())
	}
	// 量尺复制的式子必须与产品码同源：改尺寸 ⇒ bodyRows 随之变。
	// 喂给 Render 的是 bodyH（View() 先减一行，app.go:377），Render 再减 4（tableview.go:251-254）。
	l := layoutFor(120, bodyHFor(40, false), 5000, 20, nil)
	if l.bodyRows != 35 || l.measureAt != 200 || l.ncolsView != 20 {
		t.Fatalf("layout %+v", l)
	}
	// 同源的硬证据：产品自己算出的表体行数 ＝＝ 量尺算出的。
	if got, want := a.pane().Table.PageSize(), bodyHFor(20, false)-4; got != want {
		t.Fatalf("PageSize()=%d ≠ 量尺 %d（100×20，无搜索框）", got, want)
	}
	if got := fmt.Sprint(l.avail); got != "113" {
		t.Fatalf("avail=%s，期望 113（120-2-行号槽5）", got)
	}
	// 搜索态下渲染的是预览帧（app.go:437-438），不是基帧。
	sf := benchSessionFrame(64, 8, "ascii")
	a.search.StartCols(sf, false, allCols(8))
	a.search.Type("cu")
	if pv := a.search.Preview(); pv == nil || pv.NumRows() >= sf.NumRows() {
		t.Fatalf("预览没筛掉东西：%v", pv)
	}
	// 会话料的纯度：干扰池里一个命中都不许有，命中集恰好等于带目标词的行。
	for _, kind := range []string{"ascii", "cjk", "mixed"} {
		var dist []any
		switch kind {
		case "cjk":
			dist = distCJK
		case "mixed":
			dist = distMixed
		default:
			dist = distASCII
		}
		only := tuidata.New("a", "b")
		for r := 0; r < 24; r++ {
			only.AppendRow([]any{dist[r%len(dist)], dist[(r+3)%len(dist)]})
		}
		var fc tuidata.FoldCache
		for _, needle := range keysFor(kind) {
			if got := tuidata.SearchExactColsFold(only, []int{0, 1}, needle, &fc); len(got) != 0 {
				t.Fatalf("kind=%s needle=%q 在纯干扰帧上命中 %d 行——干扰池混进了搜索词", kind, needle, len(got))
			}
		}
	}
	want := 0
	for r := 0; r < sf.NumRows(); r++ {
		if r%8 == 0 {
			want++
		}
	}
	var clean tuidata.FoldCache
	if got := tuidata.SearchExactColsFold(sf, allCols(8), "cu", &clean); len(got) != want {
		t.Fatalf("首键命中 %d，期望 %d（每 8 行一个目标词）", len(got), want)
	}
}

func allCols(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
