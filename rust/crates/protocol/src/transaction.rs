use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

use crate::common::ConnectionId;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TransactionInfo {
    pub id: uuid::Uuid,
    pub connection_id: ConnectionId,
    pub status: TransactionStatus,
    pub started_at: DateTime<Utc>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub isolation_level: Option<IsolationLevel>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TransactionStatus {
    Active,
    Committed,
    RolledBack,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BeginTransactionRequest {
    #[serde(default = "uuid_default")]
    pub connection_id: ConnectionId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub isolation_level: Option<IsolationLevel>,
}

/// 客户端 body 不带 `connection_id`（handler 从 URL 路径补）时的占位值。
fn uuid_default() -> uuid::Uuid {
    uuid::Uuid::nil()
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum IsolationLevel {
    ReadUncommitted,
    #[default]
    ReadCommitted,
    RepeatableRead,
    Serializable,
}
