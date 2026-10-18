package tui

import (
	"math/rand"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/internal/theme"
)

const (
	compactColCap  = 40 // natural column width cap in compact mode
	expandedColCap = 80 // natural column width cap in expanded mode
	sampleRows     = 200
)

// TableView holds the scroll/selection state of one table widget. Rendering
// is stateless apart from remembering the last page size for paging keys.
type TableView struct {
	sel       int  // selected row
	rowOff    int  // first visible row
	colOff    int  // first visible column (expanded mode)
	colCursor int  // cursored column (expanded mode)
	expanded  bool // column mode
	modeSet   bool // column mode already picked (auto at first render, or manual 'e')

	snapCursor bool // scroll colCursor into view on next render
	lastRows   int  // page size observed at last render

	widths widthCache // per-cell display width, memoized by frame identity

	// Windowed width table for the column-limited branch. Its sub-frames are
	// rebuilt every render, so a pointer key cannot work; this table keys on
	// parent frame pointer + parent rows/ncols + cols sequence + window
	// coordinates (base,win,mlo,mhi) + capW. Independent of the widthCache
	// above: the non-column-limited branch continues to use only `widths`.
	tcWidths tableColsWidthCache

	selected map[int]bool // multi-select membership; lazy-init; nil = none
}

// selectMarker is drawn in the gutter (or the leading tag column when there
// is no gutter) of rows belonging to the multi-select set.
const selectMarker = "█"

// RenderOpts carries everything Render needs besides the frame.
type RenderOpts struct {
	Width, Height  int
	Theme          *theme.Theme
	ShowBorders    bool
	ShowRowNumbers bool
	MatchRows      map[int]bool // rows emphasized with the match style

	HeaderCur int // column index to highlight in header (-1 or 0+ valid)
	// SeverityCol, when non-empty, names a column whose value determines the row foreground color.
	// Values matching TRACE/DEBUG/INFO/WARN/ERROR/FATAL (case-insensitive) get distinct colors.
	SeverityCol string
	// TableCols, when non-nil, renders only these column indices.
	// Sheet mode passes nil here so all columns are always visible in detail.
	TableCols []int
	// NoEllipsis disables the "…" truncation indicator on cell values —
	// text is hard-clipped at the column boundary instead.
	NoEllipsis bool
	// HighlightQuery, when non-empty, highlights matching substrings in cell values.
	HighlightQuery string
	// HighlightCols restricts highlighting to these column indices (nil = all cols).
	HighlightCols []int
	// HScroll is the character-level horizontal scroll offset applied to the
	// last visible column. Used when TableCols is set to let the user pan
	// within a single wide column (e.g. OpenObserve body) with h/l keys.
	HScroll int
}

// --- state accessors -------------------------------------------------------

func (tv *TableView) Sel() int       { return tv.sel }
func (tv *TableView) Expanded() bool { return tv.expanded }
func (tv *TableView) ColCursor() int { return tv.colCursor }
func (tv *TableView) ColOff() int    { return tv.colOff }

// SetColCursor sets the column cursor to the given index (clamped to valid range).
func (tv *TableView) SetColCursor(i, ncols int) {
	if ncols <= 0 {
		return
	}
	tv.colCursor = clamp(i, 0, ncols-1)
	tv.snapCursor = true
}

// PageSize is the number of body rows shown at the last render (fallback 10).
func (tv *TableView) PageSize() int {
	if tv.lastRows <= 0 {
		return 10
	}
	return tv.lastRows
}

// Reset returns the view to the top-left with row 0 selected and re-arms the
// automatic column-mode pick for the next frame shown.
func (tv *TableView) Reset() {
	tv.sel, tv.rowOff, tv.colOff, tv.colCursor = 0, 0, 0, 0
	tv.snapCursor = false
	tv.modeSet = false
	tv.ClearSelect()
}

// ClampTo clamps selection and cursor to the bounds of f (used when the
// underlying frame changes but position should be roughly kept). A frame
// change invalidates the multi-select set, so it is cleared here too.
func (tv *TableView) ClampTo(f *tuidata.Frame) {
	if f == nil {
		tv.Reset()
		return
	}
	tv.sel = clamp(tv.sel, 0, f.NumRows()-1)
	tv.rowOff = clamp(tv.rowOff, 0, max(0, f.NumRows()-1))
	tv.colCursor = clamp(tv.colCursor, 0, f.NumCols()-1)
	tv.colOff = clamp(tv.colOff, 0, max(0, f.NumCols()-1))
	tv.ClearSelect()
}

// --- navigation ------------------------------------------------------------

func (tv *TableView) Move(delta, nrows int) {
	tv.sel = clamp(tv.sel+delta, 0, nrows-1)
}

func (tv *TableView) Top() { tv.sel = 0 }

func (tv *TableView) Bottom(nrows int) {
	tv.sel = max(0, nrows-1)
}

func (tv *TableView) HalfPageUp(nrows int)   { tv.Move(-max(1, tv.PageSize()/2), nrows) }
func (tv *TableView) HalfPageDown(nrows int) { tv.Move(max(1, tv.PageSize()/2), nrows) }
func (tv *TableView) PageUp(nrows int)       { tv.Move(-max(1, tv.PageSize()), nrows) }
func (tv *TableView) PageDown(nrows int)     { tv.Move(max(1, tv.PageSize()), nrows) }

func (tv *TableView) Random(nrows int) {
	if nrows > 0 {
		tv.sel = rand.Intn(nrows)
	}
}

// JumpTo selects a specific row (clamped).
func (tv *TableView) JumpTo(row, nrows int) {
	tv.sel = clamp(row, 0, nrows-1)
}

// ToggleExpanded flips the column mode manually; the choice sticks for the
// frame currently shown (no later auto-pick overrides it).
func (tv *TableView) ToggleExpanded() {
	tv.expanded = !tv.expanded
	tv.modeSet = true
}

// LastCol moves the column cursor to the last column and requests that it be
// scrolled into view (wide mode).
func (tv *TableView) LastCol(ncols int) {
	tv.colCursor = max(0, ncols-1)
	tv.snapCursor = true
}

// NextCol / PrevCol / FirstCol move the column cursor (both column modes)
// and request that it be scrolled into view (wide mode).
func (tv *TableView) NextCol(ncols int) {
	tv.colCursor = clamp(tv.colCursor+1, 0, ncols-1)
	tv.snapCursor = true
}

func (tv *TableView) PrevCol(ncols int) {
	tv.colCursor = clamp(tv.colCursor-1, 0, ncols-1)
	tv.snapCursor = true
}

func (tv *TableView) FirstCol() {
	tv.colCursor = 0
	tv.snapCursor = true
}

// --- multi-select ----------------------------------------------------------

// ToggleSelect adds a row to (or removes it from) the multi-select set. The
// set is lazy-initialized on first use so an untouched view stays allocation
// free.
func (tv *TableView) ToggleSelect(row int) {
	if tv.selected == nil {
		tv.selected = map[int]bool{}
	}
	tv.selected[row] = !tv.selected[row]
	if !tv.selected[row] {
		delete(tv.selected, row)
	}
}

// IsSelected reports whether a row is in the multi-select set.
func (tv *TableView) IsSelected(row int) bool {
	return tv.selected != nil && tv.selected[row]
}

// SelectedRows returns the multi-select members in ascending order. The
// returned slice is freshly allocated; callers may mutate it freely.
func (tv *TableView) SelectedRows() []int {
	if tv.selected == nil {
		return nil
	}
	out := make([]int, 0, len(tv.selected))
	for r := range tv.selected {
		out = append(out, r)
	}
	sort.Ints(out)
	return out
}

// ClearSelect empties the multi-select set.
func (tv *TableView) ClearSelect() {
	tv.selected = nil
}

// --- rendering -------------------------------------------------------------

// Render draws the table into a Width x Height cell area.
func (tv *TableView) Render(f *tuidata.Frame, o RenderOpts) string {
	th := o.Theme
	if o.Width <= 0 || o.Height <= 0 {
		return ""
	}
	if f == nil || f.NumCols() == 0 {
		return blankLines(o.Width, o.Height, th)
	}
	// Find severity column in the ORIGINAL frame before sub-framing.
	// When TableCols is set (e.g. OpenObserve summary view), severity is not in
	// the sub-frame so we must look it up from the original frame.
	severityOrigColIdx := -1
	if o.SeverityCol != "" {
		for i := 0; i < f.NumCols(); i++ {
			if f.Columns[i].Name == o.SeverityCol {
				severityOrigColIdx = i
				break
			}
		}
	}
	origFrame := f

	// D（第八轮②段）：限定列那一支的子帧挪到 `tv.rowOff` 钳位**之后**才建（见下），
	// 所以这里的 nrows 取**父帧**——它是真实行数，gutter 宽度、滚动钳位、`r >= nrows`
	// 守卫、行号显示、`ClampTo` 全部继续吃它；子帧只是"渲染行窗口"，不是整帧。
	// ncols 先按父帧取，限定列那一支建完窗口子帧后立刻改回子帧的列数（值不变）。
	nrows, ncols := origFrame.NumRows(), origFrame.NumCols()

	// Vertical budget.
	body := o.Height - 1 // header
	if o.ShowBorders {
		body = o.Height - 4 // top, header, separator, bottom
	}
	if body < 1 {
		body = 1
	}
	tv.lastRows = body

	// Keep selection visible.
	tv.sel = clamp(tv.sel, 0, max(0, nrows-1))
	if tv.sel < tv.rowOff {
		tv.rowOff = tv.sel
	}
	if tv.sel >= tv.rowOff+body {
		tv.rowOff = tv.sel - body + 1
	}
	tv.rowOff = clamp(tv.rowOff, 0, max(0, nrows-body))

	// D（第八轮②段，报告 §13）：限定列那一支只物化**渲染行窗口**——画循环只读
	// [tv.rowOff, tv.rowOff+body)（:400 一带），测宽只读 [lo, hi)（hi-lo ≤ sampleRows），
	// 所以子帧带 max(body, sampleRows) 行就够，不必把父帧 50,000 行的格子全拷一遍。
	// win ≥ sampleRows 是 U-2：真实终端高于 200 行时 body 可以大于 sampleRows，
	// 两个界都要包住 ⇒ 取 max。win ≥ 1（body 已经 ≥ 1）⇒ 子帧行数不会比绘制界少。
	//
	// rowBase：子帧第 0 行对应的**父帧绝对行号**。非限定列那一支 f 就是父帧 ⇒ rowBase 恒 0，
	// 那条道上的绝对下标读法一字不变。
	//
	// **故意写的"绝对下标"点（① 段 U-3 要求复认，不是抄来的）**：对 `Render` 体与三个
	// 行向 helper 入口 grep `\.Cell\(|CellString\(|NumRows\(\)`（`.Cells[` 已被 data 包的
	// 非导出字段消灭）⇒ 行向读点只有
	// naturalWidths(:646)/naturalWidthsCached(:670)/cellWidth(:594,598,604) 里的
	// `f.CellString(r, …)`（r 由 [lo,hi) 给出）与绘制循环那一处；`f.Columns[c].Name`（表头）
	// 是列向；`origFrame.CellString(r, severityColIdx)` 是绝对行号＋**父帧列**，故不动。
	// 窗口内绝对行号 ⇔ 窗口坐标的换算只有 `r - rowBase` 一处。
	//
	// 这一支**接**的是下面那块 tableColsWidthCache（:322 一带），**不是** §11/§12 的 widthCache：
	// 那块按帧指针建键，而这里的 f 是 :292 每帧新造的窗口子帧 ⇒ 指针每帧变、键每帧失配。
	// 所以这块表的键用父帧指针＋窗口坐标。（§13 在这一支留下的"不接缓存"决定，本轮按 ① 段
	// 实测读数**刻意反转**：原文与推翻它的读数见报告 §14.1/§14.5。）
	rowBase := 0
	if len(o.TableCols) > 0 {
		base := tv.rowOff
		win := min(nrows-base, max(body, sampleRows))
		f = tableColsSubFrameWindow(origFrame, o.TableCols, base, win)
		rowBase = base
		ncols = f.NumCols() // 与 pristine 同值：子帧列数 == len(o.TableCols)
	}

	// Horizontal budget.
	inner := o.Width
	if o.ShowBorders {
		inner = o.Width - 2
		if inner < 1 {
			inner = 1
		}
	}
	gutterW := 0
	if o.ShowRowNumbers {
		gutterW = len(strconv.Itoa(max(1, nrows))) + 1
	}
	avail := inner - gutterW
	if avail < 1 {
		avail = 1
	}

	// Column layout.
	lo := tv.rowOff
	hi := min(nrows, lo+sampleRows)
	// 测宽那三个调用现在吃**窗口坐标**：子帧第 j 行 ＝ 父帧绝对行 lo+base 起算的 j。
	// 恒等式 hi-base = min(nrows-base, sampleRows)：pristine 的子帧有整帧 nrows 行，
	// clamp 到 nrows 就是它；D 的子帧只有 win 行，win ≥ hi-base ⇒ clamp 不动 ⇒ 同一张宽度表。
	mlo, mhi := lo-rowBase, hi-rowBase

	// 【已被实测推翻，第十轮②a 更正，报告 §14.1/§14.3】这一支原来的注释原文（§12/§13 时代，
	// 逐字留在下面这块引用里，便于考古）：
	//	"限定列那一支每帧新建子帧（:246-248 → tableColsSubFrame），帧指针必变 ⇒ 宽度缓存
	//	 键必失配，只会付一份永不命中的分配；那一条道上量宽只占整帧的 3–5%，不接缓存。"
	// 逐条对账（① 段 probe14c_e_time_P1/P2.txt，同进程相邻臂；widthsNil 就是"这一支每帧重算
	// 一遍量宽"那条腿）：
	//   - "只占整帧的 3–5%" **不成立**：实测 widthsNil＝整帧 view 的 **18.3–20.1%**
	//     （三轮合并 16–26%）；即便把分母换回 §13 改前的 view 中位，当年也是 5.4–6.2%，
	//     踩在上沿之外。
	//   - "只会付一份永不命中的分配" 仍然对（键里有子帧指针就必失配），但它**不构成理由**：
	//     widthsNil 每帧只分配 16 B／1 次 ⇒ 接不接缓存与分配账无关，这条是**纯时间生意**。
	//   - 引用的锚点已漂：":246-248 → tableColsSubFrame" 现在是 ":289-295 →
	//     tableColsSubFrameWindow"。
	// ⇒ 修法：这一支改接一块**按窗口坐标建键**的宽度表（键里不能有子帧指针）；
	//   非限定列那一支照旧走 &tv.widths，一条指令没动。
	var wc *widthCache
	if len(o.TableCols) == 0 {
		wc = &tv.widths
	}

	// First render of a frame: pick the column mode automatically. When
	// compact fitting would degrade columns badly the view starts expanded.
	if !tv.modeSet {
		tv.modeSet = true
		nat := tv.naturalWidthsFor(f, origFrame, o.TableCols, rowBase, mlo, mhi, compactColCap, wc)
		tv.expanded = autoExpand(nat, fitWidths(nat, avail-(ncols-1)))
	}

	var cols []int   // visible column indices
	var widths []int // display width per visible column
	if tv.expanded {
		nat := tv.naturalWidthsFor(f, origFrame, o.TableCols, rowBase, mlo, mhi, expandedColCap, wc)
		tv.colCursor = clamp(tv.colCursor, 0, ncols-1)
		tv.colOff = clamp(tv.colOff, 0, ncols-1)
		if tv.snapCursor {
			tv.colOff = scrollColIntoView(nat, tv.colOff, tv.colCursor, avail)
			tv.snapCursor = false
		}
		cols, widths = visibleColumns(nat, tv.colOff, avail)
		// After a resize (or any window change) keep the cursor visible.
		if len(cols) > 0 {
			tv.colCursor = clamp(tv.colCursor, cols[0], cols[len(cols)-1])
		}
	} else {
		nat := tv.naturalWidthsFor(f, origFrame, o.TableCols, rowBase, mlo, mhi, compactColCap, wc)
		fitted := fitWidths(nat, avail-(ncols-1))
		tv.colCursor = clamp(tv.colCursor, 0, ncols-1)
		cols = make([]int, ncols)
		for i := range cols {
			cols[i] = i
		}
		widths = fitted
	}

	// When a column subset is active (e.g. OpenObserve timestamp+body),
	// stretch the last column to fill all remaining terminal space so the
	// content is visible. Character-level left/right scroll (HScroll) is
	// applied per cell below, not here.
	if len(o.TableCols) > 0 && len(widths) > 0 {
		used := gutterW + len(widths) - 1
		for _, w := range widths[:len(widths)-1] {
			used += w
		}
		if remaining := avail - used; remaining > widths[len(widths)-1] {
			widths[len(widths)-1] = remaining
		}
	}

	var b strings.Builder
	if o.ShowBorders {
		b.WriteString(th.Border.Render("╭" + strings.Repeat("─", inner) + "╮"))
		b.WriteString("\n")
	}

	// Header, with the cursored column emphasized (both column modes: y-copy
	// and the status bar column tag follow the cursor everywhere).
	var hb strings.Builder
	hb.WriteString(th.Header.Render(strings.Repeat(" ", gutterW)))
	for i, c := range cols {
		if i > 0 {
			hb.WriteString(th.Header.Render(" "))
		}
		name := f.Columns[c].Name
		cell := padCell(name, widths[i])
		// Highlight the cursor column with a soft warm tint; other columns use plain header style.
		if c == o.HeaderCur {
			hb.WriteString(th.ColCursor.Render(cell))
		} else {
			hb.WriteString(th.Header.Render(cell))
		}
	}
	used := gutterW
	for i, w := range widths {
		if i > 0 {
			used++
		}
		used += w
	}
	if pad := inner - used; pad > 0 {
		hb.WriteString(th.Header.Render(strings.Repeat(" ", pad)))
	}
	headLine := hb.String()
	if ansi.StringWidth(headLine) > inner {
		// Fit-mode separators can overflow a pathologically narrow viewport.
		headLine = ansi.Truncate(headLine, inner, "…")
	}
	b.WriteString(wrapBorder(headLine, o.ShowBorders, th))
	b.WriteString("\n")

	if o.ShowBorders {
		b.WriteString(th.Border.Render("├" + strings.Repeat("─", inner) + "┤"))
		b.WriteString("\n")
	}

	// Find severity column index for per-row coloring (-1 when disabled).
	// Uses severityOrigColIdx (from original frame) — already computed above.
	severityColIdx := severityOrigColIdx
	_ = origFrame // used in row loop below

	// Body rows.
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
				// Multi-select members use the match style so they stay
				// distinct from the active cursor (th.RowSelected).
				rowStyle = th.Match
				gstyle = th.Match
			case o.MatchRows != nil && o.MatchRows[r]:
				rowStyle = th.Match
			case r%2 == 1:
				rowStyle = th.RowOdd
			}
			if severityColIdx >= 0 && r != tv.sel && !sel {
				sev := strings.ToUpper(origFrame.CellString(r, severityColIdx))
				if c := severityColor(sev); c != "" {
					rowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(c))
					gstyle = rowStyle
				}
			}
			highlightedRow := false
			for j, c := range cols {
				// 绝对行号 r（= tv.rowOff+i）→ 窗口坐标 r-rowBase；非限定列那一支 rowBase==0，
				// 这一行与 pristine 逐字同义（r 仍受 :433 的 `r >= nrows` 守卫，nrows 是父帧行数）。
				val := f.CellString(r-rowBase, c)
				// Apply character-level scroll to the last column when active.
				if o.HScroll > 0 && j == len(cols)-1 && len(o.TableCols) > 0 {
					val = ansi.TruncateLeft(val, o.HScroll, "")
				}
				// Character highlight: only for non-cursor, non-multiselect rows.
				// We deliberately do NOT wrap in rowStyle so the row background
				// is neutral — only the matched characters get the highlight colour.
				if o.HighlightQuery != "" && r != tv.sel && !sel {
					raw := HighlightSubstr(val, o.HighlightQuery, th.Match, lipgloss.NewStyle())
					if w := ansi.StringWidth(raw); w > widths[j] {
						raw = ansi.Truncate(raw, widths[j], "")
					}
					if pad := widths[j] - ansi.StringWidth(raw); pad > 0 {
						raw += strings.Repeat(" ", pad)
					}
					cells[j] = raw
					highlightedRow = true
				} else {
					cells[j] = padCellOpts(val, widths[j], o.NoEllipsis)
				}
			}
			rowText := strings.Join(cells, " ")
			gut := ""
			if gutterW > 0 {
				num := strconv.Itoa(r + 1)
				if sel {
					// Replace the gutter's trailing space with the marker so
					// total width is unchanged.
					gut = gstyle.Render(padLeft(num, gutterW-1) + selectMarker)
				} else {
					gut = gstyle.Render(padLeft(num, gutterW-1) + " ")
				}
			} else if sel {
				// No gutter: lead with a 2-cell tag column and shave it off
				// the cell budget so the row keeps the same width.
				tag := th.Match.Render(selectMarker + " ")
				line = tag + rowStyle.Render(padLine(rowText, inner-2))
				b.WriteString(wrapBorder(line, o.ShowBorders, th))
				if i < body-1 || o.ShowBorders {
					b.WriteString("\n")
				}
				continue
			}
			if highlightedRow {
				line = gut + rowText
				if w := ansi.StringWidth(line); w < inner {
					line += strings.Repeat(" ", inner-w) // neutral padding, no background
				}
			} else {
				line = gut + rowStyle.Render(padLine(rowText, inner-gutterW))
			}
		}
		b.WriteString(wrapBorder(line, o.ShowBorders, th))
		if i < body-1 || o.ShowBorders {
			b.WriteString("\n")
		}
	}

	if o.ShowBorders {
		b.WriteString(th.Border.Render("╰" + strings.Repeat("─", inner) + "╯"))
	}
	return b.String()
}

func wrapBorder(line string, borders bool, th *theme.Theme) string {
	if !borders {
		return line
	}
	side := th.Border.Render("│")
	return side + line + side
}

func blankLines(w, h int, th *theme.Theme) string {
	line := th.Text.Render(strings.Repeat(" ", w))
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// --- width math (pure, unit-tested) ----------------------------------------

// widthCache 复用"每格显示宽度"：键＝帧指针 + 行数 + 列数，值＝每格 ansi.StringWidth 的
// 原始宽度（不含 capW 截断），所以 compact(40) 与 expanded(80) 两条道共用同一份料。
//
// Render 每帧都要把 [lo,hi) × 视图列数的每一格重新格式化再量宽（:603），而宽度只与帧
// 有关：同一帧第二帧起，每格从一次 ANSI 宽度解析降到一次数组读。
//
// 存"格"而不存"列宽向量"：向量把 lo 也算进去了（:290 的 lo＝tv.rowOff），翻页就会给出
// 上一帧的宽度；按格存则翻页只有新进入窗口的那几格付全价，其余命中。
//
// buf 存 width+1，0＝没算过 ⇒ 零值就是"空"，窗口外的格永不计算。
//
// 键里没有每格内容：帧的不可变由类型系统强制（data/frame.go 的 Column.cells 非导出，
// 包外写不了格值），改一格走 WithCell 换新帧（align 按帧指针判失配、clear 复用缓冲），
// 追加会改行数（AppendRow 仅限构建期）。
//
// 第七轮（§12）在这份料上加了两件事，各自独立可关：
//   - **换帧不重新 make**（reset）：块长对上就 clear 复用。①量到的建料价差 10–33×
//     （A 10,038→362 ns、C 176,236→16,957、F 12,554→381），且打字那一支每键换帧
//     （searchbar.go:111 的 s.base.Select(rows) 造新预览帧）也吃这条——那正是 §11 登记的代价。
//   - **被规模上限排除的大帧改按窗口跨度放料**：一块 widthCacheSpanRows 行的料 ＋ 一个
//     行锚 base，窗口跑出覆盖就重放锚并清零。界内帧**不走这一档**：①实测窗口跨度在界内
//     帧的滚动腿上是 6–16× 劣化（清跨度＝把整帧的记忆丢掉），所以界内仍是整帧
//     （span==rows、base==0 ⇒ 下标与第六轮逐位相同）。
type widthCache struct {
	f         *tuidata.Frame
	rows      int
	ncols     int
	base      int // buf 覆盖的第一行；界内帧恒为 0
	span      int // buf 的行跨度；界内帧恒为 rows
	buf       []uint32
	builds    int // make 了几次；测试用它钉"必须命中"这条性质
	clears    int // 复用同一块、只清零的次数（换帧与重放锚都算＝省下的 make）
	reanchors int // 窗口跑出覆盖、重放锚的次数
	outside   int // 取数落在覆盖之外的格数；产品路径上恒为 0，测试钉它
	computed  int // 真正付了 StringWidth 的格数
}

// widthCacheMaxCells 是缓存的规模上限（格数，每格 4 B）。reader 侧没有行数上限，
// 超限的大帧不再"整档不缓存"，改走下面那一档窗口跨度；连窗口都放不下的（列极多）
// 仍然按老路直接量。
const widthCacheMaxCells = 200_000

// 窗口跨度档的预算。窗口本身恒 ≤ sampleRows 行（:291 的 hi＝min(nrows, lo+sampleRows)），
// 所以一块 2*sampleRows 行的料就够；锚往上留 sampleRows/2 行 ⇒ 上下各有 100 行的免费滚动。
// 预算取自常量而不是每帧的 (lo,hi) ⇒ 块长与窗高无关 ⇒ 换帧与重放锚永远能走 clear、
// 不必回到 make。
const (
	widthCacheSpanRows = 2 * sampleRows
	widthCacheSpanLead = sampleRows / 2
)

// align 在每遍量宽开头对账：换帧就换料（长度对上就 clear 复用）；界内帧的窗口不参与锚
// （span==rows ⇒ 覆盖恒成立）；越限帧只在窗口跑出 [base, base+span) 时重放锚＋清零。
// lo/hi 要已经过夹逼——由 naturalWidthsCached 入口用与循环里同一套 clamp 保证。
func (c *widthCache) align(f *tuidata.Frame, lo, hi int) {
	if c.f != f || c.rows != f.NumRows() || c.ncols != f.NumCols() {
		c.reset(f, lo, hi)
		return
	}
	if c.buf == nil || (lo >= c.base && hi <= c.base+c.span) {
		return
	}
	c.reanchors++
	c.base = clamp(lo-widthCacheSpanLead, 0, c.rows-c.span)
	clear(c.buf)
	c.clears++ // 重放锚也是"复用同一块、只清零"，与换帧同一笔账
	c.computed = 0
}

// cellWidth 取一格宽度；没算过就地算并存下。缓存不可用（nil / 放不下）时直接量。
//
// 覆盖检查不是冗余防线：少了它，col*span+(r-base) 在 r 出界时会指向**别的槽位**并给出
// 别格的宽度（界内帧 span==rows、base==0，这一支永远成立）。产品路径上 outside 恒为 0，
// 这条不变量由 align 的对账给出、由测试钉住。
func (c *widthCache) cellWidth(f *tuidata.Frame, r, col int) int {
	if c == nil || c.buf == nil {
		return ansi.StringWidth(f.CellString(r, col))
	}
	if r < c.base || r >= c.base+c.span {
		c.outside++
		return ansi.StringWidth(f.CellString(r, col))
	}
	i := col*c.span + (r - c.base)
	if v := c.buf[i]; v != 0 {
		return int(v) - 1
	}
	w := ansi.StringWidth(f.CellString(r, col))
	c.buf[i] = uint32(w) + 1
	c.computed++
	return w
}

// reset 换键即放料：宽度本身只有 4 B/格，但滞留一个大帧比这贵得多。
func (c *widthCache) reset(f *tuidata.Frame, lo, hi int) {
	rows, ncols := f.NumRows(), f.NumCols()
	c.f, c.rows, c.ncols, c.computed = f, rows, ncols, 0
	c.base, c.span = 0, 0
	if rows <= 0 || ncols <= 0 {
		c.buf = nil
		return
	}
	limit := widthCacheMaxCells / ncols // 上限允许的行数；除一次，不写 rows*ncols 免溢出
	span := 0
	switch {
	case rows <= limit:
		span = rows // 界内：整帧跨度，base 恒 0
	case widthCacheSpanRows <= limit:
		span = min(rows, widthCacheSpanRows) // 走到这里 rows>limit≥400 ⇒ span 恒 400
		c.base = clamp(lo-widthCacheSpanLead, 0, rows-span)
	}
	if span <= 0 { // 连窗口档都放不下 ⇒ 不建缓存
		c.buf = nil
		return
	}
	c.span = span
	if len(c.buf) == span*ncols {
		clear(c.buf)
		c.clears++
		return
	}
	c.buf = make([]uint32, span*ncols)
	c.builds++
}

// naturalWidths computes, per column, max(header width, widest cell among
// rows [lo, hi)), capped at capW and floored at 1.
func naturalWidths(f *tuidata.Frame, lo, hi, capW int) []int {
	lo = clamp(lo, 0, f.NumRows())
	hi = clamp(hi, lo, f.NumRows())
	out := make([]int, f.NumCols())
	for c := range f.Columns {
		w := ansi.StringWidth(f.Columns[c].Name)
		for r := lo; r < hi; r++ {
			if cw := ansi.StringWidth(f.CellString(r, c)); cw > w {
				w = cw
			}
			if w >= capW {
				w = capW
				break
			}
		}
		out[c] = clamp(w, 1, capW)
	}
	return out
}

// naturalWidthsCached 与 naturalWidths 逐格等价，只是每格宽度改从缓存取：循环形状、
// 早停条件、clamp 全部原样保留 ⇒ 两者必须给出同一个向量。c 为 nil 时就是原路。
//
// 入口先 align：夹逼后的 (lo,hi) 只有这里知道（缓存自己不看 Render 的状态），对账必须
// 在循环之前、用与循环同一套 clamp 的值做——cellWidth 里没有第二次键检查。
func naturalWidthsCached(f *tuidata.Frame, lo, hi, capW int, c *widthCache) []int {
	lo = clamp(lo, 0, f.NumRows())
	hi = clamp(hi, lo, f.NumRows())
	if c != nil {
		c.align(f, lo, hi)
	}
	out := make([]int, f.NumCols())
	for col := range f.Columns {
		w := ansi.StringWidth(f.Columns[col].Name)
		for r := lo; r < hi; r++ {
			if cw := c.cellWidth(f, r, col); cw > w {
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

// --- 限定列那一支的窗口宽度表（第十轮②a，报告 §14，机制 M1）-----------------

// tableColsWidthCache 给限定列（RenderOpts.TableCols 非空）那一支记忆化**一整遍量宽的结果**：
// 键＝父帧指针 + 父帧行数/列数 + cols 序列（逐元素）+ 窗口坐标 (base, win, mlo, mhi) + capW。
//
// 为什么不复用上面那块 widthCache：那块按**帧指针**建键，而这一支的 f 是 :292 每帧新造的
// 窗口子帧 ⇒ 指针每帧变、键每帧失配（§13 实测那一支 builds==0 且 widths.f==nil）。这里把键
// 换成"父帧＋窗口"这一族不变量：同一个父帧、同一组列、同一个渲染窗口 ⇒ 量宽结果必同。
//
// 键的**每一维都是必要的**，少一维不是"少一次命中"而是"读到别的窗口的宽度"＝显示偏差：
//   - f/rows/ncols：帧的不可变由类型系统强制（data/frame.go，Column.cells 非导出），改一格走 WithCell 换新帧，追加改行数；
//   - cols：**父帧不变就换 cols 序列**在产品里是真会发生的（app.go:247/950-971/1306、
//     tablefetch.go:92）⇒ 只比 len 不比元素，就会把 {0,1} 的表给 {0,2} 用；
//   - base/win：窗口子帧的内容由这两维决定（§13 的机制）；
//   - mlo/mhi：这一遍扫的是子帧的哪几行；
//   - capW：compact(40) 与 expanded(80) 两条道的截断不同 ⇒ 分开存（两条道各占一块键，
//     互相顶掉；这是单槽表，代价是切换 mode 的那一帧重建）。
//
// 存"结果向量"而不是"每格宽度"：这一支的窗口整体命中/整体失配（键里有 mlo/mhi），
// 增量重放省不下任何一段；命中就是 0 次格读，正是 ① 段 widthsHit 那条下界对应的路。
//
// 交给调用方的永远是副本（cw.val 从不出门）：缓存里放一个可被下游改写的切片，等于把
// 静默错误放进长期状态。未命中时跑的是**老路**（naturalWidthsCached 传 nil ⇒ 与 §13 的
// 每帧重算由构造逐格等价），所以本机制没有第二条量宽实现可漂移。
type tableColsWidthCache struct {
	f        *tuidata.Frame
	rows     int
	ncols    int
	keyCols  []int // cols 序列的常驻副本（逐元素比）
	base     int   // 窗口子帧第 0 行对应的父帧绝对行号（＝tv.rowOff）
	win      int   // 窗口子帧行数
	mlo      int   // 测宽窗口在子帧坐标里的下界
	mhi      int   // 上界
	capW     int
	val      []int // 常驻的结果向量副本
	valid    bool
	builds   int // 真正跑了一遍量宽的次数
	hits     int // 命中回表的次数
	computed int // 按窗口预算计的格读次数（命中＝0；早停时这是上界，见 naturalWidthsFor 注释）
}

// match 逐维比键；还没有表（valid==false）时恒不命中。
func (cw *tableColsWidthCache) match(f *tuidata.Frame, cols []int, base, win, mlo, mhi, capW int) bool {
	if !cw.valid || cw.f != f || cw.rows != f.NumRows() || cw.ncols != f.NumCols() {
		return false
	}
	if cw.base != base || cw.win != win || cw.mlo != mlo || cw.mhi != mhi || cw.capW != capW {
		return false
	}
	if len(cw.keyCols) != len(cols) {
		return false
	}
	for i, c := range cols {
		if cw.keyCols[i] != c {
			return false
		}
	}
	return true
}

// get 要表：命中回副本；未命中就地跑一遍老路并把键与值存下。
//
// 越界是 fail-loud（panic）而不是静默错列：老路 naturalWidthsCached 入口会把 (mlo,mhi)
// **静默 clamp** 到子帧行数以内 ⇒ 宽度表少扫几行，而这块表会把"扫短了的值"存进一个写着
// 完整 mhi 的键里，从此长期端出一份与键不符的宽度。§13 对"窗口外读"选的是同一条口径。
func (cw *tableColsWidthCache) get(sub, orig *tuidata.Frame, cols []int, base, mlo, mhi, capW int) []int {
	win := sub.NumRows()
	if mlo < 0 || mhi > win {
		panic("tableColsWidthCache: 测宽窗口 [mlo,mhi) 越出窗口子帧行数 ⇒ 键会盖住一张被 clamp 截短的宽度表")
	}
	if cw.match(orig, cols, base, win, mlo, mhi, capW) {
		cw.hits++
		out := make([]int, len(cw.val))
		copy(out, cw.val)
		return out
	}
	nat := naturalWidthsCached(sub, mlo, mhi, capW, nil)
	cw.f, cw.rows, cw.ncols = orig, orig.NumRows(), orig.NumCols()
	cw.keyCols = append(cw.keyCols[:0], cols...) // 常驻两块切片：换窗口不再多分配
	cw.val = append(cw.val[:0], nat...)
	cw.base, cw.win, cw.mlo, cw.mhi, cw.capW = base, win, mlo, mhi, capW
	cw.valid = true
	cw.builds++
	cw.computed += (mhi - mlo) * len(cols)
	return nat
}

// naturalWidthsFor 是 Render 那三个测宽调用点唯一的出口，分流条件与改前
// `if len(o.TableCols) == 0 { wc = &tv.widths }` **同一个式子**：
//   - 非限定列 ⇒ naturalWidthsCached(sub, mlo, mhi, capW, wc)，那条道路径一字没动；
//   - 限定列 ⇒ 上面那块按窗口坐标建键的表（wc 在这一支本来是 nil，传不传都不参与）。
func (tv *TableView) naturalWidthsFor(sub, orig *tuidata.Frame, cols []int, base, mlo, mhi, capW int, wc *widthCache) []int {
	if len(cols) == 0 {
		return naturalWidthsCached(sub, mlo, mhi, capW, wc)
	}
	return tv.tcWidths.get(sub, orig, cols, base, mlo, mhi, capW)
}

// autoExpand reports whether the view should start in expanded mode: compact
// fitting (fitted) would shrink some column below ~60% of its natural width
// (nat), or below 5 cells when its natural width exceeds 8.
func autoExpand(nat, fitted []int) bool {
	for i := range nat {
		if fitted[i]*10 < nat[i]*6 {
			return true
		}
		if nat[i] > 8 && fitted[i] < 5 {
			return true
		}
	}
	return false
}

// fitWidths shrinks the widths in nat so their sum fits in avail cells,
// reducing the widest columns first, never below 1. When everything already
// fits, nat is returned unchanged (copied).
func fitWidths(nat []int, avail int) []int {
	out := make([]int, len(nat))
	copy(out, nat)
	total := 0
	maxW := 0
	for _, w := range out {
		total += w
		if w > maxW {
			maxW = w
		}
	}
	if total <= avail || len(out) == 0 {
		return out
	}
	if avail < len(out) {
		for i := range out {
			out[i] = 1
		}
		return out
	}
	// Find the largest level L such that sum(min(w, L)) <= avail; capping at
	// L trims the widest columns first.
	sumCap := func(l int) int {
		s := 0
		for _, w := range out {
			s += min(w, l)
		}
		return s
	}
	lo, hi := 1, maxW
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if sumCap(mid) <= avail {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	level := lo
	extra := avail - sumCap(level)
	for i := range out {
		if out[i] > level {
			out[i] = level
			if extra > 0 {
				out[i]++
				extra--
			}
		}
	}
	return out
}

// visibleColumns returns the column indices and widths that fit in avail
// cells starting at colOff, with a single-space separator between columns.
// At least one column is always returned.
func visibleColumns(nat []int, colOff, avail int) (cols, widths []int) {
	colOff = clamp(colOff, 0, max(0, len(nat)-1))
	used := 0
	for c := colOff; c < len(nat); c++ {
		need := nat[c]
		if len(cols) > 0 {
			need++ // separator
		}
		if used+need > avail && len(cols) > 0 {
			break
		}
		w := nat[c]
		if used+need > avail { // first column wider than the viewport
			w = avail
		}
		cols = append(cols, c)
		widths = append(widths, w)
		used += need
	}
	return cols, widths
}

// scrollColIntoView returns the new colOff such that column cursor is fully
// visible in a window of avail cells.
func scrollColIntoView(nat []int, colOff, cursor, avail int) int {
	if cursor < colOff {
		return cursor
	}
	// Advance colOff until [colOff..cursor] fits.
	for off := colOff; off <= cursor; off++ {
		w := 0
		for c := off; c <= cursor; c++ {
			if c > off {
				w++
			}
			w += nat[c]
		}
		if w <= avail || off == cursor {
			return off
		}
	}
	return cursor
}

// --- small helpers ----------------------------------------------------------

func padCell(s string, w int) string { return padCellOpts(s, w, false) }

func padCellOpts(s string, w int, noEllipsis bool) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "␤")
	s = strings.ReplaceAll(s, "\r", "␤")
	s = strings.ReplaceAll(s, "\t", " ")
	if ansi.StringWidth(s) > w {
		indicator := "…"
		if noEllipsis {
			indicator = ""
		}
		s = ansi.Truncate(s, w, indicator)
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func padLine(s string, w int) string {
	if ansi.StringWidth(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// tableColsSubFrame builds a lightweight frame containing only the columns
// at the given indices. Used by Render when RenderOpts.TableCols is set.
func tableColsSubFrame(f *tuidata.Frame, cols []int) *tuidata.Frame {
	names := make([]string, len(cols))
	for i, c := range cols {
		if c < f.NumCols() {
			names[i] = f.Columns[c].Name
		}
	}
	out := tuidata.New(names...)
	for i, c := range cols {
		if c < f.NumCols() {
			out.Columns[i].Type = f.Columns[c].Type
		}
	}
	for r := 0; r < f.NumRows(); r++ {
		row := make([]any, len(cols))
		for i, c := range cols {
			if c < f.NumCols() {
				row[i] = f.Cell(r, c)
			}
		}
		out.AppendRow(row)
	}
	return out
}

// tableColsSubFrameWindow 是 tableColsSubFrame 的**行窗口**版本（第八轮②段，报告 §13）：
// 只拷父帧的 [base, base+limit) 这几行，列选取与列序与全行版**逐字相同**。
//
// "行窗口 == 全量子帧的行前缀"这条等式**不写进代码**——它由 tableview_subframe13_test.go
// 的 TestSubFrame13WindowIsRowPrefixOfFull 独立钉住（列名/列类型/每一行每一格，含列下标
// 越界那一格的名字与类型），而不是靠这里去调全行版。
//
// 窗口外（或越界的列内行）的读**必须 panic**，这是 fail-loud，不许兜底：Cells 的长度恰好是
// win，拿绝对行号来读就是 index out of range。
func tableColsSubFrameWindow(f *tuidata.Frame, cols []int, base, limit int) *tuidata.Frame {
	names := make([]string, len(cols))
	for i, c := range cols {
		if c < f.NumCols() {
			names[i] = f.Columns[c].Name
		}
	}
	out := tuidata.New(names...)
	for i, c := range cols {
		if c < f.NumCols() {
			out.Columns[i].Type = f.Columns[c].Type
		}
	}
	start, length := windowRowBounds(f.NumRows(), base, limit)
	// 预留 cap==length ⇒ AppendRow 一路不 realloc。
	out.PreallocRows(length)
	for r := start; r < start+length; r++ {
		row := make([]any, len(cols))
		for i, c := range cols {
			if c < f.NumCols() {
				row[i] = f.Cell(r, c)
			}
		}
		out.AppendRow(row)
	}
	return out
}

// windowRowBounds 是 (base, limit) → 合法窗口坐标的那一步钳位：base 进 [0, rows]，
// limit 进 [0, rows-base]。base 不钳 ⇒ 后面 `Cells[r]` 直接 index out of range；
// limit 只按 rows-base 钳 ⇒ 越界列在 AppendRow 里少给一行 ⇒ 静默错数据。两者都不许省。
func windowRowBounds(rows, base, limit int) (int, int) {
	if base < 0 {
		base = 0
	}
	if base > rows {
		base = rows
	}
	if limit < 0 {
		limit = 0
	}
	if limit > rows-base {
		limit = rows - base
	}
	return base, limit
}

// severityColor returns a foreground color hex string for the given severity text.
// Follows OpenTelemetry conventions: TRACE=gray, DEBUG=blue, INFO=green, WARN=yellow, ERROR=red, FATAL=bright red.
func severityColor(sev string) string {
	switch {
	case strings.HasPrefix(sev, "FATAL"):
		return "#ff5f5f"
	case strings.HasPrefix(sev, "ERROR"):
		return "#e06c75"
	case strings.HasPrefix(sev, "WARN"):
		return "#e5c07b"
	case strings.HasPrefix(sev, "INFO"):
		return "#98c379"
	case strings.HasPrefix(sev, "DEBUG"):
		return "#61afef"
	case strings.HasPrefix(sev, "TRACE"):
		return "#7f848e"
	}
	return ""
}
