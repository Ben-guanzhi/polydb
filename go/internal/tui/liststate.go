package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Pad truncates s to at most w display cells (marking a truncation with an
// ellipsis) and pads it with spaces to exactly w cells, so concatenated rows
// stay aligned on multi-byte text.
func Pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// ListState tracks the cursor and scroll window of a flat list browser.
// Callers own the row slice; the state only stores indices.
//
// Invariants, restored by the methods before they return:
//   - Cursor indexes a row the caller considers selectable, or is -1 when
//     the list has no selectable row at all
//   - after ScrollToCursor: 0 <= Offset and the window
//     [Offset, Offset+ViewLen) contains Cursor whenever Cursor >= 0
type ListState struct {
	Cursor  int
	Offset  int
	ViewLen int
}

// Move advances the cursor to the next selectable row in direction dir
// (-1/+1), leaving it unchanged when none exists in that direction.
func (s *ListState) Move(n int, dir int, selectable func(i int) bool) {
	for j := s.Cursor + dir; j >= 0 && j < n; j += dir {
		if selectable(j) {
			s.Cursor = j
			return
		}
	}
}

// HomeToSelectable puts the cursor on the first selectable row (or -1 when
// there is none) and rewinds the window; used after the row list was
// rebuilt from scratch, e.g. when a filter changed.
func (s *ListState) HomeToSelectable(n int, selectable func(i int) bool) {
	s.Cursor = -1
	s.Move(n, +1, selectable)
	s.Offset = 0
}

// ClampCursor keeps the cursor on a selectable row after the row list
// changed shape (async result arrived, group toggled). Preference order:
// keep the row when still selectable, else the nearest selectable row below
// the old position, then above, then the first selectable row. With an
// empty list the cursor parks at -1.
func (s *ListState) ClampCursor(n int, selectable func(i int) bool) {
	if n <= 0 {
		s.Cursor = -1
		return
	}
	if s.Cursor >= n {
		s.Cursor = n - 1
	}
	if s.Cursor >= 0 && selectable(s.Cursor) {
		return
	}
	for j := s.Cursor + 1; j < n; j++ {
		if selectable(j) {
			s.Cursor = j
			return
		}
	}
	for j := s.Cursor - 1; j >= 0; j-- {
		if selectable(j) {
			s.Cursor = j
			return
		}
	}
	s.Cursor = -1
	s.Move(n, +1, selectable)
}

// ScrollToCursor nudges the window just enough to include the cursor, then
// clamps the window against the list end. A cursor of -1 (no selectable
// row) leaves the window untouched.
func (s *ListState) ScrollToCursor(n, viewH int) {
	if s.Cursor < 0 {
		return
	}
	if viewH < 1 {
		viewH = 1
	}
	if s.Cursor < s.Offset {
		s.Offset = s.Cursor
	}
	if s.Cursor >= s.Offset+viewH {
		s.Offset = s.Cursor - viewH + 1
	}
	if maxOff := n - viewH; s.Offset > maxOff {
		s.Offset = maxOff
	}
	if s.Offset < 0 {
		s.Offset = 0
	}
}

// NextSelectable scans rows from index i in direction dir (+1/-1) and
// returns the index of the first row satisfying selectable. When none
// exists it returns -1 for a fresh scan (i < 0), else i itself — callers
// can assign the result back to a cursor without a second bounds check.
func NextSelectable[T any](rows []T, i, dir int, selectable func(T) bool) int {
	for j := i + dir; j >= 0 && j < len(rows); j += dir {
		if selectable(rows[j]) {
			return j
		}
	}
	if i < 0 {
		return -1
	}
	return i
}

// MoveListCursor is ListState.Move over a typed row slice.
func MoveListCursor[T any](s *ListState, rows []T, dir int, selectable func(T) bool) {
	s.Move(len(rows), dir, func(i int) bool { return selectable(rows[i]) })
}

// ClampListCursor is ListState.ClampCursor over a typed row slice.
func ClampListCursor[T any](s *ListState, rows []T, selectable func(T) bool) {
	s.ClampCursor(len(rows), func(i int) bool { return selectable(rows[i]) })
}

// LazyGroup is the state of one lazily-loaded group in a browser listing
// (schema namespaces, redis key types, dashboard folders): a collapsible
// section whose items are fetched asynchronously on first expansion.
//
// Lifecycle: open -> loading -> loaded, with err set instead of items when
// the load failed. Deliver is the single write path for load results;
// Unload/Reset rewind the lifecycle without changing open.
type LazyGroup[T any] struct {
	Items   []T
	Open    bool
	Loading bool
	Loaded  bool
	Err     error
}

// EnsureLoaded starts the asynchronous load when the group has no results
// yet and none is in flight. load builds the command delivering the result
// message; it is only called when a load is actually started, so the
// closure may capture state freely. Returns nil when nothing is started.
func (g *LazyGroup[T]) EnsureLoaded(load func() tea.Cmd) tea.Cmd {
	if g.Loaded || g.Loading {
		return nil
	}
	g.Loading = true
	return load()
}

// Reload forces a fresh load, dropping any cached items first. A load
// already in flight is never doubled.
func (g *LazyGroup[T]) Reload(load func() tea.Cmd) tea.Cmd {
	if g.Loading {
		return nil
	}
	g.Loading = true
	g.Loaded = false
	g.Err = nil
	g.Items = nil
	return load()
}

// Deliver applies one load result: items on success, err otherwise. A
// failed delivery keeps the previous items so the browser can render the
// last good listing under the error note.
func (g *LazyGroup[T]) Deliver(items []T, err error) {
	g.Loading = false
	g.Loaded = true
	if err != nil {
		g.Err = err
		return
	}
	g.Err = nil
	g.Items = items
}

// Unload drops the cached results without touching an in-flight load: the
// group loads again on its next open.
func (g *LazyGroup[T]) Unload() {
	g.Loaded = false
	g.Err = nil
	g.Items = nil
}

// Reset fully rewinds the lifecycle, an in-flight load included (its result
// must be dropped by the receiver via its owner stamp).
func (g *LazyGroup[T]) Reset() {
	g.Loading = false
	g.Loaded = false
	g.Err = nil
	g.Items = nil
}

// Owned stamps an asynchronous result message with the overlay instance
// that started the fetch. Receivers drop results whose owner is not
// themselves, so a closed or replaced overlay never consumes a stale
// result. T is the owning overlay type: type fooMsg struct {
// ui.Owned[fooOverlay]; ... }.
type Owned[T any] struct {
	Owner *T
}

// Stale reports whether the result must be ignored by cur. A zero-value
// Owner (message built without a stamp) is always stale.
func (o Owned[T]) Stale(cur *T) bool { return o.Owner != cur }
