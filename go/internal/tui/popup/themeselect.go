// Package popup contains the modal overlay components (command palette,
// prompts, wizards, selectors) that plug into the app through the
// tui.Factories registry.
package popup

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func init() {
	tui.Factories["theme"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		return newThemeSelect(ctx.ThemeName(), arg), nil
	}
}

// themeSelect is a theme picker with live preview: moving the highlight
// immediately applies the theme so the whole app behind the popup previews
// it. Enter keeps the highlighted theme, esc reverts to the original.
type themeSelect struct {
	names    []string // all built-in theme names, sorted
	filtered []string // names matching the current filter, best first
	input    tui.LineInput
	sel      int    // highlighted index within filtered
	offset   int    // first visible list row
	original string // theme active when the popup opened (esc restores it)
	preview  string // last theme name emitted as a preview
}

const (
	themeListMaxRows = 16
	themePageJump    = 10
)

func newThemeSelect(original, arg string) *themeSelect {
	t := &themeSelect{
		names:    theme.Names(),
		input:    tui.NewLineInput(strings.TrimSpace(arg)),
		original: original,
		preview:  original,
	}
	t.input.Placeholder = " type to filter"
	t.refilter()
	for i, n := range t.filtered {
		if n == original {
			t.sel = i
			break
		}
	}
	return t
}

// highlighted returns the currently highlighted theme name ("" if the
// filter matches nothing).
func (t *themeSelect) highlighted() string {
	if t.sel < 0 || t.sel >= len(t.filtered) {
		return ""
	}
	return t.filtered[t.sel]
}

// refilter recomputes the filtered list from the input, trying to keep the
// highlight on the same theme name.
func (t *themeSelect) refilter() {
	// Prefer to keep the highlight where it was; if the filter had wiped it
	// out, fall back to the last previewed (i.e. currently applied) theme.
	keep := t.highlighted()
	if keep == "" {
		keep = t.preview
	}
	pattern := strings.TrimSpace(t.input.String())
	if pattern == "" {
		t.filtered = t.names
	} else {
		matches := fuzzy.Find(pattern, t.names)
		t.filtered = make([]string, len(matches))
		for i, m := range matches {
			t.filtered[i] = m.Str
		}
	}
	t.sel = 0
	for i, n := range t.filtered {
		if n == keep {
			t.sel = i
			break
		}
	}
	t.offset = 0
}

// previewCmd emits a SetThemeMsg for the highlighted theme when it changed
// since the last emission, giving the live preview behaviour.
func (t *themeSelect) previewCmd() tea.Cmd {
	name := t.highlighted()
	if name == "" || name == t.preview {
		return nil
	}
	t.preview = name
	return func() tea.Msg { return tui.SetThemeMsg{Name: name} }
}

// themeSetAndClose applies the named theme and closes the popup.
func themeSetAndClose(name string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return tui.SetThemeMsg{Name: name} },
		tui.CloseOverlay,
	)
}

func (t *themeSelect) move(delta int) tea.Cmd {
	if len(t.filtered) == 0 {
		return nil
	}
	t.sel += delta
	if t.sel < 0 {
		t.sel = 0
	}
	if t.sel > len(t.filtered)-1 {
		t.sel = len(t.filtered) - 1
	}
	return t.previewCmd()
}

func (t *themeSelect) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	if pm, ok := msg.(tea.PasteMsg); ok {
		if t.input.Paste(pm.Content) {
			t.refilter()
			return t, t.previewCmd()
		}
		return t, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return t, nil
	}
	switch key.String() {
	case "esc":
		return t, themeSetAndClose(t.original)
	case "enter":
		name := t.highlighted()
		if name == "" {
			name = t.original
		}
		return t, themeSetAndClose(name)
	case "up", "ctrl+p":
		return t, t.move(-1)
	case "down", "ctrl+n":
		return t, t.move(1)
	case "pgup":
		return t, t.move(-themePageJump)
	case "pgdown":
		return t, t.move(themePageJump)
	case "left":
		t.input.Left()
		return t, nil
	case "right":
		t.input.Right()
		return t, nil
	case "home", "ctrl+a":
		t.input.Home()
		return t, nil
	case "end", "ctrl+e":
		t.input.End()
		return t, nil
	case "backspace":
		if t.input.Backspace() {
			t.refilter()
			return t, t.previewCmd()
		}
		return t, nil
	case "ctrl+u":
		if t.input.Len() > 0 {
			t.input.Clear()
			t.refilter()
			return t, t.previewCmd()
		}
		return t, nil
	}
	if key.Text != "" {
		t.input.InsertKey(key.Text)
		t.refilter()
		return t, t.previewCmd()
	}
	return t, nil
}

// themeSwatch renders small colored blocks from the named palette's series
// colors so each row hints at what the theme looks like.
func themeSwatch(name string, bg lipgloss.Style) string {
	th, ok := theme.Builtin(name)
	if !ok {
		return ""
	}
	colors := th.Palette.Series
	if len(colors) == 0 {
		colors = []string{th.Palette.Accent}
	}
	if len(colors) > 5 {
		colors = colors[:5]
	}
	var b strings.Builder
	for _, c := range colors {
		b.WriteString(bg.Foreground(lipgloss.Color(c)).Render("██"))
	}
	return b.String()
}

// themeVisibleRows returns how many list rows fit in the given height.
func themeVisibleRows(height, n int) int {
	rows := height - 6 // box borders (2) + input (1) + hint (1) + margin (2)
	if rows > themeListMaxRows {
		rows = themeListMaxRows
	}
	if rows > n {
		rows = n
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// inputLine renders the filter prompt with the shared input renderer; the
// placeholder (set at construction) shows while the filter is empty.
func (t *themeSelect) inputLine(inner int, th *theme.Theme) string {
	return th.Subtle.Render("filter ") + t.input.Render(th)
}

// ensureVisible adjusts the scroll offset so the highlight stays in view.
func (t *themeSelect) ensureVisible(rows int) {
	if t.sel < t.offset {
		t.offset = t.sel
	}
	if t.sel >= t.offset+rows {
		t.offset = t.sel - rows + 1
	}
	if maxOff := len(t.filtered) - rows; t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

func (t *themeSelect) View(width, height int, th *theme.Theme) string {
	bw := width - 4
	if bw > 56 {
		bw = 56
	}
	if bw < 24 {
		bw = 24
	}
	inner := bw - 2

	rows := themeVisibleRows(height, len(t.filtered))
	t.ensureVisible(rows)

	var lines []string
	lines = append(lines, t.inputLine(inner, th))

	if len(t.filtered) == 0 {
		lines = append(lines, th.Placeholder.Render("  no matching themes"))
	}
	swatchW := 10 // up to 5 blocks of 2 cells
	nameW := inner - swatchW - 4
	if nameW < 8 {
		nameW = 8
	}
	for i := t.offset; i < t.offset+rows && i < len(t.filtered); i++ {
		name := t.filtered[i]
		label := name
		if len(label) > nameW {
			label = label[:nameW-1] + "…"
		}
		label = label + strings.Repeat(" ", nameW-len(label))
		var row string
		if i == t.sel {
			row = th.ListSelected.Render("▸ "+label) + " " + themeSwatch(name, th.ListSelected)
		} else {
			row = th.ListItem.Render("  "+label) + " " + themeSwatch(name, th.ListItem)
		}
		lines = append(lines, row)
	}

	lines = append(lines, th.Subtle.Render("enter keep · esc revert · ↑/↓ preview"))
	return tui.Box("theme", strings.Join(lines, "\n"), bw, th)
}
