// TableLoader is the single transient overlay that fetches a live table's
// rows off the update loop. Two modes share the fetch, keys and view:
//
//   - open mode (Refresh=false, "opentable"): on success the frame is applied
//     as a new tab;
//   - refresh mode (Refresh=true, "refreshtable"): on success the active
//     pane's base frame is replaced in place (no new tab) and a toast shows.
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
)

// TableFetchLimit caps how many rows a table fetch loads, matching the legacy
// tree-view behavior.
const TableFetchLimit = 200

// TableLoadedMsg carries the fetched rows (or the fetch error) back to the
// loading overlay.
type TableLoadedMsg struct {
	Table string
	Frame *tuidata.Frame
	Err   error
}

// TableLoader is a transient overlay: it kicks off the fetch on Init and
// closes itself, applying the result (new tab or in-place refresh) or
// surfacing the error when it lands.
type TableLoader struct {
	Ctx     AppContext
	Be      backend.Backend
	NS      string
	Table   string
	Refresh bool
}

// NewTableLoader builds a table-fetch overlay. refresh selects between
// replacing the active pane's base frame (true) and opening a new tab.
func NewTableLoader(ctx AppContext, be backend.Backend, ns, table string, refresh bool) *TableLoader {
	return &TableLoader{Ctx: ctx, Be: be, NS: ns, Table: table, Refresh: refresh}
}

func (o *TableLoader) Init() tea.Cmd {
	be, ns, table := o.Be, o.NS, o.Table
	filter := tabFilter(o.Ctx)
	return func() tea.Msg {
		f, err := fetchTableWithFilter(be, ns, table, filter)
		return TableLoadedMsg{Table: table, Frame: f, Err: err}
	}
}

// tabFilter 取当前标签的过滤文本；ctx 可能为 nil（测试里直接构造 loader）。
func tabFilter(ctx AppContext) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ctx.SheetFilter())
}

// fetchTableWithFilter 尽量把过滤下推到服务端：只有「过滤文本能解析成条件」
// 且「后端支持下推」时才走 FetchTableWithFilters；否则退回整页取回，过滤仍由
// UI 客户端完成 —— 下推失败绝不能让取数本身报错。
func fetchTableWithFilter(be backend.Backend, ns, table, filter string) (*tuidata.Frame, error) {
	if filter == "" {
		return be.FetchTable(ns, table, TableFetchLimit)
	}
	conds, err := ParseFilterConditions(filter)
	if err != nil || len(conds) == 0 {
		return be.FetchTable(ns, table, TableFetchLimit)
	}
	fb, ok := be.(backend.Filtered)
	if !ok {
		return be.FetchTable(ns, table, TableFetchLimit)
	}
	return fb.FetchTableWithFilters(ns, table, TableFetchLimit, conds)
}

func (o *TableLoader) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case TableLoadedMsg:
		return o, tea.Sequence(o.ResultCmds(m)...)
	case tea.KeyPressMsg:
		switch m.String() {
		case "esc", "q", "ctrl+c":
			// Abandon the view; a late result is dropped with the overlay.
			return o, CloseOverlay
		}
	}
	return o, nil
}

// ResultCmds is the ordered message sequence for a fetch outcome: close the
// loading overlay first, then open the tab / replace the base frame / report
// the error.
func (o *TableLoader) ResultCmds(m TableLoadedMsg) []tea.Cmd {
	if m.Err != nil {
		err := m.Err
		return []tea.Cmd{CloseOverlay, func() tea.Msg { return ErrorMsg{Err: err} }}
	}
	if o.Refresh {
		paneID := o.Ctx.ActivePaneID()
		frame := m.Frame
		return []tea.Cmd{
			CloseOverlay,
			func() tea.Msg { return ReplaceBaseMsg{Frame: frame, PaneID: paneID} },
			func() tea.Msg { return ToastMsg{Text: "refreshed"} },
		}
	}
	frame, title := m.Frame, m.Table
	msg := ApplyFrameMsg{Frame: frame, NewTab: true, TabTitle: title, Crumb: "table",
		SourceNamespace: o.NS, SourceTable: m.Table}
	return []tea.Cmd{CloseOverlay, func() tea.Msg { return msg }}
}

func (o *TableLoader) View(width, height int, th *theme.Theme) string {
	verb, boxTitle := " loading ", "table"
	if o.Refresh {
		verb, boxTitle = " refreshing ", "refresh"
	}
	w := len(o.Table) + 24
	if w > width-4 {
		w = width - 4
	}
	if w < 24 {
		w = 24
	}
	body := th.Text.Render(verb + o.Table + " ...")
	return Box(boxTitle, body, w, th)
}

// compile-time interface checks
var (
	_ Overlay       = (*TableLoader)(nil)
	_ OverlayIniter = (*TableLoader)(nil)
)
