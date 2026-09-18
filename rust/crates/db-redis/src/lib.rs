//! polydb-db-redis：基于 redis crate（同步 API）的 KV 驱动实现（对应 Go 侧 dbredis）。
//! 阻塞式调用直接放在 async 方法内（与 db-sqlite / db-oracle 一致），
//! 拨号与读/写超时兜底，避免不可达主机长挂。

use std::collections::HashMap;
use std::time::Duration;

use async_trait::async_trait;
use parking_lot::Mutex;
use redis::Value as RValue;

use polydb_core::{
    CoreError, CoreResult, DatabaseKind, RedisKeyInfo, RedisKeyType, RedisReply, RedisReplyType,
    RedisReplyValue, RedisScanPage, RedisValue, RedisZSetMember,
};
use polydb_db_core::{DatabaseDriver, KvDriver};

/// 拨号/读/写超时（与 Go 侧 5s 对齐）。
const TIMEOUT: Duration = Duration::from_secs(5);

/// conn 为 Option：close() 时 take 掉并 drop，同步 Connection 在 Drop 时关闭 socket。
pub struct RedisConn {
    conn: Mutex<Option<redis::Connection>>,
    _addr: String,
}

impl RedisConn {
    /// addr 形如 host:port，db_index 为初始 db 编号，password 为可选连接密码
    /// （明文来自 keyring，连接时一次性注入，不落库；与 Go 侧 dbredis.Open 对齐）。
    pub fn open(addr: &str, db_index: u32, password: &str) -> CoreResult<Self> {
        let (host, port) = match addr.rsplit_once(':') {
            Some((h, p)) => (h.to_string(), p.parse::<u16>().unwrap_or(6379)),
            None => (addr.to_string(), 6379),
        };
        let conn_info = redis::ConnectionInfo {
            addr: redis::ConnectionAddr::Tcp(host, port),
            redis: redis::RedisConnectionInfo {
                db: db_index as i64,
                username: None,
                password: if password.is_empty() {
                    None
                } else {
                    Some(password.to_string())
                },
                protocol: redis::ProtocolVersion::RESP2,
            },
        };
        let client = redis::Client::open(conn_info)
            .map_err(|e| CoreError::Driver(format!("open redis: {e}")))?;
        let mut conn = client
            .get_connection_with_timeout(TIMEOUT)
            .map_err(|e| CoreError::Driver(format!("connect redis: {e}")))?;
        conn.set_read_timeout(Some(TIMEOUT))
            .map_err(|e| CoreError::Driver(format!("set read timeout: {e}")))?;
        conn.set_write_timeout(Some(TIMEOUT))
            .map_err(|e| CoreError::Driver(format!("set write timeout: {e}")))?;
        redis::cmd("PING")
            .query::<()>(&mut conn)
            .map_err(|e| CoreError::Driver(format!("ping redis: {e}")))?;
        if db_index > 0 {
            redis::cmd("SELECT")
                .arg(db_index)
                .query::<()>(&mut conn)
                .map_err(|e| CoreError::Driver(format!("select db {db_index}: {e}")))?;
        }
        Ok(Self {
            conn: Mutex::new(Some(conn)),
            _addr: format!("redis://{addr}/{db_index}"),
        })
    }

    fn err(e: impl std::fmt::Display) -> CoreError {
        CoreError::Driver(format!("redis: {e}"))
    }

    fn closed() -> CoreError {
        CoreError::Driver("redis: connection closed".into())
    }

    fn key_type_of(typ: &str) -> RedisKeyType {
        match typ {
            "string" => RedisKeyType::String,
            "list" => RedisKeyType::List,
            "set" => RedisKeyType::Set,
            "zset" => RedisKeyType::Zset,
            "hash" => RedisKeyType::Hash,
            "stream" => RedisKeyType::Stream,
            _ => RedisKeyType::None,
        }
    }
}

#[async_trait]
impl DatabaseDriver for RedisConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Redis
    }

    async fn ping(&self) -> CoreResult<()> {
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        redis::cmd("PING").query::<()>(conn).map_err(Self::err)
    }

    async fn close(&self) -> CoreResult<()> {
        // take 后 drop：同步 Connection 在 Drop 时关闭底层 socket。
        let mut guard = self.conn.lock();
        drop(guard.take());
        Ok(())
    }

    fn as_kv(&self) -> Option<&dyn KvDriver> {
        Some(self)
    }
}

#[async_trait]
impl KvDriver for RedisConn {
    async fn select_db(&self, index: u32) -> CoreResult<()> {
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        redis::cmd("SELECT")
            .arg(index)
            .query::<()>(conn)
            .map_err(Self::err)
    }

    async fn scan_keys(&self, cursor: u64, pattern: &str, count: u32) -> CoreResult<RedisScanPage> {
        let pattern = if pattern.is_empty() { "*" } else { pattern };
        let count = if count == 0 { 100 } else { count };
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        let (next, keys): (u64, Vec<String>) = redis::cmd("SCAN")
            .arg(cursor)
            .arg("MATCH")
            .arg(pattern)
            .arg("COUNT")
            .arg(count)
            .query(conn)
            .map_err(Self::err)?;
        let mut page = RedisScanPage {
            cursor: next,
            keys: Vec::with_capacity(keys.len()),
        };
        for k in keys {
            let typ: String = redis::cmd("TYPE").arg(&k).query(conn).map_err(Self::err)?;
            let ttl: i64 = redis::cmd("TTL").arg(&k).query(conn).map_err(Self::err)?;
            page.keys.push(RedisKeyInfo {
                key: k,
                key_type: Self::key_type_of(&typ),
                ttl: Some(ttl),
            });
        }
        Ok(page)
    }

    async fn key_type(&self, key: &str) -> CoreResult<RedisKeyType> {
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        let typ: String = redis::cmd("TYPE").arg(key).query(conn).map_err(Self::err)?;
        Ok(Self::key_type_of(&typ))
    }

    async fn get_value(&self, key: &str) -> CoreResult<RedisValue> {
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        let typ: String = redis::cmd("TYPE").arg(key).query(conn).map_err(Self::err)?;
        match Self::key_type_of(&typ) {
            RedisKeyType::None => Err(CoreError::Driver(format!("key not found: {key}"))),
            RedisKeyType::String => {
                let v: String = redis::cmd("GET").arg(key).query(conn).map_err(Self::err)?;
                Ok(RedisValue::String(v))
            }
            RedisKeyType::List => {
                let v: Vec<String> = redis::cmd("LRANGE")
                    .arg(key)
                    .arg(0)
                    .arg(-1)
                    .query(conn)
                    .map_err(Self::err)?;
                Ok(RedisValue::List(v))
            }
            RedisKeyType::Set => {
                let v: Vec<String> = redis::cmd("SMEMBERS")
                    .arg(key)
                    .query(conn)
                    .map_err(Self::err)?;
                Ok(RedisValue::Set(v))
            }
            RedisKeyType::Zset => {
                // WITHSCORES 返回扁平 [member, score, ...]，元组 FromRedisValue
                // 只认嵌套数组，需手动配对（与 Go 侧 ZRangeWithScores 对齐）。
                let flat: Vec<String> = redis::cmd("ZRANGE")
                    .arg(key)
                    .arg(0)
                    .arg(-1)
                    .arg("WITHSCORES")
                    .query(conn)
                    .map_err(Self::err)?;
                let mut members = Vec::with_capacity(flat.len() / 2);
                let mut it = flat.into_iter();
                while let (Some(member), Some(score)) = (it.next(), it.next()) {
                    members.push(RedisZSetMember {
                        member,
                        score: score.parse().unwrap_or(0.0),
                    });
                }
                Ok(RedisValue::Zset(members))
            }
            RedisKeyType::Hash => {
                // HGETALL 返回扁平 [field, value, ...]，HashMap 的 FromRedisValue
                // 通过 as_map_iter 配对连续元素。
                let v: HashMap<String, String> = redis::cmd("HGETALL")
                    .arg(key)
                    .query(conn)
                    .map_err(Self::err)?;
                Ok(RedisValue::Hash(v))
            }
            RedisKeyType::Stream => {
                // XRANGE 每项为 [id, [field, value, ...]]，元组 + HashMap 直接解析。
                let v: Vec<(String, HashMap<String, String>)> = redis::cmd("XRANGE")
                    .arg(key)
                    .arg("-")
                    .arg("+")
                    .arg("COUNT")
                    .arg(100)
                    .query(conn)
                    .map_err(Self::err)?;
                Ok(RedisValue::Stream(serialize_stream(&v)))
            }
        }
    }

    async fn set_value(&self, key: &str, value: RedisValue) -> CoreResult<()> {
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        match &value {
            RedisValue::String(s) => redis::cmd("SET")
                .arg(key)
                .arg(s)
                .query::<()>(conn)
                .map_err(Self::err)?,
            RedisValue::List(items) => {
                set_and_expire(conn, key, |c| {
                    redis::cmd("RPUSH").arg(key).arg(items).query::<()>(c)
                })?;
            }
            RedisValue::Set(items) => {
                set_and_expire(conn, key, |c| {
                    redis::cmd("SADD").arg(key).arg(items).query::<()>(c)
                })?;
            }
            RedisValue::Zset(members) => {
                let pairs: Vec<(f64, &str)> = members
                    .iter()
                    .map(|m| (m.score, m.member.as_str()))
                    .collect();
                set_and_expire(conn, key, |c| {
                    redis::cmd("ZADD").arg(key).arg(&pairs).query::<()>(c)
                })?;
            }
            RedisValue::Hash(fields) => {
                let items: Vec<(&str, &str)> = fields
                    .iter()
                    .map(|(k, v)| (k.as_str(), v.as_str()))
                    .collect();
                set_and_expire(conn, key, |c| {
                    redis::cmd("HSET").arg(key).arg(&items).query::<()>(c)
                })?;
            }
            RedisValue::Stream(s) => {
                redis::cmd("XADD")
                    .arg(key)
                    .arg("*")
                    .arg("data")
                    .arg(s)
                    .query::<()>(conn)
                    .map_err(Self::err)?;
            }
        }
        Ok(())
    }

    async fn exec_command(&self, args: &[String]) -> CoreResult<RedisReply> {
        if args.is_empty() {
            return Ok(RedisReply {
                reply_type: RedisReplyType::Error,
                value: Some(RedisReplyValue::String("empty command".into())),
            });
        }
        let mut guard = self.conn.lock();
        let conn = guard.as_mut().ok_or_else(Self::closed)?;
        let mut cmd = redis::cmd(&args[0]);
        for a in &args[1..] {
            cmd.arg(a);
        }
        let v: RValue = cmd.query(conn).map_err(Self::err)?;
        Ok(reply_of(&v))
    }
}

// ─── 转换辅助 ────────────────────────────────────────────────

// set_and_expire 先删后建（覆盖语义）。TTL 不在 driver API（AGENTS.md 定义的
// set_value 只有 key+value），由 exec_command EXPIRE 等命令显式设置。
fn set_and_expire(
    conn: &mut redis::Connection,
    key: &str,
    build: impl FnOnce(&mut redis::Connection) -> redis::RedisResult<()>,
) -> CoreResult<()> {
    redis::cmd("DEL")
        .arg(key)
        .query::<()>(conn)
        .map_err(RedisConn::err)?;
    build(conn).map_err(RedisConn::err)
}

// reply_of 将 redis crate 的 Value 转换为协议 RedisReply。
//
// 约定（双实现统一）：RESP 的 simple string、OK status 与 bulk string 一律
// 映射为 bulk_string（对应 Go 侧 goReply：go-redis 的 Cmd.Val() 无法区分
// simple 与 bulk，两端统一按 bulk_string 处理）。数组内嵌套的 error 映射为
// error 类型，Attribute 剥壳后递归，Set/Push 按数组处理。
fn reply_of(v: &RValue) -> RedisReply {
    use RValue::*;
    match v {
        Nil => RedisReply {
            reply_type: RedisReplyType::Null,
            value: None,
        },
        Int(i) => RedisReply {
            reply_type: RedisReplyType::Integer,
            value: Some(RedisReplyValue::Integer(*i)),
        },
        BulkString(b) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(
                String::from_utf8_lossy(b).into_owned(),
            )),
        },
        SimpleString(s) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(s.clone())),
        },
        Okay => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String("OK".into())),
        },
        Array(items) | Set(items) | Push { data: items, .. } => RedisReply {
            reply_type: RedisReplyType::Array,
            value: Some(RedisReplyValue::Array(items.iter().map(reply_of).collect())),
        },
        Attribute { data, .. } => reply_of(data),
        ServerError(e) => {
            let msg = match e.details() {
                Some(d) if !d.is_empty() => format!("{} {}", e.code(), d),
                _ => e.code().to_string(),
            };
            RedisReply {
                reply_type: RedisReplyType::Error,
                value: Some(RedisReplyValue::String(msg)),
            }
        }
        Map(kvs) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(map_repr(kvs))),
        },
        Double(f) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(f.to_string())),
        },
        Boolean(b) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(b.to_string())),
        },
        VerbatimString { text, .. } => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(text.clone())),
        },
        BigNumber(n) => RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String(n.to_string())),
        },
    }
}

// map_repr 与 Go fmt.Sprint(map) 对齐：按 key 排序的 "map[k:v k2:v2]"。
// RESP3 map 属边缘情况，契约测试（RESP2）不覆盖。
fn map_repr(kvs: &[(RValue, RValue)]) -> String {
    let mut items: Vec<(String, String)> = kvs
        .iter()
        .map(|(k, v)| (scalar_repr(k), scalar_repr(v)))
        .collect();
    items.sort_by(|a, b| a.0.cmp(&b.0));
    let joined = items
        .iter()
        .map(|(k, v)| format!("{k}:{v}"))
        .collect::<Vec<_>>()
        .join(" ");
    format!("map[{joined}]")
}

fn scalar_repr(v: &RValue) -> String {
    use RValue::*;
    match v {
        Nil => "<nil>".to_string(),
        Int(i) => i.to_string(),
        BulkString(b) => String::from_utf8_lossy(b).into_owned(),
        SimpleString(s) => s.clone(),
        Okay => "OK".to_string(),
        Double(f) => f.to_string(),
        Boolean(b) => b.to_string(),
        BigNumber(n) => n.to_string(),
        _ => format!("{v:?}"),
    }
}

// serialize_stream：id {k=v, k2=v2} 格式，消息间 "; " 连接（与 Go 侧一致）。
fn serialize_stream(msgs: &[(String, HashMap<String, String>)]) -> String {
    let mut parts: Vec<String> = Vec::with_capacity(msgs.len());
    for (id, fields) in msgs {
        let mut pairs: Vec<String> = fields.iter().map(|(k, v)| format!("{k}={v}")).collect();
        pairs.sort();
        parts.push(format!("{id} {{{}}}", pairs.join(", ")));
    }
    parts.join("; ")
}
