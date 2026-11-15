//! 进程外 driver broker 的线路类型（spec/schemas/driver-broker.json）。
//!
//! 第三方驱动不进入封闭的 [`crate::DatabaseKind`] 集合，而是以 namespaced key
//! 经 WebSocket 异步契约注册到 broker。本模块只描述**声明与视图**，
//! 生命周期语义（revertible effect / ¬relied 守卫 / 能力衰减）见
//! spec/behavior.md §16，注册表实现在 app-core 的 broker 模块。

use serde::{Deserialize, Serialize};

/// namespaced provider key `P:K`。`P` 标识定义接口的包，`K` 标识驱动种类；
/// 与内置 `DatabaseKind` 的 wire 值不重叠（§6.6 key namespacing）。
pub type DriverProviderKey = String;

/// 能力位。broker 在 interception 时把它衰减成 `granted_capabilities` 的子集
/// （§6.3：不可信 provider 只获只读 query/metadata），该改写不触发 reload。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DriverCapability {
    Query,
    Execute,
    Metadata,
    Transaction,
    Kv,
}

/// provider 的注册声明。等价于组件 `(d, p, e)` 中的 coeffect specification：
/// `inject` 声明即 capability request，在组件运行前即可审查（§6.3）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DriverProvider {
    /// 唯一 namespaced key；重复提供同一 key 被拒绝（cannot provide twice, Def 19）。
    pub key: DriverProviderKey,
    /// 该 provider 所说的数据库方言 wire 名（如 `duckdb`），仅信息用途。
    pub kind: String,
    /// 构建时针对的 broker 异步契约版本；先探版本、再解引用（plugin-abi.md §2.3）。
    pub protocol_version: u32,
    /// 声明的能力请求集合。
    pub capabilities: Vec<DriverCapability>,
    /// 可选的进程外通道提示；空表示当前 WS 连接即 bridge。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub endpoint: Option<String>,
    /// §16.18 witnessed coeffect（Thm 47）：声明本 key 的服务实例可互换，
    /// 多实例可并存（负载分担）。缺省 false（rolling update 语义不变）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub commutative: Option<bool>,
}

/// broker 视图中的 provider 状态。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DriverProviderState {
    pub key: DriverProviderKey,
    pub kind: String,
    pub protocol_version: u32,
    /// 声明（请求）的能力集合。
    pub capabilities: Vec<DriverCapability>,
    /// interception 之后生效的能力集合，必为 `capabilities` 的子集；
    /// 可在运行时原地改写而不扰动依赖图（§6.3 in-place intercept）。
    pub granted_capabilities: Vec<DriverCapability>,
    /// 绑定到该 provider 的在途操作数。非零时禁止撤回：`¬ relied_n(γ)` 守卫。
    pub reliance: u32,
}

/// `notify_d` 对上下文变化的分类（Def 22）。
/// `Neutral` 对应仅改变 granted_capabilities、声明仍被满足的情形：
/// 依赖图不动，因此不触发消费者 reload。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DriverBrokerEvent {
    Activating,
    Deactivating,
    Neutral,
}
