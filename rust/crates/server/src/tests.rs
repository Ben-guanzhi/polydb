//! polydb-server 路由层测试（与 Go 侧 pkg/server 测试对拍的行为子集）。
//! 用 tower oneshot 直接在进程内驱动 axum Router，无需真实端口。

use std::sync::Arc;

use axum::http::{HeaderMap, Method, Request, StatusCode};
use axum::Router;
use serde::Deserialize;
use serde_json::json;
use serde_json::Value;
use tower::util::ServiceExt;

use polydb_app_core::AppCore;
use polydb_storage::{FileKeyring, Keyring, Storage};

use crate::router;

// 慢查询：2e8 次递归。debug 构建下 rusqlite（C 未开优化）约需数十秒，
// 保证超时（300ms）与取消窗口确定性先于查询完成。
const SLOW_SQL: &str = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 200000000) SELECT MAX(x) FROM c";

/// 每个用例独立的临时目录（存储 + keyring），互不串扰。
fn new_test_router() -> Router {
    let dir = std::env::temp_dir().join(format!("polydb-srv-test-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("polydb.db").to_str().unwrap()).unwrap();
    let kr: Arc<dyn Keyring> =
        Arc::new(FileKeyring::open(dir.to_str().unwrap(), "test-master").unwrap());
    router(Arc::new(AppCore::with_keyring(storage, kr)))
}

/// 发一个 oneshot 请求；body 为 JSON（服务端解码层支持 JSON）。
/// 响应体按 Content-Type 解成 JSON 值（msgpack 响应经 human-readable 转 JSON，
/// 与 Go 契约测试的归一化同思路，方便对同一套断言）。
async fn do_req(
    router: Router,
    method: Method,
    uri: &str,
    body: Option<Value>,
) -> (StatusCode, Value, HeaderMap) {
    let bytes = body
        .map(|v| serde_json::to_vec(&v).unwrap())
        .unwrap_or_default();
    let req = Request::builder()
        .method(method)
        .uri(uri)
        .header("content-type", "application/json")
        .body(axum::body::Body::from(bytes))
        .unwrap();
    let resp = router.oneshot(req).await.unwrap();
    let status = resp.status();
    let headers = resp.headers().clone();
    let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    let value = if raw.is_empty() {
        Value::Null
    } else if headers
        .get("content-type")
        .is_some_and(|v| v.to_str().map(|s| s.contains("msgpack")).unwrap_or(false))
    {
        let mut de = rmp_serde::Deserializer::new(raw.as_ref()).with_human_readable();
        Value::deserialize(&mut de).unwrap()
    } else {
        serde_json::from_slice(&raw).unwrap()
    };
    (status, value, headers)
}

async fn create_sqlite_conn(router: &Router, name: &str) -> String {
    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        "/api/connections",
        Some(json!({"name": name, "kind": "sqlite", "database": ":memory:"})),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "create {name}: {value}");
    value["id"].as_str().unwrap().to_string()
}

async fn query(router: &Router, id: &str, body: Value) -> (StatusCode, Value, HeaderMap) {
    do_req(
        router.clone(),
        Method::POST,
        &format!("/api/connections/{id}/query"),
        Some(body),
    )
    .await
}

#[tokio::test]
async fn health_returns_ok() {
    let router = new_test_router();
    let (status, value, _) = do_req(router, Method::GET, "/api/health", None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(value["status"], "ok");
}

#[tokio::test]
async fn connection_crud_lifecycle() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "crud").await;

    let (status, value, _) = do_req(
        router.clone(),
        Method::GET,
        &format!("/api/connections/{id}"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(value["name"], "crud");
    // 红线：连接信息不含明文密码。
    assert!(value.get("password").is_none());

    let (status, value, _) = do_req(router.clone(), Method::GET, "/api/connections", None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(value.as_array().unwrap().len(), 1);

    let (status, _, _) = do_req(
        router.clone(),
        Method::PUT,
        &format!("/api/connections/{id}"),
        Some(json!({"name": "crud-renamed"})),
    )
    .await;
    assert_eq!(status, StatusCode::OK);

    let (status, _, _) = do_req(
        router.clone(),
        Method::DELETE,
        &format!("/api/connections/{id}"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT);

    let (status, value, _) = do_req(
        router.clone(),
        Method::GET,
        &format!("/api/connections/{id}"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(value["code"], "POLYDB_ERR_CONNECTION_NOT_FOUND");
}

#[tokio::test]
async fn invalid_connection_id_rejected() {
    let router = new_test_router();
    let (status, value, _) = do_req(router, Method::GET, "/api/connections/not-a-uuid", None).await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(value["code"], "POLYDB_ERR_INVALID_PARAM");
}

#[tokio::test]
async fn query_flow_with_id_echo() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "qf").await;

    let (status, value, _) = query(
        &router,
        &id,
        json!({"sql": "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)"}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["statement_type"], "ddl");

    let (status, value, _) = query(
        &router,
        &id,
        json!({"sql": "INSERT INTO t(name) VALUES('alice'),('bob')"}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["affected_rows"], 2);

    // 客户端提供 query_id → X-Query-ID 头必须回显同一值。
    let (status, value, headers) = query(
        &router,
        &id,
        json!({"sql": "SELECT * FROM t ORDER BY id", "query_id": "11111111-2222-3333-4444-555555555555"}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["rows"].as_array().unwrap().len(), 2);
    let echoed = headers.get("x-query-id").unwrap().to_str().unwrap();
    assert_eq!(echoed, "11111111-2222-3333-4444-555555555555");

    let (status, value, _) =
        query(&router, &id, json!({"sql": "SELECT * FROM missing_table"})).await;
    assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
    assert_eq!(value["code"], "POLYDB_ERR_QUERY_FAILED");
}

#[tokio::test]
async fn query_max_rows_truncation() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "mx").await;

    // 25 行（behavior.md §5 截断对拍与 Go 侧 TestQueryMaxRows 一致）。
    let (status, value, _) =
        query(&router, &id, json!({"sql": "CREATE TABLE seq(v INTEGER)"})).await;
    assert_eq!(status, StatusCode::OK, "{value}");
    let seed = (1..=25)
        .map(|i| i.to_string())
        .collect::<Vec<_>>()
        .join("),(");
    let (status, value, _) = query(
        &router,
        &id,
        json!({"sql": format!("INSERT INTO seq(v) VALUES({seed})")}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["affected_rows"], 25);

    let (status, value, _) = query(
        &router,
        &id,
        json!({"sql": "SELECT v FROM seq ORDER BY v", "max_rows": 10}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["rows"].as_array().unwrap().len(), 10);
    assert_eq!(value["truncated"], true);
    assert_eq!(value["total_rows"], 25);

    let (status, value, _) = query(
        &router,
        &id,
        json!({"sql": "SELECT v FROM seq ORDER BY v", "max_rows": 0}),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["rows"].as_array().unwrap().len(), 25);
    assert_eq!(value["truncated"], false);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn query_timeout_returns_timeout_code() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "to").await;

    // 超时（behavior.md §2.3）：慢查询 + timeout_ms=300 → 408 POLYDB_ERR_TIMEOUT。
    // 被放弃的执行在后台线程上继续（best-effort），不影响断言。
    let (status, value, _) = query(&router, &id, json!({"sql": SLOW_SQL, "timeout_ms": 300})).await;
    assert_eq!(status, StatusCode::REQUEST_TIMEOUT, "{value}");
    assert_eq!(value["code"], "POLYDB_ERR_TIMEOUT");
}

#[tokio::test]
async fn batch_query_stop_on_error() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "bt").await;
    let (status, _, _) = query(&router, &id, json!({"sql": "CREATE TABLE t(v INTEGER)"})).await;
    assert_eq!(status, StatusCode::OK);

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/connections/{id}/query/batch"),
        Some(json!({
            "statements": [
                {"sql": "INSERT INTO t VALUES(1)"},
                {"sql": "SELECT v FROM t"}
            ],
            "stop_on_error": false
        })),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    let results = value["results"].as_array().unwrap();
    assert_eq!(results.len(), 2);

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/connections/{id}/query/batch"),
        Some(json!({
            "statements": [
                {"sql": "SELECT * FROM missing_table"},
                {"sql": "SELECT 1"}
            ],
            "stop_on_error": true
        })),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    let results = value["results"].as_array().unwrap();
    assert_eq!(results.len(), 1, "stop_on_error 必须在首个失败后停止");
    assert_eq!(results[0]["code"], "POLYDB_ERR_QUERY_FAILED");
}

#[tokio::test]
async fn transaction_lifecycle_single_finalize() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "tx").await;
    let (status, _, _) = query(&router, &id, json!({"sql": "CREATE TABLE t(v INTEGER)"})).await;
    assert_eq!(status, StatusCode::OK);

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/connections/{id}/transactions"),
        Some(json!({"connection_id": id})),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "{value}");
    let txn = value["id"].as_str().unwrap().to_string();

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/transactions/{txn}/execute"),
        Some(json!({"sql": "INSERT INTO t VALUES(1)"})),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/transactions/{txn}/commit"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["status"], "committed");

    // 只成功 finalize 一次（spec §10.1）：二次 commit → 404。
    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/transactions/{txn}/commit"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(value["code"], "POLYDB_ERR_TRANSACTION_NOT_FOUND");

    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        "/api/transactions/txn-nope/rollback",
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(value["code"], "POLYDB_ERR_TRANSACTION_NOT_FOUND");
}

#[tokio::test]
async fn cancel_unknown_query_returns_404() {
    let router = new_test_router();
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/queries/99999999-1111-2222-3333-444444444444/cancel",
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(value["code"], "POLYDB_ERR_QUERY_NOT_FOUND");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cancel_in_flight_query() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "cf").await;

    // 发起慢查询（异步等待），随后显式取消 → 408 POLYDB_ERR_CANCELLED（§2.1）。
    // oneshot future 必须被并发地轮询才会真正驱动 handler，因此用 spawn 并发跑。
    let qid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
    let req = Request::builder()
        .method(Method::POST)
        .uri(format!("/api/connections/{id}/query"))
        .header("content-type", "application/json")
        .body(axum::body::Body::from(
            serde_json::to_vec(&json!({"sql": SLOW_SQL, "query_id": qid})).unwrap(),
        ))
        .unwrap();
    let worker_router = router.clone();
    let pending = tokio::spawn(async move {
        let resp = worker_router.oneshot(req).await.unwrap();
        let status = resp.status();
        let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        (status, raw)
    });
    // 等 1s 保证取消一定命中在飞查询（SLOW_SQL 在 debug 下跑数十秒）。
    tokio::time::sleep(std::time::Duration::from_secs(1)).await;

    let (status, _, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/queries/{qid}/cancel"),
        None,
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT, "cancel 必须命中在飞查询");

    let (status, raw) = pending.await.unwrap();
    let value: Value = serde_json::from_slice(&raw).unwrap();
    assert_eq!(status, StatusCode::REQUEST_TIMEOUT, "{value}");
    assert_eq!(value["code"], "POLYDB_ERR_CANCELLED");
}

#[tokio::test]
async fn kv_not_supported_on_sqlite() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "kv").await;
    let (status, value, _) = do_req(
        router.clone(),
        Method::POST,
        &format!("/api/connections/{id}/kv/scan"),
        Some(json!({"cursor": 0, "count": 10})),
    )
    .await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED);
    assert_eq!(value["code"], "POLYDB_ERR_NOT_SUPPORTED");
}

#[tokio::test]
async fn garbage_msgpack_body_returns_unknown() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "gb").await;
    let req = Request::builder()
        .method(Method::POST)
        .uri(format!("/api/connections/{id}/query"))
        .header("content-type", "application/msgpack")
        .body(axum::body::Body::from(vec![0xffu8, 0x00]))
        .unwrap();
    let resp = router.oneshot(req).await.unwrap();
    let status = resp.status();
    let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    let value: Value = serde_json::from_slice(&raw).unwrap();
    assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
    assert_eq!(value["code"], "POLYDB_ERR_UNKNOWN");
}
