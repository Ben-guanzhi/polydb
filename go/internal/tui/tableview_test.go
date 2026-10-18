package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tuidata"
)

func testFrame(rows int) *tuidata.Frame {
	f := tuidata.New("id", "name", "comment")
	for i := 0; i < rows; i++ {
		f.AppendRow([]any{"1", "alice", "a longer comment cell"})
	}
	return f
}

// wideFrame builds a frame whose columns are far too wide to fit compactly
// in a narrow viewport.
func wideFrame(rows int) *tuidata.Frame {
	f := tuidata.New("alpha_column", "beta_column", "gamma_column", "delta_column")
	for i := 0; i < rows; i++ {
		f.AppendRow([]any{
			strings.Repeat("a", 30), strings.Repeat("b", 30),
			strings.Repeat("c", 30), strings.Repeat("d", 30),
		})
	}
	return f
}

func sum(ws []int) int {
	s := 0
	for _, w := range ws {
		s += w
	}
	return s
}

func TestFitWidthsAlreadyFits(t *testing.T) {
	nat := []int{5, 10, 3}
	got := fitWidths(nat, 18)
	if !reflect.DeepEqual(got, nat) {
		t.Fatalf("expected unchanged widths, got %v", got)
	}
}

func TestFitWidthsExactFit(t *testing.T) {
	nat := []int{5, 10, 3}
	got := fitWidths(nat, 18)
	if sum(got) != 18 {
		t.Fatalf("expected exact fit 18, got %v (sum %d)", got, sum(got))
	}
}

func TestFitWidthsShrinksWidestFirst(t *testing.T) {
	nat := []int{4, 20, 6}
	got := fitWidths(nat, 20)
	if sum(got) > 20 {
		t.Fatalf("sum %d exceeds available 20: %v", sum(got), got)
	}
	// the narrow columns must be untouched; only the widest shrinks
	if got[0] != 4 || got[2] != 6 {
		t.Fatalf("narrow columns were shrunk: %v", got)
	}
	if got[1] >= 20 {
		t.Fatalf("widest column not shrunk: %v", got)
	}
}

func TestFitWidthsEqualizesWideColumns(t *testing.T) {
	nat := []int{30, 30, 2}
	got := fitWidths(nat, 22)
	if sum(got) > 22 {
		t.Fatalf("sum %d exceeds 22: %v", sum(got), got)
	}
	if got[2] != 2 {
		t.Fatalf("narrow column shrunk: %v", got)
	}
	if diff := got[0] - got[1]; diff < -1 || diff > 1 {
		t.Fatalf("wide columns should shrink to about the same level: %v", got)
	}
}

func TestFitWidthsMinWidthOne(t *testing.T) {
	nat := []int{10, 10, 10}
	got := fitWidths(nat, 2) // less than one cell per column
	for i, w := range got {
		if w != 1 {
			t.Fatalf("column %d: expected min width 1, got %d (%v)", i, w, got)
		}
	}
}

func TestNaturalWidthsCapAndHeader(t *testing.T) {
	f := tuidata.New("a_very_long_header", "b")
	f.AppendRow([]any{"x", strings.Repeat("y", 100)})
	got := naturalWidths(f, 0, 1, 40)
	if got[0] != len("a_very_long_header") {
		t.Fatalf("header width not respected: %v", got)
	}
	if got[1] != 40 {
		t.Fatalf("cap 40 not applied: %v", got)
	}
}

func TestVisibleColumnsWindow(t *testing.T) {
	nat := []int{10, 10, 10, 10}
	cols, widths := visibleColumns(nat, 1, 21) // fits col1 (10) + sep + col2 (10)
	if !reflect.DeepEqual(cols, []int{1, 2}) {
		t.Fatalf("expected cols [1 2], got %v", cols)
	}
	if !reflect.DeepEqual(widths, []int{10, 10}) {
		t.Fatalf("expected widths [10 10], got %v", widths)
	}
}

func TestVisibleColumnsAlwaysAtLeastOne(t *testing.T) {
	nat := []int{50}
	cols, widths := visibleColumns(nat, 0, 10)
	if len(cols) != 1 || widths[0] != 10 {
		t.Fatalf("oversized single column should be clipped to viewport: %v %v", cols, widths)
	}
}

func TestScrollColIntoView(t *testing.T) {
	nat := []int{10, 10, 10, 10, 10}
	// cursor left of window snaps window to cursor
	if off := scrollColIntoView(nat, 3, 1, 25); off != 1 {
		t.Fatalf("expected off 1, got %d", off)
	}
	// cursor right of window advances until it fits: cols 3,4 = 10+1+10=21 <= 25
	if off := scrollColIntoView(nat, 0, 4, 25); off != 3 {
		t.Fatalf("expected off 3, got %d", off)
	}
	// cursor wider than viewport still lands on the cursor column
	if off := scrollColIntoView(nat, 0, 2, 5); off != 2 {
		t.Fatalf("expected off 2, got %d", off)
	}
}

func TestScrollIntoViewOnRender(t *testing.T) {
	f := testFrame(100)
	tv := &TableView{}
	th := theme.Default()
	o := RenderOpts{Width: 60, Height: 11, Theme: th} // 10 body rows

	tv.Render(f, o)
	if tv.rowOff != 0 {
		t.Fatalf("initial rowOff = %d, want 0", tv.rowOff)
	}

	tv.JumpTo(50, f.NumRows())
	tv.Render(f, o)
	if tv.sel != 50 {
		t.Fatalf("sel = %d, want 50", tv.sel)
	}
	if tv.sel < tv.rowOff || tv.sel >= tv.rowOff+10 {
		t.Fatalf("selection %d not within window [%d, %d)", tv.sel, tv.rowOff, tv.rowOff+10)
	}

	tv.JumpTo(0, f.NumRows())
	tv.Render(f, o)
	if tv.rowOff != 0 {
		t.Fatalf("rowOff = %d after jump to top, want 0", tv.rowOff)
	}
}

func TestPageSizeReflectsBorders(t *testing.T) {
	f := testFrame(100)
	th := theme.Default()

	tv := &TableView{}
	tv.Render(f, RenderOpts{Width: 60, Height: 20, Theme: th})
	if tv.PageSize() != 19 {
		t.Fatalf("borderless page size = %d, want 19", tv.PageSize())
	}
	tv.Render(f, RenderOpts{Width: 60, Height: 20, Theme: th, ShowBorders: true})
	if tv.PageSize() != 16 {
		t.Fatalf("bordered page size = %d, want 16", tv.PageSize())
	}
}

func TestClampToOnFrameSwitch(t *testing.T) {
	big := testFrame(100)
	small := testFrame(3)
	tv := &TableView{}
	tv.JumpTo(80, big.NumRows())
	tv.ClampTo(small)
	if tv.sel != 2 {
		t.Fatalf("sel = %d after clamping to 3-row frame, want 2", tv.sel)
	}
	tv.ClampTo(nil)
	if tv.sel != 0 || tv.rowOff != 0 {
		t.Fatalf("clamping to nil should reset, got sel=%d off=%d", tv.sel, tv.rowOff)
	}
}

func TestRenderLineCountAndWidth(t *testing.T) {
	f := testFrame(5)
	th := theme.Default()
	out := (&TableView{}).Render(f, RenderOpts{Width: 40, Height: 12, Theme: th, ShowRowNumbers: true})
	lines := strings.Split(out, "\n")
	if len(lines) != 12 {
		t.Fatalf("expected 12 lines, got %d", len(lines))
	}

	out = (&TableView{}).Render(f, RenderOpts{Width: 40, Height: 12, Theme: th, ShowBorders: true})
	lines = strings.Split(out, "\n")
	if len(lines) != 12 {
		t.Fatalf("expected 12 lines with borders, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[11], "╯") {
		t.Fatalf("border glyphs missing:\n%s", out)
	}
}

func TestAutoExpandThresholds(t *testing.T) {
	cases := []struct {
		name        string
		nat, fitted []int
		want        bool
	}{
		{"everything fits", []int{10, 10}, []int{10, 10}, false},
		{"exactly 60 percent stays compact", []int{20}, []int{12}, false},
		{"below 60 percent expands", []int{20}, []int{11}, true},
		{"wide column squeezed under 5 cells", []int{9, 3}, []int{4, 3}, true},
		{"narrow column may go under 5 cells", []int{8, 3}, []int{5, 3}, false},
		{"one bad column among good ones", []int{10, 40, 10}, []int{10, 20, 10}, true},
		{"empty", nil, nil, false},
	}
	for _, c := range cases {
		if got := autoExpand(c.nat, c.fitted); got != c.want {
			t.Errorf("%s: autoExpand(%v, %v) = %v, want %v", c.name, c.nat, c.fitted, got, c.want)
		}
	}
}

func TestRenderAutoPicksExpanded(t *testing.T) {
	tv := &TableView{}
	tv.Render(wideFrame(5), RenderOpts{Width: 60, Height: 10, Theme: theme.Default()})
	if !tv.Expanded() {
		t.Fatal("wide frame in a narrow viewport should auto-pick expanded mode")
	}
}

func TestRenderAutoPicksCompact(t *testing.T) {
	tv := &TableView{}
	tv.Render(testFrame(5), RenderOpts{Width: 60, Height: 10, Theme: theme.Default()})
	if tv.Expanded() {
		t.Fatal("frame that fits comfortably should auto-pick compact mode")
	}
}

func TestManualColumnModeSticks(t *testing.T) {
	th := theme.Default()
	o := RenderOpts{Width: 60, Height: 10, Theme: th}

	// Manual toggle before the first render wins over the auto pick.
	tv := &TableView{}
	tv.ToggleExpanded()
	tv.Render(testFrame(5), o) // auto would pick compact
	if !tv.Expanded() {
		t.Fatal("manual expanded choice must not be overridden by the auto pick")
	}

	// Manual toggle after the auto pick sticks across renders.
	tv2 := &TableView{}
	tv2.Render(wideFrame(5), o)
	tv2.ToggleExpanded() // back to compact by hand
	tv2.Render(wideFrame(5), o)
	if tv2.Expanded() {
		t.Fatal("manual compact choice must stick for the current frame")
	}

	// Reset re-arms the auto pick (a new frame was pushed).
	tv2.Reset()
	tv2.Render(wideFrame(5), o)
	if !tv2.Expanded() {
		t.Fatal("auto pick should run again after Reset")
	}
}

func TestHeaderEmphasizesCursoredColumnBothModes(t *testing.T) {
	f := testFrame(5)
	th := theme.Default()

	for _, expanded := range []bool{false, true} {
		tv := &TableView{}
		if expanded {
			tv.ToggleExpanded()
		} else {
			tv.modeSet = true // pin fit mode
		}
		// Header emphasis is driven by RenderOpts.HeaderCur, which the app
		// feeds from ColCursor on every render.
		o := RenderOpts{Width: 60, Height: 10, Theme: th}
		o.HeaderCur = tv.ColCursor()
		before := tv.Render(f, o)
		tv.NextCol(f.NumCols())
		o.HeaderCur = tv.ColCursor()
		after := tv.Render(f, o)
		if before == after {
			t.Errorf("expanded=%v: moving the column cursor must change the header emphasis", expanded)
		}
	}
}

func TestPadCellSanitizesCarriageReturns(t *testing.T) {
	got := padCell("line1\r\nline2\rx", 20)
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("padCell leaked a raw control char: %q", got)
	}
	if !strings.Contains(got, "line1␤line2␤x") {
		t.Fatalf("padCell = %q, want CR/LF shown as ␤", got)
	}
}

func TestToggleSelectAndIsSelected(t *testing.T) {
	tv := &TableView{}
	if tv.IsSelected(0) {
		t.Fatal("fresh view reports a selected row")
	}
	tv.ToggleSelect(3)
	if !tv.IsSelected(3) {
		t.Fatal("ToggleSelect(3) did not mark row 3 selected")
	}
	// Toggling again removes the row (dedup).
	tv.ToggleSelect(3)
	if tv.IsSelected(3) {
		t.Fatal("second ToggleSelect(3) did not clear the row")
	}
	// Multiple distinct rows coexist.
	tv.ToggleSelect(1)
	tv.ToggleSelect(5)
	if !tv.IsSelected(1) || !tv.IsSelected(5) {
		t.Fatal("independent rows not tracked after re-toggle")
	}
}

func TestSelectedRowsSorted(t *testing.T) {
	tv := &TableView{}
	tv.ToggleSelect(7)
	tv.ToggleSelect(2)
	tv.ToggleSelect(5)
	want := []int{2, 5, 7}
	if got := tv.SelectedRows(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectedRows = %v, want %v", got, want)
	}
	// Returned slice is a copy: mutating it must not affect the view.
	got := tv.SelectedRows()
	got[0] = 999
	if tv.IsSelected(999) {
		t.Fatal("SelectedRows returned an alias, not a copy")
	}
}

func TestSelectedRowsEmptyWhenNone(t *testing.T) {
	tv := &TableView{}
	if got := tv.SelectedRows(); got != nil {
		t.Fatalf("SelectedRows on untouched view = %v, want nil", got)
	}
	// After clearing, SelectedRows is nil again.
	tv.ToggleSelect(0)
	tv.ClearSelect()
	if got := tv.SelectedRows(); got != nil {
		t.Fatalf("SelectedRows after ClearSelect = %v, want nil", got)
	}
}

func TestClearSelectEmptiesSet(t *testing.T) {
	tv := &TableView{}
	tv.ToggleSelect(1)
	tv.ToggleSelect(2)
	tv.ClearSelect()
	if tv.IsSelected(1) || tv.IsSelected(2) {
		t.Fatal("ClearSelect did not empty the set")
	}
	if got := tv.SelectedRows(); len(got) != 0 {
		t.Fatalf("SelectedRows after ClearSelect = %v, want empty", got)
	}
}

func TestResetClearsSelect(t *testing.T) {
	tv := &TableView{}
	tv.ToggleSelect(1)
	tv.Reset()
	if tv.IsSelected(1) {
		t.Fatal("Reset did not clear the multi-select set")
	}
}

func TestClampToClearsSelect(t *testing.T) {
	tv := &TableView{}
	tv.ToggleSelect(1)
	tv.ClampTo(testFrame(3))
	if tv.IsSelected(1) {
		t.Fatal("ClampTo did not clear the multi-select set")
	}
	// ClampTo(nil) routes through Reset, which also clears.
	tv.ToggleSelect(0)
	tv.ClampTo(nil)
	if tv.IsSelected(0) {
		t.Fatal("ClampTo(nil) did not clear the multi-select set")
	}
}

func TestRenderSelectedRowShowsMarkerInGutter(t *testing.T) {
	f := testFrame(5)
	th := theme.Default()
	o := RenderOpts{Width: 40, Height: 12, Theme: th, ShowRowNumbers: true}

	tv := &TableView{}
	out := tv.Render(f, o)
	if strings.Contains(out, selectMarker) {
		t.Fatalf("no rows selected, but marker present:\n%s", out)
	}

	tv.ToggleSelect(0)
	out = tv.Render(f, o)
	lines := strings.Split(out, "\n")
	// Row 0 is the first body row, which sits right under the header (and
	// below a border line when borders are on).
	body0 := lines[1]
	if !strings.ContainsRune(body0, []rune(selectMarker)[0]) {
		t.Fatalf("selected row 0 missing marker in:\n%s", out)
	}
	// Row 1 is not selected: its body line must not carry the marker.
	body1 := lines[2]
	if strings.ContainsRune(body1, []rune(selectMarker)[0]) {
		t.Fatalf("non-selected row 1 unexpectedly carries marker in:\n%s", out)
	}
}

func TestRenderSelectedRowShowsTagWithoutGutter(t *testing.T) {
	f := testFrame(5)
	th := theme.Default()
	o := RenderOpts{Width: 40, Height: 12, Theme: th} // no gutter

	tv := &TableView{}
	tv.ToggleSelect(0)
	out := tv.Render(f, o)
	if !strings.Contains(out, selectMarker) {
		t.Fatalf("selected row missing marker tag without gutter:\n%s", out)
	}
	// Width of each body line must still equal the inner width so column
	// alignment is preserved.
	for i, ln := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(ln); w != o.Width {
			t.Fatalf("line %d width = %d, want %d (alignment broken):\n%s", i, w, o.Width, out)
		}
	}
}

func TestRenderCursorKeepsMarkerWhenSelected(t *testing.T) {
	f := testFrame(5)
	th := theme.Default()
	o := RenderOpts{Width: 40, Height: 12, Theme: th, ShowRowNumbers: true}

	tv := &TableView{}
	tv.JumpTo(2, f.NumRows())
	tv.ToggleSelect(2)
	out := tv.Render(f, o)

	// The cursor row (row 2, i.e. "3" in the gutter) must carry both the
	// marker and remain on screen.
	body2 := strings.Split(out, "\n")[3]
	if !strings.ContainsRune(body2, []rune(selectMarker)[0]) {
		t.Fatalf("cursor+selected row 2 missing marker:\n%s", out)
	}
}

// --- column resize (P1 grid enhancement) -----------------------------------

func TestResizeColClamps(t *testing.T) {
	tv := &TableView{}
	tv.ResizeCol(2, 50)
	if tv.colWidths[2] != 50 {
		t.Fatalf("expected 50, got %d", tv.colWidths[2])
	}
	// shrink below the floor: clamps to 1, keeps the override
	tv.ResizeCol(2, -200)
	if tv.colWidths[2] != 1 {
		t.Fatalf("expected clamp to 1, got %d", tv.colWidths[2])
	}
	// grow above the ceiling: clamps to colWidthMax
	tv.ResizeCol(2, 1000)
	if tv.colWidths[2] != colWidthMax {
		t.Fatalf("expected clamp to %d, got %d", colWidthMax, tv.colWidths[2])
	}
	tv.ResetColWidths()
	if tv.colWidths != nil {
		t.Fatalf("expected nil map after reset, got %v", tv.colWidths)
	}
}

func TestApplyWidthOverrides(t *testing.T) {
	tv := &TableView{}
	tv.colWidths = map[int]int{1: 42, 9: 99} // 9 is out of range
	nat := []int{10, 10, 10}
	tv.applyWidthOverrides(nat)
	if nat[1] != 42 {
		t.Fatalf("override not applied: %v", nat)
	}
	if nat[0] != 10 || nat[2] != 10 {
		t.Fatalf("unrelated columns changed: %v", nat)
	}
	if nat[0] != 10 {
		t.Fatalf("out-of-range override leaked: %v", nat)
	}
}

// leadingRunes counts the run of consecutive r runes at the start of the
// first body data line (line index 1; line 0 is the header). ANSI styling is
// stripped first so it does not break the count. Used to read a column's
// rendered width from a NoEllipsis render.
func leadingRunes(s string, r rune) int {
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return 0
	}
	plain := ansi.Strip(lines[1])
	n := 0
	for _, c := range plain {
		if c == r {
			n++
		} else {
			break
		}
	}
	return n
}

func TestColResizeRenderHonorsWidth(t *testing.T) {
	f := wideFrame(1)
	th := theme.Default()
	o := RenderOpts{Width: 200, Height: 10, Theme: th, ShowBorders: false, NoEllipsis: true}
	tv := &TableView{}
	tv.modeSet = true
	tv.expanded = true // force wide mode so nat is used directly

	// No override: column 0's natural width equals its 30-char cell content.
	base := tv.Render(f, o)
	if got := leadingRunes(base, 'a'); got != 30 {
		t.Fatalf("expected natural column 0 width 30, got %d", got)
	}

	// Override column 0 to 15: the leading 'a' run must shrink to 15.
	tv.ResizeCol(0, 15)
	wide := tv.Render(f, o)
	if got := leadingRunes(wide, 'a'); got != 15 {
		t.Fatalf("expected overridden column 0 width 15, got %d", got)
	}

	// Reset restores the auto-fit width.
	tv.ResetColWidths()
	if got := leadingRunes(tv.Render(f, o), 'a'); got != 30 {
		t.Fatalf("expected reset to restore width 30, got %d", got)
	}
}

func TestColResizeSurvivesFrameRefresh(t *testing.T) {
	// ClampTo is called on every refresh (same columns); overrides must
	// survive it so a resized column stays wide after reloading data.
	tv := &TableView{}
	tv.colWidths = map[int]int{0: 15}
	f := wideFrame(3)
	tv.ClampTo(f)
	if tv.colWidths[0] != 15 {
		t.Fatalf("override lost across frame clamp: %v", tv.colWidths)
	}
}

// --- column sort (P1 grid enhancement) -------------------------------------

// sortTestFrame builds a frame whose first column is a key out of order and
// whose second column is a label aligned with the key, so sort order is easy
// to read from either column.
func sortTestFrame() *tuidata.Frame {
	f := tuidata.New("n", "label")
	f.AppendRow([]any{"3", "c"})
	f.AppendRow([]any{"1", "a"})
	f.AppendRow([]any{"2", "b"})
	return f
}

func TestSortByAscendingOrder(t *testing.T) {
	f := sortTestFrame()
	tv := &TableView{}
	tv.SortBy(f, 0)
	if !tv.sorted || tv.sortCol != 0 || tv.sortDir != 1 {
		t.Fatalf("sort state = sorted=%v col=%d dir=%d, want true/0/1", tv.sorted, tv.sortCol, tv.sortDir)
	}
	want := []string{"1", "2", "3"}
	for d, w := range want {
		if got := f.CellString(tv.SrcRow(d), 0); got != w {
			t.Fatalf("display row %d: SrcRow=%d, cell=%q, want %q", d, tv.SrcRow(d), got, w)
		}
	}
}

func TestSortByTogglesDirection(t *testing.T) {
	f := sortTestFrame()
	tv := &TableView{}
	tv.SortBy(f, 0) // ascending
	tv.SortBy(f, 0) // toggle -> descending
	if tv.sortDir != -1 {
		t.Fatalf("expected descending dir -1, got %d", tv.sortDir)
	}
	want := []string{"3", "2", "1"}
	for d, w := range want {
		if got := f.CellString(tv.SrcRow(d), 0); got != w {
			t.Fatalf("display row %d: cell=%q, want %q", d, got, w)
		}
	}
	// Sorting a different column resets direction to ascending.
	tv.SortBy(f, 1)
	if tv.sortCol != 1 || tv.sortDir != 1 {
		t.Fatalf("switching column should reset dir to ascending, got col=%d dir=%d", tv.sortCol, tv.sortDir)
	}
	if got := f.CellString(tv.SrcRow(0), 1); got != "a" {
		t.Fatalf("first label after sorting column 1 should be a, got %q", got)
	}
}

func TestSortByNumericVsText(t *testing.T) {
	// Numeric columns compare by value, not lexically ("10" < "2").
	if cellLess("10", "2", 1) {
		t.Fatal("numeric: 10 should NOT sort before 2")
	}
	if !cellLess("2", "10", 1) {
		t.Fatal("numeric: 2 should sort before 10")
	}
	// Text falls back to case-insensitive comparison.
	if !cellLess("abc", "abd", 1) {
		t.Fatal("text: abc should sort before abd")
	}
	if cellLess("ABD", "abc", 1) {
		t.Fatal("text: comparison should be case-insensitive")
	}
}

func TestSortClearedByReset(t *testing.T) {
	f := sortTestFrame()
	tv := &TableView{}
	tv.SortBy(f, 0)
	tv.Reset()
	if tv.sorted || tv.order != nil {
		t.Fatalf("Reset must drop the sort: sorted=%v order=%v", tv.sorted, tv.order)
	}
	// SrcRow is identity once the sort is gone.
	if tv.SrcRow(1) != 1 {
		t.Fatalf("SrcRow should be identity after reset, got %d", tv.SrcRow(1))
	}
}

func TestSortSurvivesClampToSameFrame(t *testing.T) {
	f := sortTestFrame()
	tv := &TableView{}
	tv.SortBy(f, 0)
	// ClampTo on a refresh uses the same frame shape -> order recomputed, kept.
	tv.ClampTo(f)
	if !tv.sorted {
		t.Fatal("sort should survive ClampTo with the same frame")
	}
	if got := f.CellString(tv.SrcRow(0), 0); got != "1" {
		t.Fatalf("sort order not preserved after ClampTo: first cell %q", got)
	}
}

func TestSortRenderShowsArrowAndOrderedBody(t *testing.T) {
	f := sortTestFrame()
	th := theme.Default()
	o := RenderOpts{Width: 40, Height: 10, Theme: th, NoEllipsis: true}
	tv := &TableView{}
	tv.SortBy(f, 0)
	out := tv.Render(f, o)
	if !strings.Contains(out, sortArrowAsc) {
		t.Fatalf("sorted header should show the ascending arrow:\n%s", out)
	}
	// The first body line (line 1) must lead with the smallest key "1".
	lines := strings.Split(out, "\n")
	body := ansi.Strip(lines[1])
	if !strings.HasPrefix(strings.TrimSpace(body), "1") {
		t.Fatalf("first body row should be the min key 1, got %q", body)
	}
}
