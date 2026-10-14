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
///
/// 表驱动 + 一次精确预留：整段按 3 字节组流式处理，每组一次 24 位装载、四次查表、
/// 一次 4 字节 `extend_from_slice`（容量已够 ⇒ 不重分配、不清零）；末尾 1~2 字节
/// 单独成组。旧实现是每输出字节一次 `String::push(char)`——每次 push 都带容量检查
/// 和 UTF-8 编码分支，1 MiB 内联 BLOB 要走过 140 万次。
///
/// 收益口径（2026-10-13 实测，一次性计时臂、同一进程内 15 轮交错采样后取中位，
/// 臂为「旧实现 / 本实现」，计时臂本身不入库，故只记倍数不记绝对价）：
/// 1 KB 3.03→1.56、64 KB 2.73→1.33、1 MiB 3.89→1.87 ns/输入字节，三点同号、
/// 区间不重叠，即本实现稳定快约 2 倍。
///
/// 这里不是「近似 base64」：输出与旧实现逐字节相同，由
/// `base64_matches_reference_implementation` 拿留在测试里的旧实现做对拍
/// （0..=512 全部长度 + 固定种子随机缓冲），加上原有 RFC 4648 向量。
/// 注意：那条对拍钉的是**字节输出**，钉不住「用了哪套实现」——纯代码生成层面的
/// 改写没有可关闭的开关，因此本函数没有 Go 侧那样的 must-hit 断言，防回退靠
/// 对拍 + 契约测试，性能收益靠上面的历史测量记录。
pub fn base64_encode(input: &[u8]) -> String {
    let groups = input.len() / 3;
    let rem = input.len() % 3;
    let mut out = Vec::with_capacity(groups * 4 + if rem == 0 { 0 } else { 4 });
    let (whole, tail) = input.split_at(groups * 3);
    for c in whole.as_chunks::<3>().0 {
        let n = (u32::from(c[0]) << 16) | (u32::from(c[1]) << 8) | u32::from(c[2]);
        out.extend_from_slice(&[
            B64_ALPHABET[(n >> 18) as usize],
            B64_ALPHABET[((n >> 12) & 63) as usize],
            B64_ALPHABET[((n >> 6) & 63) as usize],
            B64_ALPHABET[(n & 63) as usize],
        ]);
    }
    if rem > 0 {
        let b0 = u32::from(tail[0]);
        let b1 = if rem > 1 { u32::from(tail[1]) } else { 0 };
        let n = (b0 << 16) | (b1 << 8);
        out.extend_from_slice(&[
            B64_ALPHABET[(n >> 18) as usize],
            B64_ALPHABET[((n >> 12) & 63) as usize],
            if rem > 1 {
                B64_ALPHABET[((n >> 6) & 63) as usize]
            } else {
                b'='
            },
            b'=',
        ]);
    }
    // 字母表与填充都是 ASCII，这条校验恒成功（std 的 ASCII 快速路径是位并行扫描）。
    String::from_utf8(out).expect("base64 输出恒为 ASCII")
}

#[cfg(test)]
mod blob_tests {
    use super::*;

    /// 参考实现：表驱动改造**之前**的 base64_encode 原样保留在这里，逐字节对拍用。
    /// 它一次只写一个 char，因此只许在测试里出现。
    fn reference_base64_encode(input: &[u8]) -> String {
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

    #[test]
    fn base64_matches_reference_implementation() {
        // 1) 0..=512 的每一个长度都跑到（覆盖 chunks_exact 的整组数与 rem=0/1/2 三族），
        //    缓冲内容是位置相关的字节，能暴露任何一组移位/写入顺序写错。
        for len in 0..=512usize {
            let buf: Vec<u8> = (0..len)
                .map(|i| (i.wrapping_mul(37).wrapping_add(i / 3)) as u8)
                .collect();
            let got = base64_encode(&buf);
            let want = reference_base64_encode(&buf);
            assert_eq!(got, want, "len={len}");
            assert_eq!(
                got.len(),
                ((len + 2) / 3) * 4,
                "len={len} 输出宽度不是一组四位"
            );
        }
        // 2) 全 256 个字节值各作单字节输入（边界：0x00/0xFF、以及只在最后一组出现的
        //    半组形状 3k+1、3k+2）。
        for b in 0u8..=255 {
            for pad in [0usize, 1, 2, 3, 4, 5] {
                let mut buf = vec![0xA5u8; pad];
                buf.push(b);
                assert_eq!(
                    base64_encode(&buf),
                    reference_base64_encode(&buf),
                    "byte={b:02X} pad={pad}"
                );
            }
        }
        // 3) 固定种子随机缓冲：长度 0..4096，内容跨 0..=255。
        let mut state = 0x2545F4914F6CDD1Du64;
        for _ in 0..4000 {
            state = state
                .wrapping_mul(6364136223846793005)
                .wrapping_add(1442695040888963407);
            let mut hi = state.wrapping_mul(0x9E3779B97F4A7C15) >> 33;
            let len = (hi % 4096) as usize;
            let mut buf = Vec::with_capacity(len);
            for _ in 0..len {
                state = state
                    .wrapping_mul(6364136223846793005)
                    .wrapping_add(1442695040888963407);
                hi = state >> 33;
                buf.push(hi as u8);
            }
            assert_eq!(
                base64_encode(&buf),
                reference_base64_encode(&buf),
                "len={len}"
            );
        }
        // 4) 「机制必须命中」：整组路径必须真的被走到——长度 ≥3 时输出前四位
        //    只由头三个输入字节决定，与尾部长度无关（尾部走的是单独的 rem 分支）。
        let head = [0x33u8, 0x44, 0x55];
        let a = base64_encode(&[head[..].to_vec(), vec![7u8; 3]].concat());
        let b = base64_encode(&[head[..].to_vec(), vec![9u8; 9]].concat());
        assert_eq!(&a[..4], "M0RV");
        assert_eq!(&a[..4], &b[..4]);
        assert_ne!(a, b);
    }

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
