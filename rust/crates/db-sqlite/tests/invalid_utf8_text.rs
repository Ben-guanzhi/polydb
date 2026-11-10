//! F5 回归：row_to_values 对 TEXT 列的非法 UTF-8 必须显式报错，
//! 而非用 from_utf8_lossy 静默替换为 U+FFFD。
//!
//! SQLite TEXT 列不强制 UTF-8。此前 Rust 侧用 from_utf8_lossy 把
//! 非法字节替换为 U+FFFD——与 Go 侧保留原始字节的行为不对称，
//! 且用户无法察觉数据已被篡改。

use polydb_db_core::DatabaseDriver;
use polydb_db_sqlite::SqliteConn;

#[test]
fn invalid_utf8_in_text_column_errors_not_silently_replaces() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(s TEXT)", &[])).unwrap();

    // 插入非法 UTF-8 到 TEXT 列：CAST 把 BLOB 重新解释为 TEXT，
    // SQLite 不验证 UTF-8，原始字节原样存储。
    polydb_rt::block_on(d.execute("INSERT INTO t(s) VALUES(CAST(x'00ff' AS TEXT))", &[])).unwrap();

    // 查询该行：row_to_values 遇到非法 UTF-8 应返回错误
    let result = polydb_rt::block_on(d.execute("SELECT s FROM t", &[]));
    assert!(
        result.is_err(),
        "TEXT 列含非法 UTF-8 时应报错，而非静默替换为 U+FFFD"
    );
}

#[test]
fn valid_utf8_in_text_column_still_works() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(s TEXT)", &[])).unwrap();
    polydb_rt::block_on(d.execute("INSERT INTO t(s) VALUES('hello 世界')", &[])).unwrap();

    let result = polydb_rt::block_on(d.execute("SELECT s FROM t", &[]));
    let r = result.expect("合法 UTF-8 应正常查询");
    assert_eq!(r.rows.len(), 1);
    assert_eq!(
        r.rows[0][0],
        polydb_core::Value::String("hello 世界".to_string())
    );
}
