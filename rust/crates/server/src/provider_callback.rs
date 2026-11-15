//! 阶段 9：受限回调面（behavior.md §16.14）。与 Go 侧
//! `go/pkg/server/provider_callback.go` 严格镜像。
//!
//! 进程端点的 provider **不管理数据库连接、不接触凭据**：它收到 provider/exec
//! 后，经一个**仅回环可达**的回调端点把 SQL 交回宿主执行（§16.14.2）。
//!
//! 安全属性（§16.14.4）：provider 永远看不到 connection_id / 主机 / 端口 /
//! 用户名 / 密码，只看到不透明 handle 与 SQL/结果。

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::IntoResponse;
use axum::routing::post;
use axum::Router;
use polydb_app_core::AppCore;
use polydb_core::protocol::error::{codes, PolyDBError};
use polydb_core::protocol::query::QueryResult;
use serde::Deserialize;

#[cfg(test)]
mod tests;

/// 回调端点的唯一路由（§16.14.2）。
pub const PROVIDER_CALLBACK_PATH: &str = "/provider/exec";

/// 回调面限制（§16.14.2）。
const PROVIDER_CALLBACK_MAX_BODY: usize = 1 << 20; // 1 MiB
const PROVIDER_CALLBACK_MAX_INFLIGHT: usize = 4;
const PROVIDER_CALLBACK_TIMEOUT_MS: u64 = 30_000;

/// callbackRequest 是 provider 回调的请求体（handle + SQL + 参数）。
#[derive(Debug, Deserialize)]
struct CallbackRequest {
    handle: String,
    sql: String,
    #[serde(default)]
    params: Vec<polydb_core::protocol::Value>,
}

/// CallbackRegistry 是宿主侧的回调面：handle ↔ connection_id 映射 + 令牌 +
/// loopback 监听。与 Go 侧 callbackRegistry 严格镜像。
pub struct CallbackRegistry {
    /// app 是宿主的 app-core：回调执行走它（复用只读判定、限行、事务）。
    app: Option<Arc<AppCore>>,

    /// handle → connection_id（§16.14.1）。
    handles: Mutex<HashMap<String, uuid::Uuid>>,
    /// connection_id → handle（同一连接复用同一 handle）。
    by_conn: Mutex<HashMap<uuid::Uuid, String>>,
    /// token 是本次进程唯一的回调令牌（每个 provider attach 时下发）。
    token: String,

    inflight: AtomicUsize,
    addr: SocketAddr,
    /// listener 保留到 serve 启动（start_for_test 不启动 serve，保留也无妨）。
    listener: Mutex<Option<std::net::TcpListener>>,
}

impl std::fmt::Debug for CallbackRegistry {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CallbackRegistry")
            .field("addr", &self.addr)
            .finish()
    }
}

impl CallbackRegistry {
    /// 构造回调面并监听 loopback（随机端口）。
    ///
    /// 同步构造（std TcpListener + 在当前 tokio runtime 里 spawn serve），
    /// 这样调用方（router_with_token）不必是 async，也不必 block_on。
    pub fn start(app: Arc<AppCore>) -> Result<Arc<Self>, String> {
        let reg = Self::bind(Some(app))?;
        let router = Router::new()
            .route(PROVIDER_CALLBACK_PATH, post(handle_exec))
            .with_state(Arc::clone(&reg));
        // 在当前 runtime 里后台 serve；没有 runtime 时回调面不可用（宿主仍能
        // 启动，provider 进程只是拿不到回调地址）。
        let handle = tokio::runtime::Handle::try_current()
            .map_err(|_| "no tokio runtime for provider callback".to_string())?;
        let listener = reg
            .listener
            .lock()
            .expect("listener poisoned")
            .take()
            .ok_or("callback listener already taken")?;
        handle.spawn(async move {
            match tokio::net::TcpListener::from_std(listener) {
                Ok(listener) => {
                    let _ = axum::serve(listener, router).await;
                }
                Err(e) => {
                    tracing::warn!(error = %e, "provider callback serve failed");
                }
            }
        });
        Ok(reg)
    }

    /// 仅测试用：只绑定监听（不起 serve），用于验证回环与 handle 语义。
    #[cfg(test)]
    pub fn start_for_test() -> Arc<Self> {
        Self::bind(None).expect("bind callback")
    }

    /// 绑定回环监听并构造 registry（不含 serve）。
    fn bind(app: Option<Arc<AppCore>>) -> Result<Arc<Self>, String> {
        let std_listener = std::net::TcpListener::bind("127.0.0.1:0")
            .map_err(|e| format!("listen callback: {e}"))?;
        // tokio::net::TcpListener::from_std 要求非阻塞模式，否则 serve 起来后
        // 整个 runtime 会被阻塞在 accept 上（provider 的回调就永远等不到）。
        std_listener
            .set_nonblocking(true)
            .map_err(|e| format!("callback nonblocking: {e}"))?;
        let addr = std_listener
            .local_addr()
            .map_err(|e| format!("callback addr: {e}"))?;
        let token = random_token();
        Ok(Arc::new(CallbackRegistry {
            app,
            handles: Mutex::new(HashMap::new()),
            by_conn: Mutex::new(HashMap::new()),
            token,
            inflight: AtomicUsize::new(0),
            addr,
            listener: Mutex::new(Some(std_listener)),
        }))
    }

    /// 回调端点的完整地址（含路径）。
    pub fn callback_url(&self) -> String {
        format!("http://{}{}", self.addr, PROVIDER_CALLBACK_PATH)
    }

    /// 回调令牌（发给 provider 进程）。
    pub fn token(&self) -> String {
        self.token.clone()
    }

    /// 为某连接分配不透明 handle（不存在则惰性分配）。
    ///
    /// handle 与 connection_id 无算术关系，provider 无法猜测别的连接的 handle。
    pub fn handle_for(&self, connection_id: &str) -> Option<String> {
        let cid = uuid::Uuid::parse_str(connection_id).ok()?;
        {
            let by_conn = self.by_conn.lock().expect("by_conn poisoned");
            if let Some(h) = by_conn.get(&cid) {
                return Some(h.clone());
            }
        }
        let raw = uuid::Uuid::new_v4().simple().to_string();
        let mut by_conn = self.by_conn.lock().expect("by_conn poisoned");
        let mut handles = self.handles.lock().expect("handles poisoned");
        // 双检：并发下同一连接可能已分配过，复用先到的那个。
        if let Some(prev) = by_conn.get(&cid) {
            return Some(prev.clone());
        }
        by_conn.insert(cid, raw.clone());
        handles.insert(raw.clone(), cid);
        Some(raw)
    }

    /// resolve 把 handle 解析回 connection_id。
    pub(crate) fn resolve(&self, handle: &str) -> Option<uuid::Uuid> {
        self.handles
            .lock()
            .expect("handles poisoned")
            .get(handle)
            .copied()
    }
}

/// handle_exec 是回调端点的处理器：校验令牌与 handle，执行 SQL，返回结果。
async fn handle_exec(
    State(reg): State<Arc<CallbackRegistry>>,
    headers: HeaderMap,
    body: axum::body::Bytes,
) -> axum::response::Response {
    // 令牌校验（§16.14.2）：不匹配一律 401。
    let expected = format!("Bearer {}", reg.token);
    if headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        != Some(expected.as_str())
    {
        return callback_error(
            StatusCode::UNAUTHORIZED,
            PolyDBError::new(codes::UNAUTHORIZED, "invalid callback token"),
        );
    }
    // 请求体上限（§16.14.2）。
    if body.len() > PROVIDER_CALLBACK_MAX_BODY {
        return callback_error(
            StatusCode::PAYLOAD_TOO_LARGE,
            PolyDBError::new(codes::PAYLOAD_TOO_LARGE, "callback body too large"),
        );
    }
    // 并发闸门（§16.14.2）。
    let inflight = reg.inflight.fetch_add(1, Ordering::SeqCst);
    if inflight >= PROVIDER_CALLBACK_MAX_INFLIGHT {
        reg.inflight.fetch_sub(1, Ordering::SeqCst);
        return callback_error(
            StatusCode::TOO_MANY_REQUESTS,
            PolyDBError::new(
                codes::DRIVER_NOT_AVAILABLE,
                "too many concurrent provider callbacks",
            ),
        );
    }
    let result = exec_callback(reg.clone(), body).await;
    reg.inflight.fetch_sub(1, Ordering::SeqCst);
    result
}

/// exec_callback 解析请求并执行 SQL（复用宿主的 app-core 逻辑，§16.14.2）。
async fn exec_callback(
    reg: Arc<CallbackRegistry>,
    body: axum::body::Bytes,
) -> axum::response::Response {
    let req: CallbackRequest = match serde_json::from_slice(&body) {
        Ok(r) => r,
        Err(e) => {
            return callback_error(
                StatusCode::BAD_REQUEST,
                PolyDBError::new(codes::INVALID_PARAM, format!("decode callback body: {e}")),
            )
        }
    };
    let Some(connection_id) = reg.resolve(&req.handle) else {
        return callback_error(
            StatusCode::BAD_REQUEST,
            PolyDBError::new(codes::INVALID_PARAM, "unknown handle"),
        );
    };
    // 执行走宿主的 app-core：只读判定、限行、事务等既有逻辑全部复用
    // （§16.14.2「不开任何后门」）。
    let Some(app) = reg.app.as_ref() else {
        return callback_error(
            StatusCode::SERVICE_UNAVAILABLE,
            PolyDBError::new(
                codes::DRIVER_NOT_AVAILABLE,
                "provider callback has no app-core",
            ),
        );
    };
    let timeout = tokio::time::Duration::from_millis(PROVIDER_CALLBACK_TIMEOUT_MS);
    let exec =
        tokio::time::timeout(timeout, app.execute(connection_id, &req.sql, &req.params)).await;
    match exec {
        Err(_) => callback_error(
            StatusCode::GATEWAY_TIMEOUT,
            PolyDBError::new(codes::TIMEOUT, "provider callback timed out"),
        ),
        Ok(Err(e)) => callback_error(StatusCode::OK, e.to_protocol()),
        Ok(Ok(result)) => match serde_json::to_string(&result) {
            Ok(body) => (
                StatusCode::OK,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                format!("{{\"result\":{body}}}"),
            )
                .into_response(),
            Err(e) => callback_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                PolyDBError::new(codes::QUERY_FAILED, format!("encode result: {e}")),
            ),
        },
    }
}

/// callback_error 写出错误响应（错误码原样映射，§16.14.2）。
fn callback_error(status: StatusCode, pe: PolyDBError) -> axum::response::Response {
    let body = serde_json::json!({ "error": pe }).to_string();
    (
        status,
        [(axum::http::header::CONTENT_TYPE, "application/json")],
        body,
    )
        .into_response()
}

/// random_token 生成随机令牌（每进程一个，§16.14.2）。
fn random_token() -> String {
    uuid::Uuid::new_v4().simple().to_string() + &uuid::Uuid::new_v4().simple().to_string()
}

/// 仅用于类型占位：QueryResult 的 JSON 编码在 spec 侧已由 genprotocol 钉死。
#[allow(dead_code)]
fn _assert_result_json(r: &QueryResult) -> String {
    serde_json::to_string(r).unwrap_or_default()
}
