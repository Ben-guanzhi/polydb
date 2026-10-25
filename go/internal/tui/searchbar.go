package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tuidata"
)

// SearchBar implements live search over the current frame. While active it
// keeps a preview frame (the filtered rows) that the app displays instead of
// the pane's top frame; the stack is only touched on commit.
type SearchBar struct {
	active bool
	fuzzy  bool
	query  string

	base       *tuidata.Frame // frame being searched (top of stack at activation)
	preview    *tuidata.Frame // live filtered result
	searchCols []int          // when non-nil, search only these column indices

	// fold 缓存 base 每格的小写形，跨按键复用；随会话起止生灭。
	fold tuidata.FoldCache
	// cell 缓存 base 每格的显示原文（不折叠），供全列精确搜索的按键路径
	// 复用：SearchExact 本身大小写敏感，不能借用 ToLower 的 fold。
	cell tuidata.CellCache
	// fuzzyCache 缓存 base 按行拼出的整行显示串，同样跨按键复用。
	fuzzyCache tuidata.FuzzyCache
}

func (s *SearchBar) Active() bool  { return s.active }
func (s *SearchBar) Fuzzy() bool   { return s.fuzzy }
func (s *SearchBar) Query() string { return s.query }

// Start activates the bar over frame f.
func (s *SearchBar) Start(f *tuidata.Frame, fuzzy bool) {
	s.StartCols(f, fuzzy, nil)
}

// StartCols activates the bar over frame f, restricting the search to the
// given column indices (nil = search all columns).
func (s *SearchBar) StartCols(f *tuidata.Frame, fuzzy bool, cols []int) {
	s.active = true
	s.fuzzy = fuzzy
	s.query = ""
	s.base = f
	s.preview = f
	s.searchCols = cols
	s.fold = tuidata.FoldCache{}
	s.cell = tuidata.CellCache{}
	s.fuzzyCache = tuidata.FuzzyCache{}
}

// Type appends printable input and recomputes the preview.
func (s *SearchBar) Type(text string) {
	if !s.active || text == "" {
		return
	}
	s.query += text
	s.recompute()
}

// Paste appends pasted text as a single edit (bracketed paste delivers the
// whole chunk in one message, not per-rune key presses), flattened to one
// line first.
func (s *SearchBar) Paste(text string) {
	s.Type(flattenPaste(text))
}

// flattenPaste makes pasted text safe for the single-line search input:
// newlines and tabs become single spaces, carriage returns and other control
// characters are dropped.
func flattenPaste(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < ' ' || r == 0x7f:
			// drop \r and other control characters
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Backspace removes the last rune and recomputes the preview.
func (s *SearchBar) Backspace() {
	if !s.active || s.query == "" {
		return
	}
	r := []rune(s.query)
	s.query = string(r[:len(r)-1])
	s.recompute()
}

func (s *SearchBar) recompute() {
	if s.base == nil {
		return
	}
	if s.query == "" {
		s.preview = s.base
		return
	}
	var rows []int
	if len(s.searchCols) > 0 {
		rows = tuidata.SearchExactColsFold(s.base, s.searchCols, s.query, &s.fold)
	} else if s.fuzzy {
		rows = tuidata.SearchFuzzyFold(s.base, s.query, &s.fuzzyCache)
	} else {
		// The plain all-column path is case-sensitive, so it reuses the
		// un-folded CellCache instead of the ToLower-ed fold cache.
		rows = tuidata.SearchExactCached(s.base, s.query, &s.cell)
	}
	s.preview = s.base.Select(rows)
}

// Preview returns the frame to display while the bar is active (never nil
// while active with a base frame).
func (s *SearchBar) Preview() *tuidata.Frame {
	if s.preview != nil {
		return s.preview
	}
	return s.base
}

// Commit deactivates the bar and returns the filtered frame plus a crumb
// label. ok is false when there is nothing to commit (empty query).
func (s *SearchBar) Commit() (f *tuidata.Frame, crumb string, ok bool) {
	f, q, fuzzy := s.preview, s.query, s.fuzzy
	s.Cancel()
	if q == "" || f == nil {
		return nil, "", false
	}
	if fuzzy {
		return f, "fuzzy", true
	}
	return f, "search", true
}

// Cancel deactivates the bar and drops the preview.
func (s *SearchBar) Cancel() {
	s.active = false
	s.query = ""
	s.base = nil
	s.preview = nil
	s.fold = tuidata.FoldCache{}
	s.cell = tuidata.CellCache{}
	s.fuzzyCache = tuidata.FuzzyCache{}
}

// View renders the input line shown at the top of the screen while searching.
func (s *SearchBar) View(width int, th *theme.Theme) string {
	rowInfo := ""
	if s.base != nil {
		n := 0
		if s.preview != nil {
			n = s.preview.NumRows()
		}
		rowInfo = th.Subtle.Render(strconv.Itoa(n) + " rows ")
	}
	rowW := ansi.StringWidth(rowInfo)
	room := width - rowW
	if room < 0 {
		room = 0
	}
	// 过滤行只占扣掉行数后的余量：整行要恰好等于 width，不能多也不能少。
	// 旧的写法先补白到 width-rowW 再额外塞一格 pad，且 strings.TrimRight 对
	// 带样式的补白是空操作（行尾是 SGR 复位序列而非空格），pad 恒被夹到 1，
	// 整行永远宽一格，终端硬回绕。
	filterPart := FilterLine(s.query, s.active, room, "type to search", th)
	if rowInfo != "" {
		// 查询过长被截断（含双宽字符回退）时过滤行会短于 room，中间的差额
		// 补成空格把行数推到行尾。
		gap := width - ansi.StringWidth(filterPart) - rowW
		if gap < 0 {
			gap = 0
		}
		filterPart += th.Text.Render(strings.Repeat(" ", gap)) + rowInfo
	}
	return filterPart
}
