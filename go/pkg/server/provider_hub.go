package server

import (
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 4/5/6/7：进程级 provider 桥接 hub（behavior.md §16.9 / §16.10 / §16.12）。
// 与 Rust 侧 rust/crates/server/src/provider_hub.rs 严格镜像。
//
// 两张全局表挂在 Server 上、跨 WS 连接共享：
//   - attached：key → 执行端（acquisition）。provider_exec 从这里取目标执行端；
//     provider_detach / 断连 / rolling update 降级时移除。
//   - pending：query_id → 结果通道。消费者会话的 runQuery 在这里等结果；
//     provider_result 到达的是 **provider 那条会话**的 runInbound，靠 query_id
//     把结果跨会话送回等待方。

// providerOutcome 是一次 provider 执行的结果。
type providerOutcome struct {
	result *protocol.QueryResult
	err    *protocol.PolyDBError
}

// endpoint 是一个已登记的执行端。inflight 是它**已收到但未完成**的 exec 数
// （rolling update 的降级判定用，§16.9.4）；retiring 标记它已被新端点替换、
// 只服务已收到的 exec；commutative 标记它是 witnessed key 的并存实例（§16.18）。
type endpoint struct {
	ep       ProviderEndpoint
	inflight int
	retiring bool
	// key 是该执行端登记时的 key（降级回执要用）。
	key protocol.DriverProviderKey
	// commutative 是 Thm 47 的 witnessed 声明：实例可互换，多实例并存（§16.18）。
	commutative bool
}

// ProviderHub 是进程级桥接状态。
type ProviderHub struct {
	mu sync.RWMutex
	// attached 是 key → 端点列表。非 commutative key 恒为单元素（rolling
	// update 替换语义，§16.9.4）；commutative key 可并存多实例，按 per-key
	// 轮询分派（§16.18.2）。列表非空即 key 可用。
	attached map[string][]*endpoint
	// rr 是 per-key 轮询计数（负载分担的分派指针，§16.18.2）。
	rr      map[string]uint64
	pending map[string]*pendingCall
	// allowed 是 attach 准入门禁白名单（behavior.md §16.10.1）。空 = deny-by-default。
	allowed map[string]bool
	// nextEndpointID 是端点 id 分配器（归属判定用，§16.12.1）。
	nextEndpointID uint64
	// broker 是进程级驱动 broker：进程端点的能力声明自动注册到它
	//（§16.13.2），与会话端点发 broker_register 同一路径。
	broker *appcore.DriverBroker
	// processes 是本 hub 拉起过的全部进程端点（含尚未 attach 的——hello 校验
	// 失败的也必须能被回收，§16.13.4）。
	processes []*ProcessEndpoint
	// callback 是阶段 9 的受限回调面（§16.14）：进程端点的 provider 经它把
	// SQL 交回宿主执行，全程看不到 connection_id 与凭据。
	callback *callbackRegistry
}

// providerProtocolVersion 是 provider 进程协议的版本（§16.12.2）。
const providerProtocolVersion = 1

// RegisterProviderCaps 把进程端点声明的能力注册到 broker（§16.13.2）。
//
// 与会话端点发 broker_register 等价：同一 key 的能力声明只会成功一次
// （重复注册被 broker 拒绝，cannot provide twice）。
func (h *ProviderHub) RegisterProviderCaps(key protocol.DriverProviderKey, caps []protocol.DriverCapability) {
	if h.broker == nil {
		return
	}
	h.broker.Register(protocol.DriverProvider{
		Key:             key,
		Kind:            string(key),
		ProtocolVersion: providerProtocolVersion,
		Capabilities:    caps,
	})
}

// CallbackURL 返回回调端点地址（仅进程端点收到；§16.14.2）。
func (h *ProviderHub) CallbackURL() string {
	if h.callback == nil {
		return ""
	}
	return h.callback.callbackURL()
}

// CallbackToken 返回回调令牌（仅进程端点收到；§16.14.2）。
func (h *ProviderHub) CallbackToken() string {
	if h.callback == nil {
		return ""
	}
	return h.callback.token()
}

// HandleFor 为某连接分配不透明句柄（§16.14.1）：provider 只见 handle，
// 看不到 connection_id。
func (h *ProviderHub) HandleFor(connectionID string) string {
	if h.callback == nil {
		return ""
	}
	handle, err := h.callback.handleFor(connectionID)
	if err != nil {
		return ""
	}
	return handle
}

// pendingCall 是一次在途转发的等待端。ep 是**投递时**的执行端（在途计数记在
// 它身上，与后续 rolling update 替换无关）。
type pendingCall struct {
	ch chan providerOutcome
	ep *endpoint
}

// NewProviderHub 从 POLYDB_PROVIDER_ALLOW（逗号分隔）读取 attach 白名单。
// **未设置或为空 = 不允许任何 attach**（deny-by-default）。
//
// callback 是阶段 9 的受限回调面；为 nil 时进程端点拿不到回调地址（无法执行
// 真实 SQL，只能回固定结果）。
func NewProviderHub(broker *appcore.DriverBroker, callback *callbackRegistry) *ProviderHub {
	raw := os.Getenv("POLYDB_PROVIDER_ALLOW")
	allowed := map[string]bool{}
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			allowed[k] = true
		}
	}
	return &ProviderHub{
		attached: map[string][]*endpoint{},
		rr:       map[string]uint64{},
		pending:  map[string]*pendingCall{},
		allowed:  allowed,
		broker:   broker,
		callback: callback,
	}
}

// NewProviderHubWithAllowed 以显式白名单构造（测试用；生产走 NewProviderHub）。
func NewProviderHubWithAllowed(keys ...string) *ProviderHub {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	return &ProviderHub{
		attached: map[string][]*endpoint{},
		rr:       map[string]uint64{},
		pending:  map[string]*pendingCall{},
		allowed:  allowed,
	}
}

// NextEndpointID 分配一个新的端点 id（归属判定用，§16.12.1）。
func (h *ProviderHub) NextEndpointID() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextEndpointID++
	return h.nextEndpointID
}

// Attach 登记一个执行端为 key 的执行端；返回当前全部 attached keys。
//
// §16.9.4 rolling update：同 key 的新端点**替换**旧端点成为当前执行端；旧端点
// 标记退役，继续服务已收到的 exec，但在途归零后自动降级。
// §16.12.1：端点是抽象，hub 不依赖其具体传输。
// Attach 登记执行端（acquisition）。
//
// §16.9.4 rolling update：非 commutative key 的新端点**替换**旧端点；旧端点
// 标记退役，在途归零后自动降级。
// §16.18 多实例：commutative key（witnessed，Thm 47）的新端点**并存**——
// 旧端点不退役，同 key 按轮询分派；同端点重复 attach 幂等。
func (h *ProviderHub) Attach(key protocol.DriverProviderKey, ep ProviderEndpoint, commutative bool) (*protocol.PolyDBError, []protocol.DriverProviderKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// §16.10.1：白名单外一律拒绝，且不登记。
	if !h.allowed[string(key)] {
		return &protocol.PolyDBError{
			Code:    protocol.ErrPermissionDenied,
			Message: "provider key is not allowed to attach: " + string(key),
		}, h.attachedKeysLocked()
	}
	list := h.attached[string(key)]
	if commutative {
		// 并存语义：同端点重复 attach 幂等；新端点追加。
		for _, cur := range list {
			if cur.ep.ID() == ep.ID() {
				return nil, h.attachedKeysLocked()
			}
		}
		h.attached[string(key)] = append(list, &endpoint{ep: ep, key: key, commutative: true})
		return nil, h.attachedKeysLocked()
	}
	// rolling update（缺省）：替换——旧的标记退役。
	for _, cur := range list {
		cur.retiring = true
	}
	h.attached[string(key)] = []*endpoint{{ep: ep, key: key}}
	return nil, h.attachedKeysLocked()
}

// Detach 移除本端点登记的 key（ID 判定，避免 rolling update 后旧端点误删新的）。
//
// §16.18：commutative key 只移除**本端点**的实例；列表非空则 key 仍可用
// （continuous availability），不向其余实例发补偿。
func (h *ProviderHub) Detach(key protocol.DriverProviderKey, ep ProviderEndpoint) (*protocol.PolyDBError, []protocol.DriverProviderKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list, ok := h.attached[string(key)]
	if !ok {
		return &protocol.PolyDBError{
			Code:    protocol.ErrInvalidParam,
			Message: "provider key not attached by this endpoint: " + string(key),
		}, h.attachedKeysLocked()
	}
	idx := -1
	for i, cur := range list {
		if cur.ep.ID() == ep.ID() {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &protocol.PolyDBError{
			Code:    protocol.ErrInvalidParam,
			Message: "provider key not attached by this endpoint: " + string(key),
		}, h.attachedKeysLocked()
	}
	list = append(list[:idx], list[idx+1:]...)
	if len(list) == 0 {
		delete(h.attached, string(key))
		delete(h.rr, string(key))
	} else {
		h.attached[string(key)] = list
	}
	return nil, h.attachedKeysLocked()
}

// DropEndpoint 在端点断开时移除该端点登记的所有 key（recovery exactness，Thm 68）。
// §16.18：commutative key 的列表非空则 key 仍登记（其余实例继续服务）。
func (h *ProviderHub) DropEndpoint(ep ProviderEndpoint) []protocol.DriverProviderKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	var dropped []protocol.DriverProviderKey
	for k, list := range h.attached {
		kept := list[:0]
		for _, cur := range list {
			if cur.ep.ID() == ep.ID() {
				dropped = append(dropped, protocol.DriverProviderKey(k))
				continue
			}
			kept = append(kept, cur)
		}
		if len(kept) == 0 {
			delete(h.attached, k)
			delete(h.rr, k)
		} else {
			h.attached[k] = kept
		}
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i] < dropped[j] })
	return dropped
}

func (h *ProviderHub) attachedKeysLocked() []protocol.DriverProviderKey {
	out := make([]protocol.DriverProviderKey, 0, len(h.attached))
	for k := range h.attached {
		out = append(out, protocol.DriverProviderKey(k))
	}
	// 稳定顺序（双端 parity）。
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AttachedKeys 返回当前全部已登记 keys。
func (h *ProviderHub) AttachedKeys() []protocol.DriverProviderKey {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.attachedKeysLocked()
}

// AttachedCountForKey 返回某 key 的登记端点数（测试/诊断观测用）：
// 配置对账的撤下（§16.19）经它观测——撤下后归零。
func (h *ProviderHub) AttachedCountForKey(key protocol.DriverProviderKey) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.attached[string(key)])
}

// ProviderEndpoint 取目标 provider 端点（供 emission / compensation）。
//
// §16.18.2：commutative key 有多实例时按 per-key 轮询分派（负载分担）。
func (h *ProviderHub) ProviderEndpoint(key protocol.DriverProviderKey) ProviderEndpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.attached[string(key)]
	if len(list) == 0 {
		return nil
	}
	if len(list) == 1 {
		return list[0].ep
	}
	// 轮询：per-key 计数推进，均匀分担。
	i := h.rr[string(key)] % uint64(len(list))
	h.rr[string(key)]++
	return list[i].ep
}

// RegisterPending 注册一次在途转发，返回等待结果的 channel 与**本次分派**的
// 端点。分派（commutative key 的 per-key 轮询，§16.18.2）与在途计数在**同一
// 把锁内**完成——分开做会让轮询推进两次，exec 与计数落在不同实例上。
// key 无端点时返回 (ch, nil)——pending 照常登记（迟到的 complete 被 withhold）。
func (h *ProviderHub) RegisterPending(queryID string, key protocol.DriverProviderKey) (chan providerOutcome, ProviderEndpoint) {
	ch := make(chan providerOutcome, 1)
	h.mu.Lock()
	var picked *endpoint
	list := h.attached[string(key)]
	if len(list) > 0 {
		if len(list) == 1 {
			picked = list[0]
		} else {
			i := h.rr[string(key)] % uint64(len(list))
			h.rr[string(key)]++
			picked = list[i]
		}
		picked.inflight++
	}
	h.pending[queryID] = &pendingCall{ch: ch, ep: picked}
	var out ProviderEndpoint
	if picked != nil {
		out = picked.ep
	}
	h.mu.Unlock()
	return ch, out
}

// Complete 把 provider_result 送回等待方。返回 false = 迟到的结果被
// **withhold**（pending 已被 cancel/timeout 清除，§16.9.2 step 5）。
func (h *ProviderHub) Complete(queryID string, out providerOutcome) bool {
	h.mu.Lock()
	call, ok := h.pending[queryID]
	if ok {
		delete(h.pending, queryID)
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	h.settle(call.ep)
	call.ch <- out
	return true
}

// Cancel 是 cancel/timeout 的 compensation：清除 pending（withhold 后续
// result）并向**本次分派**的 provider 端点发 best-effort provider_cancel
// （§16.9.3）——多实例下查询绑定的是分派端点，与其余实例无关（§16.18.2）。
// 明确不宣称 commutation 元理论成立（§16.9.5）。
func (h *ProviderHub) Cancel(queryID string, key protocol.DriverProviderKey) {
	h.mu.Lock()
	call, ok := h.pending[queryID]
	if ok {
		delete(h.pending, queryID)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	h.settle(call.ep)
	if call.ep != nil {
		call.ep.ep.Emit(&protocol.ServerMessage{
			Type:    protocol.ServerMsgProviderCancel,
			QueryID: queryID,
		})
	}
}

// settle 结算一次在途：计数 -1；若该端点已退役且归零 → 自动降级（§16.9.4）。
// 降级 = 从 attached 列表移除该端点 + 向它回 provider_ack(deactivating)（§16.9.4
// 最后一条：自动降级须回执）。降级只移除它自己；commutative key 列表里还有
// 其它实例时 key 继续可用（§16.18.2）。
func (h *ProviderHub) settle(ep *endpoint) {
	if ep == nil {
		return
	}
	h.mu.Lock()
	if ep.inflight > 0 {
		ep.inflight--
	}
	if ep.inflight != 0 || !ep.retiring {
		h.mu.Unlock()
		return
	}
	// 从列表移除自己。**注意**：rolling update 的替换在 Attach 时已把旧端点
	// 移出列表——它在途归零时仍须收到降级回执（§16.9.4），所以回执是
	// **无条件**的（commutative 实例 retiring 恒 false，走不到这里）。
	for k, list := range h.attached {
		for i, cur := range list {
			if cur != ep {
				continue
			}
			list = append(list[:i], list[i+1:]...)
			if len(list) == 0 {
				delete(h.attached, k)
				delete(h.rr, k)
			} else {
				h.attached[k] = list
			}
			break
		}
	}
	attached := h.attachedKeysLocked()
	h.mu.Unlock()
	// 回执：自动降级必须让旧端点知道（§16.9.4）。
	ep.ep.NotifyRetired(string(ep.key), attached)
}
