//! D5（spec/behavior.md §1.3）验证：最小 RESP 假服务器模拟真实 Redis 的分批 SCAN
//! （每批 ≤ COUNT 个键、MATCH 在批内过滤），钉死驱动侧游标循环的三个语义：
//! 凑满一页、无匹配时单调用穷尽、轮次上限处停下并给出真实续点。
//! 与 Go 侧 scanloop_test.go 同一键空间与断言，保证双实现行为一致。

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use polydb_core::RedisKeyType;
use polydb_db_core::KvDriver;

use crate::{RedisConn, SCAN_MAX_ROUNDS};

struct Fake {
    keys: Vec<String>,
    scans: AtomicU64,
}

/// keyspace：250 个键，i%50==7 的用 "sel:" 前缀 → sel:* 恰好匹配 5 个（i=7,57,107,157,207）。
fn start_fake() -> (String, Arc<Fake>) {
    let keys: Vec<String> = (0..250)
        .map(|i| {
            if i % 50 == 7 {
                format!("sel:{i:03}")
            } else {
                format!("k{i:03}")
            }
        })
        .collect();
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let addr = listener.local_addr().expect("local_addr").to_string();
    let fake = Arc::new(Fake {
        keys,
        scans: AtomicU64::new(0),
    });
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
    (addr, fake)
}

/// glob 只需覆盖测试用到的三种形态："*"、"prefix*"、精确名。
fn glob_match(pattern: &str, key: &str) -> bool {
    if pattern == "*" {
        return true;
    }
    if let Some(prefix) = pattern.strip_suffix('*') {
        return key.starts_with(prefix);
    }
    pattern == key
}

fn read_line(rd: &mut BufReader<TcpStream>) -> Option<String> {
    let mut s = String::new();
    if rd.read_line(&mut s).ok()? == 0 {
        return None;
    }
    Some(s.trim_end_matches(['\r', '\n']).to_string())
}

/// 解析 RESP2 数组命令（驱动只会发数组，无需内联命令）。
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

fn serve(f: Arc<Fake>, stream: TcpStream) {
    let mut writer = stream.try_clone().expect("clone");
    let mut rd = BufReader::new(stream);
    while let Some(args) = read_cmd(&mut rd) {
        let reply = match args[0].to_uppercase().as_str() {
            "PING" => "+PONG\r\n".to_string(),
            "TYPE" => "+string\r\n".to_string(),
            "TTL" => ":-1\r\n".to_string(),
            "SCAN" => scan_reply(&f, &args[1..]),
            _ => "+OK\r\n".to_string(),
        };
        if writer.write_all(reply.as_bytes()).is_err() {
            return;
        }
    }
}

fn scan_reply(f: &Fake, args: &[String]) -> String {
    let cur: u64 = args.first().and_then(|s| s.parse().ok()).unwrap_or(0);
    let mut pattern = "*".to_string();
    let mut count: usize = 10;
    let mut i = 1;
    while i + 1 < args.len() {
        match args[i].to_uppercase().as_str() {
            "MATCH" => pattern = args[i + 1].clone(),
            "COUNT" => count = args[i + 1].parse().unwrap_or(10),
            _ => {}
        }
        i += 2;
    }
    f.scans.fetch_add(1, Ordering::SeqCst);
    if count == 0 {
        count = 10;
    }
    // 真实 SCAN 语义：按游标切片采样（≤COUNT 个键），MATCH 只过滤采样批不过滤游标推进。
    let len = f.keys.len() as u64;
    let start = cur.min(len);
    let mut next = start + count as u64;
    if next >= len {
        next = 0;
    }
    let end = (start + count as u64).min(len);
    let matched: Vec<&String> = f.keys[start as usize..end as usize]
        .iter()
        .filter(|k| glob_match(&pattern, k))
        .collect();
    let mut out = format!(
        "*2\r\n${}\r\n{}\r\n*{}\r\n",
        next.to_string().len(),
        next,
        matched.len()
    );
    for k in matched {
        out.push_str(&format!("${}\r\n{}\r\n", k.len(), k));
    }
    out
}

fn open_fake(addr: &str) -> RedisConn {
    match RedisConn::open(addr, 0, "") {
        Ok(c) => c,
        Err(e) => panic!("open fake redis: {e}"),
    }
}

#[tokio::test]
async fn scan_keys_fills_one_page_across_rounds() {
    let (addr, fake) = start_fake();
    let c = open_fake(&addr);

    let page = c.scan_keys(0, "*", 20).await.expect("scan");
    assert_eq!(page.keys.len(), 20, "count=20 必须单调用凑满 20 键");
    assert_eq!(page.keys[0].key, "k000");
    assert_eq!(page.keys[19].key, "k019");
    assert_eq!(page.cursor, 20, "游标须停在真实续点");
    assert_eq!(page.db, Some(0));
    for k in &page.keys {
        assert_eq!(k.key_type, RedisKeyType::String);
        assert_eq!(k.ttl, Some(-1));
    }

    // count=7：整批保留后 len≥count 才停（真实 Redis 也允许批内多给）。
    fake.scans.store(0, Ordering::SeqCst);
    let page = c.scan_keys(0, "*", 7).await.expect("scan7");
    assert!(page.keys.len() >= 7, "循环须凑满一页");
    assert_ne!(page.cursor, 0, "250 键未扫完，游标非 0");
}

#[tokio::test]
async fn scan_keys_exhausts_with_selective_pattern() {
    let (addr, _fake) = start_fake();
    let c = open_fake(&addr);

    // sel:* 的 5 个匹配分散在 i=7,57,107,157,207：count=3 时须跨 36 轮凑满 3 个。
    let page = c.scan_keys(0, "sel:*", 3).await.expect("scan sel");
    let got: Vec<&str> = page.keys.iter().map(|k| k.key.as_str()).collect();
    assert_eq!(got, ["sel:007", "sel:057", "sel:107"]);
    assert_eq!(page.cursor, 108, "凑满即停的真实续点");

    // 续扫到底：剩余 2 个匹配 + 游标穷尽，同样单调用完成。
    let page2 = c.scan_keys(page.cursor, "sel:*", 10).await.expect("resume");
    assert_eq!(page2.keys.len(), 2);
    assert_eq!(page2.cursor, 0, "续点扫到穷尽");

    // 无匹配模式：单调用穷尽整个键空间（250/10=25 轮 < 上限）。
    let (_addr2, fake2) = start_fake();
    let c2 = open_fake(&_addr2);
    let page3 = c2.scan_keys(0, "nomatch:*", 10).await.expect("nomatch");
    assert!(page3.keys.is_empty());
    assert_eq!(
        page3.cursor, 0,
        "无匹配也必须给出穷尽游标，前端不再显示假『无匹配键』"
    );
    assert_eq!(fake2.scans.load(Ordering::SeqCst), 25);
}

#[tokio::test]
async fn scan_keys_stops_at_round_cap() {
    let (addr, fake) = start_fake();
    let c = open_fake(&addr);

    // nomatch + count=2 需 125 轮穷尽 → 必须停在 SCAN_MAX_ROUNDS 并留下非 0 续点。
    let page = c.scan_keys(0, "nomatch:*", 2).await.expect("cap");
    assert_eq!(fake.scans.load(Ordering::SeqCst), SCAN_MAX_ROUNDS as u64);
    assert_eq!(page.cursor, (SCAN_MAX_ROUNDS * 2) as u64);
    assert!(page.keys.is_empty());
}
