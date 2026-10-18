package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/transport"
)

const toastDuration = 2500 * time.Millisecond

// NamedFrame is a titled frame used to seed tabs (tests and file-less opens).
type NamedFrame struct {
	Name  string
	Frame *tuidata.Frame
}

// Options configures a new App.
type Options struct {
	Frames  []NamedFrame      // initial tabs
	Backend backend.Backend   // live SQL connection (nil when none open)
	KV      backend.KVBackend // live redis connection (nil otherwise)
	Client  transport.Client  // 数据面（铁律 2）；nil 时连接相关功能不可用

	ThemeName      string
	ShowBorders    bool
	ShowRowNumbers bool
}

// App is the root model: tabs of panes, an overlay stack, the search bar and
// the bottom status line.
type App struct {
	tabs     []*Pane
	active   int
	overlays []Overlay

	th        *theme.Theme
	themeName string

	showBorders    bool
	showRowNumbers bool

	width, height int

	toastText string
	toastID   int

	search SearchBar

	backend backend.Backend
	kv      backend.KVBackend
	cli     transport.Client

	// pendingEdit/pendingDelete hold an in-flight cell edit / row delete that
	// a confirm popup is about to commit. They are set by the dispatch cases
	// (ActEdit for Agent E, ActDelete here) and cleared by the commit/cancel
	// built-ins (saveedit/canceledit/deleterows/canceldelete).
	pendingEdit   *PendingEdit
	pendingDelete *PendingDelete

	// schemaC is this app's completion-schema snapshot (see app_schema.go).
	// Created once in New; its own mutex guards every access.
	schemaC *schemaCache
}

// toastExpiredMsg clears a toast after its timer fires.
type toastExpiredMsg struct{ id int }

// New builds the app from options. Each frame becomes a tab.
func New(opts Options) *App {
	// 主题名解析优先级：显式传入 > 环境变量 POLYDB_TUI_THEME > 默认。
	themeName := opts.ThemeName
	if env := os.Getenv("POLYDB_TUI_THEME"); env != "" {
		themeName = env
	}
	th, ok := theme.Builtin(themeName)
	name := themeName
	if !ok {
		th = theme.Default()
		name = th.Palette.Name
	}
	a := &App{
		th:             th,
		themeName:      name,
		showBorders:    opts.ShowBorders,
		showRowNumbers: opts.ShowRowNumbers,
		backend:        opts.Backend,
		kv:             opts.KV,
		cli:            opts.Client,
		schemaC:        &schemaCache{},
		width:          80,
		height:         24,
	}
	for _, nf := range opts.Frames {
		a.tabs = append(a.tabs, NewPane(nf.Name, nf.Frame))
	}
	return a
}

// Run drives the app to completion on the terminal. Whatever live
// connections the app owns are closed when the program returns, so every
// exit path (last tab closed, quit, crash-recoverable return) drops them.
func Run(app *App) error {
	_, err := tea.NewProgram(app).Run()
	app.CloseConnections()
	return err
}

// CloseConnections closes the live SQL backend and key-value connection the
// app currently holds (both nil-safe). The replacement path in SetBackendMsg
// uses the same policy: a superseded connection is closed immediately.
func (a *App) CloseConnections() {
	if a.backend != nil {
		_ = a.backend.Close() // best effort on teardown
		a.backend = nil
	}
	if a.kv != nil {
		_ = a.kv.Close()
		a.kv = nil
	}
}

// --- tea.Model ---------------------------------------------------------------

func (a *App) Init() tea.Cmd { return nil }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		return a, nil

	case PushOverlayMsg:
		if m.Overlay != nil {
			return a, a.pushOverlay(m.Overlay)
		}
		return a, nil

	case SetBackendMsg:
		if m.Backend != nil && m.Backend != a.backend {
			if a.backend != nil {
				_ = a.backend.Close() // superseded: a reconnect replaces it
			}
			a.backend = m.Backend
		}
		if m.KV != nil && m.KV != a.kv {
			if a.kv != nil {
				_ = a.kv.Close()
			}
			a.kv = m.KV
		}
		return a, nil

	case CloseOverlayMsg:
		if n := len(a.overlays); n > 0 {
			a.overlays = a.overlays[:n-1]
		}
		return a, nil

	case ErrorMsg:
		a.overlays = append(a.overlays, errBox{err: m.Err})
		return a, nil

	case ToastMsg:
		return a, a.setToast(m.Text)

	case toastExpiredMsg:
		if m.id == a.toastID {
			a.toastText = ""
		}
		return a, nil

	case ApplyFrameMsg:
		return a, a.applyFrame(m)

	case ReplaceBaseMsg:
		return a, a.replaceBase(m)

	case RunCommandMsg:
		return a, a.runCommand(m.Name, m.Arg)

	case SetThemeMsg:
		th, ok := theme.Builtin(m.Name)
		if !ok {
			a.overlays = append(a.overlays, errBox{err: fmt.Errorf("unknown theme: %s", m.Name)})
			return a, nil
		}
		a.th, a.themeName = th, m.Name
		a.persistUI()
		return a, a.setToast("theme: " + m.Name)

	case ToggleBordersMsg:
		a.showBorders = !a.showBorders
		a.persistUI()
		return a, nil

	case ToggleRowNumbersMsg:
		a.showRowNumbers = !a.showRowNumbers
		a.persistUI()
		return a, nil

	case ResetStackMsg:
		if p := a.pane(); p != nil {
			p.Reset()
		}
		return a, nil

	case JumpToRowMsg:
		if p := a.pane(); p != nil && p.Current() != nil {
			p.Table.JumpTo(m.Row, p.Current().NumRows())
		}
		return a, nil

	case JumpToTabMsg:
		if len(a.tabs) > 0 {
			a.active = clamp(m.Index, 0, len(a.tabs)-1)
		}
		return a, nil

	case CloseTabMsg:
		return a, a.closeTab(m.Index)

	case ExecProcessMsg:
		if m.Cmd == nil {
			return a, nil
		}
		return a, tea.ExecProcess(m.Cmd, tea.ExecCallback(m.OnDone))

	case CopyTextMsg:
		return a, tea.Batch(tea.SetClipboard(m.Text), a.setToast("copied to clipboard"))

	case QxEditDoneMsg:
		if m.Err != nil {
			return a, func() tea.Msg { return ErrorMsg{Err: m.Err} }
		}
		return a, tea.Batch(CloseOverlay, func() tea.Msg {
			return ApplyFrameMsg{
				Frame:    m.Frame,
				Crumb:    "edit",
				NewTab:   true,
				TabTitle: "edit",
				PaneID:   a.ActivePaneID(),
			}
		})

	case columnMetaMsg:
		// An async ColumnsMeta fetch finished. On success store the metadata
		// into the completion cache so the next View shows the real type
		// instead of the frame DType fallback. The cache may be unbound when
		// WarmCompletionSchema never ran (cold open); bind it to the current
		// backend in that case. A cache bound to a different backend
		// (connection swapped mid-flight) rejects the store.
		if m.err == nil && m.meta != nil && a.backend != nil {
			c := a.completionCache()
			c.mu.Lock()
			if c.backend == nil {
				c.backend = a.backend
				c.ns = make(map[string]string)
				c.cols = make(map[string][]string)
				c.meta = make(map[string][]backend.ColumnMeta)
				c.fetched = make(map[string]bool)
			}
			if c.backend == a.backend {
				c.meta[m.table] = m.meta
				c.fetched[m.table] = true
				if c.cols[m.table] == nil {
					cols := make([]string, len(m.meta))
					for i, cm := range m.meta {
						cols[i] = cm.Name
					}
					c.cols[m.table] = cols
				}
			}
			c.mu.Unlock()
		}
		return a, nil

	case tea.PasteMsg:
		// Bracketed paste follows key routing: the top overlay swallows it;
		// with no overlay open it feeds the active search bar.
		if n := len(a.overlays); n > 0 {
			ov, cmd := a.overlays[n-1].Update(msg)
			a.overlays[n-1] = ov
			return a, cmd
		}
		if a.search.Active() {
			a.search.Paste(m.Content)
			if p := a.pane(); p != nil {
				p.Table.ClampTo(a.search.Preview())
			}
		}
		return a, nil

	case cellSavedMsg:
		// An async UPDATE finished. On success toast and refresh the table so
		// the edit is visible; on error surface the error overlay.
		if m.err != nil {
			a.overlays = append(a.overlays, errBox{err: m.err})
			return a, nil
		}
		cmds := []tea.Cmd{a.setToast(fmt.Sprintf("updated %d row(s)", m.rows))}
		if a.backend != nil {
			cmds = append(cmds, a.runCommand("refreshtable", ""))
		}
		return a, tea.Batch(cmds...)

	case rowsDeletedMsg:
		// An async DELETE finished. On success toast, refresh the table and
		// clear the multi-select set; on error surface the error overlay.
		if m.err != nil {
			a.overlays = append(a.overlays, errBox{err: m.err})
			return a, nil
		}
		if p := a.pane(); p != nil {
			p.Table.ClearSelect()
		}
		cmds := []tea.Cmd{a.setToast(fmt.Sprintf("deleted %d row(s)", m.rows))}
		if a.backend != nil {
			cmds = append(cmds, a.runCommand("refreshtable", ""))
		}
		return a, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		// ctrl+c always quits immediately, regardless of any overlay or mode.
		if m.String() == "ctrl+c" {
			return a, tea.Quit
		}
		return a.handleKey(m)

	default:
		// Anything else (ticks, async results, ...) goes to the top overlay
		// so self-updating overlays keep working.
		if n := len(a.overlays); n > 0 {
			ov, cmd := a.overlays[n-1].Update(msg)
			a.overlays[n-1] = ov
			return a, cmd
		}
		return a, nil
	}
}

func (a *App) View() tea.View {
	w, h := a.width, a.height
	bodyH := max(1, h-1)

	// When search is active, reserve one line at the top for the search bar
	// and shrink the body accordingly. The status bar stays at the bottom.
	var topBar string
	if a.search.Active() {
		topBar = a.search.View(w, a.th) + "\n"
		bodyH = max(1, bodyH-1)
	}

	bottom := a.renderBottom(w)

	var body string
	if n := len(a.overlays); n > 0 {
		top := a.overlays[n-1]
		box := top.View(w, bodyH, a.th)
		if fs, ok := top.(FullscreenOverlay); ok && fs.Fullscreen() {
			body = FillPage(box, w, bodyH) + "\n" + bottom
		} else {
			// When a non-fullscreen popup sits on top of a fullscreen overlay
			// (e.g. time-range selector over the dashboard panel viewer), use
			// the nearest fullscreen overlay below as the background so the
			// popup floats over it instead of over the empty main content.
			bg := a.renderBody(w, bodyH)
			for i := n - 2; i >= 0; i-- {
				if fs2, ok2 := a.overlays[i].(FullscreenOverlay); ok2 && fs2.Fullscreen() {
					bg = FillPage(a.overlays[i].View(w, bodyH, a.th), w, bodyH)
					break
				}
			}
			body = Composite(bg+"\n"+bottom, box, w, bodyH+1)
		}
	} else {
		body = a.renderBody(w, bodyH) + "\n" + bottom
	}

	v := tea.NewView(topBar + body)
	v.AltScreen = true
	return v
}

// --- rendering ----------------------------------------------------------------

func (a *App) renderBody(w, h int) string {
	p := a.pane()
	if p == nil {
		msg := "no data loaded"
		if a.backend != nil || a.kv != nil {
			msg = "no tables open - press : for commands"
		}
		lines := strings.Split(blankLines(w, h, a.th), "\n")
		mid := h / 2
		if mid < len(lines) {
			pad := max(0, (w-len(msg))/2)
			lines[mid] = a.th.Subtle.Render(padLine(strings.Repeat(" ", pad)+msg, w))
		}
		return strings.Join(lines, "\n")
	}

	frame := p.Current()
	if a.search.Active() {
		frame = a.search.Preview()
	}
	// A sorted grid renders rows in a permuted order; the sheet/JSON detail
	// views read the (unchanged) frame by the source row, not the display row.
	srcRow := p.Table.SrcRow(p.Table.Sel())

	if p.Mode == ModeJsonView && frame != nil && frame.NumRows() > 0 {
		return renderJsonView(frame, srcRow, p.JsonViewOff, w, h, a.th)
	}

	if p.Mode == ModeSheet && frame != nil && frame.NumRows() > 0 {
		if p.SheetEditing {
			keyW, valW := sheetTableGeometry(frame, w)
			matched := sheetMatchedCols(frame, p.SheetFilter.String())
			actualCol := p.SheetField
			if len(matched) > 0 {
				actualCol = matched[clamp(p.SheetField, 0, len(matched)-1)]
			}
			leftContent := renderSheetKeyPanel(frame, srcRow, p.SheetOff, keyW, h, p.SheetField,
				p.SheetFilter.String(), p.SheetFiltering, a.th)
			rightContent := renderSheetDrawer(frame, srcRow, actualCol,
				[]rune(p.SheetEdit.String()), p.SheetEdit.Cursor(), p.SheetValOff, valW, h, a.cursorFieldType(), a.th)
			return joinDrawerSplit(leftContent, rightContent, keyW, valW, h, a.th)
		}
		return renderSheet(frame, srcRow, p.SheetOff, w, h, p.SheetField,
			false, nil, 0, p.SheetValOff,
			p.SheetFilter.String(), p.SheetFiltering, a.th)
	}

	return p.Table.Render(frame, RenderOpts{
		Width:          w,
		Height:         h,
		Theme:          a.th,
		ShowBorders:    a.showBorders,
		ShowRowNumbers: a.showRowNumbers,
		HeaderCur:      p.Table.ColCursor(),
		TableCols:      p.TableCols,
		NoEllipsis:     len(p.TableCols) > 0,
		HScroll:        p.HScroll,
		HighlightQuery: a.searchQuery(),
		HighlightCols:  p.TableCols,
	})
}

func (a *App) renderBottom(w int) string {
	if a.toastText != "" {
		return a.th.Success.Render(padLine(" "+a.toastText, w))
	}
	info := statusInfo{Tab: 0, NTabs: len(a.tabs), Row: 0}
	if p := a.pane(); p != nil {
		info.Tab = a.active + 1
		info.Crumbs = p.Crumbs()
		if f := p.Current(); f != nil {
			info.Row = p.Table.Sel() + 1
			info.NRows = f.NumRows()
			info.NCols = f.NumCols()
			if f.NumCols() > 0 {
				info.Mode = "fit"
				if p.Table.Expanded() {
					info.Mode = "wide"
				}
				// The column cursor is live in both modes (y-copy targets it).
				info.Col = f.Columns[clamp(p.Table.ColCursor(), 0, f.NumCols()-1)].Name
			}
		}
	}
	switch {
	case a.backend != nil:
		info.Conn = a.backend.Title()
	case a.kv != nil:
		info.Conn = a.kv.Title()
	}
	return renderStatusBar(info, w, a.th)
}

// --- key handling ---------------------------------------------------------------

func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Modal overlays swallow every key.
	if n := len(a.overlays); n > 0 {
		ov, cmd := a.overlays[n-1].Update(msg)
		a.overlays[n-1] = ov
		return a, cmd
	}

	if a.search.Active() {
		return a.handleSearchKey(msg)
	}

	key := msg.String()
	if key == "ctrl+c" {
		return a, tea.Quit
	}

	p := a.pane()
	if p != nil && p.Mode == ModeJsonView {
		switch key {
		case "j", "down":
			p.JsonViewOff++
		case "k", "up":
			if p.JsonViewOff > 0 {
				p.JsonViewOff--
			}
		case "q", "esc":
			p.Mode = ModeSheet
		}
		return a, nil
	}
	if p != nil && p.Mode == ModeSheet {
		// While inline-editing a sheet field, keys go to the edit handler
		// before the normal SheetBindings dispatch: j/k and other nav keys
		// must not move the cursor mid-edit.
		if p.SheetEditing {
			return a.handleSheetEditKey(msg)
		}
		// While the field-filter input is active, keys go to the filter
		// handler instead of the normal dispatch so "/" can pre-type the
		// first rune and esc clears the filter instead of leaving sheet mode.
		if p.SheetFiltering {
			return a.handleSheetFilterKey(msg)
		}
		return a, a.dispatch(actionFor(key, SheetBindings, GlobalBindings), key)
	}
	// In column zoom mode (f key), left/right do character-level scroll.
	if p != nil && p.Mode == ModeTable && p.ColZoomed {
		const hStep = 10
		switch key {
		case "l", "right":
			p.HScroll += hStep
			return a, nil
		case "h", "left":
			p.HScroll -= hStep
			if p.HScroll < 0 {
				p.HScroll = 0
			}
			return a, nil
		}
	}

	return a, a.dispatch(actionFor(key, TableBindings, GlobalBindings), key)
}

// handleSheetFilterKey routes keys while the sheet field-filter input is
// active. esc/ctrl+c clear the filter and exit filter input; enter commits the
// current pattern (filter stays applied, input closes); backspace pops the
// last rune; ctrl+u clears the text; up/down/j/k move the cursor within the
// matched set; printable runes append and reset the cursor to the first
// match. After each change the matched set is recomputed and SheetField is
// clamped, with the left list edge-followed.
func (a *App) handleSheetFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := a.pane()
	if p == nil {
		return a, nil
	}
	f := p.Current()
	if f == nil {
		return a, nil
	}
	key := msg.String()
	switch key {
	case "esc", "ctrl+c":
		p.SheetFiltering = false
		p.SheetFilter = LineInput{}
		p.SheetField = 0
		p.SheetOff = 0
		return a, nil
	case "enter":
		p.SheetFiltering = false
		// Keep the filter applied. Clamp the cursor to the matched set and
		// edge-follow so the entry stays visible.
		a.clampSheetToMatched(f, p)
		return a, nil
	case "backspace", "ctrl+h":
		p.SheetFilter.Backspace()
		p.SheetField = 0
		p.SheetOff = 0
		a.clampSheetToMatched(f, p)
		return a, nil
	case "ctrl+u":
		p.SheetFilter.Clear()
		p.SheetField = 0
		p.SheetOff = 0
		return a, nil
	case "up", "k":
		if p.SheetField > 0 {
			p.SheetField--
		}
		p.SheetValOff = 0
		a.edgeFollowSheet(f, p)
		return a, nil
	case "down", "j":
		matched := sheetMatchedCols(f, p.SheetFilter.String())
		if len(matched) > 0 && p.SheetField < len(matched)-1 {
			p.SheetField++
		}
		p.SheetValOff = 0
		a.edgeFollowSheet(f, p)
		return a, nil
	default:
		if t := msg.Key().Text; t != "" && msg.Mod == 0 {
			p.SheetFilter.InsertKey(t)
			p.SheetField = 0
			p.SheetOff = 0
			a.clampSheetToMatched(f, p)
		}
		return a, nil
	}
}

// clampSheetToMatched ensures SheetField is a valid index into the current
// matched set and re-runs the left-list edge-follow. Called after every
// filter mutation that can shrink the matched set.
func (a *App) clampSheetToMatched(f *tuidata.Frame, p *Pane) {
	matched := sheetMatchedCols(f, p.SheetFilter.String())
	if len(matched) == 0 {
		p.SheetField = 0
		p.SheetOff = 0
		return
	}
	if p.SheetField >= len(matched) {
		p.SheetField = len(matched) - 1
	}
	if p.SheetField < 0 {
		p.SheetField = 0
	}
	a.edgeFollowSheet(f, p)
}

// edgeFollowSheet advances SheetOff so the cursored entry stays visible in
// the sheet body, mirroring renderSheet's geometry exactly.
func (a *App) edgeFollowSheet(f *tuidata.Frame, p *Pane) {
	matched := sheetMatchedCols(f, p.SheetFilter.String())
	if len(matched) == 0 {
		p.SheetOff = 0
		return
	}
	keyW, valW := sheetTableGeometry(f, a.width)
	bodyH := max(1, a.height-1)
	// Mirror renderSheet's header accounting: row header + col header + footer
	// hint = 3. Add 1 more when the filter line is visible.
	headers := 3
	if p.SheetFiltering || strings.TrimSpace(p.SheetFilter.String()) != "" {
		headers = 4
	}
	visible := bodyH - headers
	if visible < 1 {
		visible = 1
	}
	dataRow := p.Table.SrcRow(p.Table.Sel())
	// Single Θ(m) scan builds a prefix sum; count/offset/end are all O(1)
	// (SICP-style memoization).
	heights := sheetBodyEntryHeightsCols(f, matched, dataRow, keyW, valW)
	total := heights[len(matched)]
	curStart := heights[p.SheetField]
	curEnd := heights[p.SheetField+1] - 1
	maxOff := max(0, total-visible)
	if curStart < p.SheetOff {
		p.SheetOff = curStart
	} else if curEnd >= p.SheetOff+visible {
		p.SheetOff = curEnd - visible + 1
		if p.SheetOff > curStart {
			p.SheetOff = curStart
		}
	}
	p.SheetOff = clamp(p.SheetOff, 0, maxOff)
}

// handleSheetEditKey routes keys while a sheet field is being inline-edited.
// esc/ctrl+c cancel (keeping the original value); ctrl+s commits via the
// "confirmsave" command; printable runes insert at the cursor; backspace
// deletes before the cursor; left/right/home/end/ctrl+a/ctrl+e move the
// cursor; ctrl+u clears the buffer. After each mutation the right-pane
// value scroll is re-clamped. Any other key is swallowed so the field does
// not move.
func (a *App) handleSheetEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := a.pane()
	if p == nil {
		return a, nil
	}
	f := p.Current()
	if f == nil {
		return a, nil
	}
	key := msg.String()
	switch key {
	case "esc", "ctrl+c":
		p.SheetEditing = false
		p.SheetEdit = LineInput{}
		return a, nil
	case "ctrl+s":
		if p.SheetField < 0 || p.SheetField >= f.NumCols() {
			return a, nil
		}
		a.pendingEdit = &PendingEdit{
			Frame:     f,
			Row:       p.Table.SrcRow(p.Table.Sel()),
			Col:       p.SheetField,
			ColName:   f.Columns[p.SheetField].Name,
			OldValue:  f.CellString(p.Table.SrcRow(p.Table.Sel()), p.SheetField),
			NewValue:  p.SheetEdit.String(),
			Table:     a.BaseCrumb(),
			Namespace: a.CurrentTableNamespace(),
		}
		return a, a.runCommand("confirmsave", "")
	default:
		// The shared line-input machine covers backspace/delete/cursor moves/
		// ctrl+u and unmodified printable inserts; every other key is
		// swallowed so the field does not move.
		p.SheetEdit.Handle(msg)
	}
	// Re-clamp the right-pane value scroll to the edit buffer length.
	_, valW := sheetTableGeometry(f, a.width)
	// drawer header: fieldname(type) + separator + hint = 3 lines
	visible := max(1, max(1, a.height-1)-3)
	count := sheetValueLineCount(f, p.Table.Sel(), p.SheetField, valW)
	if editing := p.SheetEdit.Len(); editing > 0 {
		count = strings.Count(ansi.Wrap(p.SheetEdit.String(), valW, ""), "\n") + 1
	}
	maxOff := max(0, count-visible)
	// Keep the cursor line in view while typing.
	curLine, _ := sheetEditLineOf([]rune(p.SheetEdit.String()), p.SheetEdit.Cursor(), valW)
	if curLine < p.SheetValOff {
		p.SheetValOff = curLine
	} else if curLine >= p.SheetValOff+visible {
		p.SheetValOff = curLine - visible + 1
	}
	p.SheetValOff = clamp(p.SheetValOff, 0, maxOff)
	return a, nil
}

func (a *App) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		a.search.Cancel()
		if p := a.pane(); p != nil {
			p.Table.ClampTo(p.Current())
		}
		return a, nil
	case "enter":
		f, crumb, ok := a.search.Commit()
		if p := a.pane(); ok && p != nil {
			p.Push(f, crumb)
		} else if p != nil {
			p.Table.ClampTo(p.Current())
		}
		return a, nil
	case "backspace":
		a.search.Backspace()
	default:
		if t := msg.Key().Text; t != "" {
			a.search.Type(t)
		}
	}
	if p := a.pane(); p != nil {
		p.Table.ClampTo(a.search.Preview())
	}
	return a, nil
}

// dispatch executes a semantic action from the keymap.
func (a *App) dispatch(action, key string) tea.Cmd {
	p := a.pane()
	var f *tuidata.Frame
	if p != nil {
		f = p.Current()
	}
	nrows, ncols := 0, 0
	if f != nil {
		nrows, ncols = f.NumRows(), f.NumCols()
	}

	switch action {
	// global
	case ActQuit:
		return tea.Quit
	case ActHelp:
		return a.runCommand("help", "")
	case ActPalette:
		return a.runCommand("palette", "")
	case ActTabSwitch:
		return a.runCommand("tabs", "")
	case ActPrevTab:
		if len(a.tabs) > 1 {
			a.active = (a.active - 1 + len(a.tabs)) % len(a.tabs)
		}
	case ActNextTab:
		if len(a.tabs) > 1 {
			a.active = (a.active + 1) % len(a.tabs)
		}
	case ActEscBack:
		// Handled before the pane check: on an empty db-mode workspace esc
		// reopens the connection form.
		return a.escBack()
	}

	if p == nil || f == nil {
		return nil
	}

	switch action {
	// table navigation
	case ActUp:
		p.Table.Move(-1, nrows)
	case ActDown:
		p.Table.Move(1, nrows)
	case ActTop:
		p.Table.Top()
	case ActBottom:
		p.Table.Bottom(nrows)
	case ActHalfUp:
		p.Table.HalfPageUp(nrows)
	case ActHalfDown:
		p.Table.HalfPageDown(nrows)
	case ActPageUp:
		p.Table.PageUp(nrows)
	case ActPageDown:
		p.Table.PageDown(nrows)
	case ActRandom:
		p.Table.Random(nrows)
	case ActLeft:
		p.Table.PrevCol(ncols)
	case ActRight:
		p.Table.NextCol(ncols)
	case ActFirstCol:
		p.Table.FirstCol()
	case ActLastCol:
		p.Table.LastCol(ncols)
	case ActExpand:
		p.Table.ToggleExpanded()
	case ActColGrow:
		if ncols > 0 {
			p.Table.ResizeCol(p.Table.ColCursor(), colWidthStep)
		}
	case ActColShrink:
		if ncols > 0 {
			p.Table.ResizeCol(p.Table.ColCursor(), -colWidthStep)
		}
	case ActColReset:
		p.Table.ResetColWidths()
	case ActSort:
		if f != nil && ncols > 0 {
			p.Table.SortBy(f, p.Table.ColCursor())
		}
	case ActColPageLeft:
		p.Table.PageColLeft(ncols)
	case ActColPageRight:
		p.Table.PageColRight(ncols)
	case ActEdit:
		// Toggle inline edit on the cursored sheet field. Only meaningful in
		// sheet mode; in table mode e is unbound. While already editing, e is
		// a no-op (esc cancels, ctrl+s commits).
		if p.Mode != ModeSheet || f == nil || nrows == 0 || ncols == 0 {
			return nil
		}
		if p.SheetEditing {
			return nil
		}
		val := f.CellString(p.Table.SrcRow(p.Table.Sel()), p.SheetField)
		p.SheetEdit = NewLineInput(val)
		p.SheetEditing = true
		p.SheetValOff = 0
		return nil
	case ActRefresh:
		if a.backend != nil {
			return a.runCommand("refreshtable", "")
		}
		if a.kv != nil {
			return a.setToast("use r in the key browser to rescan")
		}
		return a.setToast("nothing to refresh")
	case ActJsonView:
		if p != nil && p.Mode == ModeSheet {
			p.JsonViewOff = 0
			p.Mode = ModeJsonView
		}
		return nil
	case ActToggleSelect:
		if p != nil && f != nil && nrows > 0 {
			p.Table.ToggleSelect(p.Table.Sel())
		}
		return nil
	case ActDelete:
		return a.startDelete()

	case ActColFocus:
		if ncols == 0 {
			return nil
		}
		if p.ColZoomed {
			// Exit zoom: restore saved TableCols and cursor position.
			p.ColZoomed = false
			p.TableCols = p.savedTableCols
			p.savedTableCols = nil
			p.HScroll = 0
			// Restore column cursor to where it was before zoom.
			targetCols := len(p.TableCols)
			if targetCols == 0 {
				targetCols = ncols
			}
			p.Table.SetColCursor(p.savedColCursor, targetCols)
		} else {
			// Zoom into the column under the cursor.
			cur := p.Table.ColCursor()
			originalIdx := cur
			if len(p.TableCols) > 0 && cur < len(p.TableCols) {
				originalIdx = p.TableCols[cur]
			}
			p.savedTableCols = p.TableCols
			p.savedColCursor = cur // save sub-frame cursor position
			p.ColZoomed = true
			p.TableCols = []int{originalIdx}
			p.HScroll = 0
		}
		return nil

	// mode switches
	case ActSheet:
		if nrows > 0 {
			p.Mode = ModeSheet
			p.SheetOff = 0
			p.SheetField = 0
			p.SheetValOff = 0
			p.SheetEditing = false
			p.SheetEdit = LineInput{}
			p.SheetFilter = LineInput{}
			p.SheetFiltering = false
			// Warm the column-metadata cache for this table so the sheet shows
			// the real engine type (e.g. "varchar(255)") instead of the frame
			// DType fallback ("str" on mysql). Skipped when the cache is already
			// populated or in file mode (no backend -> warmColumnMeta is nil).
			if a.backend != nil && a.columnMetaFor(p.Title) == nil {
				return a.warmColumnMeta(p.Title)
			}
		}
	case ActSheetFilter:
		// "/" enters filter input with a fresh buffer. While already filtering
		// this is never reached: handleKey routes to handleSheetFilterKey
		// before dispatch, so a literal "/" lands as a printable rune.
		p.SheetFilter = LineInput{}
		p.SheetFiltering = true
		return nil
	case ActBack:
		p.Mode = ModeTable
	case ActSheetDown, ActSheetUp:
		matched := sheetMatchedCols(f, p.SheetFilter.String())
		if len(matched) == 0 {
			return nil
		}
		delta := 1
		if action == ActSheetUp {
			delta = -1
		}
		p.SheetField = clamp(p.SheetField+delta, 0, len(matched)-1)
		// A new field shows its value from the top, and cancels any in-flight
		// inline edit (the edit buffer belongs to the previous field).
		p.SheetValOff = 0
		if p.SheetEditing {
			p.SheetEditing = false
			p.SheetEdit = LineInput{}
		}
		a.edgeFollowSheet(f, p)
	case ActCopy:
		text := rowClipboardText(f, p.Table.SrcRow(p.Table.Sel()))
		return tea.Batch(tea.SetClipboard(text), a.setToast("copied to clipboard"))
	case ActCopyCell:
		if nrows == 0 || ncols == 0 {
			return nil
		}
		c := clamp(p.Table.ColCursor(), 0, ncols-1)
		r := clamp(p.Table.SrcRow(p.Table.Sel()), 0, nrows-1)
		return tea.Batch(
			tea.SetClipboard(f.CellString(r, c)),
			a.setToast("copied cell "+f.Columns[c].Name),
		)
	case ActCopyRow:
		if nrows == 0 {
			return nil
		}
		r := clamp(p.Table.SrcRow(p.Table.Sel()), 0, nrows-1)
		return tea.Batch(
			tea.SetClipboard(rowClipboardText(f, r)),
			a.setToast(fmt.Sprintf("copied row %d", r+1)),
		)

	// searches / commands
	case ActFuzzy:
		if len(p.TableCols) > 0 {
			p.HScroll = 0
			a.search.StartCols(f, false, p.TableCols)
		} else {
			a.search.Start(f, true)
		}
	case ActExact:
		a.search.Start(f, false)
	case ActInfo:
		return a.runCommand("info", "")
	case ActGoto:
		return a.runCommand("goto", key)

	// stack / tabs
	case ActPop:
		return a.pop()
	}
	return nil
}

// pop implements "q": sheet -> table, stack pop, close tab, quit on last.
func (a *App) pop() tea.Cmd {
	p := a.pane()
	if p == nil {
		return tea.Quit
	}
	if p.Mode == ModeSheet {
		p.Mode = ModeTable
		return nil
	}
	if p.Pop() {
		return nil
	}
	return a.closeTab(a.active)
}

// startDelete gathers the rows to delete (the multi-select set, or the cursor
// row when nothing is selected) and hands off to the confirm-delete popup. In
// database mode it first resolves the table's primary keys: a table without a
// primary key cannot be safely deleted and is rejected with a toast. File mode
// always has an implicit identity (row index) so it skips the PK check.
func (a *App) startDelete() tea.Cmd {
	p := a.pane()
	if p == nil {
		return nil
	}
	f := p.Current()
	if f == nil {
		return nil
	}
	nrows := f.NumRows()
	if nrows == 0 {
		return nil
	}
	rows := p.Table.SelectedRows()
	if len(rows) == 0 {
		rows = []int{clamp(p.Table.Sel(), 0, nrows-1)}
	}
	// The grid may be sorted (render-time permutation); map display rows to
	// the source row each delete targets in the (unchanged) frame.
	for i := range rows {
		rows[i] = p.Table.SrcRow(rows[i])
	}
	if a.backend != nil {
		table := a.BaseCrumb()
		ns := a.CurrentTableNamespace()
		pks, err := a.backend.PrimaryKeys(ns, table)
		if err != nil {
			e := err
			return func() tea.Msg { return ErrorMsg{Err: e} }
		}
		if len(pks) == 0 {
			return a.setToast("no primary key, cannot delete")
		}
		a.pendingDelete = &PendingDelete{Frame: f, Rows: rows, Table: table, Namespace: ns}
		return a.runCommand("confirmdelete", "")
	}
	return a.setToast("no connection, cannot delete")
}

// escBack implements esc's "go back one level" in table mode. Unlike pop it
// never closes a tab and never quits: sheet -> table, stack pop, and on the
// base frame it steps back OUT to the browser overlay (db mode) or the
// connection form (empty db-mode workspace). In file mode the base frame is
// the bottom of the hierarchy and esc does nothing.
func (a *App) escBack() tea.Cmd {
	dbMode := a.backend != nil || a.kv != nil
	p := a.pane()
	if p == nil {
		// Empty workspace: back to the connection form (registered by the
		// db-mode integration as the "connect" factory).
		if dbMode {
			return a.runCommand("connect", "")
		}
		return nil
	}
	if p.Mode == ModeSheet {
		p.Mode = ModeTable
		return nil
	}
	if p.Pop() {
		return nil
	}
	// Base frame: one level up is the schema browser (SQL backends) or the
	// key browser (the db-mode integration wraps the "schema" factory to
	// serve it when a key-value connection is live).
	if dbMode {
		return a.runCommand("schema", "")
	}
	return nil
}

// --- commands ---------------------------------------------------------------------

// runCommand resolves a command name: reserved built-ins first, then the
// popup factory registry.
func (a *App) runCommand(name, arg string) tea.Cmd {
	switch name {
	case "quit":
		return tea.Quit
	case "reset":
		if p := a.pane(); p != nil {
			p.Reset()
		}
		return nil
	case "toggleborders":
		a.showBorders = !a.showBorders
		a.persistUI()
		return nil
	case "togglerownumbers":
		a.showRowNumbers = !a.showRowNumbers
		a.persistUI()
		return nil
	case "saveedit":
		if a.pendingEdit == nil {
			return nil
		}
		pe := *a.pendingEdit
		a.pendingEdit = nil
		if p := a.pane(); p != nil {
			p.SheetEditing = false
		}
		return commitCellEdit(a, pe)
	case "canceledit":
		a.pendingEdit = nil
		if p := a.pane(); p != nil {
			p.SheetEditing = false
		}
		return a.setToast("edit canceled")
	case "deleterows":
		if a.pendingDelete == nil {
			return nil
		}
		pd := *a.pendingDelete
		a.pendingDelete = nil
		if p := a.pane(); p != nil {
			p.Table.ClearSelect()
		}
		return commitRowDelete(a, pd)
	case "canceldelete":
		a.pendingDelete = nil
		if p := a.pane(); p != nil {
			p.Table.ClearSelect()
		}
		return nil
	case "format":
		if len(a.overlays) == 0 {
			return a.setToast("nothing to format")
		}
		if f, ok := a.overlays[len(a.overlays)-1].(FormattableOverlay); ok {
			f.FormatSQL()
			return a.setToast("formatted")
		}
		return a.setToast("current view cannot be formatted")
	}

	factory, ok := Factories[name]
	if !ok {
		// The search bar is part of the app itself; serve these directly
		// when no popup has claimed the names.
		switch name {
		case "search", "fuzzysearch":
			if f := a.CurrentFrame(); f != nil {
				a.search.Start(f, name == "fuzzysearch")
			}
			return nil
		}
		a.overlays = append(a.overlays, errBox{err: fmt.Errorf("unknown command: %s", name)})
		return nil
	}
	ov, err := factory(a, arg)
	if err != nil {
		a.overlays = append(a.overlays, errBox{err: err})
		return nil
	}
	if ov != nil {
		return a.pushOverlay(ov)
	}
	return nil
}

// pushOverlay appends an overlay to the stack and starts its Init command
// when it has one (OverlayIniter).
func (a *App) pushOverlay(ov Overlay) tea.Cmd {
	a.overlays = append(a.overlays, ov)
	if in, ok := ov.(OverlayIniter); ok {
		return in.Init()
	}
	return nil
}

// PushOverlay adds an overlay before the program starts. Database mode uses
// it to show the connection form on top of the empty workspace.
func (a *App) PushOverlay(ov Overlay) {
	if ov != nil {
		a.overlays = append(a.overlays, ov)
	}
}

// applyFrame handles ApplyFrameMsg: push onto the originating pane's stack or
// open a new tab.
func (a *App) applyFrame(m ApplyFrameMsg) tea.Cmd {
	if m.Frame == nil {
		return nil
	}
	newTab := m.NewTab || len(a.tabs) == 0
	var target *Pane
	if !newTab {
		if m.PaneID != 0 {
			// Route the result back to the pane the command was run from; the
			// user may have switched tabs while it executed. A vanished pane
			// (tab closed mid-flight) falls back to opening a new tab.
			target = a.paneByID(m.PaneID)
			newTab = target == nil
		} else {
			target = a.pane()
			newTab = target == nil
		}
	}
	if newTab {
		title := m.TabTitle
		if title == "" {
			title = m.Crumb
		}
		if title == "" {
			title = "result"
		}
		p := NewPane(title, m.Frame)
		p.TableCols = m.TableCols
		p.sourceNS, p.sourceTable = m.SourceNamespace, m.SourceTable
		a.tabs = append(a.tabs, p)
		a.active = len(a.tabs) - 1
	} else {
		crumb := m.Crumb
		if crumb == "" {
			crumb = "op"
		}
		target.Push(m.Frame, crumb)
	}
	return nil
}

// replaceBase handles ReplaceBaseMsg: swap the base frame of the target pane
// (used by the refresh command to reload the current table in place).
func (a *App) replaceBase(m ReplaceBaseMsg) tea.Cmd {
	var target *Pane
	if m.PaneID != 0 {
		target = a.paneByID(m.PaneID)
	}
	if target == nil {
		target = a.pane()
	}
	if target == nil || m.Frame == nil {
		return nil
	}
	target.ReplaceBase(m.Frame)
	return nil
}

// paneByID finds an open pane by its stable ID; nil when it was closed.
func (a *App) paneByID(id int) *Pane {
	for _, p := range a.tabs {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

func (a *App) closeTab(i int) tea.Cmd {
	if len(a.tabs) == 0 {
		return tea.Quit
	}
	i = clamp(i, 0, len(a.tabs)-1)
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if len(a.tabs) == 0 {
		return tea.Quit
	}
	// Closing a tab before the active one shifts the slice left; follow the
	// active pane so the user keeps looking at the same tab.
	if i < a.active {
		a.active--
	}
	if a.active >= len(a.tabs) {
		a.active = len(a.tabs) - 1
	}
	return nil
}

func (a *App) setToast(text string) tea.Cmd {
	a.toastText = text
	a.toastID++
	id := a.toastID
	return tea.Tick(toastDuration, func(time.Time) tea.Msg {
		return toastExpiredMsg{id: id}
	})
}

// persistUI 记录 UI 偏好的意图位。主题/边框/行号的持久化由 env 桥接负责
// （见 P4 主题桥接）；此处不再写配置文件。
func (a *App) persistUI() {}

func (a *App) pane() *Pane {
	if len(a.tabs) == 0 {
		return nil
	}
	if a.active >= len(a.tabs) {
		a.active = len(a.tabs) - 1
	}
	return a.tabs[a.active]
}

// --- AppContext ----------------------------------------------------------------

func (a *App) CurrentFrame() *tuidata.Frame {
	if p := a.pane(); p != nil {
		return p.Current()
	}
	return nil
}

func (a *App) CurrentRow() int {
	if p := a.pane(); p != nil {
		return p.Table.Sel()
	}
	return 0
}

// SheetFieldCursor reports the selected field index in sheet mode. It is
// always a valid column index for the current frame, or 0 when no pane is
// open.
func (a *App) SheetFieldCursor() int {
	if p := a.pane(); p != nil {
		return p.SheetField
	}
	return 0
}

// CurrentTableNamespace reports the namespace the active table was loaded
// from, so the refresh command can re-fetch the same table. It consults the
// completion cache's table-to-namespace map first, then falls back to the
// backend's connected namespace (when the backend exposes one), and finally
// returns "" (the backend then treats it as the default namespace).
func (a *App) CurrentTableNamespace() string {
	if a.backend == nil {
		return ""
	}
	if p := a.pane(); p != nil {
		c := a.completionCache()
		c.mu.Lock()
		ns := c.ns[p.Title]
		c.mu.Unlock()
		if ns != "" {
			return ns
		}
	}
	return ""
}

// warmColumnMeta returns a tea.Cmd that fetches the real column metadata for
// table off the update loop. The resulting columnMetaMsg populates the
// completion cache, so the next View shows the engine-native type inline with
// the value instead of the frame DType fallback. The cache is touched under
// lock only when the connection still matches by the time the fetch returns.
// Returns nil in file mode (no backend).
func (a *App) warmColumnMeta(table string) tea.Cmd {
	be := a.backend
	if be == nil || table == "" {
		return nil
	}
	ns := a.CurrentTableNamespace()
	tableC := table
	return func() tea.Msg {
		m, err := be.ColumnsMeta(ns, tableC)
		return columnMetaMsg{table: tableC, meta: m, err: err}
	}
}

// cursorFieldType returns just the data-type token of the active sheet field
// (e.g. "int", "varchar(255)", "i64"). It reuses cursorFieldMeta so the same
// cache-then-DType fallback path applies: real engine type when the cache is
// warm, the frame DType otherwise. Empty when no pane/frame is active.
func (a *App) cursorFieldType() string {
	dt, _, _, _ := a.cursorFieldMeta()
	return dt
}

func (a *App) BaseCrumb() string {
	if p := a.pane(); p != nil {
		return p.Title
	}
	return ""
}

// TableSource reports the live table the active pane's base frame was
// fetched from (namespace may be empty for engines without namespaces; the
// table is empty when the pane did not originate from a table fetch).
func (a *App) TableSource() (namespace, table string) {
	if p := a.pane(); p != nil {
		return p.sourceNS, p.sourceTable
	}
	return "", ""
}

func (a *App) Crumbs() []string {
	if p := a.pane(); p != nil {
		return p.Crumbs()
	}
	return nil
}

func (a *App) ColumnNames() []string {
	if f := a.CurrentFrame(); f != nil {
		return f.ColumnNames()
	}
	return nil
}

func (a *App) TableNames() []string {
	var names []string
	if a.backend != nil {
		if nss, err := a.backend.Namespaces(); err == nil {
			for _, ns := range nss {
				if ts, err := a.backend.Tables(ns); err == nil {
					names = append(names, ts...)
				}
			}
		}
	}
	return names
}

func (a *App) Backend() backend.Backend { return a.backend }
func (a *App) KV() backend.KVBackend    { return a.kv }

// Client 实现 AppContext：暴露 transport.Client 给连接表单/选择器。
func (a *App) Client() transport.Client { return a.cli }

func (a *App) Theme() *theme.Theme { return a.th }
func (a *App) ThemeName() string   { return a.themeName }

func (a *App) ShowBorders() bool    { return a.showBorders }
func (a *App) ShowRowNumbers() bool { return a.showRowNumbers }

func (a *App) Tabs() []TabInfo {
	out := make([]TabInfo, len(a.tabs))
	for i, p := range a.tabs {
		shape := "0 x 0"
		if f := p.Current(); f != nil {
			shape = fmt.Sprintf("%d x %d", f.NumRows(), f.NumCols())
		}
		out[i] = TabInfo{Title: p.Title, Shape: shape}
	}
	return out
}

func (a *App) ActiveTab() int { return a.active }

func (a *App) ActivePaneID() int {
	if p := a.pane(); p != nil {
		return p.ID()
	}
	return 0
}

// PendingEdit reports the in-flight cell edit awaiting commit, or nil when
// no edit is in progress. Confirm popups read it to render the proposed
// change; the "saveedit" command consumes it.
func (a *App) PendingEdit() *PendingEdit { return a.pendingEdit }

// PendingDelete reports the in-flight row delete awaiting commit, or nil
// when no delete is in progress.
func (a *App) PendingDelete() *PendingDelete { return a.pendingDelete }

func (a *App) searchQuery() string {
	if a.search.Active() {
		return a.search.Query()
	}
	return ""
}

// compile-time interface checks
var (
	_ tea.Model  = (*App)(nil)
	_ AppContext = (*App)(nil)
)
