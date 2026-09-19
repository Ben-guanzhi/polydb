//! polydb-server：基于 Axum 的 HTTP 控制面，行为与 Go 实现（go/pkg/server）保持一致。

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::extract::{Path, State};
use axum::http::header::CONTENT_TYPE;
use axum::http::{HeaderMap, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{body::Bytes, Router};
use polydb_app_core::AppCore;
use polydb_core::protocol::error::codes;
use polydb_core::protocol::error::PolyDBError;
use polydb_core::protocol::{
    BatchQueryRequest, BatchQueryResult, BatchResultItem, BeginTransactionRequest, ConnectionId,
    ConnectionStatus, CreateConnectionRequest, QueryRequest, QueryResult, UpdateConnectionRequest,
};
use polydb_core::{
    CoreError, CoreResult, RedisExecCommandRequest, RedisScanRequest, RedisSelectDbRequest,
    RedisSetRequest,
};
use serde::de::DeserializeOwned;
use serde::Serialize;
use tokio::sync::Notify;
use uuid::Uuid;

const CONTENT_MSGPACK: &str = "application/msgpack";
const CONTENT_JSON: &str = "application/json";
const HEADER_X_QUERY_ID: &str = "X-Query-ID";

/// Application state passed via Axum's `State` extractor.
///
/// Wraps the AppCore plus a registry of in-flight HTTP queries that can be
/// cancelled via `POST /api/queries/{query_id}/cancel`. The registry maps a
/// query uuid to a `Notify` that the query handler awaits on; the cancel
/// handler fetches and notifies. `Deref` keeps existing `app.foo()` call sites
/// working with no changes.
#[derive(Clone)]
struct AppState {
    app: Arc<AppCore>,
    registry: Arc<Mutex<HashMap<Uuid, Arc<Notify>>>>,
    // Some(token) 时启用 Bearer 鉴权（behavior.md §12.1）。
    token: Option<String>,
}

impl std::ops::Deref for AppState {
    type Target = AppCore;
    fn deref(&self) -> &AppCore {
        self.app.as_ref()
    }
}

mod ws;

#[cfg(test)]
mod tests;

pub fn router(app: Arc<AppCore>) -> Router {
    router_with_token(app, None)
}

/// router_with_token 以可选 Bearer token 构建路由（POLYDB_SERVER_TOKEN）。
pub fn router_with_token(app: Arc<AppCore>, token: Option<String>) -> Router {
    let state = AppState {
        app,
        registry: Arc::new(Mutex::new(HashMap::new())),
        token,
    };
    let typed = Router::<AppState>::new()
        .route("/ws", get(ws::ws_upgrade))
        .route("/api/health", get(health))
        .route(
            "/api/connections",
            get(list_connections).post(create_connection),
        )
        .route(
            "/api/connections/{id}",
            get(get_connection)
                .put(update_connection)
                .delete(delete_connection),
        )
        .route("/api/connections/{id}/test", post(test_connection))
        .route("/api/connections/{id}/schemas", get(list_schemas))
        .route(
            "/api/connections/{id}/schemas/{schema}/tables",
            get(list_tables),
        )
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/columns",
            get(list_columns),
        )
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/indexes",
            get(list_indexes),
        )
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/foreign-keys",
            get(list_foreign_keys),
        )
        .route("/api/connections/{id}/query", post(execute_query))
        .route(
            "/api/connections/{id}/query/batch",
            post(execute_batch_query),
        )
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/ddl",
            get(get_ddl),
        )
        .route(
            "/api/connections/{id}/transactions",
            post(begin_transaction),
        )
        .route("/api/transactions/{txn_id}/execute", post(execute_in_tx))
        .route(
            "/api/transactions/{txn_id}/commit",
            post(commit_transaction),
        )
        .route(
            "/api/transactions/{txn_id}/rollback",
            post(rollback_transaction),
        )
        .route("/api/connections/{id}/kv/select", post(kv_select_db))
        .route("/api/connections/{id}/kv/scan", post(kv_scan_keys))
        .route(
            "/api/connections/{id}/kv/keys/{key}",
            get(kv_get_value).put(kv_set_value),
        )
        .route("/api/connections/{id}/kv/exec", post(kv_exec_command))
        .route("/api/queries/{query_id}/cancel", post(cancel_query))
        .layer(axum::middleware::from_fn_with_state(
            state.clone(),
            auth_middleware,
        ));
    typed.with_state(state)
}

/// Bearer 鉴权中间件（behavior.md §12.1）。豁免 /api/health 与 /ws
/// （WS 在 hello 阶段校验）；state.token 为 None 时直通。
async fn auth_middleware(
    State(state): State<AppState>,
    req: axum::extract::Request,
    next: axum::middleware::Next,
) -> Response {
    let path = req.uri().path();
    let Some(token) = &state.token else {
        return next.run(req).await;
    };
    if path == "/api/health" || path == "/ws" {
        return next.run(req).await;
    }
    let ok = req
        .headers()
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Bearer "))
        .is_some_and(|got| got == token);
    if ok {
        next.run(req).await
    } else {
        json_response(
            StatusCode::UNAUTHORIZED,
            PolyDBError::new(codes::UNAUTHORIZED, "missing or invalid bearer token"),
        )
    }
}

// ─── 响应辅助 ───────────────────────────────────────────────

fn json_response(status: StatusCode, v: impl Serialize) -> Response {
    let body = serde_json::to_vec(&v).unwrap_or_else(|_| b"{}".to_vec());
    (status, [(CONTENT_TYPE, CONTENT_JSON)], body).into_response()
}

fn msgpack_response(status: StatusCode, v: impl Serialize) -> Response {
    // with_struct_map 与 Go 一致（结构体编码为字段名 map）；
    // with_human_readable 令 uuid 等类型输出字符串（16 字节 raw 会与 Go 不一致）。
    let mut buf = Vec::new();
    let r = v.serialize(
        &mut rmp_serde::Serializer::new(&mut buf)
            .with_struct_map()
            .with_human_readable(),
    );
    if r.is_err() {
        buf.clear();
    }
    (status, [(CONTENT_TYPE, CONTENT_MSGPACK)], buf).into_response()
}

fn error_response(e: CoreError) -> Response {
    let pe = e.to_protocol();
    let status = match pe.code.as_str() {
        codes::CONNECTION_NOT_FOUND => StatusCode::NOT_FOUND,
        codes::CONNECTION_FAILED => StatusCode::BAD_GATEWAY,
        codes::NOT_SUPPORTED | codes::DRIVER_NOT_AVAILABLE => StatusCode::NOT_IMPLEMENTED,
        codes::INVALID_PARAM => StatusCode::BAD_REQUEST,
        "POLYDB_ERR_TRANSACTION_NOT_FOUND" => StatusCode::NOT_FOUND,
        codes::QUERY_NOT_FOUND => StatusCode::NOT_FOUND,
        // 408：取消与超时都属"请求未在预期时间内完成"（与 Go 侧 errToStatus 一致）。
        codes::CANCELLED | codes::TIMEOUT => StatusCode::REQUEST_TIMEOUT,
        codes::UNAUTHORIZED => StatusCode::UNAUTHORIZED,
        codes::READ_ONLY => StatusCode::CONFLICT,
        _ => StatusCode::INTERNAL_SERVER_ERROR,
    };
    json_response(status, pe)
}

fn unknown_response(msg: impl Into<String>) -> Response {
    json_response(
        StatusCode::INTERNAL_SERVER_ERROR,
        PolyDBError::new(codes::UNKNOWN, msg),
    )
}

// parse_conn_id：路径参数是字符串，转换为 Uuid（ConnectionId）。
fn parse_conn_id(id: String) -> Result<ConnectionId, Box<Response>> {
    id.parse::<ConnectionId>().map_err(|_| {
        Box::new(json_response(
            StatusCode::BAD_REQUEST,
            PolyDBError::new(codes::INVALID_PARAM, format!("invalid connection id: {id}")),
        ))
    })
}

fn decode_body<T: DeserializeOwned>(headers: &HeaderMap, body: &Bytes) -> Result<T, Box<Response>> {
    let ct = headers
        .get(CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    let res: Result<T, String> = if ct.contains("json") {
        serde_json::from_slice(body).map_err(|e| e.to_string())
    } else {
        let mut de = rmp_serde::Deserializer::new(body.as_ref()).with_human_readable();
        T::deserialize(&mut de).map_err(|e| e.to_string())
    };
    // 与 Go 实现一致：解码失败按未知错误处理。
    res.map_err(|e| Box::new(unknown_response(format!("decode body: {e}"))))
}

// ─── 系统 ───────────────────────────────────────────────────

async fn health() -> Response {
    json_response(
        StatusCode::OK,
        serde_json::json!({"status": "ok", "version": "0.1.0"}),
    )
}

// ─── connections ────────────────────────────────────────────

async fn list_connections(State(app): State<AppState>) -> Response {
    match app.list_connections() {
        Ok(conns) => msgpack_response(StatusCode::OK, conns),
        Err(e) => error_response(e),
    }
}

async fn create_connection(
    State(app): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let req: CreateConnectionRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.create_connection(&req) {
        Ok(info) => msgpack_response(StatusCode::CREATED, info),
        Err(e) => error_response(e),
    }
}

async fn get_connection(State(app): State<AppState>, Path(id): Path<String>) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.get_connection_info(id) {
        Ok(Some(info)) => msgpack_response(StatusCode::OK, info),
        Ok(None) => error_response(CoreError::ConnectionNotFound(id.to_string())),
        Err(e) => error_response(e),
    }
}

async fn update_connection(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: UpdateConnectionRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.update_connection(id, &req) {
        Ok(Some(info)) => msgpack_response(StatusCode::OK, info),
        Ok(None) => error_response(CoreError::ConnectionNotFound(id.to_string())),
        Err(e) => error_response(e),
    }
}

async fn delete_connection(State(app): State<AppState>, Path(id): Path<String>) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.delete_connection(id) {
        Ok(true) => StatusCode::NO_CONTENT.into_response(),
        Ok(false) => error_response(CoreError::ConnectionNotFound(id.to_string())),
        Err(e) => error_response(e),
    }
}

async fn test_connection(State(app): State<AppState>, Path(id): Path<String>) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let missing = || ConnectionStatus {
        id,
        connected: false,
        server_version: None,
        latency_ms: None,
        error: Some(format!("connection not found: {id}")),
    };
    match app.get_connection_info(id) {
        Err(_) => return msgpack_response(StatusCode::OK, missing()),
        Ok(None) => return msgpack_response(StatusCode::OK, missing()),
        Ok(Some(_)) => {}
    }
    let status = ConnectionStatus {
        id,
        connected: false,
        server_version: None,
        latency_ms: None,
        error: None,
    };
    let start = Instant::now();
    if let Err(e) = app.connect(id) {
        return msgpack_response(
            StatusCode::OK,
            ConnectionStatus {
                error: Some(e.to_string()),
                ..status
            },
        );
    }
    // connect 对懒连接池总是成功，真正测通需 ping（与 Go 侧 /test 行为对齐）。
    let ping = app.ping(id).await;
    app.disconnect(id);
    match ping {
        Ok(()) => msgpack_response(
            StatusCode::OK,
            ConnectionStatus {
                connected: true,
                latency_ms: Some(Instant::now().duration_since(start).as_micros() as f64 / 1000.0),
                ..status
            },
        ),
        Err(e) => msgpack_response(
            StatusCode::OK,
            ConnectionStatus {
                error: Some(e.to_string()),
                ..status
            },
        ),
    }
}

// ─── 元数据 ─────────────────────────────────────────────────

async fn list_schemas(State(app): State<AppState>, Path(id): Path<String>) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.list_schemas(id).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn list_tables(
    State(app): State<AppState>,
    Path((id, schema)): Path<(String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.list_tables(id, &schema).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn list_columns(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.list_columns(id, &schema, &table).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn list_indexes(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.list_indexes(id, &schema, &table).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn list_foreign_keys(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.list_foreign_keys(id, &schema, &table).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn get_ddl(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.create_table_sql(id, &schema, &table).await {
        Ok(sql) => json_response(StatusCode::OK, serde_json::json!({"sql": sql})),
        Err(e) => error_response(e),
    }
}

// ─── 查询 ───────────────────────────────────────────────────

async fn execute_query(
    State(state): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let path_id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: QueryRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    let conn_id = req.connection_id.unwrap_or(path_id);
    let query_id = req.query_id.unwrap_or_else(Uuid::new_v4);

    // Register an in-flight query so POST /api/queries/{query_id}/cancel can reach us.
    let cancel = Arc::new(Notify::new());
    {
        let mut r = state.registry.lock().unwrap();
        r.insert(query_id, Arc::clone(&cancel));
    }

    // 超时（behavior.md §2.3）：timeout_ms>0 生效，=0/缺省表示无超时，超时不自动重试。
    // 驱动无法中断已在执行的 SQL（如 sqlite 递归 CTE），因此用「后台任务 + 截止时间
    // 竞争」：到点先返回 POLYDB_ERR_TIMEOUT，被放弃的任务在后台自然结束
    // （best-effort，与 cancel 的语义一致；与 Go 侧 goroutine+timer 实现对齐）。
    enum Outcome {
        Done(CoreResult<QueryResult>),
        Cancelled,
        TimedOut,
    }
    let worker_app = Arc::clone(&state.app);
    let worker_sql = req.sql.clone();
    let worker_params = req.params.clone();
    let worker = tokio::spawn(async move {
        worker_app
            .execute(conn_id, &worker_sql, &worker_params)
            .await
    });
    let timeout = req.timeout_ms.filter(|t| *t > 0).map(Duration::from_millis);

    let outcome = tokio::select! {
        r = worker => Outcome::Done(r.unwrap_or_else(|e| {
            Err(CoreError::Internal(format!("query worker failed: {e}")))
        })),
        _ = cancel.notified() => Outcome::Cancelled,
        _ = async {
            match timeout {
                Some(d) => tokio::time::sleep(d).await,
                None => std::future::pending::<()>().await,
            }
        } => Outcome::TimedOut,
    };

    // Unregister before returning, whether we won or lost the race.
    {
        let mut r = state.registry.lock().unwrap();
        r.remove(&query_id);
    }

    let x_query_id = HeaderValue::from_str(&query_id.to_string())
        .unwrap_or_else(|_| HeaderValue::from_static(""));

    match outcome {
        Outcome::Done(Ok(result)) => {
            let result = apply_max_rows(result, query_row_limit(&req));
            let mut resp = msgpack_response(StatusCode::OK, result);
            resp.headers_mut().insert(HEADER_X_QUERY_ID, x_query_id);
            resp
        }
        Outcome::Done(Err(e)) => {
            let mut resp = error_response(e);
            resp.headers_mut().insert(HEADER_X_QUERY_ID, x_query_id);
            resp
        }
        Outcome::Cancelled => {
            let pe = CoreError::Protocol(Box::new(PolyDBError::new(
                codes::CANCELLED,
                format!("query cancelled: {query_id}"),
            )));
            let mut resp = error_response(pe);
            resp.headers_mut().insert(HEADER_X_QUERY_ID, x_query_id);
            resp
        }
        Outcome::TimedOut => {
            // behavior.md §4：TIMEOUT 是 retryable 错误（与 Go 侧一致）。
            let pe = CoreError::Protocol(Box::new(
                PolyDBError::new(codes::TIMEOUT, format!("query timed out: {query_id}"))
                    .retryable(),
            ));
            let mut resp = error_response(pe);
            resp.headers_mut().insert(HEADER_X_QUERY_ID, x_query_id);
            resp
        }
    }
}

// 默认行数上限（behavior.md §5）；max_rows 缺省或 0 视为使用默认，硬上限同为 10000。
const DEFAULT_MAX_ROWS: u64 = 10000;

fn query_row_limit(req: &QueryRequest) -> u64 {
    match req.max_rows {
        Some(0) | None => DEFAULT_MAX_ROWS,
        Some(n) => n.min(DEFAULT_MAX_ROWS),
    }
}

// apply_max_rows 按 behavior.md §5 截断结果行：超过 limit 时截断并置
// truncated=true、total_rows 记录原始行数（不额外 count）。
fn apply_max_rows(mut result: QueryResult, limit: u64) -> QueryResult {
    if result.rows.len() as u64 <= limit {
        return result;
    }
    let total = result.rows.len() as u64;
    result.rows.truncate(limit as usize);
    result.truncated = true;
    result.total_rows = Some(total);
    result
}

/// Cancel an in-flight HTTP query registered under /api/connections/{id}/query.
/// Returns 204 on hit; 404 POLYDB_ERR_QUERY_NOT_FOUND if unknown or already done.
async fn cancel_query(State(state): State<AppState>, Path(query_id_str): Path<String>) -> Response {
    let query_id = match Uuid::parse_str(&query_id_str) {
        Ok(v) => v,
        Err(_) => {
            return json_response(
                StatusCode::BAD_REQUEST,
                PolyDBError::new(
                    codes::INVALID_PARAM,
                    format!("invalid query id: {query_id_str}"),
                ),
            )
        }
    };
    let mut r = state.registry.lock().unwrap();
    if let Some(n) = r.remove(&query_id) {
        n.notify_one();
        StatusCode::NO_CONTENT.into_response()
    } else {
        json_response(
            StatusCode::NOT_FOUND,
            PolyDBError::new(
                codes::QUERY_NOT_FOUND,
                format!("query not found: {query_id}"),
            ),
        )
    }
}

async fn execute_batch_query(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let path_id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: BatchQueryRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    let start = Instant::now();
    let mut results = Vec::with_capacity(req.statements.len());
    for stmt in &req.statements {
        let conn_id = stmt.connection_id.unwrap_or(path_id);
        match app.execute(conn_id, &stmt.sql, &stmt.params).await {
            Ok(result) => results.push(BatchResultItem::Ok(apply_max_rows(
                result,
                query_row_limit(stmt),
            ))),
            Err(e) => {
                results.push(BatchResultItem::Err(e.to_protocol()));
                if req.stop_on_error {
                    break;
                }
            }
        }
    }
    msgpack_response(
        StatusCode::OK,
        BatchQueryResult {
            results,
            total_execution_time_ms: Instant::now().duration_since(start).as_micros() as f64
                / 1000.0,
        },
    )
}

// ─── 事务 ───────────────────────────────────────────────────

async fn begin_transaction(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let req: BeginTransactionRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    // 与 Go 一致：req.connection_id 缺省时用路径 id（body 未带时 serde 填 uuid::nil）。
    let req = if req.connection_id.is_nil() {
        let path_id = match id.parse::<ConnectionId>() {
            Ok(v) => v,
            Err(_) => {
                return json_response(
                    StatusCode::BAD_REQUEST,
                    PolyDBError::new(codes::INVALID_PARAM, format!("invalid connection id: {id}")),
                )
            }
        };
        BeginTransactionRequest {
            connection_id: path_id,
            isolation_level: req.isolation_level,
        }
    } else {
        req
    };
    match app.begin_transaction(&req).await {
        Ok(info) => msgpack_response(StatusCode::CREATED, info),
        Err(e) => error_response(e),
    }
}

async fn execute_in_tx(
    State(app): State<AppState>,
    Path(txn_id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let req: QueryRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app
        .execute_in_transaction(&txn_id, &req.sql, &req.params)
        .await
    {
        Ok(result) => msgpack_response(
            StatusCode::OK,
            apply_max_rows(result, query_row_limit(&req)),
        ),
        Err(e) => error_response(e),
    }
}

async fn commit_transaction(State(app): State<AppState>, Path(txn_id): Path<String>) -> Response {
    match app.commit_transaction(&txn_id).await {
        Ok(info) => msgpack_response(StatusCode::OK, info),
        Err(e) => error_response(e),
    }
}

async fn rollback_transaction(State(app): State<AppState>, Path(txn_id): Path<String>) -> Response {
    match app.rollback_transaction(&txn_id).await {
        Ok(info) => msgpack_response(StatusCode::OK, info),
        Err(e) => error_response(e),
    }
}

// ─── KV（Redis）────────────────────────────────────────────

async fn kv_select_db(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: RedisSelectDbRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.select_db(id, req.index).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => error_response(e),
    }
}

async fn kv_scan_keys(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: RedisScanRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.scan_keys(id, req.cursor, &req.pattern, req.count).await {
        Ok(page) => msgpack_response(StatusCode::OK, page),
        Err(e) => error_response(e),
    }
}

async fn kv_get_value(
    State(app): State<AppState>,
    Path((id, key)): Path<(String, String)>,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    match app.get_value(id, &key).await {
        Ok(v) => msgpack_response(StatusCode::OK, v),
        Err(e) => error_response(e),
    }
}

async fn kv_set_value(
    State(app): State<AppState>,
    Path((id, key)): Path<(String, String)>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: RedisSetRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.set_value(id, &key, req.value).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => error_response(e),
    }
}

async fn kv_exec_command(
    State(app): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: RedisExecCommandRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.exec_command(id, &req.args).await {
        Ok(reply) => msgpack_response(StatusCode::OK, reply),
        Err(e) => error_response(e),
    }
}
