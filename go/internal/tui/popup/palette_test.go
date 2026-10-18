package popup

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func paletteTestTheme() *theme.Theme {
	return theme.New(theme.Palette{
		Name: "test",
		Bg:   "#000000", Fg: "#ffffff", BgSoft: "#111111", FgDim: "#888888",
		Header: "#ffcc00", Accent: "#3355ff", AccentFg: "#ffffff", Highlight: "#00ffcc",
		Error: "#ff0000", Warning: "#ffaa00", Success: "#00ff00",
		Series: []string{"#111111", "#222222", "#333333", "#444444"},
	})
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeString(t *testing.T, p *palette, s string) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	for _, r := range s {
		k := keyPress(string(r))
		if r == ' ' {
			k = keyPress("space")
		}
		var ov tui.Overlay
		ov, cmd = p.Update(k)
		if cmd != nil {
			return cmd // early dispatch (alias + space)
		}
		if ov != p {
			t.Fatalf("Update replaced the overlay unexpectedly")
		}
	}
	return cmd
}

func TestPaletteResolve(t *testing.T) {
	cases := map[string]string{
		"q": "query", "s": "select", "f": "filter", "o": "order",
		"sort": "order", "themeselector": "theme", "filter": "filter",
		"QUIT": "quit", " help ": "help",
	}
	for in, want := range cases {
		got, ok := paletteResolve(in)
		if !ok || got != want {
			t.Errorf("paletteResolve(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	if _, ok := paletteResolve("nosuchcmd"); ok {
		t.Error("paletteResolve accepted an unknown command")
	}
	if _, ok := paletteResolve(""); ok {
		t.Error("paletteResolve accepted empty input")
	}
}

func TestPaletteFilterEmptyListsAll(t *testing.T) {
	got := paletteFilter("")
	if len(got) != len(Commands) {
		t.Fatalf("empty filter returned %d commands, want %d", len(got), len(Commands))
	}
	for i, ci := range got {
		if ci != i {
			t.Fatalf("empty filter not in table order at %d: got %d", i, ci)
		}
	}
}

func TestPaletteFilterFuzzy(t *testing.T) {
	got := paletteFilter("fltr")
	if len(got) == 0 || Commands[got[0]].Name != "filter" {
		t.Fatalf("filter(fltr) best match = %v, want filter first", got)
	}
	// Exact alias match ranks its command first and dedupes name+alias hits.
	got = paletteFilter("q")
	if len(got) == 0 || Commands[got[0]].Name != "query" {
		t.Fatalf("filter(q) best match not query: %v", got)
	}
	seen := map[int]bool{}
	for _, ci := range got {
		if seen[ci] {
			t.Fatalf("filter(q) returned command %d twice", ci)
		}
		seen[ci] = true
	}
	if got := paletteFilter("zzzzqx"); len(got) != 0 {
		t.Fatalf("nonsense pattern matched %v", got)
	}
}

func TestPaletteDispatchTargetSelection(t *testing.T) {
	p := newPalette("")
	if name, arg, ok := p.dispatchTarget(); !ok || name != Commands[0].Name || arg != "" {
		t.Fatalf("default target = %q %q %v, want %q", name, arg, ok, Commands[0].Name)
	}
	// Arrow down selects the second command.
	p.Update(keyPress("down"))
	if name, _, ok := p.dispatchTarget(); !ok || name != Commands[1].Name {
		t.Fatalf("after down, target = %q, want %q", name, Commands[1].Name)
	}
}

func TestPaletteInlineArgFullName(t *testing.T) {
	p := newPalette("")
	typeString(t, p, "filter age > 30")
	name, arg, ok := p.dispatchTarget()
	if !ok || name != "filter" || arg != "age > 30" {
		t.Fatalf("got %q %q %v; want filter %q", name, arg, ok, "age > 30")
	}
}

func TestPaletteAliasSpaceDispatchesImmediately(t *testing.T) {
	for alias, want := range map[string]string{"f": "filter", "s": "select", "o": "order", "q": "query"} {
		p := newPalette("")
		typeString(t, p, alias)
		_, cmd := p.Update(keyPress("space"))
		if cmd == nil {
			t.Fatalf("typing %q + space produced no command", alias)
		}
		if got := p.input.String(); got != alias {
			t.Fatalf("input mutated on dispatch: %q", got)
		}
		_ = want
	}
	// Non-alias single letters just insert the space.
	p := newPalette("")
	typeString(t, p, "x")
	_, cmd := p.Update(keyPress("space"))
	if cmd != nil {
		t.Fatal("'x ' should not dispatch")
	}
	if p.input.String() != "x " {
		t.Fatalf("input = %q, want %q", p.input.String(), "x ")
	}
}

func TestPaletteEnterUnknownFallsThrough(t *testing.T) {
	p := newPalette("")
	typeString(t, p, "zzzzqx dothing")
	name, arg, ok := p.dispatchTarget()
	if !ok || name != "zzzzqx" || arg != "dothing" {
		t.Fatalf("unknown token target = %q %q %v", name, arg, ok)
	}
}

func TestPaletteEditingKeys(t *testing.T) {
	p := newPalette("")
	typeString(t, p, "abc")
	p.Update(keyPress("left"))
	p.Update(keyPress("backspace")) // deletes 'b'
	if p.input.String() != "ac" || p.input.Cursor() != 1 {
		t.Fatalf("after edits input=%q cursor=%d, want ac/1", p.input.String(), p.input.Cursor())
	}
	typeString(t, p, "X") // insert at cursor
	if p.input.String() != "aXc" {
		t.Fatalf("mid insert got %q, want aXc", p.input.String())
	}
	p.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // kill to start
	if p.input.String() != "c" || p.input.Cursor() != 0 {
		t.Fatalf("ctrl+u got %q cursor=%d", p.input.String(), p.input.Cursor())
	}
}

func TestPaletteSelectionClampAndRefilterReset(t *testing.T) {
	p := newPalette("")
	p.Update(keyPress("up")) // no-op at top
	if p.sel != 0 {
		t.Fatalf("sel = %d after up at top", p.sel)
	}
	for i := 0; i < len(Commands)+5; i++ {
		p.Update(keyPress("down"))
	}
	if p.sel != len(p.filtered)-1 {
		t.Fatalf("sel = %d, want %d", p.sel, len(p.filtered)-1)
	}
	typeString(t, p, "q")
	if p.sel != 0 {
		t.Fatalf("sel not reset on refilter: %d", p.sel)
	}
}

func TestPaletteView(t *testing.T) {
	th := paletteTestTheme()
	p := newPalette("")
	out := p.View(80, 24, th)
	if !strings.Contains(out, "query") || !strings.Contains(out, "commands") {
		t.Fatalf("view missing expected content:\n%s", out)
	}
	// Small sizes must not panic.
	_ = p.View(10, 3, th)
	_ = newPalette("").View(0, 0, th)
	typeString(t, p, "fltr")
	out = p.View(80, 24, th)
	if !strings.Contains(out, "filter") {
		t.Fatalf("filtered view missing match:\n%s", out)
	}
}

func TestPaletteFactoryRegistered(t *testing.T) {
	f, ok := tui.Factories["palette"]
	if !ok {
		t.Fatal("palette factory not registered")
	}
	ov, err := f(nil, "")
	if err != nil || ov == nil {
		t.Fatalf("factory returned %v, %v", ov, err)
	}
}

func TestPalettePaste(t *testing.T) {
	p := newPalette("")

	// The whole chunk lands as one edit and refilters the list.
	ov, cmd := p.Update(tea.PasteMsg{Content: "theme"})
	if ov != tui.Overlay(p) || cmd != nil {
		t.Fatal("paste must not replace the overlay or dispatch")
	}
	if p.input.String() != "theme" {
		t.Fatalf("input = %q, want theme", p.input.String())
	}
	if len(p.filtered) == 0 || Commands[p.filtered[0]].Name != "theme" {
		t.Fatalf("filter after paste: %v", p.filtered)
	}

	// Newlines and CR are flattened to spaces.
	p = newPalette("")
	p.Update(tea.PasteMsg{Content: "filter\r\nage > 1"})
	if p.input.String() != "filter age > 1" {
		t.Fatalf("flattened input = %q", p.input.String())
	}

	// Pasting a space after a single-letter alias must NOT trigger the
	// typed-space alias dispatch: a paste is never an implicit enter.
	p = newPalette("q")
	_, cmd = p.Update(tea.PasteMsg{Content: " select 1"})
	if cmd != nil {
		t.Fatal("paste dispatched a command")
	}
	if p.input.String() != "q select 1" {
		t.Fatalf("input = %q, want %q", p.input.String(), "q select 1")
	}

	// Empty paste is a no-op.
	p = newPalette("x")
	p.input.SetCursor(0)
	p.Update(tea.PasteMsg{Content: "\r"})
	if p.input.String() != "x" || p.input.Cursor() != 0 {
		t.Fatalf("empty paste changed state: %q cursor %d", p.input.String(), p.input.Cursor())
	}
}
