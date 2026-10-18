package tui

import (
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
)

// TabInfo is a summary of one open tab for switchers and schema listings.
type TabInfo struct {
	Title string
	Shape string // e.g. "120 x 5"
}

// AppContext is the read-only view of the application that popup factories
// receive. It exists so popup packages can be wired in without importing the
// app (avoiding cycles).
type AppContext interface {
	CurrentFrame() *tuidata.Frame // top of current pane stack (nil if no tabs)
	CurrentRow() int              // selected row in current table view
	SheetFieldCursor() int        // selected field in sheet mode
	CurrentTableNamespace() string
	BaseCrumb() string // current tab title
	Crumbs() []string  // full breadcrumb chain
	ColumnNames() []string
	TableNames() []string     // backend tables for completion
	Backend() backend.Backend // nil when no connection is open
	KV() backend.KVBackend    // nil unless redis mode
	Theme() *theme.Theme
	ThemeName() string
	ShowBorders() bool
	ShowRowNumbers() bool
	Tabs() []TabInfo
	ActiveTab() int
	ActivePaneID() int // stable identity of the active pane (0 if no tabs)
	PendingEdit() *PendingEdit
	PendingDelete() *PendingDelete
}

// Factories maps command names to overlay constructors. Popup packages
// register themselves here from init(). A factory may return (nil, nil) for
// pure side-effect commands that need no overlay.
var Factories = map[string]func(ctx AppContext, arg string) (Overlay, error){}
