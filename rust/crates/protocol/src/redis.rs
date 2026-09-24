use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RedisKeyType {
    String,
    List,
    Set,
    Zset,
    Hash,
    Stream,
    None,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", content = "value", rename_all = "snake_case")]
pub enum RedisValue {
    String(String),
    List(Vec<String>),
    Set(Vec<String>),
    Zset(Vec<RedisZSetMember>),
    Hash(std::collections::HashMap<String, String>),
    Stream(String),
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisZSetMember {
    pub member: String,
    pub score: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisScanPage {
    pub cursor: u64,
    /// 本次扫描所在 db（behavior.md §1.3）：驱动持有专用连接，SELECT 是连接级状态，
    /// 故驱动跟踪值即服务端真值。None = 驱动无法确定，前端回落到本地记忆值。
    #[serde(skip_serializing_if = "Option::is_none", default)]
    pub db: Option<u32>,
    pub keys: Vec<RedisKeyInfo>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisKeyInfo {
    pub key: String,
    #[serde(rename = "type")]
    pub key_type: RedisKeyType,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ttl: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisReply {
    #[serde(rename = "type")]
    pub reply_type: RedisReplyType,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value: Option<RedisReplyValue>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RedisReplyType {
    SimpleString,
    Error,
    Integer,
    BulkString,
    Array,
    Null,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RedisReplyValue {
    String(String),
    Integer(i64),
    Array(Vec<RedisReply>),
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisSelectDbRequest {
    pub index: u32,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisScanRequest {
    #[serde(default)]
    pub cursor: u64,
    #[serde(default = "default_pattern")]
    pub pattern: String,
    #[serde(default = "default_count")]
    pub count: u32,
}

fn default_pattern() -> String {
    "*".into()
}
fn default_count() -> u32 {
    100
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisSetRequest {
    pub key: String,
    pub value: RedisValue,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ttl: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisExecCommandRequest {
    pub args: Vec<String>,
}
