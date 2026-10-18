package dbmode

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

// --- key browser -----------------------------------------------------------------

// keySection is one key-type group of the browser. Sections load lazily: the
// key list is only scanned when the section is first opened (or rescanned).
type keySection struct {
	keyType string
	tui.LazyGroup[string]
}

// keysLoadedMsg delivers one section's scan result.
type keysLoadedMsg struct {
	keyType string
	keys    []string
	err     error
}

// keyBrowser browses the keys of a live key-value connection, grouped by key
// type. Enter on a section header toggles it (triggering a lazy scan), enter
// on a key opens the value viewer, "r" rescans everything. "/" starts a
// substring filter that narrows the keys of the loaded sections; esc clears
// it before stepping back to the connection form.
type keyBrowser struct {
	kv       backend.KVBackend
	openTabs int // open tabs when the browser was built (see closeCmd)
	sections []keySection
	state    tui.ListState

	filter    tui.LineInput // substring the shown keys must contain
	filtering bool         // keys currently go to the filter input
}

func newKeyBrowser(kv backend.KVBackend, openTabs int) *keyBrowser {
	b := &keyBrowser{kv: kv, openTabs: openTabs}
	b.state.ViewLen = 10
	for _, kt := range KeyTypes {
		b.sections = append(b.sections, keySection{keyType: kt})
	}
	return b
}

// closeCmd is what q does at the top level of the browser: close it, back to
// the table if one is open. With no open tabs closing would leave a blank
// workspace, so it steps back to the connection form instead.
func (b *keyBrowser) closeCmd() tea.Cmd {
	if b.openTabs == 0 {
		return b.escCmd()
	}
	return tui.CloseOverlay
}

// escCmd is what esc does at the top level of the browser. The browser is a
// page in the login -> browser -> table stack, so esc always steps back to
// the connection form, whether or not tabs are open.
func (b *keyBrowser) escCmd() tea.Cmd {
	return tea.Sequence(
		tui.CloseOverlay,
		func() tea.Msg { return tui.RunCommandMsg{Name: "connect"} },
	)
}

// Fullscreen marks the browser as a full page (tui.FullscreenOverlay); it
// only exists in database mode.
func (b *keyBrowser) Fullscreen() bool { return true }

// Init opens the first section so the browser shows something immediately;
// the remaining sections stay lazy.
func (b *keyBrowser) Init() tea.Cmd {
	if len(b.sections) == 0 {
		return nil
	}
	b.sections[0].Open = true
	return b.scanSection(0)
}

// scanSection starts the asynchronous key scan for one section, dropping any
// cached keys first.
func (b *keyBrowser) scanSection(i int) tea.Cmd {
	s := &b.sections[i]
	kv, kt := b.kv, s.keyType
	return s.Reload(func() tea.Cmd {
		return func() tea.Msg {
			keys, err := kv.ScanKeys(kt)
			return keysLoadedMsg{keyType: kt, keys: keys, err: err}
		}
	})
}

// browserRow is one display line: a section header (key == "") or a key.
type browserRow struct {
	section int
	key     string // "" for headers and notes
	note    string // informational text for non-selectable note rows
}

func (r browserRow) selectable() bool { return r.note == "" }

// matchKeys returns the section keys that contain the filter substring
// (case-insensitive); with an empty filter every key passes.
func (b *keyBrowser) matchKeys(keys []string) []string {
	pat := strings.ToLower(strings.TrimSpace(b.filter.String()))
	if pat == "" {
		return keys
	}
	var out []string
	for _, k := range keys {
		if strings.Contains(strings.ToLower(k), pat) {
			out = append(out, k)
		}
	}
	return out
}

// rows flattens the sections into display lines, applying the key filter to
// the loaded sections.
func (b *keyBrowser) rows() []browserRow {
	filtered := strings.TrimSpace(b.filter.String()) != ""
	var out []browserRow
	for i := range b.sections {
		s := &b.sections[i]
		out = append(out, browserRow{section: i})
		if !s.Open {
			continue
		}
		switch {
		case s.Loading:
			out = append(out, browserRow{section: i, note: "loading..."})
		case s.Err != nil:
			out = append(out, browserRow{section: i, note: "error: " + s.Err.Error()})
		case len(s.Items) == 0:
			out = append(out, browserRow{section: i, note: "(no keys)"})
		default:
			keys := b.matchKeys(s.Items)
			if len(keys) == 0 && filtered {
				out = append(out, browserRow{section: i, note: "(no matching keys)"})
				continue
			}
			for _, k := range keys {
				out = append(out, browserRow{section: i, key: k})
			}
		}
	}
	return out
}

func (b *keyBrowser) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case keysLoadedMsg:
		for i := range b.sections {
			s := &b.sections[i]
			if s.keyType != m.keyType {
				continue
			}
			s.Deliver(m.keys, m.err)
		}
		tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
		return b, nil

	case tea.KeyPressMsg:
		if b.filtering {
			return b.filterKey(m)
		}
		rows := b.rows()
		switch m.String() {
		case "q":
			return b, b.closeCmd()
		case "esc":
			if b.filter.Len() > 0 {
				b.filter.Clear()
				tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
				return b, nil
			}
			return b, b.escCmd()
		case "/":
			b.filtering = true
			return b, nil
		case "up", "k":
			tui.MoveListCursor(&b.state, rows, -1, browserRow.selectable)
		case "down", "j":
			tui.MoveListCursor(&b.state, rows, +1, browserRow.selectable)
		case "g", "home":
			b.state.Cursor = 0
		case "G", "end":
			b.state.Cursor = len(rows) - 1
			if b.state.Cursor >= 0 && !rows[b.state.Cursor].selectable() {
				tui.MoveListCursor(&b.state, rows, -1, browserRow.selectable)
			}
		case "r":
			return b, b.rescan()
		case "enter":
			return b, b.activate(rows)
		}
		b.state.ScrollToCursor(len(rows), b.state.ViewLen)
		return b, nil
	}
	return b, nil
}

// filterKey handles one key press while the filter input is focused.
func (b *keyBrowser) filterKey(m tea.KeyPressMsg) (tui.Overlay, tea.Cmd) {
	switch m.String() {
	case "esc":
		// esc clears the filter (and leaves the input); a second esc on the
		// browser then closes it.
		b.filter.Clear()
		b.filtering = false
		tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
		return b, nil
	case "enter":
		// Keep the narrowed list and give the keys back to navigation.
		b.filtering = false
		return b, nil
	case "up":
		tui.MoveListCursor(&b.state, b.rows(), -1, browserRow.selectable)
		b.state.ScrollToCursor(len(b.rows()), b.state.ViewLen)
		return b, nil
	case "down":
		tui.MoveListCursor(&b.state, b.rows(), +1, browserRow.selectable)
		b.state.ScrollToCursor(len(b.rows()), b.state.ViewLen)
		return b, nil
	case "backspace":
		if b.filter.Backspace() {
			tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
		}
		return b, nil
	case "ctrl+u":
		b.filter.Clear()
		tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
		return b, nil
	case "ctrl+c":
		return b, tui.CloseOverlay
	}
	if t := m.Text; t != "" {
		if b.filter.InsertKey(t) {
			tui.ClampListCursor(&b.state, b.rows(), browserRow.selectable)
		}
	}
	return b, nil
}

// rescan reloads every open section and marks the rest unloaded.
func (b *keyBrowser) rescan() tea.Cmd {
	var cmds []tea.Cmd
	for i := range b.sections {
		s := &b.sections[i]
		if s.Open {
			if cmd := b.scanSection(i); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else {
			s.Unload()
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// activate handles enter: toggle a section (lazily scanning it) or open the
// value viewer for a key.
func (b *keyBrowser) activate(rows []browserRow) tea.Cmd {
	if b.state.Cursor < 0 || b.state.Cursor >= len(rows) {
		return nil
	}
	r := rows[b.state.Cursor]
	if !r.selectable() {
		return nil
	}
	if r.key == "" { // section header
		s := &b.sections[r.section]
		s.Open = !s.Open
		if s.Open && !s.Loaded {
			return b.scanSection(r.section)
		}
		return nil
	}
	kv, key := b.kv, r.key
	viewer := newValueViewer(key, func() (string, error) { return kv.Value(key) })
	return func() tea.Msg { return tui.PushOverlayMsg{Overlay: viewer} }
}

func (b *keyBrowser) View(width, height int, th *theme.Theme) string {
	boxW := width - 4
	if boxW > 70 {
		boxW = 70
	}
	if boxW < 30 {
		boxW = 30
	}
	inner := boxW - 2

	rows := b.rows()
	showFilter := b.filtering || b.filter.Len() > 0
	avail := height - 2 - 1 // borders + footer hint
	if showFilter {
		avail-- // filter line
	}
	if avail < 3 {
		avail = 3
	}
	if avail > len(rows) {
		avail = len(rows)
	}
	b.state.ViewLen = avail
	if b.state.ViewLen < 1 {
		b.state.ViewLen = 1
	}
	b.state.ScrollToCursor(len(rows), b.state.ViewLen)

	var lines []string
	if showFilter {
		lines = append(lines, tui.FilterLine(b.filter.String(), b.filtering, inner, "type to filter keys", th))
	}
	for i := b.state.Offset; i < b.state.Offset+b.state.ViewLen && i < len(rows); i++ {
		r := rows[i]
		s := b.sections[r.section]
		switch {
		case r.note != "":
			lines = append(lines, th.Subtle.Render(ansi.Truncate("   "+r.note, inner, "…")))
		case r.key == "":
			marker := "+"
			if s.Open {
				marker = "-"
			}
			label := " " + marker + " " + s.keyType
			if i == b.state.Cursor {
				lines = append(lines, th.ListSelected.Render(tui.Pad(label, inner)))
			} else {
				lines = append(lines, th.PopupTitle.Render(ansi.Truncate(label, inner, "…")))
			}
		case i == b.state.Cursor:
			if b.filter.Len() > 0 {
				prefix := " > "
				keyW := inner - ansi.StringWidth(prefix)
				if keyW < 0 {
					keyW = 0
				}
				lines = append(lines, th.ListSelected.Render(prefix)+tui.HighlightPad(r.key, b.filter.String(), keyW, th.Match, th.ListSelected))
			} else {
				lines = append(lines, th.ListSelected.Render(tui.Pad(" > "+r.key, inner)))
			}
		default:
			if b.filter.Len() > 0 {
				prefix := "   "
				keyW := inner - ansi.StringWidth(prefix)
				if keyW < 0 {
					keyW = 0
				}
				lines = append(lines, th.ListItem.Render(prefix)+tui.HighlightPad(r.key, b.filter.String(), keyW, th.Match, th.ListItem))
			} else {
				lines = append(lines, th.ListItem.Render(tui.Pad("   "+r.key, inner)))
			}
		}
	}
	hint := " enter open · / filter · r rescan · q close · esc back"
	if b.filtering {
		hint = " enter done · esc clear · ↑↓ move"
	} else if b.filter.Len() > 0 {
		hint = " enter open · / edit filter · esc clear filter"
	}
	lines = append(lines, th.Subtle.Render(hint))

	return tui.Box("keys "+b.kv.Title(), strings.Join(lines, "\n"), boxW, th)
}

// --- value viewer -----------------------------------------------------------------

// valueLoadedMsg delivers the asynchronously fetched value text.
type valueLoadedMsg struct {
	text string
	err  error
}

// valueViewer shows one scrollable block of text (a rendered key value or a
// raw-command result). With a non-nil loader it fetches the text on Init.
type valueViewer struct {
	title   string
	loader  func() (string, error)
	loading bool
	text    string
	errText string
	offset  int
	pageLen int
}

// newValueViewer builds a viewer that loads its text asynchronously when
// shown.
func newValueViewer(title string, loader func() (string, error)) *valueViewer {
	return &valueViewer{title: title, loader: loader, loading: true, pageLen: 10}
}

// newValueViewerText builds a viewer around already-available text.
func newValueViewerText(title, text string) *valueViewer {
	return &valueViewer{title: title, text: text, pageLen: 10}
}

// Fullscreen marks the viewer as a full page (tui.FullscreenOverlay); it
// only exists in database mode.
func (v *valueViewer) Fullscreen() bool { return true }

func (v *valueViewer) Init() tea.Cmd {
	if v.loader == nil {
		v.loading = false
		return nil
	}
	load := v.loader
	return func() tea.Msg {
		text, err := load()
		return valueLoadedMsg{text: text, err: err}
	}
}

func (v *valueViewer) lines() []string {
	switch {
	case v.loading:
		return []string{"loading..."}
	case v.errText != "":
		return strings.Split(v.errText, "\n")
	default:
		return strings.Split(v.text, "\n")
	}
}

func (v *valueViewer) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case valueLoadedMsg:
		v.loading = false
		if m.err != nil {
			v.errText = m.err.Error()
		} else {
			v.text = m.text
		}
		v.offset = 0
		return v, nil

	case tea.KeyPressMsg:
		n := len(v.lines())
		switch m.String() {
		case "q", "esc":
			return v, tui.CloseOverlay
		case "up", "k":
			v.offset--
		case "down", "j":
			v.offset++
		case "pgup", "ctrl+b":
			v.offset -= v.pageLen
		case "pgdown", "ctrl+f":
			v.offset += v.pageLen
		case "g", "home":
			v.offset = 0
		case "G", "end":
			v.offset = n - v.pageLen
		}
		if maxOff := n - v.pageLen; v.offset > maxOff {
			v.offset = maxOff
		}
		if v.offset < 0 {
			v.offset = 0
		}
		return v, nil
	}
	return v, nil
}

func (v *valueViewer) View(width, height int, th *theme.Theme) string {
	boxW := width - 4
	if boxW > 76 {
		boxW = 76
	}
	if boxW < 30 {
		boxW = 30
	}
	inner := boxW - 2

	all := v.lines()
	avail := height - 2 - 1 // borders + footer hint
	if avail < 3 {
		avail = 3
	}
	if avail > len(all) {
		avail = len(all)
	}
	v.pageLen = avail
	if maxOff := len(all) - avail; v.offset > maxOff {
		v.offset = maxOff
	}
	if v.offset < 0 {
		v.offset = 0
	}

	style := th.Text
	if v.errText != "" {
		style = th.Error
	}
	if v.loading {
		style = th.Subtle
	}
	var lines []string
	for i := v.offset; i < v.offset+avail && i < len(all); i++ {
		lines = append(lines, style.Render(ansi.Truncate(" "+all[i], inner, "…")))
	}
	hint := " ↑↓ scroll · q/esc back"
	if len(all) > avail {
		hint = strings.TrimRight(hint, " ") + " · " +
			viewerRange(v.offset, avail, len(all))
	}
	lines = append(lines, th.Subtle.Render(hint))

	title := v.title
	if title == "" {
		title = "value"
	}
	return tui.Box(title, strings.Join(lines, "\n"), boxW, th)
}

func viewerRange(off, page, total int) string {
	end := off + page
	if end > total {
		end = total
	}
	return fmt.Sprintf("%d-%d of %d", off+1, end, total)
}

// --- raw command prompt --------------------------------------------------------------

// redisDoneMsg reports the outcome of one raw command execution.
type redisDoneMsg struct {
	command string
	out     string
	err     error
}

// redisPrompt is a single-line raw-command input with completion from the
// built-in command reference. Tab applies/cycles suggestions, enter runs the
// command asynchronously and shows the result in the value viewer.
type redisPrompt struct {
	kv      backend.KVBackend
	input   tui.LineInput
	sugg    []CommandHelp
	sel     int
	cycling bool // true while tab is cycling through sugg
	errText string
	running bool
}

func newRedisPrompt(ctx tui.AppContext, arg string) *redisPrompt {
	p := &redisPrompt{kv: ctx.KV(), input: tui.NewLineInput(arg)}
	p.recompute()
	return p
}

// recompute refreshes the suggestion list: command names that the current
// input is a case-insensitive prefix of.
func (p *redisPrompt) recompute() {
	p.sugg = nil
	p.sel = 0
	p.cycling = false
	text := strings.ToUpper(strings.TrimLeft(p.input.String(), " "))
	if text == "" {
		return
	}
	for _, h := range CommandHelps {
		if strings.HasPrefix(h.Command, text) {
			p.sugg = append(p.sugg, h)
			if len(p.sugg) == maxPromptSuggestions {
				return
			}
		}
	}
}

// maxPromptSuggestions caps the completion dropdown size.
const maxPromptSuggestions = 8

// applyTab applies the highlighted suggestion; repeated tabs cycle through
// the list (the suggestion list is intentionally not recomputed so cycling
// keeps working after the first application).
func (p *redisPrompt) applyTab() {
	if len(p.sugg) == 0 {
		return
	}
	if p.cycling {
		p.sel = (p.sel + 1) % len(p.sugg)
	} else {
		p.cycling = true
	}
	p.input.Set(p.sugg[p.sel].Command + " ")
}

// startRun executes the current input asynchronously. A second run while one
// is in flight is ignored.
func (p *redisPrompt) startRun() tea.Cmd {
	fields := strings.Fields(p.input.String())
	if p.running || len(fields) == 0 {
		return nil
	}
	p.running = true
	p.errText = ""
	kv, command := p.kv, strings.Join(fields, " ")
	return func() tea.Msg {
		out, err := kv.Do(fields)
		return redisDoneMsg{command: command, out: out, err: err}
	}
}

func (p *redisPrompt) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case redisDoneMsg:
		p.running = false
		if m.err != nil {
			p.errText = m.err.Error()
			return p, nil
		}
		// Replace the prompt with the result viewer, titled with the command.
		return newValueViewerText(m.command, m.out), nil

	case tea.KeyPressMsg:
		switch m.String() {
		case "esc", "ctrl+c":
			return p, tui.CloseOverlay
		case "enter":
			return p, p.startRun()
		case "tab":
			p.applyTab()
			return p, nil
		case "up":
			if n := len(p.sugg); n > 0 {
				p.sel = (p.sel - 1 + n) % n
			}
			return p, nil
		case "down":
			if n := len(p.sugg); n > 0 {
				p.sel = (p.sel + 1) % n
			}
			return p, nil
		case "backspace":
			p.input.Backspace()
			p.recompute()
			return p, nil
		case "ctrl+u":
			p.input.Clear()
			p.recompute()
			return p, nil
		}
		if t := m.Text; t != "" {
			if p.input.InsertKey(t) {
				p.recompute()
			}
		}
		return p, nil
	}
	return p, nil
}

func (p *redisPrompt) View(width, height int, th *theme.Theme) string {
	boxW := width - 4
	if boxW > 70 {
		boxW = 70
	}
	if boxW < 30 {
		boxW = 30
	}
	inner := boxW - 2

	// Input line with the tail kept visible while typing long commands.
	val := p.input.String()
	room := inner - 4
	if room < 1 {
		room = 1
	}
	if r := []rune(val); len(r) > room {
		val = "…" + string(r[len(r)-room+1:])
	}
	cur := th.ListSelected.Render(" ")
	lines := []string{th.Text.Render(" > ") + th.Input.Render(val) + cur}

	for i, h := range p.sugg {
		detail := " " + h.Args
		if h.Desc != "" {
			detail += " — " + h.Desc
		}
		name := " " + h.Command
		if i == p.sel {
			line := name + detail
			lines = append(lines, th.ListSelected.Render(tui.Pad(line, inner)))
		} else {
			row := th.ListItem.Render(name) + th.Subtle.Render(detail)
			lines = append(lines, ansi.Truncate(row, inner, "…"))
		}
	}

	lines = append(lines, "")
	switch {
	case p.running:
		lines = append(lines, th.Warning.Render(" running..."))
	case p.errText != "":
		for _, l := range strings.Split(ansi.Wrap(p.errText, inner-2, ""), "\n") {
			lines = append(lines, th.Error.Render(" "+l))
		}
	default:
		lines = append(lines, th.Placeholder.Render(" enter run · tab complete · esc close"))
	}

	return tui.Box("redis command", strings.Join(lines, "\n"), boxW, th)
}

// compile-time interface checks
var (
	_ tui.Overlay           = (*keyBrowser)(nil)
	_ tui.OverlayIniter     = (*keyBrowser)(nil)
	_ tui.FullscreenOverlay = (*keyBrowser)(nil)
	_ tui.Overlay           = (*valueViewer)(nil)
	_ tui.OverlayIniter     = (*valueViewer)(nil)
	_ tui.FullscreenOverlay = (*valueViewer)(nil)
	_ tui.Overlay           = (*redisPrompt)(nil)
)
