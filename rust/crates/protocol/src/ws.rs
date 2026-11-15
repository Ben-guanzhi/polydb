use serde::{Deserialize, Serialize};

use crate::common::ConnectionId;
use crate::driver_broker::{
    DriverBrokerEvent, DriverCapability, DriverProvider, DriverProviderKey, DriverProviderState,
};
use crate::error::PolyDBError;
use crate::query::{QueryRequest, QueryResult};

/// hello 阶段的服务端鉴权凭据（behavior.md §12.2）。
/// 服务端未启用 POLYDB_SERVER_TOKEN 时忽略该字段。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WsAuth {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ClientMessage {
    Hello {
        connection_id: ConnectionId,
        #[serde(skip_serializing_if = "Option::is_none")]
        client_version: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        auth: Option<WsAuth>,
    },
    Query {
        query_id: uuid::Uuid,
        #[serde(flatten)]
        request: QueryRequest,
    },
    QueryCancel {
        query_id: uuid::Uuid,
    },
    /// 注册进程外驱动 provider（behavior.md §16）。注册是 tracked effect，
    /// broker 持有其逆；同一 key 重复注册被拒（cannot provide twice）。
    BrokerRegister {
        provider: DriverProvider,
    },
    /// 撤回 provider。reliance > 0 时 `¬ relied_n(γ)` 守卫拒绝撤回。
    BrokerRevoke {
        key: DriverProviderKey,
    },
    /// 阶段 5：原地改写某 key 的生效能力集合（capability attenuation，
    /// behavior.md §16.10.2）。只能收紧不能放宽。
    BrokerSetGranted {
        key: DriverProviderKey,
        granted_capabilities: Vec<DriverCapability>,
    },
    /// 阶段 4：provider → 宿主，登记本会话为 key 的执行端（acquisition）。
    ProviderAttach {
        key: DriverProviderKey,
        /// §16.18 witnessed coeffect（Thm 47）：实例可互换，多实例并存。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        commutative: Option<bool>,
    },
    /// 阶段 4：撤销执行端登记（acquisition 的逆）。
    ProviderDetach {
        key: DriverProviderKey,
    },
    /// 阶段 4：provider → 宿主，回传一次 SQL 执行结果。
    ProviderResult {
        query_id: uuid::Uuid,
        #[serde(skip_serializing_if = "Option::is_none")]
        result: Option<QueryResult>,
        #[serde(skip_serializing_if = "Option::is_none")]
        error: Option<PolyDBError>,
    },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ServerMessage {
    HelloAck {
        server_version: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        db_version: Option<String>,
    },
    QueryStarted {
        query_id: uuid::Uuid,
    },
    QueryProgress {
        query_id: uuid::Uuid,
        #[serde(skip_serializing_if = "Option::is_none")]
        rows_fetched: Option<u64>,
        #[serde(skip_serializing_if = "Option::is_none")]
        message: Option<String>,
    },
    QueryResult {
        query_id: uuid::Uuid,
        result: QueryResult,
    },
    QueryError {
        query_id: uuid::Uuid,
        error: PolyDBError,
    },
    QueryCancelled {
        query_id: uuid::Uuid,
        #[serde(skip_serializing_if = "Option::is_none")]
        rows_returned: Option<u64>,
    },
    /// broker 当前视图；backing provider 装卸不触发消费者 reload（§6.2）。
    BrokerState {
        providers: Vec<DriverProviderState>,
        #[serde(skip_serializing_if = "Option::is_none")]
        error: Option<PolyDBError>,
    },
    /// `notify_d` 对上下文变化的分类（Def 22）。
    BrokerEvent {
        event: DriverBrokerEvent,
        key: DriverProviderKey,
        #[serde(skip_serializing_if = "Option::is_none")]
        reason: Option<String>,
    },
    /// 阶段 4：宿主 → provider，attach/detach 确认（状态帧）。
    ProviderAck {
        event: DriverBrokerEvent,
        key: DriverProviderKey,
        attached: Vec<DriverProviderKey>,
        /// 阶段 9：宿主进程内的仅回环回调端点地址（仅进程端点收到；§16.14.2）。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        callback_url: Option<String>,
        /// 阶段 9：本次 attach 的回调令牌（仅进程端点收到；不落盘、不进日志）。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        callback_token: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        error: Option<PolyDBError>,
    },
    /// 阶段 4：宿主 → provider，把查询推过桥接边界（emission，不可逆）。
    ProviderExec {
        query_id: uuid::Uuid,
        sql: String,
        /// 阶段 9：不透明连接句柄（仅进程端点收到；§16.14.1）。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        handle: Option<String>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        params: Vec<crate::Value>,
    },
    /// 阶段 4：宿主 → provider，best-effort compensation。
    ProviderCancel {
        query_id: uuid::Uuid,
    },
}
