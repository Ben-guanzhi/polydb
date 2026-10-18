package popup

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/explain"
	"github.com/polydb/polydb/pkg/protocol"
)

// qxExplainKind resolves the active backend engine kind for dialect-aware
// EXPLAIN wrapping. File mode (no backend) has no dialect, so it returns "".
func qxExplainKind(ctx tui.AppContext) protocol.DatabaseKind {
	if be := ctx.Backend(); be != nil {
		return protocol.DatabaseKind(be.Kind())
	}
	return ""
}

// qxExplainCmd wraps sqlText in a dialect-specific EXPLAIN statement and runs
// it, pushing the plan as a new tab. The plan is just another row set (SQLite
// returns QUERY PLAN rows; postgres/mysql return a single JSON column), so it
// reuses the same ApplyFrameMsg plumbing as every other query result.
func qxExplainCmd(ctx tui.AppContext, sqlText string) tea.Cmd {
	wrapped := explain.BuildSQL(sqlText, qxExplainKind(ctx))
	paneID := ctx.ActivePaneID()
	return qxRunCmd(ctx, wrapped, func(f *tuidata.Frame) tea.Msg {
		return tui.ApplyFrameMsg{
			Frame:    f,
			Crumb:    "explain",
			NewTab:   true,
			TabTitle: qxShortTitle(wrapped),
			PaneID:   paneID,
		}
	})
}

func init() {
	RegisterCommand(
		Command{Name: "explain", Aliases: []string{"plan"}, Description: "show the execution plan for a SQL statement"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			if a := strings.TrimSpace(arg); a != "" {
				// Argument already spelled out ("explain SELECT …"): wrap and
				// run immediately via the qxAutoRun initer overlay.
				return &qxAutoRun{label: explain.BuildSQL(a, qxExplainKind(ctx)), cmd: qxExplainCmd(ctx, a)}, nil
			}
			return &qxExplain{ctx: ctx, in: tui.NewLineInput("")}, nil
		},
	)
}

// qxExplain is the interactive variant: one line where the user types or
// pastes a statement; enter wraps and runs it.
type qxExplain struct {
	ctx tui.AppContext
	in  tui.LineInput
}

func (e *qxExplain) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return e, nil
	}
	switch key.String() {
	case "esc":
		return e, tui.CloseOverlay
	case "enter":
		sqlText := strings.TrimSpace(e.in.String())
		if sqlText == "" {
			return e, tui.CloseOverlay
		}
		return e, tea.Batch(tui.CloseOverlay, qxExplainCmd(e.ctx, sqlText))
	}
	e.in.Handle(key)
	return e, nil
}

func (e *qxExplain) View(width, height int, th *theme.Theme) string {
	w := qxBoxWidth(width)
	lines := []string{" " + e.in.View(w-4, th)}
	lines = append(lines, th.Placeholder.Render(" enter explain · esc close"))
	return tui.Box("explain", strings.Join(lines, "\n"), w, th)
}
