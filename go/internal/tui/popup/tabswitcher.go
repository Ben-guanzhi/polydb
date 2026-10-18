package popup

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func init() {
	tui.Factories["tabs"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		tabs := ctx.Tabs()
		if len(tabs) == 0 {
			return nil, fmt.Errorf("tabs: no tabs open")
		}
		o := &tabSwitcher{
			tabs:   tabs,
			cursor: tabClamp(ctx.ActiveTab(), 0, len(tabs)-1),
		}
		o.refilter()
		return o, nil
	}
}

// tabFilterThreshold is the tab count above which the switcher grows a
// type-to-filter line. Below it the single-letter shortcuts (j/k/g/G, x/d,
// q/t) keep working as before.
const tabFilterThreshold = 8

// tabSwitcher lists the open tabs; enter jumps, x/d (ctrl+x while the filter
// is active) closes the highlighted tab. It works on a snapshot of the tab
// list and mirrors closes locally so the shown indices stay in sync with the
// app. With many tabs a fuzzy filter line narrows the list as you type.
type tabSwitcher struct {
	tabs     []tui.TabInfo
	filtered []int   // positions into tabs shown by the list (filter applied)
	matchPos [][]int // fuzzy match rune positions per filtered entry (parallel to filtered)
	query    []rune  // filter text (only used above the threshold)
	cursor   int     // index into filtered

	offset   int // first visible list row
	viewRows int // list rows shown by the last View (page size)
}

// filterEnabled reports whether the type-to-filter line is active: many tabs,
// or a leftover filter that still needs clearing after closes shrank the list.
func (o *tabSwitcher) filterEnabled() bool {
	return len(o.tabs) > tabFilterThreshold || len(o.query) > 0
}

// refilter recomputes the visible tab positions from the query and keeps the
// cursor inside the list.
func (o *tabSwitcher) refilter() {
	titles := make([]string, len(o.tabs))
	for i, t := range o.tabs {
		titles[i] = t.Title
	}
	q := strings.TrimSpace(string(o.query))
	if q == "" {
		o.filtered = fuzzyIndices("", titles)
		o.matchPos = nil
	} else {
		o.filtered, o.matchPos = fuzzyMatches(q, titles)
	}
	o.cursor = tabClamp(o.cursor, 0, max(0, len(o.filtered)-1))
}

// ensureFiltered lazily initialises the filtered view (defensive for
// direct-construction paths).
func (o *tabSwitcher) ensureFiltered() {
	if o.filtered == nil {
		o.refilter()
	}
}

// tabKey applies one key and returns the resulting command (nil for pure
// navigation). It reports whether the overlay should close.
func (o *tabSwitcher) tabKey(key string) (cmd tea.Cmd, close bool) {
	o.ensureFiltered()
	page := max(1, o.viewRows)
	filterOn := o.filterEnabled()

	switch key {
	case "esc":
		if len(o.query) > 0 {
			o.query = nil
			o.refilter()
			o.cursor = 0
			return nil, false
		}
		return nil, true
	case "ctrl+c":
		return nil, true
	case "up":
		o.cursor--
	case "down":
		o.cursor++
	case "pgup", "ctrl+b":
		o.cursor -= page
	case "pgdown", "ctrl+d", "ctrl+f":
		o.cursor += page
	case "ctrl+u":
		if len(o.query) > 0 {
			o.query = nil
			o.refilter()
			o.cursor = 0
			return nil, false
		}
		o.cursor -= page
	case "backspace":
		if len(o.query) > 0 {
			o.query = o.query[:len(o.query)-1]
			o.refilter()
			o.cursor = 0
		}
		return nil, false
	case "enter":
		if len(o.filtered) == 0 {
			return nil, true
		}
		idx := o.filtered[tabClamp(o.cursor, 0, len(o.filtered)-1)]
		return func() tea.Msg { return tui.JumpToTabMsg{Index: idx} }, true
	case "ctrl+x":
		return o.closeCurrent()
	default:
		if !filterOn {
			switch key {
			case "q", "t":
				return nil, true
			case "k":
				o.cursor--
			case "j":
				o.cursor++
			case "g", "home":
				o.cursor = 0
			case "G", "end":
				o.cursor = len(o.filtered) // clamped below
			case "x", "d":
				return o.closeCurrent()
			}
		} else if r := []rune(key); len(r) == 1 && unicode.IsPrint(r[0]) {
			o.query = append(o.query, r[0])
			o.refilter()
			o.cursor = 0
			return nil, false
		}
	}
	o.cursor = tabClamp(o.cursor, 0, max(0, len(o.filtered)-1))
	return nil, false
}

// closeCurrent closes the highlighted tab, mirroring the removal locally so
// later CloseTabMsg indices stay correct.
func (o *tabSwitcher) closeCurrent() (tea.Cmd, bool) {
	if len(o.tabs) == 0 {
		return nil, true
	}
	if len(o.filtered) == 0 {
		return nil, false
	}
	idx := o.filtered[tabClamp(o.cursor, 0, len(o.filtered)-1)]
	cmd := func() tea.Msg { return tui.CloseTabMsg{Index: idx} }
	o.tabs = append(o.tabs[:idx:idx], o.tabs[idx+1:]...)
	if len(o.tabs) == 0 {
		// Closing the last tab quits the app; nothing left to show.
		return cmd, true
	}
	o.refilter()
	return cmd, false
}

func (o *tabSwitcher) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	cmd, close := o.tabKey(key.String())
	if close {
		if cmd != nil {
			return o, tea.Batch(cmd, tui.CloseOverlay)
		}
		return o, tui.CloseOverlay
	}
	return o, cmd
}

func (o *tabSwitcher) View(width, height int, th *theme.Theme) string {
	o.ensureFiltered()
	boxW := tabClamp(60, 24, max(24, width-4))
	inner := boxW - 2
	filterOn := o.filterEnabled()

	avail := height - 2 - 1 // borders + footer hint
	if filterOn {
		avail-- // filter line
	}
	if avail < 3 {
		avail = 3
	}
	if avail > len(o.filtered) {
		avail = len(o.filtered)
	}
	o.viewRows = max(1, avail)
	o.cursor = tabClamp(o.cursor, 0, max(0, len(o.filtered)-1))

	// Keep the cursor inside the visible window.
	if o.cursor < o.offset {
		o.offset = o.cursor
	}
	if o.cursor >= o.offset+o.viewRows {
		o.offset = o.cursor - o.viewRows + 1
	}
	o.offset = tabClamp(o.offset, 0, max(0, len(o.filtered)-o.viewRows))

	var lines []string
	if filterOn {
		line := tui.FilterLine(string(o.query), true, inner, "type to filter", th)
		lines = append(lines, line)
	}
	if len(o.filtered) == 0 {
		lines = append(lines, th.Placeholder.Render("  no matching tabs"))
	}
	for i := o.offset; i < o.offset+o.viewRows && i < len(o.filtered); i++ {
		idx := o.filtered[i]
		t := o.tabs[idx]
		suffix := fmt.Sprintf(" (%s)", t.Shape)
		prefix := fmt.Sprintf(" %d: ", idx+1)
		if len(o.query) > 0 && o.matchPos != nil && i < len(o.matchPos) {
			// Highlight the title portion; keep the numeric prefix and shape suffix plain.
			baseStyle := th.ListItem
			if i == o.cursor {
				baseStyle = th.ListSelected
			}
			highlighted := tui.HighlightRunes(t.Title, o.matchPos[i], th.Match, baseStyle)
			plainPrefix := baseStyle.Render(prefix)
			plainSuffix := baseStyle.Render(suffix)
			// Calculate total display width: prefix + title + suffix
			totalW := ansi.StringWidth(prefix) + ansi.StringWidth(t.Title) + ansi.StringWidth(suffix)
			padW := max(0, inner-totalW)
			line := plainPrefix + highlighted + plainSuffix + baseStyle.Render(strings.Repeat(" ", padW))
			lines = append(lines, line)
		} else {
			text := fmt.Sprintf("%s%s%s", prefix, t.Title, suffix)
			text = ansi.Truncate(text, inner, "…")
			if pad := inner - ansi.StringWidth(text); pad > 0 {
				text += strings.Repeat(" ", pad)
			}
			if i == o.cursor {
				lines = append(lines, th.ListSelected.Render(text))
			} else {
				lines = append(lines, th.ListItem.Render(text))
			}
		}
	}
	hint := " enter jump  •  x/d close tab  •  esc cancel"
	if filterOn {
		hint = " enter jump  •  ctrl+x close tab  •  esc clear/cancel"
	}
	lines = append(lines, th.Subtle.Render(hint))

	return tui.Box("tabs", strings.Join(lines, "\n"), boxW, th)
}

func tabClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
