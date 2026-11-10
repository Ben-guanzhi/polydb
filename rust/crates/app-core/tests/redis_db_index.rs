//! PD-16（2026-11-09）回归测试：Redis db 编号的解析纪律。
//!
//! 缺陷：Go 与 Rust 两侧对 `connection.database`（Redis db 号）的解析规则不同，
//! 且**都会在某些输入上静默回落 0**——用户连到 db 0 而不是自己填的库，
//! 数据写进错误的库且无任何提示。
//!
//!   Go   (旧)：逐字符累加，任何非数字字符 ⇒ 0；整数不溢出（int 是 64 位）。
//!   Rust (旧)：`d.trim().parse::<u32>().ok().unwrap_or(0)`——trim 后解析，
//!               解析失败（含超出 u32 上界）⇒ 0。
//!
//! 于是 `" 3"` 两端分别得到 0 与 3；`"4294967296"` 两端分别得到一个大 db 号
//! 与 0。AGENTS.md §14 红线「双实现行为不一致」被违反。
//!
//! 修复后的规则（两端逐字一致）：
//!   空串 / 未填      → 0
//!   纯十进制数字    → 该值（0..=4294967295）
//!   其余            → 报 POLYDB_ERR_INVALID_PARAM
//!                  （含空白、正负号、小数点、非 ASCII 数字、超出 u32 上界）
//!
//! Go 侧对应用例见 go/pkg/dbredis/registry_dbindex_test.go。

use polydb_app_core::AppCore;
use polydb_core::{CreateConnectionRequest, DatabaseKind};

/// 造一个 redis 连接请求。database 传 None 表示未填。
fn req(database: Option<&str>) -> CreateConnectionRequest {
    CreateConnectionRequest {
        name: "redis-db".to_string(),
        kind: DatabaseKind::Redis,
        host: Some("127.0.0.1".to_string()),
        // 不可达端口：若真去拨号，错误会是连接错误而非参数错误。
        port: Some(1),
        database: database.map(str::to_string),
        username: None,
        password_ref: None,
        password: None,
        options: Default::default(),
        ssh_tunnel: None,
        default_schema: None,
        read_only: None,
        group: None,
        color: None,
    }
}

/// setup 与 app_core_integration.rs 同款：临时目录 + 文件 keyring。
fn setup() -> AppCore {
    let dir = std::env::temp_dir().join(format!("polydb-test-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = polydb_storage::Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = polydb_storage::FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    AppCore::with_keyring(storage, std::sync::Arc::new(kr))
}

/// 非法输入必须报错，绝不静默回落 0。
#[test]
fn redis_db_number_rejects_invalid_input() {
    for bad in [
        " 3",
        "3 ",
        " 3 ",
        "3a",
        "a3",
        "+3",
        "-1",
        "3.0",
        // 超出 u32 上界（Go 侧 int 不溢出，会得到大 db 号；Rust 侧 u32 溢出）
        "4294967296",
        "99999999999999999999",
        // 全角数字
        "０",
    ] {
        let app = setup();
        let info = app.create_connection(&req(Some(bad))).unwrap();

        let err = app.connect(info.id).unwrap_err();
        assert_eq!(
            err.to_protocol().code,
            "POLYDB_ERR_INVALID_PARAM",
            "redis db number {bad:?} must be rejected as INVALID_PARAM (got {:?})",
            err.to_protocol().code
        );
    }
}

/// 合法输入不应被参数校验拦下（后续是否连上是网络问题，与本用例无关）。
#[test]
fn redis_db_number_accepts_valid_input() {
    for good in ["0", "1", "15", "16", "03", "4294967295"] {
        let app = setup();
        let info = app.create_connection(&req(Some(good))).unwrap();

        let err = app.connect(info.id).unwrap_err();
        assert_ne!(
            err.to_protocol().code,
            "POLYDB_ERR_INVALID_PARAM",
            "redis db number {good:?} must pass parameter validation"
        );
    }
}

/// 未填 database → db 0（合法默认），不得报错为参数错误。
#[test]
fn redis_db_number_defaults_to_zero() {
    let app = setup();
    let info = app.create_connection(&req(None)).unwrap();

    let err = app.connect(info.id).unwrap_err();
    assert_ne!(
        err.to_protocol().code,
        "POLYDB_ERR_INVALID_PARAM",
        "absent redis db number must default to 0, not be rejected"
    );
}
