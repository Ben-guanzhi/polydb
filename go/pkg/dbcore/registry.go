package dbcore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/polydb/polydb/pkg/protocol"
)

// 注册表层的哨兵错误（不占用协议错误码：错误码属 spec/ 契约，新增需走四门禁）。
// 调用方用 errors.Is 判定，具体文案由 fmt.Errorf 附上 kind。
var (
	// ErrDriverNotRegistered 表示该 kind 没有登记 opener。
	ErrDriverNotRegistered = errors.New("dbcore: no driver opener registered")
	// ErrDriverInUse 表示该 kind 仍有打开动作在途，撤回被 `¬ relies_n(γ)` 守卫拒绝。
	ErrDriverInUse = errors.New("dbcore: driver opener still in use")
)

// DriverOpener 打开一条协议原生（protocol.* 类型）驱动连接。
//
// 这是 polydb 侧的驱动分派注册表（PD-01/PD-02，2026-11-09）：每个驱动包在自己的
// init() 里用 RegisterDriver 登记一个 opener，appcore 只按 kind 查表打开连接，
// 不再手写 switch。新增引擎因此退化为「驱动包 init 里登记一项 + driverSpecs
// 里补一项元数据」，与 AGENTS.md §12 的声称一致。
//
// opener 收到的是已解析的连接信息与从 keyring 取回的明文密码；DSN/地址的方言
// 知识只存在于各驱动包内部（方言与协议知识只此一份，AGENTS.md §12）。
type DriverOpener func(ctx context.Context, info protocol.ConnectionInfo, password string) (Driver, error)

// driverRegistry 是 kind → opener 的单源表。它同时承担内核 `dbcore` 工厂注册表
// 在 polydb 协议层的对应角色：ref 计数是 `relies_n(γ)` 的注册表级对应物，
// UnregisterDriver 在 ref > 0 时拒绝撤回（PD-06）。
//
// 与内核注册表的关系：内核 `github.com/dbcore/dbcore` 的 ConnSpec/Driver 面是
// 协议无关的，polydb 的 protocol.* 面无法直接消费（见 bridge 包头注释的 Option B
// 说明）。因此这里保留一层协议适配注册表，并用 registry_kinds_test 把两端的
// kind 集合钉在一起——新增引擎若只在一边登记，门禁即红灯（PD-15）。
var driverRegistry = struct {
	sync.RWMutex
	m   map[protocol.DatabaseKind]DriverOpener
	ref map[protocol.DatabaseKind]int
}{m: map[protocol.DatabaseKind]DriverOpener{}, ref: map[protocol.DatabaseKind]int{}}

// RegisterDriver 安装 kind 的 opener。重复登记同一 kind 会 panic，与内核
// RegisterFactory 的编程错误纪律一致（database/sql.Register 同款）。
func RegisterDriver(kind protocol.DatabaseKind, opener DriverOpener) {
	if opener == nil {
		panic("dbcore: nil driver opener for " + string(kind))
	}
	driverRegistry.Lock()
	defer driverRegistry.Unlock()
	if _, dup := driverRegistry.m[kind]; dup {
		panic("dbcore: driver opener already registered for " + string(kind))
	}
	driverRegistry.m[kind] = opener
}

// UnregisterDriver 撤回 kind 的 opener。这是 RegisterDriver 的逆操作，也是
// `¬ relies_n(γ)` 在 polydb 注册表层的表达（PD-06）：只要仍有打开动作在途
// （ref > 0）就拒绝撤回，避免半途的打开动作拿到已消失的 provider。
func UnregisterDriver(kind protocol.DatabaseKind) error {
	driverRegistry.Lock()
	defer driverRegistry.Unlock()
	if _, ok := driverRegistry.m[kind]; !ok {
		return fmt.Errorf("%w: %s", ErrDriverNotRegistered, kind)
	}
	if driverRegistry.ref[kind] > 0 {
		return fmt.Errorf("%w: %s by %d opener(s)", ErrDriverInUse, kind, driverRegistry.ref[kind])
	}
	delete(driverRegistry.m, kind)
	return nil
}

// DriverReliance 报告 kind 当前有多少打开动作在途。
func DriverReliance(kind protocol.DatabaseKind) int {
	driverRegistry.RLock()
	defer driverRegistry.RUnlock()
	return driverRegistry.ref[kind]
}

// DriverInUse 是 DriverReliance 的布尔便捷形式。
func DriverInUse(kind protocol.DatabaseKind) bool { return DriverReliance(kind) > 0 }

// HasDriver 报告 kind 是否已登记 opener。
func HasDriver(kind protocol.DatabaseKind) bool {
	driverRegistry.RLock()
	defer driverRegistry.RUnlock()
	_, ok := driverRegistry.m[kind]
	return ok
}

// RegisteredDriverKinds 列出已登记的 kind（排序）。
func RegisteredDriverKinds() []protocol.DatabaseKind {
	driverRegistry.RLock()
	defer driverRegistry.RUnlock()
	out := make([]protocol.DatabaseKind, 0, len(driverRegistry.m))
	for k := range driverRegistry.m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// OpenDriver 按 kind 查表打开连接。查不到时返回 NOT_SUPPORTED（含可操作的
// 导入提示），与内核 Open 的错误形态对齐。
func OpenDriver(ctx context.Context, kind protocol.DatabaseKind, info protocol.ConnectionInfo, password string) (Driver, error) {
	if string(kind) == "" {
		return nil, fmt.Errorf("%w: empty DatabaseKind", ErrDriverNotRegistered)
	}
	driverRegistry.Lock()
	opener, ok := driverRegistry.m[kind]
	if !ok {
		driverRegistry.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDriverNotRegistered, kind)
	}
	driverRegistry.ref[kind]++
	driverRegistry.Unlock()
	// 无论成败都在 opener 返回后释放 reliance，使 kind 能再次被撤回。
	defer func() {
		driverRegistry.Lock()
		if driverRegistry.ref[kind]--; driverRegistry.ref[kind] <= 0 {
			delete(driverRegistry.ref, kind)
		}
		driverRegistry.Unlock()
	}()
	d, err := opener(ctx, info, password)
	if err != nil {
		return nil, err
	}
	return d, nil
}
