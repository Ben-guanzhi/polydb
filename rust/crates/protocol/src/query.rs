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

// ─── 表数据浏览（M11，behavior.md §13）───────────────────────

/// 过滤操作符（behavior.md §13.3）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum FilterOperator {
    #[serde(rename = "eq")]
    Eq,
    #[serde(rename = "ne")]
    Ne,
    #[serde(rename = "lt")]
    Lt,
    #[serde(rename = "le")]
    Le,
    #[serde(rename = "gt")]
    Gt,
    #[serde(rename = "ge")]
    Ge,
    #[serde(rename = "like")]
    Like,
    #[serde(rename = "not_like")]
    NotLike,
    #[serde(rename = "in")]
    In,
    #[serde(rename = "not_in")]
    NotIn,
    #[serde(rename = "between")]
    Between,
    #[serde(rename = "null")]
    Null,
    #[serde(rename = "not_null")]
    NotNull,
    /// 解码回退（#[serde(other)]）：未知 op 不在解码期拒绝，交给 browse 校验层
    /// 返回 POLYDB_ERR_INVALID_PARAM（behavior.md §13.3）。序列化时跳过，不出现在线上。
    #[serde(other)]
    #[serde(skip_serializing)]
    Unknown,
}

impl FilterOperator {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Eq => "eq",
            Self::Ne => "ne",
            Self::Lt => "lt",
            Self::Le => "le",
            Self::Gt => "gt",
            Self::Ge => "ge",
            Self::Like => "like",
            Self::NotLike => "not_like",
            Self::In => "in",
            Self::NotIn => "not_in",
            Self::Between => "between",
            Self::Null => "null",
            Self::NotNull => "not_null",
            Self::Unknown => "unknown",
        }
    }
}

/// 多条件组合方式。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum FilterLogic {
    #[serde(rename = "and")]
    And,
    #[serde(rename = "or")]
    Or,
}

/// 排序方向。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum SortDirection {
    #[serde(rename = "asc")]
    Asc,
    #[serde(rename = "desc")]
    Desc,
}

/// Value::Null 序列化辅助：null 视为"未提供"，与 Go 侧 omitempty(nil interface) 一致。
pub fn value_is_null(v: &Value) -> bool {
    matches!(v, Value::Null)
}

fn default_null_value() -> Value {
    Value::Null
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FilterCondition {
    pub column: String,
    pub op: FilterOperator,
    #[serde(default = "default_null_value", skip_serializing_if = "value_is_null")]
    pub value: Value,
    #[serde(default = "default_null_value", skip_serializing_if = "value_is_null")]
    pub second_value: Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub values: Vec<Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OrderClause {
    pub column: String,
    pub dir: SortDirection,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TableRowsRequest {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub columns: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub conditions: Vec<FilterCondition>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub logic: Option<FilterLogic>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub order_by: Vec<OrderClause>,
    #[serde(default)]
    pub offset: u64,
    #[serde(default)]
    pub limit: u32,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TableRowsResult {
    pub columns: Vec<ResultColumn>,
    pub rows: Vec<Vec<Value>>,
    pub offset: u64,
    #[serde(default)]
    pub has_more: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub total_estimate: Option<u64>,
    #[serde(default)]
    pub execution_time_ms: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TableCountResult {
    pub count: u64,
}
