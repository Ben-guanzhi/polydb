use serde::{Deserialize, Serialize};

use crate::common::{ConnectionId, GenericType, PaginationParams, Value};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueryRequest {
    pub sql: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub params: Vec<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub connection_id: Option<ConnectionId>,
    /// Optional client-provided query identifier. When provided, the server echoes it
    /// in the X-Query-ID response header and can be targeted by
    /// POST /api/queries/{query_id}/cancel. When omitted the server generates one.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub query_id: Option<uuid::Uuid>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub schema: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pagination: Option<PaginationParams>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeout_ms: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_rows: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueryResult {
    pub columns: Vec<ResultColumn>,
    pub rows: Vec<Vec<Value>>,
    pub affected_rows: u64,
    pub execution_time_ms: f64,
    #[serde(default)]
    pub truncated: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total_rows: Option<u64>,
    #[serde(default)]
    pub has_more: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub statement_type: Option<StatementType>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResultColumn {
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub table: Option<String>,
    #[serde(rename = "type")]
    pub data_type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub generic_type: Option<GenericType>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nullable: Option<bool>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum StatementType {
    Select,
    Insert,
    Update,
    Delete,
    Ddl,
    Other,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BatchQueryRequest {
    pub statements: Vec<QueryRequest>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub connection_id: Option<ConnectionId>,
    #[serde(default = "default_stop_on_error")]
    pub stop_on_error: bool,
}

fn default_stop_on_error() -> bool {
    true
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BatchQueryResult {
    pub results: Vec<BatchResultItem>,
    pub total_execution_time_ms: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum BatchResultItem {
    Ok(QueryResult),
    Err(crate::error::PolyDBError),
}
