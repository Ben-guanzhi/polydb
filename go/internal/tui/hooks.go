package tui

import (
	"errors"
	"fmt"
	"sync"

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

// ─── PD-09：工厂注册表生命周期 ────────────────────────────────
//
// 此前 `Factories` 是一个裸 map：注册无重复检测（last-wins）、无撤回路径，
// §6.6 的 `¬ relied_n(γ)` 守卫在此结构性不可能。这里补上注册表级的生命周期，
// 与内核 `dbcore` 的 RegisterFactory/UnregisterFactory 同纪律：
//
//   - AcquireFactory / ReleaseFactory 是 dispatch 侧的依赖计数（relies_n(γ)）；
//   - UnregisterFactory 在仍有 dispatch 在途时拒绝撤回；
//   - FactoryInUse / FactoryReliance 供调用方查询。
//
// 注意：`Factories` 本身仍被若干 init() 直接赋值（含 dbmode 对 query/schema 的
// 有意包装），因此这里**不**改成 panic-on-duplicate 的严格注册——那会破坏
// dbmode 的包装语义。重复检测留给后续把注册点全部收敛到 RegisterFactory 的
// 专门改动；本轮先把「撤回 + 依赖计数」这个结构性缺口补上。
var factoryRefs = struct {
	sync.RWMutex
	m map[string]int
}{m: map[string]int{}}

// AcquireFactory 标记一次经由 name 工厂的 overlay 派发开始。
func AcquireFactory(name string) {
	factoryRefs.Lock()
	factoryRefs.m[name]++
	factoryRefs.Unlock()
}

// ReleaseFactory 标记一次派发结束；计数归零时删除条目。
func ReleaseFactory(name string) {
	factoryRefs.Lock()
	if n := factoryRefs.m[name] - 1; n > 0 {
		factoryRefs.m[name] = n
	} else {
		delete(factoryRefs.m, name)
	}
	factoryRefs.Unlock()
}

// FactoryReliance 报告 name 工厂当前有多少派发在途。
func FactoryReliance(name string) int {
	factoryRefs.RLock()
	defer factoryRefs.RUnlock()
	return factoryRefs.m[name]
}

// FactoryInUse 是 FactoryReliance 的布尔便捷形式。
func FactoryInUse(name string) bool { return FactoryReliance(name) > 0 }

// UnregisterFactory 从注册表撤回 name。仍有派发在途时拒绝（ErrFactoryInUse），
// 这是 §6.6 `¬ relied_n(γ)` 在 UI 工厂层的表达。
func UnregisterFactory(name string) error {
	if FactoryInUse(name) {
		return fmt.Errorf("%w: factory %q still in use by %d dispatch(es)", ErrFactoryInUse, name, FactoryReliance(name))
	}
	delete(Factories, name)
	return nil
}

// ErrFactoryInUse 表示工厂仍有派发在途，撤回被守卫拒绝。
var ErrFactoryInUse = errors.New("tui: factory in use")
