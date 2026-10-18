// Confirm-delete overlay: prompts the user to commit or cancel a pending row
// delete. Dispatches to the reserved "deleterows" / "canceldelete" commands so
// the app owns the commit lifecycle.
package popup

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func init() {
	tui.Factories["confirmdelete"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		pd := ctx.PendingDelete()
		if pd == nil {
			return nil, fmt.Errorf("no pending delete")
		}
		return &confirmDeleteOverlay{ctx: ctx, pd: pd}, nil
	}
	addConfirmDeleteCommand()
}

// addConfirmDeleteCommand lists "confirmdelete" in the palette table once.
func addConfirmDeleteCommand() {
	for _, c := range Commands {
		if c.Name == "confirmdelete" {
			return
		}
	}
	Commands = append(Commands, Command{Name: "confirmdelete", Description: "confirm delete rows"})
}

// confirmDeleteOverlay asks the user whether to delete the pending rows. A
// "y"/enter confirmation closes the box then runs "deleterows"; "n"/esc/ctrl+c
// runs "canceldelete" instead.
type confirmDeleteOverlay struct {
	ctx tui.AppContext
	pd  *tui.PendingDelete
}

func (o *confirmDeleteOverlay) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	switch key.String() {
	case "y", "Y", "enter":
		return o, tea.Sequence(tui.CloseOverlay, func() tea.Msg {
			return tui.RunCommandMsg{Name: "deleterows"}
		})
	case "n", "N", "esc", "ctrl+c":
		return o, tea.Sequence(tui.CloseOverlay, func() tea.Msg {
			return tui.RunCommandMsg{Name: "canceldelete"}
		})
	}
	return o, nil
}

func (o *confirmDeleteOverlay) View(width, height int, th *theme.Theme) string {
	boxW := 44
	if boxW > width-4 && width-4 >= 24 {
		boxW = width - 4
	}
	if boxW < 24 {
		boxW = 24
	}
	inner := boxW - 2

	question := fmt.Sprintf("Delete %d row(s)?", len(o.pd.Rows))
	var lines []string
	lines = append(lines, " "+th.Warning.Render(confirmTrunc(question, inner-2)))
	if o.ctx.Backend() != nil && o.pd.Table != "" {
		lines = append(lines, " "+th.Subtle.Render("from ")+th.Text.Render(confirmTrunc(o.pd.Table, inner-7)))
	}
	lines = append(lines, "")
	lines = append(lines, th.Subtle.Render(" y confirm  •  n/esc cancel"))

	if len(lines) > height-2 {
		lines = lines[:max(1, height-2)]
	}

	return tui.Box("delete rows", strings.Join(lines, "\n"), boxW, th)
}

var _ tui.Overlay = (*confirmDeleteOverlay)(nil)
