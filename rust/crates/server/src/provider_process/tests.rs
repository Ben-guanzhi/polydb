//! 阶段 8 进程端点的**纯逻辑**单测。
//!
//! 真实进程的拉起与转发由 `test/contract/provider_process_test.go` 覆盖（双端
//! parity，用 cmd/polydb-provider 作为参考进程）。这里只锁不需要外部进程
//! 的部分：命令解析与响应帧解码（§16.13.3）。

use super::*;

#[test]
fn split_command_respects_quotes() {
    // 引号内的空白不算分隔符（Windows 路径常含空格）。
    let parts = split_command(r#""C:\Program Files\pv\provider.exe" -key a"#);
    assert_eq!(parts.len(), 3);
    assert_eq!(parts[0], r"C:\Program Files\pv\provider.exe");
    assert_eq!(parts[1], "-key");
    assert_eq!(parts[2], "a");
}

#[test]
fn split_command_plain() {
    let parts = split_command("/usr/bin/provider -key a -rows 2");
    assert_eq!(parts, vec!["/usr/bin/provider", "-key", "a", "-rows", "2"]);
}

#[test]
fn decode_exec_result_maps_result() {
    // §16.13.3：{"result": {...}} → QueryResult。
    let frame = serde_json::json!({
        "jsonrpc": "2.0",
        "id": 1,
        "result": {"result": {
            "columns": [{"name": "v", "type": "INTEGER"}],
            "rows": [[1]],
            "affected_rows": 0,
            "execution_time_ms": 3,
        }}
    });
    let out = decode_exec_result(&frame).expect("must decode");
    assert_eq!(out.rows.len(), 1);
    assert_eq!(out.columns.len(), 1);
    assert_eq!(out.columns[0].name, "v");
}

#[test]
fn decode_exec_result_maps_error() {
    // {"result": {"error": {...}}} → PolyDBError。
    let frame = serde_json::json!({
        "jsonrpc": "2.0",
        "id": 1,
        "result": {"error": {"code": "POLYDB_ERR_QUERY_FAILED", "message": "boom"}}
    });
    let err = decode_exec_result(&frame).expect_err("must be an error");
    assert_eq!(err.code, "POLYDB_ERR_QUERY_FAILED");
    assert_eq!(err.message, "boom");
}

#[test]
fn decode_exec_result_maps_rpc_error() {
    // JSON-RPC 层的 error → PolyDBError（code 归一为 QUERY_FAILED）。
    let frame = serde_json::json!({
        "jsonrpc": "2.0",
        "id": 1,
        "error": {"code": -32000, "message": "internal"}
    });
    let err = decode_exec_result(&frame).expect_err("must be an error");
    assert_eq!(err.code, polydb_core::protocol::error::codes::QUERY_FAILED);
    assert_eq!(err.message, "internal");
}

#[test]
fn decode_exec_result_rejects_empty() {
    let frame = serde_json::json!({"jsonrpc": "2.0", "id": 1, "result": {}});
    let err = decode_exec_result(&frame).expect_err("must be an error");
    assert_eq!(err.code, polydb_core::protocol::error::codes::INVALID_PARAM);
}

#[test]
fn parse_capability_rejects_unknown() {
    assert_eq!(parse_capability("query"), Some(DriverCapability::Query));
    assert_eq!(parse_capability("kv"), Some(DriverCapability::Kv));
    assert_eq!(parse_capability("nope"), None);
}
