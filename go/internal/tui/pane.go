package tui

import (
	"sync/atomic"

	"github.com/polydb/polydb/internal/tuidata"
)

// ViewMode selects what the pane body shows.
type ViewMode int

const (
	ModeTable ViewMode = iota
	ModeSheet
	ModeJsonView
)

// entry is one level of a pane's frame stack.
type entry struct {
	frame *tuidata.Frame
	crumb string
}

// Pane is one tab: a stack of frames (base + one per applied operation),
// a table view state and a sheet scroll offset.
type Pane struct {
	Title string
	id    int
	stack []entry

	// sourceNS/sourceTable record the live table the base frame was fetched
	// from (set only by the table-open path). They let the inline prompts
	// name the real table instead of guessing from the tab title.
	sourceNS    string
	sourceTable string

	Table      TableView
	Mode       ViewMode
	SheetOff   int // left-list scroll offset (key column)
	SheetField int // cursored field index in sheet mode
	// SheetValOff is the right-pane value scroll offset within the cursored
	// field. It is reset to 0 each time the cursor moves to a new field.
	SheetValOff int
	// SheetEditing is true while the sheet view is inline-editing the
	// cursored field. The "e" key toggles it on; saveedit/canceledit clear it.
	SheetEditing bool
	// SheetEdit holds the text being typed during an inline sheet edit.
	SheetEdit LineInput
	// SheetFilter holds the current field-filter pattern typed after "/".
	// SheetField is always interpreted as an index INTO the matched set
	// (sheetMatchedCols), not a raw column index; with an empty filter the
	// matched set is every column so the two coincide.
	SheetFilter    LineInput
	SheetFiltering bool
	// JsonViewOff is the scroll offset when viewing a row as JSON.
	JsonViewOff int

	ColZoomed      bool  // true when user zoomed into a single column via z key
	savedTableCols []int // TableCols saved before zoom, restored on exit
	savedColCursor int   // colCursor saved before zoom, restored on exit
	// TableCols, when non-nil, restricts which columns the table view renders.
	// Sheet/row-detail mode always shows all columns regardless of this field.
	TableCols []int
	// HScroll is the character-level horizontal offset applied to the last
	// TableCols column so the user can pan within wide content (e.g. body).
	HScroll int
}

// paneIDCounter hands out stable unique pane IDs (see Pane.ID).
var paneIDCounter atomic.Int64

// NewPane creates a pane with a base frame.
func NewPane(title string, f *tuidata.Frame) *Pane {
	return &Pane{Title: title, id: int(paneIDCounter.Add(1)), stack: []entry{{frame: f}}}
}

// ID returns the pane's stable unique identity (positive; 0 is never used, so
// it can act as an "unset" marker in messages).
func (p *Pane) ID() int { return p.id }

// Current returns the frame on top of the stack (nil for an empty pane).
func (p *Pane) Current() *tuidata.Frame {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1].frame
}

// Depth reports the stack depth.
func (p *Pane) Depth() int { return len(p.stack) }

// Push adds a derived frame with a crumb label and resets the view onto it.
func (p *Pane) Push(f *tuidata.Frame, crumb string) {
	p.stack = append(p.stack, entry{frame: f, crumb: crumb})
	p.Table.Reset()
	p.resetSheet()
}

// clearTail nils every slot of s so the frames parked there stop being GC
// roots. Truncating the stack's length is not enough: the runtime walks a
// slice's backing array by its allocation size, not its len, so slots past
// len keep their pointers and stay roots — a Reset would pin every dropped
// derived frame (all its cell strings) until a later Push happened to
// overwrite them in place. Measured on a throwaway program: truncate-only left
// 3 of 3 test objects uncollected, nil-then-truncate collected all 3. Safe to
// clobber: nothing reads past len(p.stack) — Current and Crumbs are both
// bounded by len — and the next Push appends right here. Capacity is kept so a
// shallow stack never reallocates.
func clearTail(s []entry) {
	for i := range s {
		s[i] = entry{}
	}
}

// Pop removes the top frame. It reports false when the stack is already at
// its base (the caller then closes the tab instead).
func (p *Pane) Pop() bool {
	if len(p.stack) <= 1 {
		return false
	}
	clearTail(p.stack[len(p.stack)-1:])
	p.stack = p.stack[:len(p.stack)-1]
	p.Table.ClampTo(p.Current())
	p.resetSheet()
	return true
}

// Reset drops every derived frame, back to the base.
func (p *Pane) Reset() {
	if len(p.stack) > 1 {
		clearTail(p.stack[1:])
		p.stack = p.stack[:1]
	}
	p.Table.ClampTo(p.Current())
	p.resetSheet()
}

// ReplaceBase swaps the pane's base frame (stack[0]) and re-clamps the
// view onto it, used by the refresh command to reload the current table.
func (p *Pane) ReplaceBase(f *tuidata.Frame) {
	if len(p.stack) > 0 {
		p.stack[0].frame = f
	}
	p.Table.ClampTo(f)
	p.resetSheet()
}

// resetSheet clears every sheet-mode cursor/scroll/edit field and returns the
// pane to table mode. Called on Push/Pop/Reset/ReplaceBase so a stale sheet
// state never survives a frame change.
func (p *Pane) resetSheet() {
	p.Mode = ModeTable
	p.SheetOff = 0
	p.SheetField = 0
	p.SheetValOff = 0
	p.SheetEditing = false
	p.SheetEdit = LineInput{}
	p.SheetFilter = LineInput{}
	p.SheetFiltering = false
	p.JsonViewOff = 0
	p.ColZoomed = false
}

// Crumbs returns the breadcrumb chain: tab title, then one label per
// derived frame.
func (p *Pane) Crumbs() []string {
	out := []string{p.Title}
	for _, e := range p.stack[1:] {
		out = append(out, e.crumb)
	}
	return out
}
