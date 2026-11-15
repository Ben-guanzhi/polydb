package server

import (
	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 7：provider 执行端抽象（behavior.md §16.12.1）。与 Rust 侧
// rust/crates/server/src/provider_hub.rs 的 ProviderEndpoint trait 严格镜像。
//
// 执行端 = 「能接收一次 exec 并最终产出一个结果」的东西。端点对自己的传输
// 无感：broker 只知道「向某个端点投递 exec」与「端点报告结果」。会话端点
// （kind 1）与未来的进程端点（kind 2）是同一抽象的两个实现——新增端点类型
// 不需要改 broker 的分派逻辑。

// EndpointKind 区分执行端类型（§16.12.1）。
type EndpointKind string

const (
	// EndpointSession 是会话端点（kind 1）：一条 WS 会话承载一个 provider。
	EndpointSession EndpointKind = "session"
	// EndpointProcess 是进程端点（kind 2）：一个独立进程承载一个 provider。
	EndpointProcess EndpointKind = "process"
)

// ProviderEndpoint 是 provider 执行端。
type ProviderEndpoint interface {
	// ID 是端点唯一标识：归属判定用（detach / 断连 / 降级只移除自己的
	// 登记，Thm 68）。
	ID() uint64
	// Kind 是端点类型：进程端点才需要不透明 handle 与回调面（§16.14）。
	Kind() EndpointKind
	// Emit 投递一帧给端点（emission：返回 true = 已越过边界，不可逆）。
	Emit(msg *protocol.ServerMessage) bool
	// NotifyRetired 在端点被自动降级时回执（§16.9.4：降级须通知）。
	NotifyRetired(key string, attached []protocol.DriverProviderKey)
}

// SessionEndpoint 是会话端点（§16.12.1 kind 1）：一条 WS 会话承载一个
// provider。exec 经该会话的 socket 发出，结果经该会话回到宿主。
type SessionEndpoint struct {
	id      uint64
	session *wsSession
}

// NewSessionEndpoint 构造会话端点。
func NewSessionEndpoint(id uint64, s *wsSession) *SessionEndpoint {
	return &SessionEndpoint{id: id, session: s}
}

func (e *SessionEndpoint) ID() uint64 { return e.id }

func (e *SessionEndpoint) Kind() EndpointKind { return EndpointSession }

func (e *SessionEndpoint) Emit(msg *protocol.ServerMessage) bool {
	if e.session == nil {
		return false
	}
	return e.session.enqueue(msg)
}

func (e *SessionEndpoint) NotifyRetired(key string, attached []protocol.DriverProviderKey) {
	if e.session == nil {
		return
	}
	e.session.send(&protocol.ServerMessage{
		Type:     protocol.ServerMsgProviderAck,
		Event:    protocol.DriverBrokerEventDeactivating,
		Key:      protocol.DriverProviderKey(key),
		Attached: attached,
	})
}
