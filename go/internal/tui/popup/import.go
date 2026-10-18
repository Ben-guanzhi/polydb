package popup

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func init() {
	RegisterCommand(
		Command{Name: "import", Aliases: nil, Description: "import a csv/json file as a new tab"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			if a := strings.TrimSpace(arg); a != "" {
				// "import /tmp/x.csv" runs immediately.
				return &qxAutoRun{label: a, cmd: qxImportCmd(ctx, a)}, nil
			}
			return &qxImport{ctx: ctx, in: tui.NewLineInput("")}, nil
		},
	)
}

// qxImportCmd reads a csv/json file and pushes the resulting frame as a new
// tab. Format is inferred from the file extension.
func qxImportCmd(ctx tui.AppContext, path string) tea.Cmd {
	path = strings.TrimSpace(path)
	paneID := ctx.ActivePaneID()
	return func() tea.Msg {
		f, err := qxParseFile(path)
		if err != nil {
			return tui.ErrorMsg{Err: err}
		}
		return tui.ApplyFrameMsg{
			Frame:    f,
			Crumb:    "import",
			NewTab:   true,
			TabTitle: "import",
			PaneID:   paneID,
		}
	}
}

// qxImport is the interactive variant: one line with the file path.
type qxImport struct {
	ctx tui.AppContext
	in  tui.LineInput
	err string
}

func (e *qxImport) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	if m, ok := msg.(tui.ErrorMsg); ok {
		e.err = m.Err.Error()
		return e, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return e, nil
	}
	switch key.String() {
	case "esc":
		return e, tui.CloseOverlay
	case "enter":
		a := strings.TrimSpace(e.in.String())
		if a == "" {
			return e, tui.CloseOverlay
		}
		return e, tea.Batch(tui.CloseOverlay, qxImportCmd(e.ctx, a))
	}
	e.in.Handle(key)
	return e, nil
}

func (e *qxImport) View(width, height int, th *theme.Theme) string {
	w := qxBoxWidth(width)
	lines := []string{" " + e.in.View(w-4, th)}
	if e.err != "" {
		lines = append(lines, th.Error.Render(" "+e.err))
	}
	lines = append(lines, th.Placeholder.Render(" path to .csv/.json · enter import · esc close"))
	return tui.Box("import", strings.Join(lines, "\n"), w, th)
}
