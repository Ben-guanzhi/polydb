package tui

import (
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/transport"
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
	// SheetFilter 是当前标签的字段过滤文本（"/" 后输入的内容），无过滤时为空串。
	// 供表格取数把过滤下推到服务端，见 tablefetch.go。
	SheetFilter() string
	// EditQueue 是当前标签累积的未提交编辑（changes 队列）；从不编辑的标签为 nil。
	// 编辑先入队、Review 后整批提交，见 celledit.go 的 commitQueue。
	EditQueue() *changes.Queue
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
	// Client 暴露 transport.Client（铁律 2：UI 只经 transport 访问数据）。
	// 连接表单/选择器用它做连接 CRUD。
	Client() transport.Client
}

// Factories maps command names to overlay constructors. Popup packages
// register themselves here from init(). A factory may return (nil, nil) for
// pure side-effect commands that need no overlay.
var Factories = map[string]func(ctx AppContext, arg string) (Overlay, error){}
