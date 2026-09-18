use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolyDBError {
    pub code: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<serde_json::Value>,
    #[serde(default)]
    pub retryable: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cause: Option<String>,
}

impl PolyDBError {
    pub fn new(code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
            detail: None,
            retryable: false,
            cause: None,
        }
    }

    pub fn retryable(mut self) -> Self {
        self.retryable = true;
        self
    }

    pub fn with_cause(mut self, cause: impl Into<String>) -> Self {
        self.cause = Some(cause.into());
        self
    }

    pub fn with_detail(mut self, detail: serde_json::Value) -> Self {
        self.detail = Some(detail);
        self
    }
}

impl std::fmt::Display for PolyDBError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "[{}] {}", self.code, self.message)
    }
}

impl std::error::Error for PolyDBError {}

pub mod codes {
    pub const UNKNOWN: &str = "POLYDB_ERR_UNKNOWN";
    pub const CONNECTION_FAILED: &str = "POLYDB_ERR_CONNECTION_FAILED";
    pub const CONNECTION_NOT_FOUND: &str = "POLYDB_ERR_CONNECTION_NOT_FOUND";
    pub const CONNECTION_EXISTS: &str = "POLYDB_ERR_CONNECTION_EXISTS";
    pub const AUTH_FAILED: &str = "POLYDB_ERR_AUTH_FAILED";
    pub const TIMEOUT: &str = "POLYDB_ERR_TIMEOUT";
    pub const QUERY_FAILED: &str = "POLYDB_ERR_QUERY_FAILED";
    pub const SYNTAX_ERROR: &str = "POLYDB_ERR_SYNTAX_ERROR";
    pub const PERMISSION_DENIED: &str = "POLYDB_ERR_PERMISSION_DENIED";
    pub const SCHEMA_NOT_FOUND: &str = "POLYDB_ERR_SCHEMA_NOT_FOUND";
    pub const TABLE_NOT_FOUND: &str = "POLYDB_ERR_TABLE_NOT_FOUND";
    pub const COLUMN_NOT_FOUND: &str = "POLYDB_ERR_COLUMN_NOT_FOUND";
    pub const DUPLICATE_KEY: &str = "POLYDB_ERR_DUPLICATE_KEY";
    pub const CONSTRAINT_VIOLATION: &str = "POLYDB_ERR_CONSTRAINT_VIOLATION";
    pub const DEADLOCK: &str = "POLYDB_ERR_DEADLOCK";
    pub const TRANSACTION_FAILED: &str = "POLYDB_ERR_TRANSACTION_FAILED";
    pub const INVALID_PARAM: &str = "POLYDB_ERR_INVALID_PARAM";
    pub const NOT_SUPPORTED: &str = "POLYDB_ERR_NOT_SUPPORTED";
    pub const DRIVER_NOT_AVAILABLE: &str = "POLYDB_ERR_DRIVER_NOT_AVAILABLE";
    pub const SSH_TUNNEL_FAILED: &str = "POLYDB_ERR_SSH_TUNNEL_FAILED";
    pub const STORAGE_FAILED: &str = "POLYDB_ERR_STORAGE_FAILED";
    pub const KEYRING_FAILED: &str = "POLYDB_ERR_KEYRING_FAILED";
    pub const CANCELLED: &str = "POLYDB_ERR_CANCELLED";
    pub const QUERY_NOT_FOUND: &str = "POLYDB_ERR_QUERY_NOT_FOUND";
}
