//! F6 回归：value_to_rusqlite 对 Array/Object 中的 NaN/Inf
//! 必须序列化为字符串值，而非被 serde_json 静默转为 null。
//!
//! serde_json 默认把 NaN/Inf 序列化为 null——Array/Object 参数中的
//! NaN/Inf 会静默丢失为 null，与 Go 侧保留原始值的行为不对称。

use polydb_core::Value;
use polydb_db_core::DatabaseDriver;
use polydb_db_sqlite::SqliteConn;

#[test]
fn array_with_nan_preserves_nan_as_string() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(j TEXT)", &[])).unwrap();

    // 插入含 NaN 的 Array 参数：序列化后 NaN 应保留为 "NaN" 字符串，
    // 而非被 serde_json 静默转为 null。
    let arr = Value::Array(vec![
        Value::Float(1.5),
        Value::Float(f64::NAN),
        Value::Float(3.5),
    ]);
    let result = polydb_rt::block_on(d.execute("INSERT INTO t(j) VALUES(?)", &[arr]));
    assert!(result.is_ok(), "含 NaN 的 Array 参数应成功执行");

    // 查询存储的 JSON 文本，验证 NaN 被保留为 "NaN" 而非 null
    let rows = polydb_rt::block_on(d.execute("SELECT j FROM t", &[])).unwrap();
    let stored = match &rows.rows[0][0] {
        Value::String(s) => s.clone(),
        other => panic!("expected String, got {:?}", other),
    };
    let parsed: serde_json::Value = serde_json::from_str(&stored).unwrap();
    assert_eq!(parsed[0], 1.5, "第一个元素应保留 1.5");
    assert_eq!(parsed[1], "NaN", "NaN 应保留为字符串 'NaN'，而非 null");
    assert_eq!(parsed[2], 3.5, "第三个元素应保留 3.5");
}

#[test]
fn array_with_infinity_preserves_infinity_as_string() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(j TEXT)", &[])).unwrap();

    let arr = Value::Array(vec![
        Value::Float(f64::INFINITY),
        Value::Float(f64::NEG_INFINITY),
    ]);
    let result = polydb_rt::block_on(d.execute("INSERT INTO t(j) VALUES(?)", &[arr]));
    assert!(result.is_ok(), "含 Inf 的 Array 参数应成功执行");

    let rows = polydb_rt::block_on(d.execute("SELECT j FROM t", &[])).unwrap();
    let stored = match &rows.rows[0][0] {
        Value::String(s) => s.clone(),
        other => panic!("expected String, got {:?}", other),
    };
    let parsed: serde_json::Value = serde_json::from_str(&stored).unwrap();
    assert_eq!(parsed[0], "Infinity", "+Inf 应保留为字符串 'Infinity'");
    assert_eq!(parsed[1], "-Infinity", "-Inf 应保留为字符串 '-Infinity'");
}

#[test]
fn object_with_nan_preserves_nan_as_string() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(j TEXT)", &[])).unwrap();

    let mut map = std::collections::HashMap::new();
    map.insert("value".to_string(), Value::Float(f64::NAN));
    let obj = Value::Object(map);
    let result = polydb_rt::block_on(d.execute("INSERT INTO t(j) VALUES(?)", &[obj]));
    assert!(result.is_ok(), "含 NaN 的 Object 参数应成功执行");

    let rows = polydb_rt::block_on(d.execute("SELECT j FROM t", &[])).unwrap();
    let stored = match &rows.rows[0][0] {
        Value::String(s) => s.clone(),
        other => panic!("expected String, got {:?}", other),
    };
    let parsed: serde_json::Value = serde_json::from_str(&stored).unwrap();
    assert_eq!(parsed["value"], "NaN", "Object 中 NaN 应保留为字符串 'NaN'");
}

#[test]
fn array_without_nan_inf_serializes_normally() {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(d.execute("CREATE TABLE t(j TEXT)", &[])).unwrap();

    let arr = Value::Array(vec![
        Value::Integer(1),
        Value::String("hello".to_string()),
        Value::Bool(true),
    ]);
    let result = polydb_rt::block_on(d.execute("INSERT INTO t(j) VALUES(?)", &[arr]));
    assert!(result.is_ok());

    let rows = polydb_rt::block_on(d.execute("SELECT j FROM t", &[])).unwrap();
    let stored = match &rows.rows[0][0] {
        Value::String(s) => s.clone(),
        other => panic!("expected String, got {:?}", other),
    };
    let parsed: serde_json::Value = serde_json::from_str(&stored).unwrap();
    assert_eq!(parsed[0], 1);
    assert_eq!(parsed[1], "hello");
    assert_eq!(parsed[2], true);
}
