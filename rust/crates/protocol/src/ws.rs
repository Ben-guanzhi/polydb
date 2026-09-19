use serde::{Deserialize, Serialize};

use crate::common::ConnectionId;
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
}
