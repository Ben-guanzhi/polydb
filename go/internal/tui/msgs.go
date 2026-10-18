package tui

import (
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
)

// SetBackendMsg attaches a live database connection to a running app.
// Database mode starts the UI with no connection and wires one in after the
// connection form succeeds. Nil fields are left untouched.
type SetBackendMsg struct {
	Backend backend.Backend
	KV      backend.KVBackend
}

// ApplyFrameMsg pushes a derived frame onto a pane's stack, or opens it as a
// new tab when NewTab is set. Crumb is a short operation label ("filter",
// "query", "sort") shown in the breadcrumb.
type ApplyFrameMsg struct {
	Frame    *tuidata.Frame
	Crumb    string
	NewTab   bool
	TabTitle string
	// PaneID identifies the pane the operation was started from (see
	// AppContext.ActivePaneID). When set (non-zero) and NewTab is false the
	// frame is pushed onto that pane even if the user has switched tabs while
	// the command ran; if the pane is gone the frame opens as a new tab.
	PaneID int
	// TableCols, when non-nil, limits which column indices the table view
	// renders. Sheet/row-detail mode always shows all columns.
	TableCols []int
	// SourceNamespace/SourceTable, set together when opening a new tab from
	// a live table fetch, record the table the base frame came from (see
	// Pane's source fields and App.TableSource).
	SourceNamespace string
	SourceTable     string
}

// ReplaceBaseMsg swaps the base frame of the pane identified by PaneID
// (or the active pane when zero), used by the refresh command to reload
// the current table without opening a new tab.
type ReplaceBaseMsg struct {
	Frame  *tuidata.Frame
	PaneID int
}

// RunCommandMsg invokes a named command. Built-in names (quit, reset,
// toggleborders, togglerownumbers, reloadconfig) are handled by the app
// directly; everything else is resolved through the Factories registry.
type RunCommandMsg struct {
	Name string
	Arg  string
}

// SetThemeMsg switches the active theme by built-in name.
type SetThemeMsg struct{ Name string }

// ToggleBordersMsg flips the table border toggle.
type ToggleBordersMsg struct{}

// ToggleRowNumbersMsg flips the row-number gutter toggle.
type ToggleRowNumbersMsg struct{}

// ResetStackMsg pops the current pane back to its base frame.
type ResetStackMsg struct{}

// JumpToRowMsg selects a row (0-based) in the current table view.
type JumpToRowMsg struct{ Row int }

// JumpToTabMsg activates the tab at Index (0-based, clamped).
type JumpToTabMsg struct{ Index int }

// CloseTabMsg closes the tab at Index; closing the last tab quits.
type CloseTabMsg struct{ Index int }

// ExecProcessMsg runs an external program with the terminal released and
// resumes the app afterwards. OnDone (optional) converts the process error
// into a follow-up message.
type ExecProcessMsg struct {
	Cmd    *exec.Cmd
	OnDone func(error) tea.Msg
}

// CopyTextMsg copies text to the system clipboard via OSC52.
type CopyTextMsg struct{ Text string }

// QxEditDoneMsg delivers a frame re-imported after an external $EDITOR
// session (the "edit" command). The editor writes a temp CSV; on exit the
// popup's ExecProcessMsg callback re-parses it and returns this message,
// which the app turns into a new tab and closes the overlay.
type QxEditDoneMsg struct {
	Frame *tuidata.Frame
	Err   error
}

// columnMetaMsg delivers asynchronously-fetched column metadata for a table so
// the sheet view can display the real column type. The cache stores it under
// the table name when the err is nil and the backend still matches.
type columnMetaMsg struct {
	table string
	meta  []backend.ColumnMeta
	err   error
}
