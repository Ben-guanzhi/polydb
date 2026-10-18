package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
)

// SanitizePaste flattens bracketed-paste text for a single-line input:
// newlines and tabs become single spaces (so multi-line content keeps its
// token boundaries), while carriage returns and all other control
// characters are dropped.
func SanitizePaste(s string) string {
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

// LineInput is the single-line editor state machine shared by every popup
// input line: a rune buffer plus a rune-index cursor.
//
// Invariants, restored by every method before it returns:
//   - 0 <= cursor <= len(text)
//   - text holds only printable runes for key/paste input (control
//     characters are dropped at the entry points), verbatim for Insert
//
// Keys with overlay-level meaning (enter, esc, tab, up/down, ...) are not
// part of the machine: callers intercept them before Handle and keep the
// commit/cancel semantics to themselves.
type LineInput struct {
	text   []rune
	cursor int

	// Placeholder, when non-empty, is rendered after an empty cursor cell
	// while the buffer is empty (View and Render only).
	Placeholder string
}

// NewLineInput returns an input prefilled with s, cursor at the end.
func NewLineInput(prefill string) LineInput {
	r := []rune(prefill)
	return LineInput{text: r, cursor: len(r)}
}

func (in *LineInput) String() string { return string(in.text) }

// Len reports the buffer size in runes; the cursor is a rune index, so Len
// is its upper bound.
func (in *LineInput) Len() int { return len(in.text) }

// Cursor reports the insertion point, a rune index in [0, Len].
func (in *LineInput) Cursor() int { return in.cursor }

// ByteCursor converts the rune cursor to the byte offset of the same
// position inside String().
func (in *LineInput) ByteCursor() int { return len(string(in.text[:in.cursor])) }

// SetCursor moves the insertion point, clamped into [0, Len].
func (in *LineInput) SetCursor(c int) {
	if c < 0 {
		c = 0
	}
	if c > len(in.text) {
		c = len(in.text)
	}
	in.cursor = c
}

// Set replaces the whole content and puts the cursor at the end.
func (in *LineInput) Set(s string) {
	in.text = []rune(s)
	in.cursor = len(in.text)
}

// insertRunes splices rs at the cursor as one edit; the cursor follows the
// inserted text. Reports whether anything was inserted.
func (in *LineInput) insertRunes(rs []rune) bool {
	if len(rs) == 0 {
		return false
	}
	in.text = append(in.text[:in.cursor], append(rs, in.text[in.cursor:]...)...)
	in.cursor += len(rs)
	return true
}

// Insert splices s at the cursor verbatim. Callers pre-filter when the
// source text is untrusted.
func (in *LineInput) Insert(s string) bool { return in.insertRunes([]rune(s)) }

// InsertKey splices key text at the cursor, dropping control characters
// (everything below space plus DEL) so no key press carries a line break
// into a single-line input. Reports whether anything was inserted.
func (in *LineInput) InsertKey(t string) bool {
	if t == "" {
		return false
	}
	ins := make([]rune, 0, len(t))
	for _, r := range t {
		if r >= ' ' && r != 0x7f {
			ins = append(ins, r)
		}
	}
	return in.insertRunes(ins)
}

// Paste splices pasted text at the cursor as one edit, flattened to a
// single line first. Reports whether the input changed.
func (in *LineInput) Paste(s string) bool {
	return in.insertRunes([]rune(SanitizePaste(s)))
}

// Backspace deletes the rune before the cursor; no-op at position 0.
func (in *LineInput) Backspace() bool {
	if in.cursor == 0 {
		return false
	}
	in.text = append(in.text[:in.cursor-1], in.text[in.cursor:]...)
	in.cursor--
	return true
}

// Delete deletes the rune under the cursor; no-op at the end.
func (in *LineInput) Delete() bool {
	if in.cursor >= len(in.text) {
		return false
	}
	in.text = append(in.text[:in.cursor], in.text[in.cursor+1:]...)
	return true
}

// Left moves the cursor one rune back; clamped at 0.
func (in *LineInput) Left() { in.SetCursor(in.cursor - 1) }

// Right moves the cursor one rune forward; clamped at Len.
func (in *LineInput) Right() { in.SetCursor(in.cursor + 1) }

// Home moves the cursor to the start of the line.
func (in *LineInput) Home() { in.cursor = 0 }

// End moves the cursor to the end of the line.
func (in *LineInput) End() { in.cursor = len(in.text) }

// CtrlU kills the text from the start of the line up to the cursor; no-op
// when the cursor already sits at position 0.
func (in *LineInput) CtrlU() bool {
	if in.cursor == 0 {
		return false
	}
	in.text = append([]rune{}, in.text[in.cursor:]...)
	in.cursor = 0
	return true
}

// Clear wipes the whole buffer (the "clear input" ctrl+u variant used by
// callers without a cursor concept).
func (in *LineInput) Clear() {
	in.text = nil
	in.cursor = 0
}

// DeleteWordBack deletes the run of spaces and then the run of non-spaces
// before the cursor (ctrl+w word kill); no-op at position 0.
func (in *LineInput) DeleteWordBack() bool {
	if in.cursor == 0 {
		return false
	}
	start := in.cursor
	for start > 0 && in.text[start-1] == ' ' {
		start--
	}
	for start > 0 && in.text[start-1] != ' ' {
		start--
	}
	in.text = append(in.text[:start], in.text[in.cursor:]...)
	in.cursor = start
	return true
}

// Handle applies one editing key press. It reports whether the key was
// consumed and whether the text changed. Printable text only lands when the
// key is unmodified (no ctrl/alt chord): modified combos are never inserted
// as literal text. ctrl+h is accepted as backspace alongside "backspace".
func (in *LineInput) Handle(msg tea.KeyPressMsg) (handled, changed bool) {
	switch msg.String() {
	case "backspace", "ctrl+h":
		return true, in.Backspace()
	case "delete":
		return true, in.Delete()
	case "left":
		in.Left()
		return true, false
	case "right":
		in.Right()
		return true, false
	case "home", "ctrl+a":
		in.Home()
		return true, false
	case "end", "ctrl+e":
		in.End()
		return true, false
	case "ctrl+u":
		return true, in.CtrlU()
	case "ctrl+w":
		return true, in.DeleteWordBack()
	}
	if t := msg.Text; t != "" && msg.Mod == 0 {
		if !in.InsertKey(t) {
			return false, false
		}
		return true, true
	}
	return false, false
}

// View renders the input with a visible cursor cell, horizontally scrolled
// so the cursor stays inside width w (assumes narrow runes, which holds for
// SQL and path text). With a placeholder set and an empty buffer it renders
// the placeholder after an empty cursor cell instead.
//
// Window invariant: start <= cursor <= start+w-1, i.e. the rendered window
// [start, start+w) always contains the cursor.
func (in *LineInput) View(w int, th *theme.Theme) string {
	if len(in.text) == 0 && in.Placeholder != "" {
		return th.ListSelected.Render(" ") + th.Placeholder.Render(in.Placeholder)
	}
	if w < 2 {
		w = 2
	}
	start := 0
	if in.cursor > w-1 {
		start = in.cursor - (w - 1)
	}
	before := string(in.text[start:in.cursor])
	curCh := " "
	after := ""
	if in.cursor < len(in.text) {
		curCh = string(in.text[in.cursor])
		rest := in.text[in.cursor+1:]
		if maxAfter := w - (in.cursor - start) - 1; len(rest) > maxAfter {
			if maxAfter < 0 {
				maxAfter = 0
			}
			rest = rest[:maxAfter]
		}
		after = string(rest)
	}
	return th.Input.Render(before) + th.Input.Reverse(true).Render(curCh) + th.Input.Render(after)
}

// Render draws the full buffer with a block cursor, without horizontal
// scrolling (for inputs whose caller guarantees or truncates the width).
// With a placeholder set and an empty buffer it renders the placeholder
// after an empty cursor cell instead.
func (in *LineInput) Render(th *theme.Theme) string {
	if len(in.text) == 0 && in.Placeholder != "" {
		return th.ListSelected.Render(" ") + th.Placeholder.Render(in.Placeholder)
	}
	before := string(in.text[:in.cursor])
	under, after := " ", ""
	if in.cursor < len(in.text) {
		under = string(in.text[in.cursor])
		after = string(in.text[in.cursor+1:])
	}
	return th.Input.Render(before) + th.ListSelected.Render(under) + th.Input.Render(after)
}
