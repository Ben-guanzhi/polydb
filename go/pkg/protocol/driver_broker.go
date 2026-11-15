// 进程外 driver broker 的线路类型（spec/schemas/driver-broker.json）。
//
// 第三方驱动不进入封闭的 DatabaseKind 集合，而是以 namespaced key 经 WS 异步
// 契约注册到 broker。生命周期语义见 spec/behavior.md §16；注册表实现在
// pkg/appcore 的 broker 模块。

package protocol

// DriverProviderKey 是 namespaced provider key "P:K"。P 标识定义接口的包，
// K 标识驱动种类；与内置 DatabaseKind 的 wire 值不重叠（§6.6 key namespacing）。
type DriverProviderKey = string

// DriverCapability 是 capability 位。broker 在 interception 时把它衰减成
// GrantedCapabilities 的子集（§6.3），该改写不触发 reload。
type DriverCapability string

const (
	DriverCapQuery       DriverCapability = "query"
	DriverCapExecute     DriverCapability = "execute"
	DriverCapMetadata    DriverCapability = "metadata"
	DriverCapTransaction DriverCapability = "transaction"
	DriverCapKv          DriverCapability = "kv"
)

// DriverProvider 是 provider 的注册声明；inject 声明即 capability request，
// 在组件运行前即可审查（§6.3）。
type DriverProvider struct {
	Key             DriverProviderKey  `json:"key" msgpack:"key"`
	Kind            string             `json:"kind" msgpack:"kind"`
	ProtocolVersion int64              `json:"protocol_version" msgpack:"protocol_version"`
	Capabilities    []DriverCapability `json:"capabilities" msgpack:"capabilities"`
	Endpoint        string             `json:"endpoint,omitempty" msgpack:"endpoint,omitempty"`
	// Commutative 是 §16.18 的 witnessed coeffect（Thm 47）：声明本 key 的服务
	// 实例可互换，多实例可并存（负载分担）。缺省 false（rolling update 不变）。
	Commutative bool `json:"commutative,omitempty" msgpack:"commutative,omitempty"`
}

// DriverProviderState 是 broker 视图中的 provider 状态。
type DriverProviderState struct {
	Key                 DriverProviderKey  `json:"key" msgpack:"key"`
	Kind                string             `json:"kind" msgpack:"kind"`
	ProtocolVersion     int64              `json:"protocol_version" msgpack:"protocol_version"`
	Capabilities        []DriverCapability `json:"capabilities" msgpack:"capabilities"`
	GrantedCapabilities []DriverCapability `json:"granted_capabilities" msgpack:"granted_capabilities"`
	Reliance            int64              `json:"reliance" msgpack:"reliance"`
}

// DriverBrokerEvent 是 notify_d 对上下文变化的分类（Def 22）。
// DriverBrokerEventNeutral 对应仅改变 GrantedCapabilities、声明仍被满足的
// 情形：依赖图不动，因此不触发消费者 reload。
type DriverBrokerEvent string

const (
	DriverBrokerEventActivating   DriverBrokerEvent = "activating"
	DriverBrokerEventDeactivating DriverBrokerEvent = "deactivating"
	DriverBrokerEventNeutral      DriverBrokerEvent = "neutral"
)
