//! P1-2（POLISH-PLAN §57.4 #2）验证：假 RESP 服务器模拟真实 Redis 的六类有界窗口读取，
//! 钉死驱动侧 `get_value_window` 的四个语义：
//! 1. 首窗只取限额内的值（不把 1M 成员/超长 string 一次拉进进程）；
//! 2. `total` 是键的完整规模（STRLEN/LLEN/SCARD/ZCARD/HLEN/XLEN），与窗口无关；
//! 3. `truncated` 标记还有更多，`next_cursor` 给出真实续点（string 无续读）；
//! 4. 续读用上一窗的 `next_cursor`，结果与首窗拼起来覆盖完整值。
//!
//! 与 Go 侧内核 `BoundedValueReader`（dbcore/redis/redis.go `GetValueBounded`）
//! 逐类型语义对齐。

use std::collections::{HashMap, HashSet};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

use polydb_core::{RedisKeyType, RedisValue, RedisZSetMember};
use polydb_db_core::{KvDriver, RedisValueLimit, RedisValueWindow};

use crate::RedisConn;

/// 假服务器里的值空间（每键一种类型）。
#[derive(Clone)]
enum BdData {
    Str(String),
    List(Vec<String>),
    Set(Vec<String>),
    Zset(Vec<(String, f64)>),
    Hash(Vec<(String, String)>),
    Stream(Vec<(String, Vec<(String, String)>)>),
}

struct BdFake {
    data: HashMap<String, BdData>,
}

fn start_fake(data: HashMap<String, BdData>) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let addr = listener.local_addr().expect("local_addr").to_string();
    let fake = Arc::new(BdFake { data });
    let f = Arc::clone(&fake);
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let f = Arc::clone(&f);
            std::thread::spawn(move || {
                if let Ok(stream) = stream {
                    serve(f, stream);
                }
            });
        }
    });
    addr
}

// ─── RESP 构造 ───

fn bulk(s: &str) -> String {
    format!("${}\r\n{}\r\n", s.len(), s)
}
fn nil() -> String {
    "$-1\r\n".to_string()
}
fn integer(n: i64) -> String {
    format!(":{}\r\n", n)
}
fn simple(s: &str) -> String {
    format!("+{}\r\n", s)
}
fn array(items: Vec<String>) -> String {
    let mut out = format!("*{}\r\n", items.len());
    for i in items {
        out.push_str(&i);
    }
    out
}

// ─── 命令解析 ───

fn read_line(rd: &mut BufReader<TcpStream>) -> Option<String> {
    let mut s = String::new();
    if rd.read_line(&mut s).ok()? == 0 {
        return None;
    }
    Some(s.trim_end_matches(['\r', '\n']).to_string())
}

fn read_cmd(rd: &mut BufReader<TcpStream>) -> Option<Vec<String>> {
    let header = read_line(rd)?;
    let n: usize = header.strip_prefix('*')?.parse().ok()?;
    if n < 1 {
        return None;
    }
    let mut args = Vec::with_capacity(n);
    for _ in 0..n {
        let h = read_line(rd)?;
        let size: usize = h.strip_prefix('$')?.parse().ok()?;
        let mut buf = vec![0u8; size + 2]; // 含结尾 CRLF
        rd.read_exact(&mut buf).ok()?;
        args.push(String::from_utf8_lossy(&buf[..size]).into_owned());
    }
    Some(args)
}

fn serve(f: Arc<BdFake>, stream: TcpStream) {
    let mut writer = stream.try_clone().expect("clone");
    let mut rd = BufReader::new(stream);
    while let Some(args) = read_cmd(&mut rd) {
        let reply = dispatch(&f, &args);
        if writer.write_all(reply.as_bytes()).is_err() {
            return;
        }
    }
}

fn dispatch(f: &BdFake, args: &[String]) -> String {
    match args[0].to_uppercase().as_str() {
        "PING" => simple("PONG"),
        "TYPE" => type_reply(f, &args[1]),
        "STRLEN" => integer(strlen(f, &args[1])),
        "GET" => get_reply(f, &args[1]),
        "GETRANGE" => getrange_reply(f, &args[1], &args[2], &args[3]),
        "LLEN" => integer(llen(f, &args[1])),
        "LRANGE" => lrange_reply(f, &args[1], &args[2], &args[3]),
        "SCARD" => integer(scard(f, &args[1])),
        "SSCAN" => sscan_reply(f, &args[1], &args[2], &args[4]),
        "ZCARD" => integer(zcard(f, &args[1])),
        "ZRANGE" => zrange_reply(f, &args[1], &args[2], &args[3]),
        "HLEN" => integer(hlen(f, &args[1])),
        "HSCAN" => hscan_reply(f, &args[1], &args[2], &args[4]),
        "XLEN" => integer(xlen(f, &args[1])),
        "XRANGE" => xrange_reply(f, &args[1], &args[2], &args[5]),
        _ => simple("OK"),
    }
}

fn type_reply(f: &BdFake, key: &str) -> String {
    match f.data.get(key) {
        None => simple("none"),
        Some(BdData::Str(_)) => simple("string"),
        Some(BdData::List(_)) => simple("list"),
        Some(BdData::Set(_)) => simple("set"),
        Some(BdData::Zset(_)) => simple("zset"),
        Some(BdData::Hash(_)) => simple("hash"),
        Some(BdData::Stream(_)) => simple("stream"),
    }
}

fn strlen(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::Str(s)) => s.len() as i64,
        _ => 0,
    }
}

fn get_reply(f: &BdFake, key: &str) -> String {
    match f.data.get(key) {
        Some(BdData::Str(s)) => bulk(s),
        _ => nil(),
    }
}

/// GETRANGE key start end（end 含）。
fn getrange_reply(f: &BdFake, key: &str, start: &str, end: &str) -> String {
    let Some(BdData::Str(s)) = f.data.get(key) else {
        return nil();
    };
    let bytes = s.as_bytes();
    let len = bytes.len() as i64;
    let start: i64 = start.parse().unwrap_or(0);
    let end: i64 = end.parse().unwrap_or(0);
    let s = start.max(0).min(len);
    let e = end.max(0).min(len - 1);
    if s > e {
        return bulk("");
    }
    bulk(&String::from_utf8_lossy(&bytes[s as usize..=e as usize]))
}

fn llen(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::List(v)) => v.len() as i64,
        _ => 0,
    }
}

/// LRANGE key start stop（两端含，越界钳制）。
fn lrange_reply(f: &BdFake, key: &str, start: &str, stop: &str) -> String {
    let Some(BdData::List(v)) = f.data.get(key) else {
        return array(vec![]);
    };
    let len = v.len() as i64;
    let start: i64 = start.parse().unwrap_or(0);
    let stop: i64 = stop.parse().unwrap_or(0);
    let s = start.max(0).min(len);
    let e = stop.max(0).min(len - 1);
    if s > e {
        return array(vec![]);
    }
    array(v[s as usize..=e as usize].iter().map(|x| bulk(x)).collect())
}

fn scard(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::Set(v)) => v.iter().collect::<HashSet<_>>().len() as i64,
        _ => 0,
    }
}

/// SSCAN：游标即成员下标（驱动只把游标当不透明字符串回传，假服务器可自定编码）。
fn sscan_reply(f: &BdFake, key: &str, cursor: &str, count: &str) -> String {
    let Some(BdData::Set(v)) = f.data.get(key) else {
        return array(vec![bulk("0"), array(vec![])]);
    };
    let len = v.len() as u64;
    let cur: u64 = cursor.parse().unwrap_or(0);
    let count: u64 = count.parse().unwrap_or(10).max(1);
    let start = cur.min(len);
    let end = (start + count).min(len);
    let batch: Vec<String> = v[start as usize..end as usize]
        .iter()
        .map(|x| bulk(x))
        .collect();
    let next = if end < len { end } else { 0 };
    array(vec![bulk(&next.to_string()), array(batch)])
}

fn zcard(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::Zset(v)) => v.len() as i64,
        _ => 0,
    }
}

/// ZRANGE key start stop WITHSCORES：交替 member/score。
fn zrange_reply(f: &BdFake, key: &str, start: &str, stop: &str) -> String {
    let Some(BdData::Zset(v)) = f.data.get(key) else {
        return array(vec![]);
    };
    let len = v.len() as i64;
    let start: i64 = start.parse().unwrap_or(0);
    let stop: i64 = stop.parse().unwrap_or(0);
    let s = start.max(0).min(len);
    let e = stop.max(0).min(len - 1);
    if s > e {
        return array(vec![]);
    }
    let mut items = Vec::new();
    for (m, sc) in &v[s as usize..=e as usize] {
        items.push(bulk(m));
        items.push(bulk(&format!("{}", sc)));
    }
    array(items)
}

fn hlen(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::Hash(v)) => v.len() as i64,
        _ => 0,
    }
}

/// HSCAN：游标即字段下标，返回 [cursor, [field, value, ...]]。
fn hscan_reply(f: &BdFake, key: &str, cursor: &str, count: &str) -> String {
    let Some(BdData::Hash(v)) = f.data.get(key) else {
        return array(vec![bulk("0"), array(vec![])]);
    };
    let len = v.len() as u64;
    let cur: u64 = cursor.parse().unwrap_or(0);
    let count: u64 = count.parse().unwrap_or(10).max(1);
    let start = cur.min(len);
    let end = (start + count).min(len);
    let mut flat = Vec::new();
    for (k, val) in &v[start as usize..end as usize] {
        flat.push(bulk(k));
        flat.push(bulk(val));
    }
    let next = if end < len { end } else { 0 };
    array(vec![bulk(&next.to_string()), array(flat)])
}

fn xlen(f: &BdFake, key: &str) -> i64 {
    match f.data.get(key) {
        Some(BdData::Stream(v)) => v.len() as i64,
        _ => 0,
    }
}

/// XRANGE key start stop COUNT n：start 支持 "-" 与 "(<id>"（开区间）。
/// 真实 Redis 形状（已对拍 6379 实机）：外层 *N，每项 *2 [id, fields]，
/// fields 是**扁平**数组 *M [k1, v1, k2, v2, ...]（不是嵌套 pair）。
fn xrange_reply(f: &BdFake, key: &str, start_tok: &str, count: &str) -> String {
    let Some(BdData::Stream(v)) = f.data.get(key) else {
        return array(vec![]);
    };
    let count: usize = count.parse().unwrap_or(10);
    let start = start_tok.strip_prefix('(').unwrap_or(start_tok);
    let start = if start == "-" { "" } else { start };
    let mut entries = Vec::new();
    for (id, fields) in v.iter().filter(|(id, _)| id.as_str() > start).take(count) {
        let mut inner = Vec::new();
        for (k, val) in fields {
            inner.push(bulk(k));
            inner.push(bulk(val));
        }
        entries.push(array(vec![bulk(id), array(inner)]));
    }
    array(entries)
}

fn open_fake(addr: &str) -> RedisConn {
    match RedisConn::open(addr, 0, "") {
        Ok(c) => c,
        Err(e) => panic!("open fake redis: {e}"),
    }
}

fn mk(data: HashMap<String, BdData>) -> RedisConn {
    open_fake(&start_fake(data))
}

async fn window(
    c: &RedisConn,
    key: &str,
    limit: RedisValueLimit,
    cursor: &str,
) -> RedisValueWindow {
    c.get_value_window(key, limit, cursor)
        .await
        .expect("get_value_window")
}

// ─── 断言访问器（RedisValue 无 PartialEq，按变体取内部）───

fn as_str(v: &RedisValue) -> &str {
    match v {
        RedisValue::String(s) => s,
        other => panic!("expected string, got {other:?}"),
    }
}

fn as_list(v: &RedisValue) -> &[String] {
    match v {
        RedisValue::List(v) => v,
        other => panic!("expected list, got {other:?}"),
    }
}
fn as_set(v: &RedisValue) -> &[String] {
    match v {
        RedisValue::Set(v) => v,
        other => panic!("expected set, got {other:?}"),
    }
}
fn as_zset(v: &RedisValue) -> &[RedisZSetMember] {
    match v {
        RedisValue::Zset(v) => v,
        other => panic!("expected zset, got {other:?}"),
    }
}
fn as_hash(v: &RedisValue) -> &HashMap<String, String> {
    match v {
        RedisValue::Hash(v) => v,
        other => panic!("expected hash, got {other:?}"),
    }
}
fn as_stream(v: &RedisValue) -> &str {
    match v {
        RedisValue::Stream(s) => s,
        other => panic!("expected stream, got {other:?}"),
    }
}

// ─── 测试 ───

/// 限额缺省解析：<=0 → 内核常量（64KB / 1000）。
#[test]
fn limit_defaults_resolve_to_kernel_constants() {
    let r = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: -5,
    }
    .resolved();
    assert_eq!(r.max_string_bytes, 64 << 10);
    assert_eq!(r.max_entries, 1000);
}

#[tokio::test]
async fn bounded_string_truncates_on_utf8_boundary() {
    // "ab你cd"：a(1) b(1) 你(3) c(1) d(1) = 7 字节；限 4 字节切在你中间。
    let mut data = HashMap::new();
    data.insert("k".to_string(), BdData::Str("ab你cd".to_string()));
    let c = mk(data);

    let w = window(
        &c,
        "k",
        RedisValueLimit {
            max_string_bytes: 4,
            max_entries: 0,
        },
        "",
    )
    .await;
    assert_eq!(w.total, 7, "STRLEN 是完整字节数");
    assert!(w.truncated, "7 > 4 必须截断");
    assert_eq!(as_str(&w.value), "ab", "回退到字符边界");
    assert_eq!(w.next_cursor, "", "string 无续读游标");
}

#[tokio::test]
async fn bounded_string_under_limit_returns_full() {
    let mut data = HashMap::new();
    data.insert("k".to_string(), BdData::Str("hello".to_string()));
    let c = mk(data);

    let w = window(
        &c,
        "k",
        RedisValueLimit {
            max_string_bytes: 10,
            max_entries: 0,
        },
        "",
    )
    .await;
    assert_eq!(w.total, 5);
    assert!(!w.truncated);
    assert_eq!(as_str(&w.value), "hello");
    assert_eq!(w.next_cursor, "");
}

#[tokio::test]
async fn bounded_list_window_and_continuation() {
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::List(vec![
            "a".into(),
            "b".into(),
            "c".into(),
            "d".into(),
            "e".into(),
        ]),
    );
    let c = mk(data);
    let lim = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: 2,
    };

    let w1 = window(&c, "k", lim, "").await;
    assert_eq!(w1.total, 5);
    assert!(w1.truncated);
    assert_eq!(as_list(&w1.value), vec!["a".to_string(), "b".to_string()]);
    assert_eq!(w1.next_cursor, "rank:2");

    let w2 = window(&c, "k", lim, &w1.next_cursor).await;
    assert_eq!(w2.total, 5);
    assert!(w2.truncated);
    assert_eq!(as_list(&w2.value), vec!["c".to_string(), "d".to_string()]);
    assert_eq!(w2.next_cursor, "rank:4");

    let w3 = window(&c, "k", lim, &w2.next_cursor).await;
    assert_eq!(w3.total, 5);
    assert!(!w3.truncated, "末窗无更多");
    assert_eq!(as_list(&w3.value), vec!["e".to_string()]);
    assert_eq!(w3.next_cursor, "");
}

/// SSCAN 在同一批内可能重复成员（rehash 中途），驱动窗口内去重后排序。
#[tokio::test]
async fn bounded_set_dedups_and_sorts() {
    // 数据里 "b" 重复，但 SCARD 是去重基数（2），SSCAN 一批取回全部。
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Set(vec!["b".into(), "b".into(), "a".into()]),
    );
    let c = mk(data);
    let w = window(
        &c,
        "k",
        RedisValueLimit {
            max_string_bytes: 0,
            max_entries: 3,
        },
        "",
    )
    .await;
    assert_eq!(w.total, 2, "SCARD 去重基数");
    assert!(!w.truncated, "一批取完，游标归零");
    assert_eq!(
        as_set(&w.value),
        vec!["a".to_string(), "b".to_string()],
        "批内去重 + 排序"
    );
    assert_eq!(w.next_cursor, "");
}

#[tokio::test]
async fn bounded_set_window_and_continuation() {
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Set(vec![
            "a".into(),
            "b".into(),
            "c".into(),
            "d".into(),
            "e".into(),
        ]),
    );
    let c = mk(data);
    let lim = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: 2,
    };

    let w1 = window(&c, "k", lim, "").await;
    assert_eq!(w1.total, 5);
    assert!(w1.truncated);
    assert_eq!(as_set(&w1.value), vec!["a".to_string(), "b".to_string()]);
    assert_eq!(w1.next_cursor, "scan:2");

    let w2 = window(&c, "k", lim, &w1.next_cursor).await;
    assert_eq!(w2.total, 5);
    assert!(w2.truncated);
    assert_eq!(as_set(&w2.value), vec!["c".to_string(), "d".to_string()]);
    assert_eq!(w2.next_cursor, "scan:4");

    let w3 = window(&c, "k", lim, &w2.next_cursor).await;
    assert_eq!(w3.total, 5);
    assert!(!w3.truncated, "末窗无更多");
    assert_eq!(as_set(&w3.value), vec!["e".to_string()]);
    assert_eq!(w3.next_cursor, "");
}

#[tokio::test]
async fn bounded_zset_window_and_continuation() {
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Zset(vec![
            ("m1".to_string(), 1.0),
            ("m2".to_string(), 2.0),
            ("m3".to_string(), 3.0),
        ]),
    );
    let c = mk(data);
    let lim = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: 2,
    };

    let w1 = window(&c, "k", lim, "").await;
    assert_eq!(w1.total, 3);
    assert!(w1.truncated);
    let z = as_zset(&w1.value);
    assert_eq!(z.len(), 2);
    assert_eq!(z[0].member, "m1");
    assert_eq!(z[0].score, 1.0);
    assert_eq!(z[1].member, "m2");
    assert_eq!(z[1].score, 2.0);
    assert_eq!(w1.next_cursor, "rank:2");

    let w2 = window(&c, "k", lim, &w1.next_cursor).await;
    assert_eq!(w2.total, 3);
    assert!(!w2.truncated);
    let z = as_zset(&w2.value);
    assert_eq!(z.len(), 1);
    assert_eq!(z[0].member, "m3");
    assert_eq!(z[0].score, 3.0);
    assert_eq!(w2.next_cursor, "");
}

#[tokio::test]
async fn bounded_hash_window_and_continuation() {
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Hash(vec![
            ("f1".to_string(), "v1".to_string()),
            ("f2".to_string(), "v2".to_string()),
            ("f3".to_string(), "v3".to_string()),
        ]),
    );
    let c = mk(data);
    let lim = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: 2,
    };

    let w1 = window(&c, "k", lim, "").await;
    assert_eq!(w1.total, 3);
    assert!(w1.truncated);
    let m = as_hash(&w1.value);
    assert_eq!(m.len(), 2);
    assert_eq!(m.get("f1").map(String::as_str), Some("v1"));
    assert_eq!(m.get("f2").map(String::as_str), Some("v2"));
    assert!(!w1.next_cursor.is_empty());

    let w2 = window(&c, "k", lim, &w1.next_cursor).await;
    assert_eq!(w2.total, 3);
    assert!(!w2.truncated);
    let m = as_hash(&w2.value);
    assert_eq!(m.len(), 1);
    assert_eq!(m.get("f3").map(String::as_str), Some("v3"));
    assert_eq!(w2.next_cursor, "");
}

#[tokio::test]
async fn bounded_stream_window_and_continuation() {
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Stream(
            (1..=5)
                .map(|i| (format!("{i}-0"), vec![(format!("f{i}"), format!("v{i}"))]))
                .collect(),
        ),
    );
    let c = mk(data);
    let lim = RedisValueLimit {
        max_string_bytes: 0,
        max_entries: 2,
    };

    let w1 = window(&c, "k", lim, "").await;
    assert_eq!(w1.total, 5);
    assert!(w1.truncated);
    assert_eq!(w1.next_cursor, "stream:2-0", "游标是末条 ID");
    let s1 = as_stream(&w1.value);
    assert!(s1.contains("1-0"), "首窗含 1-0: {s1}");
    assert!(s1.contains("2-0"), "首窗含 2-0: {s1}");

    let w2 = window(&c, "k", lim, &w1.next_cursor).await;
    assert_eq!(w2.total, 5);
    assert!(w2.truncated);
    assert_eq!(w2.next_cursor, "stream:4-0");
    let s2 = as_stream(&w2.value);
    assert!(s2.contains("3-0"), "续窗含 3-0: {s2}");
    assert!(s2.contains("4-0"), "续窗含 4-0: {s2}");

    let w3 = window(&c, "k", lim, &w2.next_cursor).await;
    assert_eq!(w3.total, 5);
    assert!(!w3.truncated);
    assert_eq!(w3.next_cursor, "");
    assert!(as_stream(&w3.value).contains("5-0"));
}

#[tokio::test]
async fn bounded_missing_key_errors() {
    let c = mk(HashMap::new());
    let err = c
        .get_value_window("nope", RedisValueLimit::default(), "")
        .await
        .expect_err("缺失键必须报错");
    assert!(err.to_string().contains("key not found"));
}

#[tokio::test]
async fn bounded_wrong_type_cursor_is_opaque() {
    // 驱动的游标是私有不透明串：把 list 的游标喂给 zset 不应 panic，
    // 只按 zset 名次窗口解释（rank:2 → 从第 2 项起）。
    let mut data = HashMap::new();
    data.insert(
        "k".to_string(),
        BdData::Zset(vec![
            ("m1".to_string(), 1.0),
            ("m2".to_string(), 2.0),
            ("m3".to_string(), 3.0),
        ]),
    );
    let c = mk(data);
    let w = window(
        &c,
        "k",
        RedisValueLimit {
            max_string_bytes: 0,
            max_entries: 1,
        },
        "rank:2",
    )
    .await;
    assert_eq!(w.total, 3);
    let z = as_zset(&w.value);
    assert_eq!(z.len(), 1);
    assert_eq!(z[0].member, "m3");
    assert_eq!(z[0].score, 3.0);
}

/// 类型标签仍可用（顺带覆盖 TYPE 分派）。
#[tokio::test]
async fn bounded_key_type_dispatch() {
    let mut data = HashMap::new();
    data.insert("s".to_string(), BdData::Str("x".into()));
    data.insert("l".to_string(), BdData::List(vec!["x".into()]));
    data.insert("z".to_string(), BdData::Zset(vec![("x".into(), 1.0)]));
    let c = mk(data);
    assert_eq!(c.key_type("s").await.unwrap(), RedisKeyType::String);
    assert_eq!(c.key_type("l").await.unwrap(), RedisKeyType::List);
    assert_eq!(c.key_type("z").await.unwrap(), RedisKeyType::Zset);
    assert_eq!(c.key_type("nope").await.unwrap(), RedisKeyType::None);
}
