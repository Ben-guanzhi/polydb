package popup

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
)

func TestPromptFactoriesRegistered(t *testing.T) {
	for _, kind := range []string{"select", "filter", "order"} {
		f, ok := tui.Factories[kind]
		if !ok {
			t.Errorf("Factories[%q] not registered", kind)
			continue
		}
		ov, err := f(qxLiveCtx(), "")
		if err != nil {
			t.Errorf("%s factory: %v", kind, err)
			continue
		}
		p, ok := ov.(*qxPrompt)
		if !ok {
			t.Errorf("%s factory returned %T", kind, ov)
			continue
		}
		if p.kind != kind {
			t.Errorf("kind = %q", p.kind)
		}
	}
}

func TestPromptTitleShowsTemplate(t *testing.T) {
	ov, _ := tui.Factories["filter"](qxLiveCtx(), "")
	p := ov.(*qxPrompt)
	if want := `SELECT * FROM "people" WHERE …`; p.title != want {
		t.Errorf("title = %q, want %q", p.title, want)
	}
	out := p.View(80, 24, qxTestTheme())
	if !strings.Contains(out, `WHERE`) {
		t.Error("view does not show the expansion template")
	}
}

func TestPromptInteractiveExecute(t *testing.T) {
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, cols: []string{"name", "age"}, base: "people"}
	ov, _ := tui.Factories["filter"](ctx, "")
	ov, _ = qxTypeText(ov, "age >= 2")
	_, cmd := qxSend(ov, "enter")
	msgs := qxDrain(cmd)

	if _, ok := qxFindMsg[tui.CloseOverlayMsg](msgs); !ok {
		t.Errorf("enter did not close the prompt: %v", msgs)
	}
	apply, ok := qxFindMsg[tui.ApplyFrameMsg](msgs)
	if !ok {
		t.Fatalf("no ApplyFrameMsg in %v", msgs)
	}
	if apply.NewTab || apply.Crumb != "filter" {
		t.Errorf("NewTab=%v Crumb=%q, want stack push with kind crumb", apply.NewTab, apply.Crumb)
	}
	if want := `SELECT * FROM "people" WHERE age >= 2`; be.last != want {
		t.Errorf("backend statement = %q, want %q", be.last, want)
	}
}

func TestPromptCompletionColumnsOnly(t *testing.T) {
	ctx := qxLiveCtx()
	ctx.tables = []string{"nachos"} // must NOT appear in prompt suggestions
	ov, _ := tui.Factories["order"](ctx, "")
	ov, _ = qxTypeText(ov, "na")
	p := ov.(*qxPrompt)
	if !p.sug.open() {
		t.Fatal("no suggestions for column prefix")
	}
	for _, it := range p.sug.items {
		if it.Text == "nachos" {
			t.Error("table name offered in inline prompt completion")
		}
	}
	if p.sug.selected().Text != "name" {
		t.Errorf("selected = %q, want column name", p.sug.selected().Text)
	}

	// Enter applies the suggestion instead of executing.
	ov, cmd := qxSend(p, "enter")
	p = ov.(*qxPrompt)
	if cmd != nil || p.in.String() != "name" || p.sug.open() {
		t.Errorf("after apply: %q open=%v cmd=%v", p.in.String(), p.sug.open(), cmd)
	}
}

func TestPromptEscCloses(t *testing.T) {
	ov, _ := tui.Factories["select"](qxLiveCtx(), "")
	_, cmd := qxSend(ov, "esc")
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.CloseOverlayMsg](msgs); !ok {
		t.Errorf("esc: got %v, want CloseOverlayMsg", msgs)
	}
}

func TestPromptAutoRunWithArg(t *testing.T) {
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, cols: []string{"name", "age"}, base: "people"}
	ov, err := tui.Factories["order"](ctx, "age DESC")
	if err != nil {
		t.Fatal(err)
	}
	ar, ok := ov.(*qxAutoRun)
	if !ok {
		t.Fatalf("factory with arg returned %T, want *qxAutoRun", ov)
	}

	// Pushing the overlay runs its Init, which fires the statement and
	// closes the box immediately (no follow-up message needed).
	cmd := ar.Init()
	next := tui.Overlay(ar)
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.CloseOverlayMsg](msgs); !ok {
		t.Errorf("auto-run did not close itself: %v", msgs)
	}
	apply, ok := qxFindMsg[tui.ApplyFrameMsg](msgs)
	if !ok {
		t.Fatalf("no ApplyFrameMsg in %v", msgs)
	}
	if apply.Crumb != "order" || apply.NewTab {
		t.Errorf("Crumb=%q NewTab=%v", apply.Crumb, apply.NewTab)
	}
	if apply.Frame == nil || apply.Frame.NumRows() != 3 {
		t.Fatalf("ordered frame = %v", apply.Frame)
	}
	if want := `SELECT * FROM "people" ORDER BY age DESC`; be.last != want {
		t.Errorf("backend statement = %q, want %q", be.last, want)
	}

	// It fires exactly once.
	if _, cmd := next.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil {
		t.Error("auto-run fired twice")
	}

	// It still renders something sane while waiting.
	if out := next.View(80, 24, qxTestTheme()); !strings.Contains(out, "ORDER BY") {
		t.Errorf("auto-run view = %q", out)
	}
}

func TestPromptLiveDatabaseTarget(t *testing.T) {
	// In live database mode the prompt targets the current tab's table
	// (BaseCrumb heuristic) instead of the "_" alias.
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, base: "users", cols: []string{"id"}}

	ov, err := tui.Factories["filter"](ctx, "id = 1")
	if err != nil {
		t.Fatal(err)
	}
	_, cmd := ov.Update(struct{}{})
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.ApplyFrameMsg](msgs); !ok {
		t.Fatalf("live-mode auto-run: %v", msgs)
	}
	if want := `SELECT * FROM "users" WHERE id = 1`; be.last != want {
		t.Errorf("backend statement = %q, want %q", be.last, want)
	}
}

func TestPromptEmptyEnterCloses(t *testing.T) {
	ov, _ := tui.Factories["filter"](qxLiveCtx(), "")
	_, cmd := qxSend(ov, "enter")
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.CloseOverlayMsg](msgs); !ok {
		t.Errorf("empty enter: got %v, want CloseOverlayMsg", msgs)
	}
	if _, ok := qxFindMsg[tui.ApplyFrameMsg](msgs); ok {
		t.Error("empty enter must not execute")
	}
}

// qxLiveCtx replaces the removed file-mode qxFileCtx: a live-database
// context whose fake backend returns the test frame. Inline-prompt SQL is
// verified through the recorded statement (be.last), not engine-side
// filtering — the merged TUI has no embedded engine.
func qxLiveCtx() *qxCtx {
	return &qxCtx{
		backend: &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}},
		cols:    []string{"name", "age"},
		tables:  []string{"users"},
		base:    "people",
	}
}

// qxMySQLBackend is qxFakeBackend reporting the mysql dialect.
type qxMySQLBackend struct{ qxFakeBackend }

func (b *qxMySQLBackend) Kind() string { return "mysql" }

func TestPromptMySQLTargetUsesBackticks(t *testing.T) {
	// MySQL treats double-quoted identifiers as string literals by default,
	// so the inline prompts must backtick-quote the target table.
	be := &qxMySQLBackend{qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}}
	ctx := &qxCtx{backend: be, base: "users", cols: []string{"id"}}

	ov, err := tui.Factories["filter"](ctx, "id = 1")
	if err != nil {
		t.Fatal(err)
	}
	_, cmd := ov.Update(struct{}{})
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.ApplyFrameMsg](msgs); !ok {
		t.Fatalf("mysql auto-run: %v", msgs)
	}
	if want := "SELECT * FROM `users` WHERE id = 1"; be.last != want {
		t.Errorf("backend statement = %q, want %q", be.last, want)
	}
}

func TestPromptPaste(t *testing.T) {
	be := &qxFakeBackend{res: backend.Result{Frame: qxTestFrame()}}
	ctx := &qxCtx{backend: be, cols: []string{"name", "age"}, base: "people"}
	ov, err := tui.Factories["filter"](ctx, "")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	p, ok := ov.(*qxPrompt)
	if !ok {
		t.Fatalf("factory returned %T, want *qxPrompt", ov)
	}

	// One PasteMsg inserts the whole chunk, flattened to a single line.
	ov, _ = p.Update(tea.PasteMsg{Content: "age >\r\n1"})
	p = ov.(*qxPrompt)
	if got := p.in.String(); got != "age > 1" {
		t.Fatalf("prompt text after paste = %q", got)
	}

	// The pasted fragment executes like typed input: the flattened text
	// lands in the statement sent to the backend.
	ov, cmd := p.Update(qxKey("enter"))
	if _, ok := ov.(*qxPrompt); !ok {
		t.Fatalf("enter returned %T", ov)
	}
	msgs := qxDrain(cmd)
	if _, ok := qxFindMsg[tui.ApplyFrameMsg](msgs); !ok {
		t.Fatalf("enter after paste: got %v, want ApplyFrameMsg", msgs)
	}
	if want := `SELECT * FROM "people" WHERE age > 1`; be.last != want {
		t.Errorf("backend statement = %q, want %q", be.last, want)
	}
}
