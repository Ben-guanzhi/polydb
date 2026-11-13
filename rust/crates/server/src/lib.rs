//! polydb-server：基于 Axum 的 HTTP 控制面，行为与 Go 实现（go/pkg/server）保持一致。

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::extract::{Path, State};
use axum::http::header::{
    ACCEPT_RANGES, ACCESS_CONTROL_ALLOW_HEADERS, ACCESS_CONTROL_ALLOW_METHODS,
    ACCESS_CONTROL_ALLOW_ORIGIN, ACCESS_CONTROL_MAX_AGE, CONTENT_RANGE, CONTENT_TYPE, HOST, ORIGIN,
    RANGE, VARY,
};
use axum::http::{HeaderMap, HeaderValue, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{body::Bytes, Router};
use polydb_app_core::AppCore;
use polydb_core::protocol::error::codes;
use polydb_core::protocol::error::PolyDBError;
use polydb_core::protocol::{
    BatchQueryRequest, BatchQueryResult, BatchResultItem, BeginTransactionRequest, ConnectionId,
    ConnectionStatus, CreateConnectionRequest, QueryRequest, QueryResult, TableCountResult,
    TableRowsRequest, UpdateConnectionRequest, Value,
};
use polydb_core::{
    CoreError, CoreResult, RedisExecCommandRequest, RedisScanRequest, RedisSelectDbRequest,
    RedisSetRequest, SchemaDdlRequest, SchemaDiffRequest,
};
use serde::de::DeserializeOwned;
use serde::Serialize;
use tokio::sync::Notify;
use uuid::Uuid;

const CONTENT_MSGPACK: &str = "application/msgpack";
const CONTENT_JSON: &str = "application/json";
const HEADER_X_QUERY_ID: &str = "X-Query-ID";

// 请求体上限，与 Go 侧 maxBodyBytes 对齐。请求体只装 SQL 文本与参数，16 MiB
// 远超正常量级又不至于成为放大器。大文件走 blobs 旁路。
const MAX_BODY_BYTES: usize = 16 * 1024 * 1024;

/// body_limit_middleware 与 Go 侧 bodyLimitMiddleware 逐语义对齐：先把请求体
/// 完整读入内存并检查上限，超限回 413 + POLYDB_ERR_PAYLOAD_TOO_LARGE 包络。
/// 之前用 axum 的 DefaultBodyLimit，它直接掐断连接回一个裸 413（无 PolyDB
/// 错误包络），前端拿不到 code/message，与 Go 侧契约漂移。
///
/// 与 Go 一致的两点细节：声明 Content-Length 超限在读任何字节之前就拒绝
/// （占满连接与带宽的攻击面）；chunked 无声明长度时整体读入后判超限。
async fn body_limit_middleware(
    req: axum::extract::Request,
    next: axum::middleware::Next,
) -> Response {
    let declared = req
        .headers()
        .get(axum::http::header::CONTENT_LENGTH)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<usize>().ok());
    if let Some(n) = declared {
        if n > MAX_BODY_BYTES {
            return payload_too_large(format!(
                "request body too large: {n} bytes exceeds {MAX_BODY_BYTES}"
            ));
        }
    }
    let (parts, body) = req.into_parts();
    match axum::body::to_bytes(body, MAX_BODY_BYTES + 1).await {
        Ok(data) => {
            if data.len() > MAX_BODY_BYTES {
                return payload_too_large(format!(
                    "request body too large: more than {MAX_BODY_BYTES} bytes"
                ));
            }
            let req = axum::extract::Request::from_parts(parts, axum::body::Body::from(data));
            next.run(req).await
        }
        Err(_) => payload_too_large(format!(
            "request body too large: more than {MAX_BODY_BYTES} bytes"
        )),
    }
}

fn payload_too_large(msg: String) -> Response {
    json_response(
        StatusCode::PAYLOAD_TOO_LARGE,
        PolyDBError::new(codes::PAYLOAD_TOO_LARGE, msg),
    )
}

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
    // HTTP 查询并发闸门（behavior.md §6.1 对齐项）：与 WS 的 per-session
    // WS_MAX_IN_FLIGHT=16 对齐。此前 HTTP 路径对每个请求直接 tokio::spawn，
    // 无上限——单个 HTTP 客户端可开无上限并发查询，打满 DB 连接与内存；Go 侧
    // 已有 httpMaxConcurrentQueries=16 的同款闸门（等待 30s 后 429），Rust
    // 单侧缺席是登记册漏登的漂移。容量 16 与 Go/WS 一致。
    http_gate: Arc<tokio::sync::Semaphore>,
    // Some(token) 时启用 Bearer 鉴权（behavior.md §12.1）。
    token: Option<String>,
}

// HTTP_MAX_CONCURRENT_QUERIES 与 Go 侧 httpMaxConcurrentQueries、WS 侧
// WS_MAX_IN_FLIGHT 三者一致：16。
const HTTP_MAX_CONCURRENT_QUERIES: usize = 16;

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
        http_gate: Arc::new(tokio::sync::Semaphore::new(HTTP_MAX_CONCURRENT_QUERIES)),
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
        .route("/api/schema/diff", post(diff_schema))
        .route("/api/schema/ddl", post(generate_schema_ddl))
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/rows/query",
            post(browse_rows),
        )
        .route(
            "/api/connections/{id}/schemas/{schema}/tables/{table}/rows/count",
            post(browse_rows_count),
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
        .route("/api/connections/{id}/blobs/{blob_id}", get(get_blob))
        .route("/api/queries/{query_id}/cancel", post(cancel_query))
        .layer(axum::middleware::from_fn_with_state(
            state.clone(),
            auth_middleware,
        ))
        .layer(axum::middleware::from_fn(body_limit_middleware))
        .layer(axum::middleware::from_fn(cors_middleware));
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

/// CORS 中间件，与 Go 侧 corsMiddleware 对齐。此前 HTTP 侧完全没有 CORS，只有
/// WS 有 Origin 校验；生产环境把前端放到独立域名后所有跨源请求都会被浏览器拦下。
/// 判定规则复用 ws::origin_allowed，两处白名单不会各写一遍后漂移。
async fn cors_middleware(req: axum::extract::Request, next: axum::middleware::Next) -> Response {
    let headers = req.headers().clone();
    let origin = headers
        .get(ORIGIN)
        .and_then(|v| v.to_str().ok())
        .filter(|o| !o.is_empty())
        .map(str::to_owned);
    let host = headers
        .get(HOST)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let allowed = origin
        .as_deref()
        .is_some_and(|o| ws::origin_allowed(host.as_deref(), Some(o)));

    // 预检不携带 Bearer token，必须在任何鉴权之前被应答，否则浏览器拒绝后续请求。
    if req.method() == Method::OPTIONS {
        let mut resp = StatusCode::NO_CONTENT.into_response();
        set_cors_headers(&mut resp, allowed, origin.as_deref());
        return resp;
    }
    let mut resp = next.run(req).await;
    set_cors_headers(&mut resp, allowed, origin.as_deref());
    resp
}

/// 回具体 Origin，不用通配符：通配符会把 API 重新开放给所有网页。
fn set_cors_headers(resp: &mut Response, allowed: bool, origin: Option<&str>) {
    if !allowed {
        return;
    }
    let Some(origin) = origin else {
        return;
    };
    let Ok(v) = HeaderValue::from_str(origin) else {
        return;
    };
    let h = resp.headers_mut();
    h.insert(ACCESS_CONTROL_ALLOW_ORIGIN, v);
    h.insert(VARY, HeaderValue::from_static("Origin"));
    h.insert(
        ACCESS_CONTROL_ALLOW_METHODS,
        HeaderValue::from_static("GET, POST, PUT, DELETE, OPTIONS"),
    );
    // Authorization 与带 charset 的 Content-Type 都不是 CORS 安全列表内的请求头，
    // 漏掉它们预检会直接失败，Bearer 鉴权在跨源部署下会卡死。
    h.insert(
        ACCESS_CONTROL_ALLOW_HEADERS,
        HeaderValue::from_static("Authorization, Content-Type"),
    );
    h.insert(ACCESS_CONTROL_MAX_AGE, HeaderValue::from_static("600"));
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
        codes::TRANSACTION_NOT_FOUND => StatusCode::NOT_FOUND,
        codes::QUERY_NOT_FOUND => StatusCode::NOT_FOUND,
        codes::BLOB_NOT_FOUND => StatusCode::NOT_FOUND,
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

// ─── 大字段（spec/encoding.md §5.2 旁路 Range 取回）────────────

/// [`parse_byte_range`] 结果状态：0 全量（200）、1 部分（206）、2 不可满足（416）。
/// 与 Go `server.parseByteRange` 的 iota 常量同序对拍。
const RANGE_FULL: u8 = 0;
const RANGE_PARTIAL: u8 = 1;
const RANGE_UNSATISFIABLE: u8 = 2;

/// 解析单段 `Range: bytes=` 头（spec/encoding.md §5.2 取回表），与 Go
/// `server.parseByteRange` 同表对拍：非 bytes= 前缀、多段（含逗号）、
/// 语法错误、a > b 一律忽略（全量）；a ≥ size 或 `bytes=-0` 不可满足；
/// b 超界钳到 size-1；`bytes=-N` 取末 N 字节，N ≥ size 回全量 206。
/// 返回 `(start, end, mode)`；全量时 end 无消费者（size=0 下 Rust 报 0，
/// Go 报 -1），长度调用方按 mode 计算。
fn parse_byte_range(h: &str, size: u64) -> (u64, u64, u8) {
    let full = || (0u64, size.saturating_sub(1), RANGE_FULL);
    if h.is_empty() {
        return full();
    }
    let Some(spec) = h.strip_prefix("bytes=") else {
        return full();
    };
    if spec.contains(',') {
        return full();
    }
    if let Some(n_str) = spec.strip_prefix('-') {
        let Ok(n) = n_str.parse::<u64>() else {
            return full();
        };
        if n == 0 || size == 0 {
            return (0, 0, RANGE_UNSATISFIABLE);
        }
        if n >= size {
            return (0, size - 1, RANGE_PARTIAL);
        }
        return (size - n, size - 1, RANGE_PARTIAL);
    }
    let Some(dash) = spec.find('-') else {
        return full();
    };
    let Ok(a) = spec[..dash].parse::<u64>() else {
        return full();
    };
    if a >= size {
        return (0, 0, RANGE_UNSATISFIABLE);
    }
    let b_str = &spec[dash + 1..];
    if b_str.is_empty() {
        return (a, size - 1, RANGE_PARTIAL);
    }
    let Ok(b) = b_str.parse::<u64>() else {
        return full();
    };
    if b < a {
        return full();
    }
    (a, b.min(size - 1), RANGE_PARTIAL)
}

/// GET /api/connections/{id}/blobs/{blob_id}：旁路大字段取回（§5.2）。
/// 连接 id 按既有约定只校验格式；blob_id 须为合法 UUID（非法 400），
/// 未知/过期 404 BLOB_NOT_FOUND；单段 Range 按 §5.2 表格，无 Range 全量。
async fn get_blob(
    State(state): State<AppState>,
    Path((id, blob_id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Response {
    if let Err(res) = parse_conn_id(id) {
        return *res;
    }
    if Uuid::parse_str(&blob_id).is_err() {
        return json_response(
            StatusCode::BAD_REQUEST,
            PolyDBError::new(codes::INVALID_PARAM, format!("invalid blob id: {blob_id}")),
        );
    }
    let store = state.app.blob_store();
    let size = match store.size(&blob_id) {
        Ok(s) => s,
        Err(e) => return error_response(e),
    };
    let range = headers
        .get(RANGE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    let (start, end, mode) = parse_byte_range(range, size);

    if mode == RANGE_UNSATISFIABLE {
        // Go 侧先设 Accept-Ranges / octet-stream 再 writeJSON 覆盖类型，
        // 净效果即以下头部组合。
        let mut resp = json_response(
            StatusCode::RANGE_NOT_SATISFIABLE,
            PolyDBError::new(codes::INVALID_PARAM, "requested range not satisfiable"),
        );
        resp.headers_mut()
            .insert(ACCEPT_RANGES, HeaderValue::from_static("bytes"));
        resp.headers_mut().insert(
            CONTENT_RANGE,
            match format!("bytes */{size}").parse::<HeaderValue>() {
                Ok(v) => v,
                Err(_) => {
                    return error_response(CoreError::Protocol(Box::new(PolyDBError::new(
                        codes::UNKNOWN,
                        "failed to build content-range header",
                    ))));
                }
            },
        );
        return resp;
    }

    let length = if mode == RANGE_FULL {
        size
    } else {
        end - start + 1
    };
    let data = match store.read_range(&blob_id, start, length) {
        Ok(d) => d,
        Err(e) => return error_response(e),
    };
    let status = if mode == RANGE_PARTIAL {
        StatusCode::PARTIAL_CONTENT
    } else {
        StatusCode::OK
    };
    if mode == RANGE_PARTIAL {
        (
            status,
            [
                (ACCEPT_RANGES, HeaderValue::from_static("bytes")),
                (
                    CONTENT_TYPE,
                    HeaderValue::from_static("application/octet-stream"),
                ),
                (
                    CONTENT_RANGE,
                    format!("bytes {start}-{end}/{size}")
                        .parse::<HeaderValue>()
                        .unwrap(),
                ),
            ],
            data,
        )
            .into_response()
    } else {
        (
            status,
            [
                (ACCEPT_RANGES, HeaderValue::from_static("bytes")),
                (
                    CONTENT_TYPE,
                    HeaderValue::from_static("application/octet-stream"),
                ),
            ],
            data,
        )
            .into_response()
    }
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
    // 已连接的连接直接 ping 现有驱动：无条件 connect+disconnect 会砸掉在用的
    // 驱动、丢弃其上活动事务并关闭 SSH 隧道（Go 侧 server.go 的 IsConnected
    // 分支 2026-10-28 已修，双端行为对齐）。
    let already_connected = app
        .connection_status(id)
        .map(|st| st.connected)
        .unwrap_or(false);
    if already_connected {
        return match app.ping(id).await {
            Ok(()) => msgpack_response(
                StatusCode::OK,
                ConnectionStatus {
                    connected: true,
                    latency_ms: Some(
                        Instant::now().duration_since(start).as_micros() as f64 / 1000.0,
                    ),
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
        };
    }
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

// ─── 结构比对（Compare & Sync E2，behavior.md §14）──────────

async fn diff_schema(State(app): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    let req: SchemaDiffRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.schema_diff(&req).await {
        Ok(plan) => msgpack_response(StatusCode::OK, plan),
        Err(e) => error_response(e),
    }
}

async fn generate_schema_ddl(
    State(app): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let req: SchemaDdlRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.schema_ddl(&req).await {
        Ok(script) => msgpack_response(StatusCode::OK, script),
        Err(e) => error_response(e),
    }
}

// ─── 查询 ───────────────────────────────────────────────────

// ─── 表数据浏览（M11，behavior.md §13）──────────────────────

async fn browse_rows(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: TableRowsRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.browse_rows(id, &schema, &table, &req).await {
        Ok(out) => msgpack_response(StatusCode::OK, out),
        Err(e) => error_response(e),
    }
}

async fn browse_rows_count(
    State(app): State<AppState>,
    Path((id, schema, table)): Path<(String, String, String)>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let id = match parse_conn_id(id) {
        Ok(v) => v,
        Err(res) => return *res,
    };
    let req: TableRowsRequest = match decode_body(&headers, &body) {
        Ok(r) => r,
        Err(res) => return *res,
    };
    match app.browse_rows_count(id, &schema, &table, &req).await {
        Ok(count) => msgpack_response(StatusCode::OK, TableCountResult { count }),
        Err(e) => error_response(e),
    }
}

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

    // HTTP 并发闸门：超限时等待最多 30s（与 Go 侧一致），仍无空位回 429
    // 包络。槽位由 worker 任务持有到查询真正结束（而不是 handler 返回时）——
    // 超时/取消路径提前返回后被放弃的查询还占着 DB 连接，只有它真正结束才
    // 腾出门控名额。permit 在 worker spawn 之前取得，spawn 本身受限。
    let gate_permit = match tokio::time::timeout(
        Duration::from_secs(30),
        state.http_gate.clone().acquire_owned(),
    )
    .await
    {
        Ok(Ok(permit)) => Some(permit),
        Ok(Err(_)) => None, // semaphore closed：server 正在关停，直接回 503
        Err(_) => {
            return msgpack_response(
                StatusCode::TOO_MANY_REQUESTS,
                PolyDBError::new(
                    codes::QUERY_FAILED,
                    format!("too many concurrent queries (max {HTTP_MAX_CONCURRENT_QUERIES})"),
                ),
            );
        }
    };

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
        // permit move 进任务：查询结束（自然完成/驱动出错）即释放槽位。
        let _gate = gate_permit;
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
            // PR-03：cancel 只「放弃等待」——worker 任务被 detach，仍在跑并占着
            // 池连接与准入 permit 直到自然结束（见 872-875 的注释）。这条日志是
            // 唯一能解释「刚取消完就 pool timed out」的信号。
            tracing::info!(
                query_id = %query_id,
                connection_id = ?conn_id,
                "query cancelled by client; the abandoned worker keeps its pool slot until it finishes"
            );
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
            // PR-03：与 cancel 同理，超时只是不再等，查询本身还在后台跑。
            tracing::warn!(
                query_id = %query_id,
                connection_id = ?conn_id,
                timeout_ms = req.timeout_ms,
                "query timed out; the abandoned worker keeps its pool slot until it finishes"
            );
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

pub(crate) fn query_row_limit(req: &QueryRequest) -> u64 {
    match req.max_rows {
        Some(0) | None => DEFAULT_MAX_ROWS,
        Some(n) => n.min(DEFAULT_MAX_ROWS),
    }
}

// apply_max_rows 按 behavior.md §5 截断结果行：超过 limit 时截断并置
// truncated=true、total_rows 记录原始行数（不额外 count）。
//
// 行上限之外还有响应字节上限（与 Go 侧 maxResponseBytes 对齐）：行数封了
// 10000 但每行可携带内联 blob（§5.1 允许 ≤1MB），10000 行 × 1MB = 10GB 的
// 响应会在序列化前把 server OOM。超过字节上限时同样截断。
pub(crate) fn apply_max_rows(mut result: QueryResult, limit: u64) -> QueryResult {
    let total = result.rows.len() as u64;
    if total > limit {
        result.rows.truncate(limit as usize);
        result.truncated = true;
        result.total_rows = Some(total);
    }
    if res_bytes(&result) > MAX_RESPONSE_BYTES {
        let mut keep = 0usize;
        let mut acc = 0u64;
        for row in &result.rows {
            acc += row_bytes(row);
            keep += 1;
            if acc >= MAX_RESPONSE_BYTES {
                break;
            }
        }
        if keep < result.rows.len() {
            result.rows.truncate(keep);
            result.truncated = true;
            if result.total_rows.is_none() {
                result.total_rows = Some(total);
            }
        }
    }
    result
}

// maxResponseBytes 的 Rust 对应：单个查询响应的载荷字节上限（256MB）。超过
// 它的响应在 msgpack 序列化之前就把 server 拖进 OOM，截断是唯一的防线。
const MAX_RESPONSE_BYTES: u64 = 256 << 20;

// row_bytes 与 Go 侧 rowBytes 同式：msgpack 由 Value 再编，字符串长度就是
// 负载下界（blob 编码里 base64 已放大 4/3，估计偏保守但截断方向正确）。
fn value_bytes(v: &Value) -> u64 {
    match v {
        Value::String(s) => s.len() as u64,
        Value::Array(a) => a.iter().map(value_bytes).sum::<u64>() + 8,
        Value::Object(m) => {
            m.iter()
                .map(|(k, v)| (k.len() as u64) + value_bytes(v))
                .sum::<u64>()
                + 8
        }
        _ => 8,
    }
}

fn row_bytes(row: &[Value]) -> u64 {
    row.iter().map(value_bytes).sum::<u64>() + 8
}

fn res_bytes(res: &QueryResult) -> u64 {
    res.rows.iter().map(|r| row_bytes(r)).sum::<u64>() + 64
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

#[cfg(test)]
mod response_cap_tests {
    use super::*;
    use polydb_core::protocol::Value;

    // 超过 MAX_RESPONSE_BYTES 的响应在行数限制之内也会被截断：10 行各 100MB
    // 的内联 blob，行数上限 10000 拦不住，字节上限拦住。
    #[test]
    fn oversized_rows_truncate_by_bytes() {
        let big = "x".repeat(100 * 1024 * 1024); // 100MB 字符串 ≈ 100MB 负载
        let rows: Vec<Vec<Value>> = (0..10).map(|_| vec![Value::String(big.clone())]).collect();
        let result = QueryResult {
            columns: vec![],
            rows,
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        };
        let out = apply_max_rows(result, 10_000);
        assert!(out.truncated, "byte cap must truncate");
        assert!(
            out.rows.len() < 10,
            "rows = {}, must be cut below 10",
            out.rows.len()
        );
        assert_eq!(out.total_rows, Some(10));
    }

    // 正常大小不受影响：25 行小值直接通过，truncated 不置位。
    #[test]
    fn normal_sized_result_passes_through() {
        let rows: Vec<Vec<Value>> = (0..25).map(|i| vec![Value::Integer(i)]).collect();
        let result = QueryResult {
            columns: vec![],
            rows,
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        };
        let out = apply_max_rows(result, 10_000);
        assert!(!out.truncated);
        assert_eq!(out.rows.len(), 25);
        assert_eq!(out.total_rows, None);
    }

    // 行上限与字节上限同时命中：先按行截到 limit，再按字节截。total_rows
    // 记录的是最初的总行数。
    #[test]
    fn row_cap_then_byte_cap_keeps_original_total() {
        let big = "x".repeat(60 * 1024 * 1024); // 60MB：5 行 300MB 超 256MB
        let rows: Vec<Vec<Value>> = (0..8).map(|_| vec![Value::String(big.clone())]).collect();
        let result = QueryResult {
            columns: vec![],
            rows,
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        };
        let out = apply_max_rows(result, 5);
        assert!(out.truncated);
        assert!(out.rows.len() <= 5);
        assert_eq!(out.total_rows, Some(8), "total must be the original 8");
    }
}
