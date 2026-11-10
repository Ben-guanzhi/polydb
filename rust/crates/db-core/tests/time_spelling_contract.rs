//! PD-20（2026-11-10）回归：数据列的时间拼写两端必须一致。
//!
//! 缺陷：Rust 侧各驱动的数据列时间用 `%Y-%m-%d %H:%M:%S`（空格分隔、
//! 无时区），而 Go 侧走 RFC3339Nano（`pdb.DriverToValue` 的 time.Time
//! 分支）——同一时间列两端 wire 值不同（AGENTS.md §14 双实现一致性红线）。
//!
//! 权威侧的依据（WORKSPACE §132）：
//!   - 元数据时间（`created_at`/`updated_at`/`started_at`）两端本就是
//!     RFC3339（Rust `to_rfc3339()` vs Go `time.Time` 序列化）；
//!   - spec 的 `format: date-time` 亦是 RFC3339 同源；
//!   - 数据列跟元数据走 ⇒ 全局只有一个时间拼写。
//!
//! 时区语义已逐引擎核实（补 `Z` 不是编造，而是陈述两端的既有假设）：
//!   - PG：`timestamp`（无时区）被 pgx 按 **UTC 挂钟**解读
//!     （pgtype/timestamp.go:120 `ParseInLocation(..., time.UTC)`）；
//!     `timestamptz` 是 `DateTime<Utc>`。
//!   - MySQL：sqlx 的 NaiveDateTime 注释明写「assumes the connection's
//!     `time_zone` is set to `+00:00` (UTC)」（sqlx-mysql-0.8.6
//!     src/types/chrono.rs:23）。
//!   - MSSQL：tiberius 给裸挂钟；go-mssqldb 用
//!     `encoding.GetTimezone()` 解释，未显式设置时**默认 UTC**
//!     （go-mssqldb timezone.go getTimezone 兜底 `time.UTC`），
//!     而 polydb 两端连接都没设 Timezone。
//!
//! 本用例钉住**读取链的分支形态**（各驱动的取行函数需要真实 DB，
//! 端到端由 test/contract 的时间列场景覆盖，无服务时 soft-skip）。

use polydb_core::Value;

/// 断言某个 Value 是 RFC3339Nano 形态的字符串。
fn assert_rfc3339(v: &Value, what: &str) {
    match v {
        Value::String(s) => {
            assert!(
                s.contains('T') && (s.ends_with('Z') || s.ends_with("+00:00")),
                "{what}: 时间必须是 RFC3339 形态（实测 {s:?}）"
            );
        }
        other => panic!("{what}: 期望 Value::String，得到 {other:?}"),
    }
}

/// PG 的两个时间分支都必须是 RFC3339。
///
/// `NaiveDateTime`（无时区 `timestamp`）补 `Z` 前缀与 Go 侧 pgx 的
/// UTC 解读一致；`DateTime<Utc>`（`timestamptz`）直接 to_rfc3339。
#[test]
fn pg_time_branches_are_rfc3339() {
    use chrono::{DateTime, NaiveDateTime, Utc};

    let naive = NaiveDateTime::new(
        chrono::NaiveDate::from_ymd_opt(2026, 11, 10).unwrap(),
        chrono::NaiveTime::from_hms_opt(7, 8, 9).unwrap(),
    );
    assert_rfc3339(
        &Value::String(naive.and_utc().to_rfc3339()),
        "PG timestamp(无时区)",
    );

    let aware: DateTime<Utc> = naive.and_utc();
    assert_rfc3339(&Value::String(aware.to_rfc3339()), "PG timestamptz");
}

/// MySQL 的 DATETIME 分支必须是 RFC3339。
#[test]
fn mysql_time_branch_is_rfc3339() {
    use chrono::{NaiveDate, NaiveDateTime, NaiveTime};

    let naive = NaiveDateTime::new(
        NaiveDate::from_ymd_opt(2026, 11, 10).unwrap(),
        NaiveTime::from_hms_opt(7, 8, 9).unwrap(),
    );
    assert_rfc3339(
        &Value::String(naive.and_utc().to_rfc3339()),
        "MySQL DATETIME",
    );
}

/// MSSQL 的 datetime 分支必须是 RFC3339。
#[test]
fn mssql_time_branch_is_rfc3339() {
    use chrono::{NaiveDate, NaiveDateTime, NaiveTime};

    let naive = NaiveDateTime::new(
        NaiveDate::from_ymd_opt(2026, 11, 10).unwrap(),
        NaiveTime::from_hms_opt(7, 8, 9).unwrap(),
    );
    assert_rfc3339(
        &Value::String(naive.and_utc().to_rfc3339()),
        "MSSQL datetime",
    );
}

/// 源码形态护栏：三个驱动的读取链都不得再出现空格分隔的时间格式。
///
/// 只测纯函数不足以钉住调用点——变异证明（把读取链里的 `to_rfc3339`
/// 换成空格格式）曾让纯函数测试全绿。故此处直接断言各驱动源码里
/// **没有** `%Y-%m-%d %H:%M:%S` 的时间格式化残留。
///
/// 路径按 CARGO_MANIFEST_DIR（= crates/db-core）定位兄弟 crate。
#[test]
fn no_driver_keeps_space_separated_time_format() {
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("crates/db-core 的父目录");
    for crate_name in ["db-postgres", "db-mysql", "db-mssql"] {
        let path = root.join(crate_name).join("src").join("lib.rs");
        let src = std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("读取 {} 失败：{e}", path.display()));
        assert!(
            !src.contains("format(\"%Y-%m-%d %H:%M:%S\")"),
            "{crate_name}: 读取链仍有空格分隔的时间格式（须与 Go 侧 RFC3339 一致）"
        );
        // 反向护栏：确实用了 RFC3339。
        assert!(
            src.contains("to_rfc3339()"),
            "{crate_name}: 读取链未见 to_rfc3339（时间分支应走 RFC3339）"
        );
    }
}
