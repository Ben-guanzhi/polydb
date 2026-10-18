package popup

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tuidata"
)

func init() {
	RegisterCommand(
		Command{Name: "export", Aliases: nil, Description: "export current frame to a csv/json/sql file"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			if a := strings.TrimSpace(arg); a != "" {
				// "export csv /tmp/x.csv" runs immediately.
				return &qxAutoRun{label: a, cmd: qxExportCmd(ctx, a)}, nil
			}
			return &qxExport{ctx: ctx, in: tui.NewLineInput("")}, nil
		},
	)
}

// qxExportCmd parses "format path" (format optional; inferred from extension)
// and writes the current frame. Success/error surface as a toast/error popup.
func qxExportCmd(ctx tui.AppContext, arg string) tea.Cmd {
	return func() tea.Msg {
		format, path, err := qxParseExportArg(arg)
		if err != nil {
			return tui.ErrorMsg{Err: err}
		}
		f := ctx.CurrentFrame()
		if f == nil {
			return tui.ErrorMsg{Err: fmt.Errorf("no frame to export")}
		}
		if err := qxWriteExport(path, format, f, ctx); err != nil {
			return tui.ErrorMsg{Err: err}
		}
		return tui.ToastMsg{Text: fmt.Sprintf("exported %d rows → %s", f.NumRows(), path)}
	}
}

func qxParseExportArg(arg string) (format, path string, err error) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("usage: export [csv|json|sql] <path>")
	}
	if len(fields) == 1 {
		path = fields[0]
		format = qxFormatFromExt(path)
	} else {
		format = strings.ToLower(fields[0])
		path = strings.Join(fields[1:], " ")
	}
	switch format {
	case "csv", "json", "sql":
	default:
		return "", "", fmt.Errorf("unknown format %q (use csv|json|sql)", format)
	}
	return format, path, nil
}

func qxFormatFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".sql":
		return "sql"
	default:
		return "csv"
	}
}

func qxWriteExport(path, format string, f *tuidata.Frame, ctx tui.AppContext) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	switch format {
	case "csv":
		w := csv.NewWriter(out)
		if err := qxFrameToCSV(w, f); err != nil {
			return err
		}
	case "json":
		b, err := qxFrameToJSON(f)
		if err != nil {
			return err
		}
		if _, err := out.Write(b); err != nil {
			return err
		}
	case "sql":
		kind := ""
		if be := ctx.Backend(); be != nil {
			kind = be.Kind()
		}
		table := ctx.BaseCrumb()
		if table == "" {
			table = "exported"
		}
		if _, err := out.WriteString(qxFrameToSQL(f, table, kind)); err != nil {
			return err
		}
	}
	return nil
}

// qxExport is the interactive variant: one line "format path".
type qxExport struct {
	ctx tui.AppContext
	in  tui.LineInput
	err string
}

func (e *qxExport) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
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
		return e, tea.Batch(tui.CloseOverlay, qxExportCmd(e.ctx, a))
	}
	e.in.Handle(key)
	return e, nil
}

func (e *qxExport) View(width, height int, th *theme.Theme) string {
	w := qxBoxWidth(width)
	lines := []string{" " + e.in.View(w-4, th)}
	if e.err != "" {
		lines = append(lines, th.Error.Render(" "+e.err))
	}
	lines = append(lines, th.Placeholder.Render(" format path · enter export · esc close"))
	return tui.Box("export", strings.Join(lines, "\n"), w, th)
}
