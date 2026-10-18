package popup

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
)

func init() {
	tui.Factories["schema"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		return newSchemaOverlay(ctx), nil
	}
}

type schemaKind uint8

const (
	schemaHeader schemaKind = iota // section title, not selectable
	schemaNote                     // informational line, not selectable
	schemaTab                      // open tab entry
	schemaLive                     // live backend table entry
	schemaGroup                    // namespace group header (enter expands/collapses)
)

// schemaRow is one display line of the schema browser. Rows are rebuilt from
// the overlay state (groups, filter) on every update, so the cursor always
// indexes the freshly built list.
type schemaRow struct {
	kind     schemaKind
	label    string
	tab      int    // tab index for schemaTab rows
	ns       string // namespace for schemaLive rows
	table    string // table name for schemaLive rows
	group    int    // index into groups for schemaGroup rows
	matchPos []int  // fuzzy match rune positions in label (non-nil when filtering)
}

func (r schemaRow) selectable() bool {
	return r.kind == schemaTab || r.kind == schemaLive || r.kind == schemaGroup
}

// schemaNsGroup is the live-table listing of one namespace. Only the
// connected namespace loads eagerly; the others stay collapsed until the
// user expands them (lazy, asynchronous).
type schemaNsGroup struct {
	ns        string
	connected bool
	tui.LazyGroup[string]
}

// schemaOverlay browses all open tabs plus (in db mode) the live tables of
// the connected backend, grouped by namespace. A fuzzy filter line at the
// top narrows both sections; enter jumps to a tab, opens a live table, or
// expands a namespace group. All backend listings run asynchronously
// (tea.Cmd goroutines) so a slow or dead connection can never freeze the
// update loop.
type schemaOverlay struct {
	tabs       []tui.TabInfo
	backend    backend.Backend
	beTitle    string
	fullscreen bool   // db mode: render as a full page, not a floating box
	nsLoaded   bool   // namespace listing arrived
	nsErr      string // namespace listing error
	flat       bool   // single empty namespace: no group headers
	groups     []schemaNsGroup
	connected  string // connected namespace ("" when unknown)

	input     tui.LineInput // fuzzy filter text
	filtering bool          // filter bar is visible and accepting input
	state     tui.ListState
}

// schemaNamespacesMsg delivers the async namespace listing (plus the eagerly
// loaded tables of the connected namespace) to its overlay.
type schemaNamespacesMsg struct {
	tui.Owned[schemaOverlay]
	nss       []string
	connected string
	err       error

	eager     string // namespace whose tables were loaded eagerly
	hasEager  bool
	tables    []string
	tablesErr error
}

// schemaTablesMsg delivers one namespace's lazy table listing.
type schemaTablesMsg struct {
	tui.Owned[schemaOverlay]
	ns     string
	tables []string
	err    error
}

func newSchemaOverlay(ctx tui.AppContext) *schemaOverlay {
	o := &schemaOverlay{tabs: ctx.Tabs()}
	o.state.ViewLen = 10
	if be := ctx.Backend(); be != nil {
		o.backend = be
		o.beTitle = be.Title()
		o.fullscreen = true
	}

	// Start on the active tab when possible, else the first selectable row.
	rows := o.rows()
	o.state.Cursor = -1
	active := ctx.ActiveTab()
	for i, r := range rows {
		if r.kind == schemaTab && r.tab == active {
			o.state.Cursor = i
			break
		}
	}
	if o.state.Cursor < 0 {
		o.state.Cursor = tui.NextSelectable(rows, -1, +1, schemaRow.selectable)
	}
	return o
}

// Init kicks off the async namespace listing (tui.OverlayIniter). The
// connected namespace — reported by backends implementing the optional
// CurrentNamespace interface — is resolved and eagerly listed in the same
// goroutine.
func (o *schemaOverlay) Init() tea.Cmd {
	return o.loadCmd()
}

// loadCmd runs the async namespace listing (plus the eager tables of the
// connected namespace) and stamps the result with this overlay so stale
// messages from a previous instance are ignored. It is used by Init and by
// the 'r' refresh handler.
func (o *schemaOverlay) loadCmd() tea.Cmd {
	be := o.backend
	if be == nil {
		return nil
	}
	return func() tea.Msg {
		m := schemaNamespacesMsg{Owned: tui.Owned[schemaOverlay]{Owner: o}}
		m.nss, m.err = be.Namespaces()
		if cn, ok := be.(backend.Namespaced); ok {
			m.connected = cn.CurrentNamespace()
		}
		if eager, ok := schemaEagerNs(m.nss, m.connected); ok {
			m.eager, m.hasEager = eager, true
			m.tables, m.tablesErr = be.Tables(eager)
		}
		return m
	}
}

// schemaEagerNs picks the namespace to list eagerly: the connected one when
// it is present in the listing, else a sole namespace (sqlite-style).
func schemaEagerNs(nss []string, connected string) (string, bool) {
	if connected != "" {
		for _, ns := range nss {
			if ns == connected {
				return ns, true
			}
		}
	}
	if len(nss) == 1 {
		return nss[0], true
	}
	return "", false
}

// applyNamespaces rebuilds the group list from the namespace listing,
// putting the connected namespace first.
func (o *schemaOverlay) applyNamespaces(m schemaNamespacesMsg) {
	o.nsLoaded = true
	if m.err != nil {
		o.nsErr = m.err.Error()
	}
	o.connected = m.connected
	o.flat = len(m.nss) == 1 && m.nss[0] == ""

	ordered := make([]string, 0, len(m.nss))
	if m.hasEager {
		ordered = append(ordered, m.eager)
	}
	for _, ns := range m.nss {
		if m.hasEager && ns == m.eager {
			continue
		}
		ordered = append(ordered, ns)
	}
	o.groups = make([]schemaNsGroup, 0, len(ordered))
	for _, ns := range ordered {
		g := schemaNsGroup{ns: ns, connected: ns == m.connected}
		if m.hasEager && ns == m.eager {
			g.LazyGroup = tui.LazyGroup[string]{Open: true, Loaded: true, Items: m.tables, Err: m.tablesErr}
		}
		o.groups = append(o.groups, g)
	}
}

// pattern returns the current (trimmed) filter text.
func (o *schemaOverlay) pattern() string {
	return strings.TrimSpace(o.input.String())
}

// fuzzyIndices returns the indices of items fuzzy-matching pattern, best
// match first; with an empty pattern every index is returned in order.
func fuzzyIndices(pattern string, items []string) []int {
	if pattern == "" {
		idx := make([]int, len(items))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	matches := fuzzy.Find(pattern, items)
	idx := make([]int, len(matches))
	for i, m := range matches {
		idx[i] = m.Index
	}
	return idx
}

// fuzzyMatches returns the indices of items fuzzy-matching pattern (best match
// first) together with the matched rune positions within each item. An empty
// (whitespace-only) pattern returns nil, nil.
func fuzzyMatches(pattern string, items []string) ([]int, [][]int) {
	if strings.TrimSpace(pattern) == "" {
		return nil, nil
	}
	ms := fuzzy.Find(pattern, items)
	cols := make([]int, len(ms))
	positions := make([][]int, len(ms))
	for i, m := range ms {
		cols[i] = m.Index
		positions[i] = m.MatchedIndexes
	}
	return cols, positions
}

// rows flattens the overlay state into display lines, honouring the filter:
// tabs and every already-loaded table are searched; unloaded namespaces stay
// collapsed and are flagged instead of scanned.
func (o *schemaOverlay) rows() []schemaRow {
	pattern := o.pattern()
	var out []schemaRow

	// --- open tabs (file mode only) -------------------------------------
	// In db mode the browser is a page in the connect -> browser -> table
	// stack; open tabs are not relevant, so the tabs section is skipped.
	if o.backend == nil {
		titles := make([]string, len(o.tabs))
		for i, t := range o.tabs {
			titles[i] = t.Title
		}
		var tabIdx []int
		var tabPos [][]int
		if pattern == "" {
			tabIdx = make([]int, len(titles))
			for i := range tabIdx {
				tabIdx[i] = i
			}
		} else {
			tabIdx, tabPos = fuzzyMatches(pattern, titles)
		}
		if pattern == "" || len(tabIdx) > 0 {
			out = append(out, schemaRow{kind: schemaHeader, label: "tabs"})
			if len(o.tabs) == 0 {
				out = append(out, schemaRow{kind: schemaNote, label: "(none)"})
			}
			for ii, i := range tabIdx {
				row := schemaRow{
					kind:  schemaTab,
					label: fmt.Sprintf("%s  %s", o.tabs[i].Title, o.tabs[i].Shape),
					tab:   i,
				}
				if tabPos != nil {
					row.matchPos = tabPos[ii]
				}
				out = append(out, row)
			}
		}
		return out
	}

	// --- live tables -----------------------------------------------------
	out = append(out, schemaRow{kind: schemaHeader, label: "tables " + o.beTitle})
	switch {
	case !o.nsLoaded:
		out = append(out, schemaRow{kind: schemaNote, label: "(loading …)"})
	case o.nsErr != "":
		out = append(out, schemaRow{kind: schemaNote, label: "error: " + o.nsErr})
	case o.flat:
		out = append(out, o.flatRows(pattern)...)
	default:
		out = append(out, o.groupRows(pattern)...)
	}
	return out
}

// flatRows lists the sole (empty) namespace without group headers.
func (o *schemaOverlay) flatRows(pattern string) []schemaRow {
	if len(o.groups) == 0 {
		return nil
	}
	g := &o.groups[0]
	switch {
	case g.Err != nil:
		return []schemaRow{{kind: schemaNote, label: fmt.Sprintf("error: %s", g.Err)}}
	case g.Loading:
		return []schemaRow{{kind: schemaNote, label: "(loading …)"}}
	case len(g.Items) == 0 && pattern == "":
		return []schemaRow{{kind: schemaNote, label: "(no tables)"}}
	}
	var out []schemaRow
	idx, pos := fuzzyMatches(pattern, g.Items)
	if pattern == "" {
		// no filter: show all
		idx = make([]int, len(g.Items))
		for i := range idx {
			idx[i] = i
		}
		pos = nil
	}
	for ii, i := range idx {
		row := schemaRow{kind: schemaLive, label: g.Items[i], table: g.Items[i]}
		if pos != nil {
			row.matchPos = pos[ii]
		}
		out = append(out, row)
	}
	return out
}

// groupRows lists the namespaces as sections. Without a filter every group
// renders with its open/collapsed state; with a filter the loaded tables are
// searched across all groups (collapsed ones included, they just do not get
// loaded), and unloaded groups only surface when their name matches.
func (o *schemaOverlay) groupRows(pattern string) []schemaRow {
	var out []schemaRow
	unshownUnloaded := 0
	for gi := range o.groups {
		g := &o.groups[gi]
		if pattern == "" {
			out = append(out, schemaRow{kind: schemaGroup, label: schemaGroupLabel(g), group: gi})
			if !g.Open {
				continue
			}
			switch {
			case g.Loading:
				out = append(out, schemaRow{kind: schemaNote, label: "(loading …)"})
			case g.Err != nil:
				out = append(out, schemaRow{kind: schemaNote, label: "error: " + g.Err.Error()})
			case len(g.Items) == 0:
				out = append(out, schemaRow{kind: schemaNote, label: "(no tables)"})
			default:
				for _, t := range g.Items {
					out = append(out, schemaRow{kind: schemaLive, label: t, ns: g.ns, table: t})
				}
			}
			continue
		}

		// Filtering: search loaded groups; flag unloaded ones.
		if g.Loaded {
			idx, pos := fuzzyMatches(pattern, g.Items)
			if len(idx) == 0 {
				continue
			}
			out = append(out, schemaRow{kind: schemaHeader, label: schemaGroupLabel(g), group: gi})
			for ii, i := range idx {
				row := schemaRow{kind: schemaLive, label: g.Items[i], ns: g.ns, table: g.Items[i]}
				if pos != nil {
					row.matchPos = pos[ii]
				}
				out = append(out, row)
			}
			continue
		}
		if len(fuzzyIndices(pattern, []string{g.ns})) > 0 {
			label := "▸ " + g.ns + "  (not loaded — enter to load)"
			if g.Loading {
				label = "▸ " + g.ns + "  (loading …)"
			}
			out = append(out, schemaRow{kind: schemaGroup, label: label, group: gi})
		} else {
			unshownUnloaded++
		}
	}
	if pattern != "" && unshownUnloaded > 0 {
		out = append(out, schemaRow{kind: schemaNote,
			label: fmt.Sprintf("(%d namespaces not loaded — clear filter to expand)", unshownUnloaded)})
	}
	return out
}

// schemaGroupLabel renders a namespace group header for the unfiltered view.
func schemaGroupLabel(g *schemaNsGroup) string {
	marker := "▸"
	if g.Open {
		marker = "▾"
	}
	label := marker + " " + g.ns
	if g.connected {
		label += "  (connected)"
	}
	if !g.Open {
		label += "  (enter to expand)"
	}
	return label
}

// resetCursor puts the cursor on the first selectable row (after the filter
// changed and the row list was rebuilt from scratch).
func (o *schemaOverlay) resetCursor(rows []schemaRow) {
	o.state.HomeToSelectable(len(rows), func(i int) bool { return rows[i].selectable() })
}

// clampCursor keeps the cursor on a selectable row after the rows changed
// shape (async load arrived, group toggled).
func (o *schemaOverlay) clampCursor(rows []schemaRow) {
	tui.ClampListCursor(&o.state, rows, schemaRow.selectable)
}

func (o *schemaOverlay) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case schemaNamespacesMsg:
		if m.Stale(o) {
			return o, nil // stale result from a closed instance
		}
		o.applyNamespaces(m)
		rows := o.rows()
		o.clampCursor(rows)
		o.scrollToCursor(rows)
		return o, nil

	case schemaTablesMsg:
		if m.Stale(o) {
			return o, nil
		}
		for i := range o.groups {
			g := &o.groups[i]
			if g.ns != m.ns {
				continue
			}
			g.Deliver(m.tables, m.err)
		}
		rows := o.rows()
		o.clampCursor(rows)
		o.scrollToCursor(rows)
		return o, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	rows := o.rows()
	switch key.String() {
	case "esc":
		// First esc: stop filtering and clear input. Second esc: close.
		if o.filtering || o.input.String() != "" {
			o.filtering = false
			o.input = tui.LineInput{}
			o.resetCursor(o.rows())
			return o, nil
		}
		return o, o.closeCmd()
	case "ctrl+c":
		return o, tui.CloseOverlay
	case "up", "ctrl+p":
		o.state.Cursor = tui.NextSelectable(rows, o.state.Cursor, -1, schemaRow.selectable)
	case "down", "ctrl+n":
		o.state.Cursor = tui.NextSelectable(rows, o.state.Cursor, +1, schemaRow.selectable)
	case "pgup":
		o.movePage(rows, -1)
	case "pgdown":
		o.movePage(rows, +1)
	case "r":
		// Re-trigger the async namespace listing, dropping any cached state
		// so the refresh starts from a clean slate. Only acts as refresh
		// when not in filter mode; otherwise 'r' is a filter character.
		if !o.filtering {
			o.nsLoaded = false
			o.nsErr = ""
			o.groups = nil
			return o, o.loadCmd()
		}
		before := o.input.String()
		o.input.Handle(key)
		if o.input.String() != before {
			o.resetCursor(o.rows())
			return o, nil
		}
	case "/":
		o.filtering = true
		return o, nil
	case "enter":
		return o, o.activate(rows)
	default:
		// Accept printable characters whether or not "/" was pressed first.
		// Set filtering=true so the filter line becomes visible.
		before := o.input.String()
		o.input.Handle(key)
		if o.input.String() != before {
			o.filtering = true
			o.resetCursor(o.rows())
		}
	}
	o.scrollToCursor(rows)
	return o, nil
}

// closeCmd is what esc does at the top level of the browser. In database
// mode the browser is a page in the login -> browser -> table stack, so esc
// always steps back to the connection form — whether or not tabs are open
// (the "connect" factory is registered by the db-mode integration). In file
// mode there is no form and esc just closes the box.
func (o *schemaOverlay) closeCmd() tea.Cmd {
	if o.backend != nil {
		return tea.Sequence(
			tui.CloseOverlay,
			func() tea.Msg { return tui.RunCommandMsg{Name: "connect"} },
		)
	}
	return tui.CloseOverlay
}

// Fullscreen reports whether the browser renders as a full page
// (tui.FullscreenOverlay): only in database mode, where a live backend
// section exists. In file mode it stays a floating box over the table.
func (o *schemaOverlay) Fullscreen() bool { return o.fullscreen }

// movePage jumps the cursor by one window height, snapping to a selectable
// row in the same direction (falling back to the opposite one at the ends).
func (o *schemaOverlay) movePage(rows []schemaRow, dir int) {
	target := o.state.Cursor + dir*max(1, o.state.ViewLen)
	target = schemaClamp(target, 0, len(rows)-1)
	if target >= 0 && target < len(rows) && rows[target].selectable() {
		o.state.Cursor = target
		return
	}
	if next := tui.NextSelectable(rows, target, dir, schemaRow.selectable); next >= 0 && next < len(rows) && rows[next].selectable() {
		o.state.Cursor = next
		return
	}
	o.state.Cursor = tui.NextSelectable(rows, target, -dir, schemaRow.selectable)
}

// activate returns the command for the row under the cursor.
func (o *schemaOverlay) activate(rows []schemaRow) tea.Cmd {
	if o.state.Cursor < 0 || o.state.Cursor >= len(rows) {
		return nil
	}
	r := rows[o.state.Cursor]
	switch r.kind {
	case schemaTab:
		idx := r.tab
		return tea.Sequence(
			tui.CloseOverlay,
			func() tea.Msg { return tui.JumpToTabMsg{Index: idx} },
		)
	case schemaLive:
		// Delegates to the "opentable" factory registered by the db-mode
		// integration. Close first so the pop cannot race with the pushed
		// table loader overlay.
		arg := r.ns + "\t" + r.table
		return tea.Sequence(
			tui.CloseOverlay,
			func() tea.Msg { return tui.RunCommandMsg{Name: "opentable", Arg: arg} },
		)
	case schemaGroup:
		return o.toggleGroup(r.group)
	}
	return nil
}

// toggleGroup expands or collapses a namespace group, starting the lazy
// asynchronous table listing on first expansion.
func (o *schemaOverlay) toggleGroup(gi int) tea.Cmd {
	if gi < 0 || gi >= len(o.groups) {
		return nil
	}
	g := &o.groups[gi]
	if g.Open && o.pattern() == "" {
		g.Open = false
		return nil
	}
	g.Open = true
	be, ns := o.backend, g.ns
	return g.EnsureLoaded(func() tea.Cmd {
		return func() tea.Msg {
			tables, err := be.Tables(ns)
			return schemaTablesMsg{
				Owned: tui.Owned[schemaOverlay]{Owner: o},
				ns:    ns, tables: tables, err: err,
			}
		}
	})
}

// scrollToCursor keeps the cursor inside the visible window. Unlike
// tui.ListState.ScrollToCursor it also pulls the section header directly
// above the cursor into view when the cursor moves up, so a live table
// never renders detached from its group header.
func (o *schemaOverlay) scrollToCursor(rows []schemaRow) {
	if o.state.Cursor < 0 {
		return
	}
	vl := max(1, o.state.ViewLen)
	if o.state.Cursor < o.state.Offset {
		o.state.Offset = o.state.Cursor
		// Pull a section header directly above the cursor into view.
		if o.state.Offset > 0 && !rows[o.state.Offset-1].selectable() {
			o.state.Offset--
		}
	}
	if o.state.Cursor >= o.state.Offset+vl {
		o.state.Offset = o.state.Cursor - vl + 1
	}
	o.state.Offset = schemaClamp(o.state.Offset, 0, max(0, len(rows)-vl))
}

func (o *schemaOverlay) View(width, height int, th *theme.Theme) string {
	boxW := schemaClamp(70, 30, max(30, width-4))
	if o.fullscreen {
		// Full page: the frame fills most of the body (the app blanks the
		// rest), so the table behind can never show through.
		boxW = max(30, width-8)
	}
	inner := boxW - 2

	filterVisible := o.filtering || len(o.input.String()) > 0

	rows := o.rows()
	avail := height - 2 - 1 // borders + footer hint
	if filterVisible {
		avail-- // filter line
	}
	if o.fullscreen {
		avail = height - 2 - 2 - 1 // frame is body height-2, centered
		if filterVisible {
			avail--
		}
	}
	if avail < 3 {
		avail = 3
	}
	if !o.fullscreen && avail > len(rows) {
		avail = len(rows)
	}
	o.state.ViewLen = max(1, avail)
	o.state.Offset = schemaClamp(o.state.Offset, 0, max(0, len(rows)-o.state.ViewLen))

	var lines []string
	if filterVisible {
		lines = append(lines, tui.FilterLine(o.input.String(), o.filtering, inner, "type to filter", th))
	}
	if len(rows) == 0 {
		lines = append(lines, th.Placeholder.Render("  no matches"))
	}
	for i := o.state.Offset; i < o.state.Offset+o.state.ViewLen && i < len(rows); i++ {
		r := rows[i]
		switch {
		case r.kind == schemaHeader:
			lines = append(lines, th.PopupTitle.Render(ansi.Truncate(" "+r.label, inner, "…")))
		case r.kind == schemaNote:
			lines = append(lines, th.Subtle.Render(ansi.Truncate("   "+r.label, inner, "…")))
		case r.kind == schemaGroup && i == o.state.Cursor:
			if r.matchPos != nil {
				highlighted := tui.HighlightRunes(r.label, r.matchPos, th.Match, th.ListSelected)
				prefix := th.ListSelected.Render(" ")
				pad := max(0, inner-1-ansi.StringWidth(r.label))
				lines = append(lines, prefix+highlighted+th.ListSelected.Render(strings.Repeat(" ", pad)))
			} else {
				lines = append(lines, th.ListSelected.Render(tui.Pad(" "+r.label, inner)))
			}
		case r.kind == schemaGroup:
			lines = append(lines, th.PopupTitle.Render(ansi.Truncate(" "+r.label, inner, "…")))
		case i == o.state.Cursor:
			if r.matchPos != nil {
				highlighted := tui.HighlightRunes(r.label, r.matchPos, th.Match, th.ListSelected)
				prefix := th.ListSelected.Render(" > ")
				pad := max(0, inner-3-ansi.StringWidth(r.label))
				lines = append(lines, prefix+highlighted+th.ListSelected.Render(strings.Repeat(" ", pad)))
			} else {
				lines = append(lines, th.ListSelected.Render(tui.Pad(" > "+r.label, inner)))
			}
		default:
			if r.matchPos != nil {
				highlighted := tui.HighlightRunes(r.label, r.matchPos, th.Match, th.ListItem)
				prefix := th.ListItem.Render("   ")
				pad := max(0, inner-3-ansi.StringWidth(r.label))
				lines = append(lines, prefix+highlighted+th.ListItem.Render(strings.Repeat(" ", pad)))
			} else {
				lines = append(lines, th.ListItem.Render(tui.Pad("   "+r.label, inner)))
			}
		}
	}

	// Full page: pad the listing with blank rows so the frame keeps its
	// fixed height even when there are few entries.
	if o.fullscreen {
		for len(lines) < o.state.ViewLen {
			lines = append(lines, "")
		}
	}

	hint := " enter open/expand  •  / filter  •  esc close"
	if o.fullscreen {
		hint = " enter open/expand  •  / filter  •  esc back to connect"
	}
	if o.filtering {
		hint = " enter open/expand  •  esc stop filtering"
	}
	if o.input.String() != "" && !o.filtering {
		hint = " enter open  •  esc clear filter"
	}
	if len(rows) > o.state.ViewLen {
		hint = fmt.Sprintf(" %d-%d of %d  • %s",
			o.state.Offset+1, min(o.state.Offset+o.state.ViewLen, len(rows)), len(rows), strings.TrimPrefix(hint, " "))
	}
	lines = append(lines, th.Subtle.Render(hint))

	return tui.Box("schema", strings.Join(lines, "\n"), boxW, th)
}

func schemaClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// compile-time interface checks
var (
	_ tui.Overlay           = (*schemaOverlay)(nil)
	_ tui.OverlayIniter     = (*schemaOverlay)(nil)
	_ tui.FullscreenOverlay = (*schemaOverlay)(nil)
)
