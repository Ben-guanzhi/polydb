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
async fn diff_schema_identical_source_and_target_is_empty_plan() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "same").await;
    let (status, _, _) = query(
        &router,
        &id,
        json!({"sql": "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(20))"}),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/diff",
        Some(json!({"source": {"connection_id": id}, "target": {"connection_id": id}})),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(value["entries"], json!([]), "同库自比必须空计划");
}

#[tokio::test]
async fn diff_schema_reports_missing_table_and_type_change() {
    let router = new_test_router();
    let source = create_sqlite_conn(&router, "src").await;
    let target = create_sqlite_conn(&router, "dst").await;
    query(
        &router,
        &source,
        json!({"sql": "CREATE TABLE t(id INTEGER, name VARCHAR(20))"}),
    )
    .await;
    query(
        &router,
        &target,
        json!({"sql": "CREATE TABLE t(id INTEGER, name VARCHAR(30))"}),
    )
    .await;
    query(
        &router,
        &target,
        json!({"sql": "CREATE TABLE u(id INTEGER)"}),
    )
    .await;
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/diff",
        Some(json!({
            "source": {"connection_id": source},
            "target": {"connection_id": target}
        })),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    // 相位序：create_table(50) 必在 alter_column(70) 之前。
    assert_eq!(
        value["entries"],
        json!([
            {
                "id": "u//create_table",
                "op": "create_table",
                "phase": 50,
                "table": "u",
                "destructive": false,
                "selected": true,
                "reason": "表中缺失，需新建"
            },
            {
                "id": "t/name/alter_column",
                "op": "alter_column",
                "phase": 70,
                "table": "t",
                "object": "name",
                "from": "varchar(20)",
                "to": "varchar(30)",
                "flags": ["type"],
                "destructive": false,
                "selected": true,
                "reason": "类型 varchar(20) → varchar(30)"
            }
        ])
    );
}

#[tokio::test]
async fn diff_schema_unknown_connection_reports_not_found() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "known").await;
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/diff",
        Some(json!({
            "source": {"connection_id": id},
            "target": {"connection_id": "00000000-0000-0000-0000-000000000000"}
        })),
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(value["code"], "POLYDB_ERR_CONNECTION_NOT_FOUND");
}

#[tokio::test]
async fn diff_schema_accepts_msgpack_body() {
    // 数据面必须走 msgpack（AGENTS.md 红线）：连接 id 在此路径下是 human-readable 字符串。
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "mp").await;
    let body = rmp_serde::to_vec(&json!({
        "source": {"connection_id": id},
        "target": {"connection_id": id},
        "options": {"ignore_defaults": true, "select_destructive": true}
    }))
    .unwrap();
    let req = Request::builder()
        .method(Method::POST)
        .uri("/api/schema/diff")
        .header("content-type", "application/msgpack")
        .body(axum::body::Body::from(body))
        .unwrap();
    let resp = router.oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    let mut de = rmp_serde::Deserializer::new(raw.as_ref()).with_human_readable();
    let value = Value::deserialize(&mut de).unwrap();
    assert_eq!(value["entries"], json!([]));
}

#[tokio::test]
async fn ddl_empty_selection_produces_empty_arrays() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "ddl_empty").await;
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/ddl",
        Some(json!({
            "source": {"connection_id": id},
            "target": {"connection_id": id},
            "selected": []
        })),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    // 空勾选必须序列化成 `[]` 而非 null（behavior.md §15.1）。
    assert_eq!(value["statements"], json!([]));
    assert_eq!(value["rollback"], json!([]));
    assert_eq!(value["pragmas_outside_tx"], json!(false));
    assert_eq!(value["ddl_in_tx"], json!(true));
}

#[tokio::test]
async fn ddl_generates_direct_statements_for_selected_entries() {
    let router = new_test_router();
    let source = create_sqlite_conn(&router, "ddl_src").await;
    let target = create_sqlite_conn(&router, "ddl_dst").await;
    query(
        &router,
        &source,
        json!({"sql": "CREATE TABLE t(id INTEGER)"}),
    )
    .await;
    query(
        &router,
        &target,
        json!({"sql": "CREATE TABLE t(id INTEGER, note TEXT)"}),
    )
    .await;
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/ddl",
        Some(json!({
            "source": {"connection_id": source},
            "target": {"connection_id": target},
            "selected": ["t/note/add_column"]
        })),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{value}");
    assert_eq!(
        value["statements"],
        json!([
            {
                "entry_ids": ["t/note/add_column"],
                "sql": "ALTER TABLE \"t\" ADD COLUMN \"note\" text;",
                "phase": 60,
                "table": "t",
                "non_reversible": false
            }
        ])
    );
    // 回滚方向按镜像 id 重算：add_column ↔ drop_column，且删列自身不可逆。
    assert_eq!(
        value["rollback"],
        json!([
            {
                "entry_ids": ["t/note/drop_column"],
                "sql": "ALTER TABLE \"t\" DROP COLUMN \"note\";",
                "phase": 30,
                "table": "t",
                "warnings": ["该列若被 CHECK 约束引用，删除后约束会一并消失（快照不可见）"],
                "non_reversible": true
            }
        ])
    );
}

#[tokio::test]
async fn ddl_stale_selected_id_reports_invalid_param() {
    let router = new_test_router();
    let id = create_sqlite_conn(&router, "ddl_stale").await;
    let (status, value, _) = do_req(
        router,
        Method::POST,
        "/api/schema/ddl",
        Some(json!({
            "source": {"connection_id": id},
            "target": {"connection_id": id},
            "selected": ["t/ghost/drop_index"]
        })),
    )
    .await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{value}");
    assert_eq!(value["code"], "POLYDB_ERR_INVALID_PARAM");
    assert_eq!(
        value["message"],
        "勾选的条目不在当前比对计划里：t/ghost/drop_index（请重新比对后再执行）"
    );
}

#[tokio::test]
async fn ddl_accepts_msgpack_body() {
    let router = new_test_router();
    let source = create_sqlite_conn(&router, "ddl_mp_src").await;
    let target = create_sqlite_conn(&router, "ddl_mp_dst").await;
    query(
        &router,
        &source,
        json!({"sql": "CREATE TABLE t(id INTEGER)"}),
    )
    .await;
    let body = rmp_serde::to_vec(&json!({
        "source": {"connection_id": source},
        "target": {"connection_id": target},
        "selected": ["t//drop_table"]
    }))
    .unwrap();
    let resp = router
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri("/api/schema/ddl")
                .header("content-type", "application/msgpack")
                .body(axum::body::Body::from(body))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    let mut de = rmp_serde::Deserializer::new(raw.as_ref()).with_human_readable();
    let value = Value::deserialize(&mut de).unwrap();
    assert_eq!(value["statements"][0]["sql"], json!("DROP TABLE \"t\";"));
    assert_eq!(value["statements"][0]["non_reversible"], json!(true));
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

// ─── WebSocket 鉴权门（behavior.md §12.2，与 Go 侧 ws_auth 用例对拍）────────

use futures_util::{SinkExt, StreamExt};
use polydb_core::protocol::ws::{ClientMessage, ServerMessage, WsAuth};
use polydb_core::protocol::QueryRequest;
use serde::Serialize;
use tokio_tungstenite::tungstenite::Message as WsMessage;

use crate::router_with_token;

fn new_test_router_with_token(token: &str) -> Router {
    let dir = std::env::temp_dir().join(format!("polydb-srv-test-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("polydb.db").to_str().unwrap()).unwrap();
    let kr: Arc<dyn Keyring> =
        Arc::new(FileKeyring::open(dir.to_str().unwrap(), "test-master").unwrap());
    router_with_token(
        Arc::new(AppCore::with_keyring(storage, kr)),
        Some(token.to_string()),
    )
}

/// 启用 token 的服务上创建 SQLite 连接（HTTP 端点需 Bearer 头）。
async fn authed_create_sqlite_conn(router: &Router, name: &str, token: &str) -> String {
    let bytes = serde_json::to_vec(&json!({
        "name": name,
        "kind": "sqlite",
        "database": ":memory:"
    }))
    .unwrap();
    let req = Request::builder()
        .method(Method::POST)
        .uri("/api/connections")
        .header("content-type", "application/json")
        .header("authorization", format!("Bearer {token}"))
        .body(axum::body::Body::from(bytes))
        .unwrap();
    let resp = router.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED);
    let raw = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    // 响应为 msgpack（服务端默认），与 do_req 的解码方式一致。
    let mut de = rmp_serde::Deserializer::new(raw.as_ref()).with_human_readable();
    let value = Value::deserialize(&mut de).unwrap();
    value["id"].as_str().unwrap().to_string()
}

async fn spawn_ws_server(router: Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    format!("ws://{addr}/ws")
}

fn ws_encode(msg: &ClientMessage) -> Vec<u8> {
    let mut buf = Vec::new();
    msg.serialize(
        &mut rmp_serde::Serializer::new(&mut buf)
            .with_struct_map()
            .with_human_readable(),
    )
    .unwrap();
    buf
}

async fn ws_recv(ws: &mut tokio_tungstenite::WebSocketStream<tokio_tungstenite::MaybeTlsStream<tokio::net::TcpStream>>) -> ServerMessage {
    let msg = tokio::time::timeout(std::time::Duration::from_secs(5), ws.next())
        .await
        .expect("ws recv timeout")
        .expect("ws stream ended")
        .expect("ws error");
    match msg {
        WsMessage::Binary(b) => {
            let mut de = rmp_serde::Deserializer::new(b.as_ref()).with_human_readable();
            ServerMessage::deserialize(&mut de).unwrap()
        }
        other => panic!("unexpected ws message: {other:?}"),
    }
}

fn ws_query(conn_id: uuid::Uuid) -> ClientMessage {
    ClientMessage::Query {
        query_id: uuid::Uuid::new_v4(),
        request: QueryRequest {
            sql: "SELECT 1".into(),
            params: vec![],
            connection_id: Some(conn_id),
            query_id: None,
            schema: None,
            pagination: None,
            timeout_ms: None,
            max_rows: None,
        },
    }
}

#[tokio::test]
async fn ws_query_without_auth_hello_rejected() {
    let router = new_test_router_with_token("secret-token");
    let id = authed_create_sqlite_conn(&router, "ws-auth-gate", "secret-token").await;
    let url = spawn_ws_server(router).await;

    let (mut ws, _) = tokio_tungstenite::connect_async(&url).await.unwrap();

    // 跳过 hello 直接带 connection_id 发 query：必须收到 UNAUTHORIZED 并被断开。
    ws.send(WsMessage::Binary(ws_encode(&ws_query(id.parse().unwrap())).into()))
        .await
        .unwrap();
    match ws_recv(&mut ws).await {
        ServerMessage::QueryError { error, .. } => {
            assert_eq!(error.code, "POLYDB_ERR_UNAUTHORIZED", "got: {error:?}");
        }
        other => panic!("expected query_error, got {other:?}"),
    }
    // 服务端拒绝后关闭连接：下一次读取应以 Close/EOF 收场。
    let next = tokio::time::timeout(std::time::Duration::from_secs(5), ws.next()).await;
    match next {
        Err(_) => panic!("ws should close after unauthorized query"),
        Ok(None) | Ok(Some(Err(_))) | Ok(Some(Ok(WsMessage::Close(_)))) => {}
        Ok(Some(Ok(other))) => panic!("expected close, got {other:?}"),
    }
}

#[tokio::test]
async fn ws_auth_hello_then_query_succeeds() {
    let router = new_test_router_with_token("secret-token");
    let id = authed_create_sqlite_conn(&router, "ws-auth-ok", "secret-token").await;
    let url = spawn_ws_server(router).await;

    let (mut ws, _) = tokio_tungstenite::connect_async(&url).await.unwrap();

    let hello = ClientMessage::Hello {
        connection_id: id.parse().unwrap(),
        client_version: Some("0.1.0".into()),
        auth: Some(WsAuth {
            token: Some("secret-token".into()),
        }),
    };
    ws.send(WsMessage::Binary(ws_encode(&hello).into())).await.unwrap();
    assert!(matches!(ws_recv(&mut ws).await, ServerMessage::HelloAck { .. }));

    ws.send(WsMessage::Binary(ws_encode(&ws_query(id.parse().unwrap())).into()))
        .await
        .unwrap();
    assert!(matches!(
        ws_recv(&mut ws).await,
        ServerMessage::QueryStarted { .. }
    ));
    assert!(matches!(
        ws_recv(&mut ws).await,
        ServerMessage::QueryResult { .. }
    ));
}

#[tokio::test]
async fn ws_cross_origin_handshake_rejected() {
    let router = new_test_router_with_token("secret-token");
    let url = spawn_ws_server(router).await;

    let req = tokio_tungstenite::tungstenite::http::Request::builder()
        .uri(&url)
        .header("Origin", "https://evil.example.com")
        .body(())
        .unwrap();
    assert!(
        tokio_tungstenite::connect_async(req).await.is_err(),
        "cross-origin handshake must be rejected"
    );
}
