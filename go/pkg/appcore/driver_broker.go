// 进程外 driver broker 骨架（spec/behavior.md §16）。
//
// 与 Rust 侧 rust/crates/app-core/src/driver_broker.rs 严格镜像：只实现组合
// 语义，不碰 IO、不碰 WS 收发循环、不碰内置 DatabaseKind 分派。
//
//   - 时间维度（§3.1）：每次登记是 tracked effect，逆压进 accumulator；
//     撤回按 LIFO 应用，一个 provider 只撤回自己的贡献（Thm 68）。
//   - 空间维度（§3.2/§6.3）：Capabilities 是 coeffect 声明，
//     GrantedCapabilities 是 interception 后的能力；后者原地改写不触发 reload
//     （notify_d 分类为 Neutral）。

package appcore

import (
	"sort"
	"sync"

	"github.com/polydb/polydb/pkg/protocol"
)

// BrokerProtocolVersion 是当前 broker 异步契约版本。注册时先探版本、再解引用
// （docs/plugin-abi.md §2.3 的同口径）。
const BrokerProtocolVersion int64 = 1

// BrokerTransition 是组合操作的结果：终态视图 + notify_d 分类 + 可选错误。
// Error 非 nil 表示失败且不产生转移（Def 19 / 版本不符 / 守卫拒绝）。
type BrokerTransition struct {
	State []protocol.DriverProviderState
	Event protocol.DriverBrokerEvent
	Error *protocol.PolyDBError
}

func brokerOK(state []protocol.DriverProviderState, event protocol.DriverBrokerEvent) BrokerTransition {
	return BrokerTransition{State: state, Event: event}
}

func brokerErr(state []protocol.DriverProviderState, event protocol.DriverBrokerEvent, code, msg string) BrokerTransition {
	return BrokerTransition{
		State: state,
		Event: event,
		Error: &protocol.PolyDBError{Code: code, Message: msg},
	}
}

// fiber 是单个 provider 的 fiber 状态。Inverse 是该 fiber 的 accumulator
// 条目（revertible effect 的显式逆）：撤回时按 LIFO 弹出并精确移除这一条登记，
// 不影响其它 provider。
type fiber struct {
	provider protocol.DriverProvider
	granted  []protocol.DriverCapability
	reliance int64
	inverse  protocol.DriverProviderKey
}

func (f *fiber) toState() protocol.DriverProviderState {
	return protocol.DriverProviderState{
		Key:                 f.provider.Key,
		Kind:                f.provider.Kind,
		ProtocolVersion:     f.provider.ProtocolVersion,
		Capabilities:        append([]protocol.DriverCapability(nil), f.provider.Capabilities...),
		GrantedCapabilities: append([]protocol.DriverCapability(nil), f.granted...),
		Reliance:            f.reliance,
	}
}

// DriverBroker 是进程内 broker 视图（并发安全）。
//
// accumulator 是 twisted composition 的 φ：正向登记按发生顺序压栈，撤回时
// 逆序弹出，故 Thm 16 的 LIFO 与 Thm 68 的 recovery exactness 在任意交错下成立。
type DriverBroker struct {
	mu          sync.RWMutex
	fibers      map[protocol.DriverProviderKey]*fiber
	accumulator []protocol.DriverProviderKey
}

func NewDriverBroker() *DriverBroker {
	return &DriverBroker{fibers: map[protocol.DriverProviderKey]*fiber{}}
}

// snapshot 生成按 key 排序的视图（调用方须持有 mu）。
func (b *DriverBroker) snapshot() []protocol.DriverProviderState {
	out := make([]protocol.DriverProviderState, 0, len(b.fibers))
	for _, f := range b.fibers {
		out = append(out, f.toState())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Register 注册一个 backing provider（tracked effect）。
//
// 校验顺序：版本 → 能力集合 → cannot provide twice。任一失败都不产生转移：
// 不压 accumulator、不改 fibers。
func (b *DriverBroker) Register(p protocol.DriverProvider) BrokerTransition {
	b.mu.Lock()
	defer b.mu.Unlock()

	before := b.snapshot()
	if p.ProtocolVersion != BrokerProtocolVersion {
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"broker protocol version mismatch")
	}
	if !validCapabilities(p.Capabilities) {
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"capabilities must be a non-empty set without duplicates")
	}
	if f, exists := b.fibers[p.Key]; exists {
		// §16.18.3：commutative key（Thm 47 witnessed coeffect）的多实例声明
		// 是**幂等中性**——能力集与既有声明一致时无转移（不重复压栈），回
		// neutral 事件；声明不一致（能力/方言/版本漂移）仍拒绝。
		if p.Commutative && f.provider.Kind == p.Kind &&
			f.provider.ProtocolVersion == p.ProtocolVersion &&
			sameCapabilities(f.provider.Capabilities, p.Capabilities) {
			return brokerOK(b.snapshot(), protocol.DriverBrokerEventNeutral)
		}
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"provider key already provided: "+string(p.Key))
	}

	// set(key, value) 本身是 ctx.effect：登记与其逆一起写入。
	inverse := p.Key
	b.fibers[inverse] = &fiber{
		provider: p,
		granted:  append([]protocol.DriverCapability(nil), p.Capabilities...),
		reliance: 0,
		inverse:  inverse,
	}
	b.accumulator = append(b.accumulator, inverse)
	return brokerOK(b.snapshot(), protocol.DriverBrokerEventActivating)
}

// Revoke 撤回一个 provider。
//
// 决定与动作分离：¬ relied_n(γ) 守卫先判定；拒绝时不应用 accumulator
// （不产生转移）。接受后才 LIFO 弹出该 fiber 的逆并移除它，只移除目标 fiber，
// 其它 fiber 的登记与逆栈条目保持不变（Thm 68）。
func (b *DriverBroker) Revoke(key protocol.DriverProviderKey) BrokerTransition {
	b.mu.Lock()
	defer b.mu.Unlock()

	before := b.snapshot()
	f, exists := b.fibers[key]
	if !exists {
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"provider key not registered: "+string(key))
	}
	if f.reliance > 0 {
		return brokerErr(before, protocol.DriverBrokerEventDeactivating, protocol.ErrDriverNotAvailable,
			"¬ relied_n(γ): provider still has in-flight operations")
	}

	// 动作：应用该 fiber 的 accumulator。若它是栈顶则直接弹出；否则从栈中
	// 移除属于它的那一条，保持其余条目的相对顺序不变——正是 twisted
	// composition 的 g₂∘g₁ 逆序在「只撤回一个 fiber」情形下的表现。
	for i := len(b.accumulator) - 1; i >= 0; i-- {
		if b.accumulator[i] == f.inverse {
			b.accumulator = append(b.accumulator[:i], b.accumulator[i+1:]...)
			break
		}
	}
	delete(b.fibers, key)
	return brokerOK(b.snapshot(), protocol.DriverBrokerEventDeactivating)
}

// SetGranted 是 interception：原地改写生效能力集合，不触发 reload。
//
// granted 必须是 Declared Capabilities 的子集。合法改动被 notify_d 分类为
// Neutral（声明仍被满足），不产生 activating/deactivating 转移。
func (b *DriverBroker) SetGranted(key protocol.DriverProviderKey, granted []protocol.DriverCapability) BrokerTransition {
	b.mu.Lock()
	defer b.mu.Unlock()

	before := b.snapshot()
	f, exists := b.fibers[key]
	if !exists {
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"provider key not registered: "+string(key))
	}
	if !validCapabilities(granted) || !isSubset(granted, f.provider.Capabilities) {
		return brokerErr(before, protocol.DriverBrokerEventNeutral, protocol.ErrInvalidParam,
			"granted_capabilities must be a non-empty subset of declared capabilities")
	}
	f.granted = append([]protocol.DriverCapability(nil), granted...)
	return brokerOK(b.snapshot(), protocol.DriverBrokerEventNeutral)
}

// CheckAndAcquire 把能力校验与在途绑定放在**同一个写临界区**里原子完成
// （behavior.md §16.8）。分开做「先 Snapshot 查能力、再 Acquire」会在两步之间
// 给 Revoke 留下窗口，破坏 Thm 71 的 resolution coherence：校验通过后 provider
// 被撤回，查询仍会绑到一个已消失的 fiber。一次加锁完成三件事：存在性 →
// GrantedCapabilities 含 query → reliance++。
func (b *DriverBroker) CheckAndAcquire(key protocol.DriverProviderKey) *protocol.PolyDBError {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.fibers[key]
	if !ok {
		return &protocol.PolyDBError{
			Code:    protocol.ErrDriverNotAvailable,
			Message: "provider key not registered: " + string(key),
		}
	}
	if !containsCapability(f.granted, protocol.DriverCapQuery) {
		return &protocol.PolyDBError{
			Code:    protocol.ErrPermissionDenied,
			Message: "provider " + string(key) + " is not granted the 'query' capability",
		}
	}
	f.reliance++
	return nil
}

func containsCapability(caps []protocol.DriverCapability, want protocol.DriverCapability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// Acquire 记录一次在途绑定（供 ¬ relied_n(γ) 守卫使用），返回递增后的 reliance。
func (b *DriverBroker) Acquire(key protocol.DriverProviderKey) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.fibers[key]
	if !ok {
		return 0, false
	}
	f.reliance++
	return f.reliance, true
}

// Release 归还一次在途绑定。
func (b *DriverBroker) Release(key protocol.DriverProviderKey) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.fibers[key]
	if !ok {
		return 0, false
	}
	if f.reliance > 0 {
		f.reliance--
	}
	return f.reliance, true
}

// Snapshot 返回当前视图（按 key 排序，双端一致）。
func (b *DriverBroker) Snapshot() []protocol.DriverProviderState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshot()
}

// AccumulatorLen 是 accumulator 长度（测试观察用；生产代码不应依赖）。
func (b *DriverBroker) AccumulatorLen() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.accumulator)
}

// validCapabilities：非空、无重复。
func validCapabilities(caps []protocol.DriverCapability) bool {
	if len(caps) == 0 {
		return false
	}
	seen := map[protocol.DriverCapability]bool{}
	for _, c := range caps {
		if seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// sameCapabilities：两个能力集合相同（集合相等，与顺序无关）——
// §16.18.3 commutative 多实例声明一致性的判据。
func sameCapabilities(a, b []protocol.DriverCapability) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[protocol.DriverCapability]int{}
	for _, c := range a {
		set[c]++
	}
	for _, c := range b {
		set[c]--
		if set[c] < 0 {
			return false
		}
	}
	return true
}

// isSubset：granted ⊆ declared（集合包含，与顺序无关）。
func isSubset(granted, declared []protocol.DriverCapability) bool {
	set := map[protocol.DriverCapability]bool{}
	for _, d := range declared {
		set[d] = true
	}
	for _, g := range granted {
		if !set[g] {
			return false
		}
	}
	return true
}
