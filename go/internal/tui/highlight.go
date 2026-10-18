package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// HighlightSubstr finds all case-insensitive occurrences of needle in s and
// wraps them with matchStyle; surrounding text uses baseStyle.
// Returns baseStyle.Render(s) when needle is empty or not found.
func HighlightSubstr(s, needle string, matchStyle, baseStyle lipgloss.Style) string {
	if needle == "" {
		return baseStyle.Render(s)
	}
	lower := strings.ToLower(s)
	lowerNeedle := strings.ToLower(needle)
	runes := []rune(s)
	lowerRunes := []rune(lower)
	needleRunes := []rune(lowerNeedle)
	nLen := len(needleRunes)
	if nLen == 0 {
		return baseStyle.Render(s)
	}

	var b strings.Builder
	i := 0
	found := false
	for i <= len(runes)-nLen {
		match := true
		for j := 0; j < nLen; j++ {
			if lowerRunes[i+j] != needleRunes[j] {
				match = false
				break
			}
		}
		if match {
			found = true
			b.WriteString(matchStyle.Render(string(runes[i : i+nLen])))
			i += nLen
		} else {
			b.WriteString(baseStyle.Render(string(runes[i : i+1])))
			i++
		}
	}
	// Remaining runes after last match window
	if i < len(runes) {
		b.WriteString(baseStyle.Render(string(runes[i:])))
	}
	if !found {
		return baseStyle.Render(s)
	}
	return b.String()
}

// HighlightRunes wraps the rune positions listed in indices with matchStyle;
// all other runes use baseStyle. Indices must be valid 0-based rune positions in s.
func HighlightRunes(s string, indices []int, matchStyle, baseStyle lipgloss.Style) string {
	if len(indices) == 0 {
		return baseStyle.Render(s)
	}
	runes := []rune(s)
	idxSet := make(map[int]bool, len(indices))
	for _, i := range indices {
		idxSet[i] = true
	}

	// Merge consecutive runs of same-style runes into single Render calls for efficiency.
	var b strings.Builder
	var segment []rune
	inMatch := false
	flush := func(isMatch bool) {
		if len(segment) == 0 {
			return
		}
		text := string(segment)
		if isMatch {
			b.WriteString(matchStyle.Render(text))
		} else {
			b.WriteString(baseStyle.Render(text))
		}
		segment = segment[:0]
	}
	for i, r := range runes {
		match := idxSet[i]
		if match != inMatch && len(segment) > 0 {
			flush(inMatch)
			inMatch = match
		} else {
			inMatch = match
		}
		segment = append(segment, r)
	}
	flush(inMatch)
	return b.String()
}

// HighlightPad is like padLine but uses HighlightSubstr on the content before
// padding. The returned string already contains ANSI codes so ansi.StringWidth
// must be used (not len) when measuring it.
func HighlightPad(s, needle string, w int, matchStyle, baseStyle lipgloss.Style) string {
	// Strip existing ANSI for width measurement after highlighting.
	highlighted := HighlightSubstr(s, needle, matchStyle, baseStyle)
	// Truncate by display width if needed.
	visW := ansi.StringWidth(highlighted)
	if visW > w {
		// Truncate the plain string first, then re-highlight.
		plain := ansi.Strip(highlighted)
		plain = ansi.Truncate(plain, w, "")
		highlighted = HighlightSubstr(plain, needle, matchStyle, baseStyle)
		visW = ansi.StringWidth(highlighted)
	}
	if pad := w - visW; pad > 0 {
		highlighted += baseStyle.Render(strings.Repeat(" ", pad))
	}
	return highlighted
}
