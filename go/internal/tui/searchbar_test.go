package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tuidata"
)

func searchFrame() *tuidata.Frame {
	f := tuidata.New("name", "city")
	f.AppendRow([]any{"alice", "amsterdam"})
	f.AppendRow([]any{"bob", "berlin"})
	f.AppendRow([]any{"carol", "chicago"})
	return f
}

func TestSearchPreviewLive(t *testing.T) {
	f := searchFrame()
	var s SearchBar
	s.Start(f, false)

	if !s.Active() {
		t.Fatal("bar should be active after Start")
	}
	if s.Preview() != f {
		t.Fatal("empty query should preview the base frame")
	}

	s.Type("a")
	p := s.Preview()
	if p.NumRows() != 2 { // "alice amsterdam" and "carol chicago" contain "a"
		t.Fatalf("preview rows = %d, want 2", p.NumRows())
	}

	s.Type("li")
	if s.Query() != "ali" {
		t.Fatalf("query = %q", s.Query())
	}
	if s.Preview().NumRows() != 1 {
		t.Fatalf("preview rows = %d, want 1", s.Preview().NumRows())
	}

	s.Backspace()
	if s.Query() != "al" {
		t.Fatalf("query after backspace = %q", s.Query())
	}
	if s.Preview().NumRows() != 1 { // still only alice
		t.Fatalf("preview rows after backspace = %d, want 1", s.Preview().NumRows())
	}
}

func TestSearchCommit(t *testing.T) {
	f := searchFrame()
	var s SearchBar
	s.Start(f, false)
	s.Type("bob")

	got, crumb, ok := s.Commit()
	if !ok {
		t.Fatal("commit with a query should succeed")
	}
	if crumb != "search" {
		t.Fatalf("crumb = %q, want search", crumb)
	}
	if got.NumRows() != 1 || got.CellString(0, 0) != "bob" {
		t.Fatalf("committed frame wrong: %d rows", got.NumRows())
	}
	if s.Active() {
		t.Fatal("bar should deactivate after commit")
	}
}

func TestSearchCommitEmptyQuery(t *testing.T) {
	var s SearchBar
	s.Start(searchFrame(), true)
	if _, _, ok := s.Commit(); ok {
		t.Fatal("commit with empty query should report not-ok")
	}
	if s.Active() {
		t.Fatal("bar should deactivate")
	}
}

func TestSearchCancel(t *testing.T) {
	var s SearchBar
	s.Start(searchFrame(), true)
	s.Type("xyz")
	s.Cancel()
	if s.Active() || s.Query() != "" || s.Preview() != nil {
		t.Fatal("cancel should clear all state")
	}
}

func TestFlattenPaste(t *testing.T) {
	if got := flattenPaste("a\r\nb\tc\x01d"); got != "a b cd" {
		t.Fatalf("flattenPaste = %q, want %q", got, "a b cd")
	}
	if got := flattenPaste("plain"); got != "plain" {
		t.Fatalf("flattenPaste passthrough = %q", got)
	}
	if got := flattenPaste("\r\x7f"); got != "" {
		t.Fatalf("control-only paste = %q, want empty", got)
	}
}

func TestSearchPaste(t *testing.T) {
	f := searchFrame()
	var s SearchBar
	s.Start(f, false)

	// A multi-character paste lands as one edit and updates the preview.
	s.Paste("bob")
	if s.Query() != "bob" {
		t.Fatalf("query = %q, want bob", s.Query())
	}
	if s.Preview().NumRows() != 1 {
		t.Fatalf("preview rows = %d, want 1", s.Preview().NumRows())
	}

	// Newlines and CR are flattened; empty paste is a no-op.
	s.Cancel()
	s.Start(f, false)
	s.Paste("a\r\nli")
	if s.Query() != "a li" {
		t.Fatalf("flattened query = %q, want %q", s.Query(), "a li")
	}
	s.Paste("")
	if s.Query() != "a li" {
		t.Fatalf("empty paste changed query: %q", s.Query())
	}

	// An inactive bar ignores pastes.
	s.Cancel()
	s.Paste("x")
	if s.Query() != "" {
		t.Fatalf("inactive bar accepted paste: %q", s.Query())
	}
}

// pasteRecorder is a stub overlay that records pasted content.
type pasteRecorder struct{ got string }

func (r *pasteRecorder) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if pm, ok := msg.(tea.PasteMsg); ok {
		r.got += pm.Content
	}
	return r, nil
}

func (r *pasteRecorder) View(w, h int, th *theme.Theme) string { return "" }

func TestAppPasteRouting(t *testing.T) {
	a := newTestApp(t)

	// No overlay, no search: the paste is dropped without side effects.
	a.Update(tea.PasteMsg{Content: "zzz"})
	if a.search.Active() {
		t.Fatal("stray paste must not activate search")
	}

	// Active search bar receives pasted text, flattened.
	a.Update(key("/"))
	a.Update(tea.PasteMsg{Content: "row\n1"})
	if got := a.search.Query(); got != "row 1" {
		t.Fatalf("search query after paste = %q, want %q", got, "row 1")
	}

	// With an overlay open, the paste goes to the top overlay, not the bar.
	rec := &pasteRecorder{}
	a.Update(PushOverlayMsg{Overlay: rec})
	a.Update(tea.PasteMsg{Content: "abc"})
	if rec.got != "abc" {
		t.Fatalf("overlay paste = %q, want abc", rec.got)
	}
	if got := a.search.Query(); got != "row 1" {
		t.Fatalf("search bar stole overlay paste: %q", got)
	}
}

func TestSearchFuzzyCrumb(t *testing.T) {
	var s SearchBar
	s.Start(searchFrame(), true)
	s.Type("crl")
	got, crumb, ok := s.Commit()
	if !ok || crumb != "fuzzy" {
		t.Fatalf("crumb = %q ok=%v, want fuzzy", crumb, ok)
	}
	if got.NumRows() != 1 || got.CellString(0, 0) != "carol" {
		t.Fatalf("fuzzy result wrong: %d rows", got.NumRows())
	}
}

// colsFrame 的 name 列大小写混杂，city 列藏着 "berlin" 之外的陷阱词。
func colsFrame() *tuidata.Frame {
	f := tuidata.New("name", "city")
	f.AppendRow([]any{"Alice", "amsterdam"})
	f.AppendRow([]any{"BOB", "berlin"})
	f.AppendRow([]any{"carol", "Zürich 客户"})
	f.AppendRow([]any{"Customer_Name 42", "rome"})
	return f
}

// TestSearchColsLivePath 钉住接了缓存的那条路（searchCols 非空）：
// 逐键的预览都必须等于 tuidata.SearchExactCols 的参考结果，且只搜被选中的列。
func TestSearchColsLivePath(t *testing.T) {
	f := colsFrame()
	keys := []string{"a", "l", "i", "c", "e", "z", "z", "客", "户", "N", "A", "M", "E", " ", "ü"}
	for _, cols := range [][]int{{0}, {1}, {0, 1}} {
		var s SearchBar
		s.StartCols(f, false, cols)
		q := ""
		for i, k := range keys {
			q += k
			s.Type(k)
			want := tuidata.SearchExactCols(f, cols, q)
			got := s.Preview()
			if got.NumRows() != len(want) {
				t.Fatalf("cols=%v 第%d键 query=%q：预览 %d 行，参考 %d 行", cols, i, q, got.NumRows(), len(want))
			}
			for j, r := range want {
				if got.CellString(j, 0) != f.CellString(r, 0) {
					t.Fatalf("cols=%v query=%q 第%d行身份不对：got=%q want=%q", cols, q, j, got.CellString(j, 0), f.CellString(r, 0))
				}
			}
		}
		// 只搜 cols 之外的列时，命中集合必须随之收窄（防接线传错 cols）。
		s.Cancel()
		s.StartCols(f, false, []int{0})
		s.Type("amsterdam")
		if s.Preview().NumRows() != 0 {
			t.Fatalf("city 列的词不该在 name 列命中")
		}
	}
}

// TestSearchFoldCacheLifecycle 缓存随会话生灭：连打几键后必须还在（跨按键复用生效的
// 唯一可观测证据——builds 是包内私有的，ui 侧只能看 FoldCache 是否非零），
// Cancel/Commit 之后必须清空（大帧的小写串不能滞留）。
func TestSearchFoldCacheLifecycle(t *testing.T) {
	f := colsFrame()
	var s SearchBar
	s.StartCols(f, false, []int{0, 1})
	if !reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("刚 Start 就有缓存料")
	}
	s.Type("ali")
	s.Type("ce")
	if reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("打了三个键缓存还是空的——缓存没挂进 recompute，本轮优化没落地")
	}
	s.Cancel()
	if !reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("Cancel 后必须释放缓存")
	}

	// 换一帧重开：旧帧的缓存不能带进新会话。
	s.StartCols(f, false, []int{0, 1})
	s.Type("zzz")
	if reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("第二个会话没建自己的缓存")
	}
	if _, _, ok := s.Commit(); !ok {
		t.Fatal("commit 应成功")
	}
	if !reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("Commit 后必须释放缓存")
	}

	// 全列搜索（searchCols 为 nil）不走这条路，也必须保持空缓存。
	s.Start(f, false)
	s.Type("bob")
	if !reflect.DeepEqual(s.fold, tuidata.FoldCache{}) {
		t.Fatal("未限定列时不该建列缓存")
	}
	s.Cancel()
}

// TestSearchBarViewWidthNeverOverflows 钉住搜索条的宽度不变量：整行必须恰好等于
// 视口宽，多一格终端就会把最后一个字符硬回绕到下一行。
//
// 金样测试第一次生成时就抓到了这个 bug：过滤行补白到 width-行数宽之后，又额外
// 塞了一格 pad 再拼行数——而 strings.TrimRight 对带样式的补白是空操作（行尾是
// SGR 复位序列，不是空格），所以 pad 恒被夹到 1，整行永远是 width+1。
func TestSearchBarViewWidthNeverOverflows(t *testing.T) {
	f := searchFrame()
	th := theme.Default()
	// 覆盖：空查询（占位符）、短查询、CJK 查询、以及远长于可用宽度的查询。
	queries := []string{"", "a", "ali", "客户", "zzz", strings.Repeat("客户", 30)}
	for _, width := range []int{16, 40, 80, 160} {
		for _, q := range queries {
			var s SearchBar
			s.Start(f, false)
			s.Type(q)
			out := s.View(width, th)
			if w := ansi.StringWidth(out); w != width {
				t.Errorf("width=%d query=%q: 搜索条宽 = %d，想要 %d（%q）", width, q, w, width, ansi.Strip(out))
			}
		}
	}

	// 未激活时没有行数段，行宽同样必须恰好等于视口宽。
	var idle SearchBar
	for _, width := range []int{16, 80, 160} {
		out := idle.View(width, th)
		if w := ansi.StringWidth(out); w != width {
			t.Errorf("未激活 width=%d: 行宽 = %d，想要 %d（%q）", width, w, width, ansi.Strip(out))
		}
	}
}
