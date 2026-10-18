package popup

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
)

func init() {
	RegisterCommand(
		Command{Name: "edit", Aliases: nil, Description: "edit current frame in $EDITOR, reimport on save"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			if ctx.CurrentFrame() == nil {
				return nil, fmt.Errorf("no frame to edit")
			}
			return &qxEdit{ctx: ctx}, nil
		},
	)
}

// qxEdit dumps the current frame to a temp CSV, launches $EDITOR, and on
// save re-imports the edited file as a new tab (via QxEditDoneMsg).
type qxEdit struct {
	ctx  tui.AppContext
	path string
	err  string
}

// Init writes the temp file and launches the editor. The terminal is
// released to the editor; on exit the callback re-parses and closes the
// overlay with the imported frame.
func (e *qxEdit) Init() tea.Cmd {
	f := e.ctx.CurrentFrame()
	tmp, err := os.CreateTemp("", "polydb-edit-*.csv")
	if err != nil {
		return func() tea.Msg { return tui.ErrorMsg{Err: err} }
	}
	w := csv.NewWriter(tmp)
	if err := qxFrameToCSV(w, f); err != nil {
		tmp.Close()
		return func() tea.Msg { return tui.ErrorMsg{Err: err} }
	}
	if err := tmp.Close(); err != nil {
		return func() tea.Msg { return tui.ErrorMsg{Err: err} }
	}
	e.path = tmp.Name()
	cmd := qxEditorCommand(e.path)
	return tea.ExecProcess(cmd, tea.ExecCallback(func(err error) tea.Msg {
		if err != nil {
			return tui.QxEditDoneMsg{Err: err}
		}
		reimported, perr := qxParseFile(e.path)
		if perr != nil {
			return tui.QxEditDoneMsg{Err: perr}
		}
		return tui.QxEditDoneMsg{Frame: reimported}
	}))
}

func (e *qxEdit) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	if m, ok := msg.(tui.ErrorMsg); ok {
		e.err = m.Err.Error()
		return e, nil
	}
	return e, nil
}

func (e *qxEdit) View(width, height int, th *theme.Theme) string {
	w := qxBoxWidth(width)
	var body string
	if e.err != "" {
		body = th.Error.Render(" " + e.err)
	} else if e.path != "" {
		body = th.Text.Render(" editing " + e.path + " in $EDITOR…")
	} else {
		body = th.Placeholder.Render(" preparing temp file…")
	}
	lines := []string{body, th.Placeholder.Render(" save & quit to reimport · the editor owns the terminal")}
	return tui.Box("edit", strings.Join(lines, "\n"), w, th)
}

// qxEditorCommand resolves the editor from $EDITOR/$VISUAL (fallback: vi)
// and returns a command opening path in it.
func qxEditorCommand(path string) *exec.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	args := append(parts[1:], path)
	return exec.Command(parts[0], args...)
}

var _ tui.OverlayIniter = (*qxEdit)(nil)
