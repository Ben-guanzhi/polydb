use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DatabaseKind {
    Sqlite,
    #[serde(rename = "mysql")]
    MySql,
    Postgres,
    Mssql,
    Oracle,
    Redis,
}

impl std::fmt::Display for DatabaseKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Sqlite => write!(f, "sqlite"),
            Self::MySql => write!(f, "mysql"),
            Self::Postgres => write!(f, "postgres"),
            Self::Mssql => write!(f, "mssql"),
            Self::Oracle => write!(f, "oracle"),
            Self::Redis => write!(f, "redis"),
        }
    }
}

pub type ConnectionId = uuid::Uuid;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum Value {
    Null,
    Bool(bool),
    Integer(i64),
    Float(f64),
    String(String),
    Array(Vec<Value>),
    Object(std::collections::HashMap<String, Value>),
}

impl From<i64> for Value {
    fn from(v: i64) -> Self {
        Self::Integer(v)
    }
}
impl From<f64> for Value {
    fn from(v: f64) -> Self {
        Self::Float(v)
    }
}
impl From<String> for Value {
    fn from(v: String) -> Self {
        Self::String(v)
    }
}
impl From<bool> for Value {
    fn from(v: bool) -> Self {
        Self::Bool(v)
    }
}
impl<T: Into<Value>> From<Option<T>> for Value {
    fn from(v: Option<T>) -> Self {
        match v {
            Some(inner) => inner.into(),
            None => Self::Null,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SshTunnelConfig {
    pub host: String,
    pub port: u16,
    pub username: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub password_ref: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub password: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub private_key_path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub private_key_passphrase_ref: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub private_key_passphrase: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PaginationParams {
    #[serde(default)]
    pub offset: u64,
    #[serde(default = "default_limit")]
    pub limit: u32,
}

fn default_limit() -> u32 {
    100
}

impl Default for PaginationParams {
    fn default() -> Self {
        Self {
            offset: 0,
            limit: 100,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SortOrder {
    Asc,
    Desc,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum GenericType {
    Boolean,
    Smallint,
    Integer,
    Bigint,
    Float,
    Double,
    Decimal,
    Numeric,
    Char,
    Varchar,
    Text,
    Binary,
    Varbinary,
    Blob,
    Date,
    Time,
    Datetime,
    Timestamp,
    Interval,
    Json,
    Jsonb,
    Xml,
    Uuid,
    Array,
    Enum,
    Other,
}

/// 二进制单元格的编码阈值（spec/encoding.md §5）：
/// ≤1MB 内联 base64；>1MB 走 §5.2 旁路引用（`ref` 字段 + Range 取回，
/// 无 sink 时仅回长度占位）。
pub const MAX_INLINE_BLOB_BYTES: usize = 1_048_576;

/// spec/encoding.md §5.2 旁路引用 ID 前缀：完整引用形如
/// `$polydb_blob:<uuid>`，REST 取回路径使用其后的 UUID。
pub const BLOB_REF_PREFIX: &str = "$polydb_blob:";

/// 按 spec/encoding.md §5.1 的约定编码内联二进制单元格：
/// `{"$blob": {"len": N, "base64": "..."}}`；超过阈值时省略 `base64`。
/// **驱动层一律调用 `polydb_db_core::blob_value`**（sink 感知，>阈值改发
/// §5.2 引用）；本函数保留给 ≤阈值内联与无 sink 回落。
/// 用既有的 Object 变体承载，msgpack 与 JSON 通道都能无损往返。
pub fn blob_value(bytes: &[u8]) -> Value {
    let mut inner = std::collections::HashMap::new();
    inner.insert("len".to_string(), Value::Integer(bytes.len() as i64));
    if bytes.len() <= MAX_INLINE_BLOB_BYTES {
        inner.insert("base64".to_string(), Value::String(base64_encode(bytes)));
    }
    let mut outer = std::collections::HashMap::new();
    outer.insert("$blob".to_string(), Value::Object(inner));
    Value::Object(outer)
}

const B64_ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// 标准 base64（RFC 4648，带 `=` 填充），与 Go `base64.StdEncoding` 逐字节一致。
pub fn base64_encode(input: &[u8]) -> String {
    let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
    for chunk in input.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = chunk.get(1).copied().unwrap_or(0) as u32;
        let b2 = chunk.get(2).copied().unwrap_or(0) as u32;
        let n = (b0 << 16) | (b1 << 8) | b2;
        out.push(B64_ALPHABET[(n >> 18) as usize & 63] as char);
        out.push(B64_ALPHABET[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 {
            B64_ALPHABET[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            B64_ALPHABET[n as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

#[cfg(test)]
mod blob_tests {
    use super::*;

    #[test]
    fn base64_matches_rfc4648_vectors() {
        assert_eq!(base64_encode(b""), "");
        assert_eq!(base64_encode(b"f"), "Zg==");
        assert_eq!(base64_encode(b"fo"), "Zm8=");
        assert_eq!(base64_encode(b"foo"), "Zm9v");
        assert_eq!(base64_encode(b"foob"), "Zm9vYg==");
        assert_eq!(base64_encode(b"fooba"), "Zm9vYmE=");
        assert_eq!(base64_encode(b"foobar"), "Zm9vYmFy");
        assert_eq!(base64_encode(&[0x01, 0x02]), "AQI=");
    }

    #[test]
    fn small_blob_inlines_base64() {
        let v = blob_value(&[1, 2]);
        let Value::Object(outer) = v else {
            panic!("expected object");
        };
        let Some(Value::Object(inner)) = outer.get("$blob") else {
            panic!("expected $blob key");
        };
        assert_eq!(inner.get("len"), Some(&Value::Integer(2)));
        assert_eq!(
            inner.get("base64"),
            Some(&Value::String("AQI=".to_string()))
        );
    }

    #[test]
    fn oversized_blob_keeps_length_only() {
        let big = vec![7u8; MAX_INLINE_BLOB_BYTES + 1];
        let v = blob_value(&big);
        let Value::Object(outer) = v else {
            panic!("expected object");
        };
        let Some(Value::Object(inner)) = outer.get("$blob") else {
            panic!("expected $blob key");
        };
        assert_eq!(
            inner.get("len"),
            Some(&Value::Integer((MAX_INLINE_BLOB_BYTES + 1) as i64))
        );
        assert!(!inner.contains_key("base64"));
    }
}
