//! polydb-db-redis：基于 redis crate（同步 API）的 KV 驱动实现（对应 Go 侧 dbredis）。
//! 阻塞式调用直接放在 async 方法内（与 db-sqlite / db-oracle 一致），
//! 拨号与读/写超时兜底，避免不可达主机长挂。

use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;
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

/// D5（spec/behavior.md §1.3）：单次 scan_keys 内部续扫的轮次上限，Go 侧同名同值。
const SCAN_MAX_ROUNDS: u32 = 100;

#[cfg(test)]
mod scan_tests;

/// conn 为 Option：close() 时 take 掉并 drop，同步 Connection 在 Drop 时关闭 socket。
pub struct RedisConn {
    // Arc 包裹：阻塞调用经 spawn_blocking 卸载到阻塞池，闭包需要 'static
    // 的连接句柄（sqlite/oracle 同范式）。
    conn: Arc<Mutex<Option<redis::Connection>>>,
    /// 该专用连接当前所在 db：SELECT 是连接级状态，驱动独占此连接，故即服务端真值。
    db: AtomicU32,
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
            conn: Arc::new(Mutex::new(Some(conn))),
            db: AtomicU32::new(db_index),
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

    // with_conn runs one blocking redis round-trip on the blocking pool: the
    // sync client called inline would pin a runtime worker for the whole
    // round trip (sqlite/oracle 同范式，wave 15/16 + 38).
    async fn with_conn<T, F>(&self, f: F) -> CoreResult<T>
    where
        T: Send + 'static,
        F: FnOnce(&mut redis::Connection) -> CoreResult<T> + Send + 'static,
    {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let mut guard = conn.lock();
            let conn = guard.as_mut().ok_or_else(Self::closed)?;
            f(conn)
        })
        .await
        .map_err(|e| CoreError::Driver(format!("redis task: {e}")))?
    }
}

#[async_trait]
impl DatabaseDriver for RedisConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Redis
    }

    async fn ping(&self) -> CoreResult<()> {
        self.with_conn(|conn| redis::cmd("PING").query::<()>(conn).map_err(Self::err))
            .await
    }

    async fn close(&self) -> CoreResult<()> {
        // take 后 drop：同步 Connection 在 Drop 时关闭底层 socket。不走
        // with_conn——take 需要的是 Option 本体，而 with_conn 交付的是已
        // 解引用的连接。
        let conn = Arc::clone(&self.conn);
        let res = tokio::task::spawn_blocking(move || {
            drop(conn.lock().take());
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Driver(format!("redis task: {e}")))?;
        res
    }

    fn as_kv(&self) -> Option<&dyn KvDriver> {
        Some(self)
    }
}

#[async_trait]
impl KvDriver for RedisConn {
    async fn select_db(&self, index: u32) -> CoreResult<()> {
        // 只在服务端确认 OK 后更新：失败时连接的 db 未变。
        self.with_conn(move |conn| {
            redis::cmd("SELECT")
                .arg(index)
                .query::<()>(conn)
                .map_err(Self::err)?;
            Ok(())
        })
        .await?;
        self.db.store(index, Ordering::Relaxed);
        Ok(())
    }

    async fn scan_keys(&self, cursor: u64, pattern: &str, count: u32) -> CoreResult<RedisScanPage> {
        let pattern: String = if pattern.is_empty() { "*".to_string() } else { pattern.to_string() };
        let count = if count == 0 { 100 } else { count };
        let db = self.db.load(Ordering::Relaxed);
        let (cursor, keys) = self
            .with_conn(move |conn| {
                // D5 服务端游标循环（spec/behavior.md §1.3）：单轮 SCAN 只给建议批次，
                // 这里续扫到凑满 count、游标回 0，或触及轮次上限（与 Go 侧严格一致）。
                let mut cursor = cursor;
                let mut keys: Vec<RedisKeyInfo> = Vec::new();
                for _ in 0..SCAN_MAX_ROUNDS {
                    let (next, batch): (u64, Vec<String>) = redis::cmd("SCAN")
                        .arg(cursor)
                        .arg("MATCH")
                        .arg(&pattern)
                        .arg("COUNT")
                        .arg(count)
                        .query(conn)
                        .map_err(Self::err)?;
                    for k in batch {
                        let typ: String = redis::cmd("TYPE").arg(&k).query(conn).map_err(Self::err)?;
                        let ttl: i64 = redis::cmd("TTL").arg(&k).query(conn).map_err(Self::err)?;
                        keys.push(RedisKeyInfo {
                            key: k,
                            key_type: Self::key_type_of(&typ),
                            ttl: Some(ttl),
                        });
                    }
                    cursor = next;
                    if cursor == 0 || keys.len() >= count as usize {
                        break;
                    }
                }
                Ok((cursor, keys))
            })
            .await?;
        Ok(RedisScanPage {
            cursor,
            db: Some(db),
            keys,
        })
    }

    async fn key_type(&self, key: &str) -> CoreResult<RedisKeyType> {
        let owned = key.to_string();
        let typ: String = self
            .with_conn(move |conn| redis::cmd("TYPE").arg(owned).query(conn).map_err(Self::err))
            .await?;
        Ok(Self::key_type_of(&typ))
    }

    async fn get_value(&self, key: &str) -> CoreResult<RedisValue> {
        let owned = key.to_string();
        let probe = owned.clone();
        let typ: String = self
            .with_conn(move |conn| redis::cmd("TYPE").arg(probe).query(conn).map_err(Self::err))
            .await?;
        match Self::key_type_of(&typ) {
            RedisKeyType::None => Err(CoreError::Driver(format!("key not found: {owned}"))),
            RedisKeyType::String => {
                let v: String = self
                    .with_conn(move |conn| redis::cmd("GET").arg(owned).query(conn).map_err(Self::err))
                    .await?;
                Ok(RedisValue::String(v))
            }
            RedisKeyType::List => {
                let list_key = owned.clone();
                let v: Vec<String> = self
                    .with_conn(move |conn| {
                        redis::cmd("LRANGE").arg(list_key).arg(0).arg(-1).query(conn).map_err(Self::err)
                    })
                    .await?;
                Ok(RedisValue::List(v))
            }
            RedisKeyType::Set => {
                let set_key = owned.clone();
                let v: Vec<String> = self
                    .with_conn(move |conn| {
                        redis::cmd("SMEMBERS").arg(set_key).query(conn).map_err(Self::err)
                    })
                    .await?;
                Ok(RedisValue::Set(v))
            }
            RedisKeyType::Zset => {
                // WITHSCORES 返回扁平 [member, score, ...]，元组 FromRedisValue
                // 只认嵌套数组，需手动配对（与 Go 侧 ZRangeWithScores 对齐）。
                let zset_key = owned.clone();
                let flat: Vec<String> = self
                    .with_conn(move |conn| {
                        redis::cmd("ZRANGE")
                            .arg(zset_key)
                            .arg(0)
                            .arg(-1)
                            .arg("WITHSCORES")
                            .query(conn)
                            .map_err(Self::err)
                    })
                    .await?;
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
                let hash_key = owned.clone();
                let v: HashMap<String, String> = self
                    .with_conn(move |conn| {
                        redis::cmd("HGETALL").arg(hash_key).query(conn).map_err(Self::err)
                    })
                    .await?;
                Ok(RedisValue::Hash(v))
            }
            RedisKeyType::Stream => {
                // XRANGE 每项为 [id, [field, value, ...]]，元组 + HashMap 直接解析。
                let stream_key = owned.clone();
                let v: Vec<(String, HashMap<String, String>)> = self
                    .with_conn(move |conn| {
                        redis::cmd("XRANGE")
                            .arg(stream_key)
                            .arg("-")
                            .arg("+")
                            .arg("COUNT")
                            .arg(100)
                            .query(conn)
                            .map_err(Self::err)
                    })
                    .await?;
                Ok(RedisValue::Stream(serialize_stream(&v)))
            }
        }
    }

    async fn set_value(&self, key: &str, value: RedisValue) -> CoreResult<()> {
        let key = key.to_string();
        self.with_conn(move |conn| {
            match &value {
                RedisValue::String(s) => redis::cmd("SET")
                    .arg(&key)
                    .arg(s)
                    .query::<()>(conn)
                    .map_err(Self::err)?,
                RedisValue::List(items) => {
                    set_and_expire(conn, &key, |c| {
                        redis::cmd("RPUSH").arg(&key).arg(items).query::<()>(c)
                    })?;
                }
                RedisValue::Set(items) => {
                    set_and_expire(conn, &key, |c| {
                        redis::cmd("SADD").arg(&key).arg(items).query::<()>(c)
                    })?;
                }
                RedisValue::Zset(members) => {
                    let pairs: Vec<(f64, &str)> = members
                        .iter()
                        .map(|m| (m.score, m.member.as_str()))
                        .collect();
                    set_and_expire(conn, &key, |c| {
                        redis::cmd("ZADD").arg(&key).arg(&pairs).query::<()>(c)
                    })?;
                }
                RedisValue::Hash(fields) => {
                    let items: Vec<(&str, &str)> = fields
                        .iter()
                        .map(|(k, v)| (k.as_str(), v.as_str()))
                        .collect();
                    set_and_expire(conn, &key, |c| {
                        redis::cmd("HSET").arg(&key).arg(&items).query::<()>(c)
                    })?;
                }
                RedisValue::Stream(s) => {
                    redis::cmd("XADD")
                        .arg(&key)
                        .arg("*")
                        .arg("data")
                        .arg(s)
                        .query::<()>(conn)
                        .map_err(Self::err)?;
                }
            }
            Ok(())
        })
        .await
    }

    async fn exec_command(&self, args: &[String]) -> CoreResult<RedisReply> {
        if args.is_empty() {
            return Ok(RedisReply {
                reply_type: RedisReplyType::Error,
                value: Some(RedisReplyValue::String("empty command".into())),
            });
        }
        let args = args.to_vec();
        let v: RValue = self
            .with_conn(move |conn| {
                let mut cmd = redis::cmd(&args[0]);
                for a in &args[1..] {
                    cmd.arg(a);
                }
                cmd.query(conn).map_err(Self::err)
            })
            .await?;
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
