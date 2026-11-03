//! /ws WebSocket 处理器。契约见 spec/asyncapi.yaml。

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::extract::ws::{Message, WebSocket, WebSocketUpgrade};
use axum::extract::State;
use axum::http::{header, HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use futures_util::{SinkExt, StreamExt};
use polydb_app_core::AppCore;
use polydb_core::protocol::error::codes;
use polydb_core::protocol::error::PolyDBError;
use polydb_core::protocol::ws::{ClientMessage, ServerMessage};
use polydb_core::protocol::{ConnectionId, QueryRequest};
use serde::Serialize;
use tokio::sync::{mpsc, Notify};
use uuid::Uuid;

type AppState = super::AppState;
type AppRef = Arc<AppCore>;

const SERVER_VERSION: &str = "0.1.0";

// 单条 WS 消息上限，与 Go 侧 maxWSMessageBytes 对齐。axum 缺省 max_message_size
// 为 64 MiB、max_frame_size 为 16 MiB，这里显式收紧。WS 消息只装查询文本与参数，
// 正常量级在 KB。
const MAX_WS_MESSAGE: usize = 16 * 1024 * 1024;

pub async fn ws_upgrade(
    State(state): State<AppState>,
    headers: HeaderMap,
    ws: WebSocketUpgrade,
) -> Response {
    // 防浏览器跨站 WebSocket 劫持（CSWSH），判定规则见 origin_allowed。
    if !check_origin(&headers) {
        return (
            StatusCode::FORBIDDEN,
            "cross-origin websocket handshake rejected",
        )
            .into_response();
    }
    let app = state.app.clone();
    let token = state.token.clone();
    ws.max_message_size(MAX_WS_MESSAGE)
        .max_frame_size(MAX_WS_MESSAGE)
        .on_upgrade(move |socket| session(app, token, socket))
}

/// Origin 是否可信（behavior.md §12.2）：无 Origin 头放行（TUI/CLI 等非浏览器
/// 客户端）；命中 POLYDB_ALLOWED_ORIGINS（逗号分隔）白名单放行；Origin host 与
/// 请求 Host 一致（同源，覆盖服务端自托管 Web 前端）放行；其余拒绝。
/// HTTP CORS 中间件与 WS 升级共用这一份，避免两处白名单各写一遍后漂移。
pub(super) fn origin_allowed(host: Option<&str>, origin: Option<&str>) -> bool {
    let Some(origin) = origin.filter(|o| !o.is_empty()) else {
        return true;
    };
    if let Some(allowed) = std::env::var("POLYDB_ALLOWED_ORIGINS")
        .ok()
        .filter(|v| !v.is_empty())
    {
        if allowed
            .split(',')
            .any(|a| a.trim().eq_ignore_ascii_case(origin))
        {
            return true;
        }
    }
    host.is_some_and(|h| origin_host(origin).is_some_and(|o| o.eq_ignore_ascii_case(h)))
}

/// 从请求头判定 Origin 是否可信。
pub(super) fn check_origin(headers: &HeaderMap) -> bool {
    let host = headers.get(header::HOST).and_then(|v| v.to_str().ok());
    let origin = headers.get(header::ORIGIN).and_then(|v| v.to_str().ok());
    origin_allowed(host, origin)
}

/// 取 Origin 的 host[:port] 部分：剥离 `scheme://` 前缀，其余即 host。
/// Origin 规范上不含路径/查询，多余部分会导致与 Host 不匹配而被拒，可接受。
fn origin_host(origin: &str) -> Option<&str> {
    let after_scheme = origin
        .split_once("://")
        .map(|(_, rest)| rest)
        .unwrap_or(origin);
    let host = after_scheme.split(['/', '?', '#']).next()?;
    if host.is_empty() {
        None
    } else {
        Some(host)
    }
}

async fn session(app: AppRef, token: Option<String>, socket: WebSocket) {
    let (mut ws_tx, mut ws_rx) = socket.split();
    let (tx, mut rx) = mpsc::unbounded_channel::<ServerMessage>();

    let mut session = Session {
        app,
        token,
        tx: tx.clone(),
        connection_id: None,
        authed: false,
        in_flight: Arc::new(Mutex::new(HashMap::new())),
    };

    let mut send = tokio::spawn(async move {
        while let Some(msg) = rx.recv().await {
            let bytes = match encode(&msg) {
                Ok(b) => b,
                Err(_) => continue,
            };
            if ws_tx.send(Message::Binary(bytes.into())).await.is_err() {
                return;
            }
        }
    });

    tokio::select! {
        _ = &mut send => {},
        _ = receive_loop(&mut session, &mut ws_rx) => {}
    }

    session.cancel_all();
    // 先丢掉 session（内含 tx 的克隆）再 drop 原始 tx：否则 rx.recv() 永不返回
    // None，outbound 任务收不了尾，连接在协议要求"关闭"时（如鉴权失败）保持悬挂。
    drop(session);
    drop(tx);
    let _ = send.await;
}

struct Session {
    app: AppRef,
    token: Option<String>,
    tx: mpsc::UnboundedSender<ServerMessage>,
    connection_id: Option<ConnectionId>,
    authed: bool,
    // 共享给每个查询的后台任务：完成路径必须注销自己，否则条目无界增长、
    // 对已完成查询的取消永远石沉大海。std Mutex 够用——临界区里没有 await。
    in_flight: Arc<Mutex<HashMap<Uuid, Arc<Notify>>>>,
}

// 单会话并发在途查询闸门，与 Go 侧 wsMaxConcurrentQueries 对齐。
const WS_MAX_IN_FLIGHT: usize = 16;

fn register_in_flight(
    registry: &Mutex<HashMap<Uuid, Arc<Notify>>>,
    query_id: Uuid,
) -> Option<Arc<Notify>> {
    let mut map = registry.lock().expect("in_flight poisoned");
    if map.len() >= WS_MAX_IN_FLIGHT {
        return None;
    }
    let cancel = Arc::new(Notify::new());
    map.insert(query_id, Arc::clone(&cancel));
    Some(cancel)
}

/// 注销一条在途查询；返回是否仍登记着（完成路径与取消路径经同一出口，
/// notify_one 只会送达仍在等待的一方）。
fn retire_in_flight(registry: &Mutex<HashMap<Uuid, Arc<Notify>>>, query_id: Uuid) -> bool {
    let mut map = registry.lock().expect("in_flight poisoned");
    map.remove(&query_id).is_some()
}

async fn receive_loop(
    session: &mut Session,
    stream: &mut futures_util::stream::SplitStream<WebSocket>,
) {
    while let Some(msg) = stream.next().await {
        let bytes = match msg {
            Ok(Message::Text(t)) => t.as_bytes().to_vec(),
            Ok(Message::Binary(b)) => b.to_vec(),
            Ok(Message::Ping(p)) => {
                tracing::debug!("ws ping, len={}", p.len());
                continue;
            }
            Ok(Message::Pong(_)) | Ok(Message::Close(_)) => return,
            Err(e) => {
                tracing::debug!("ws recv error: {e}");
                return;
            }
        };
        let msg: ClientMessage = match decode::<ClientMessage>(&bytes) {
            Ok(m) => m,
            Err(e) => {
                let _ = session.send(ServerMessage::QueryError {
                    query_id: Uuid::nil(),
                    error: PolyDBError::new(codes::INVALID_PARAM, format!("decode: {e}")),
                });
                continue;
            }
        };
        match msg {
            ClientMessage::Hello {
                connection_id,
                auth,
                ..
            } => {
                // behavior.md §12.2：启用 token 时 hello 必须携带匹配 auth.token，
                // 失败发送 query_error(UNAUTHORIZED) 后关闭连接。
                if let Some(expected) = &session.token {
                    let provided = auth.as_ref().and_then(|a| a.token.as_deref());
                    if provided != Some(expected.as_str()) {
                        let _ = session.send(ServerMessage::QueryError {
                            query_id: Uuid::nil(),
                            error: PolyDBError::new(
                                codes::UNAUTHORIZED,
                                "missing or invalid bearer token",
                            ),
                        });
                        return;
                    }
                }
                let ok = session.handle_hello(connection_id).await;
                if !ok {
                    return;
                }
            }
            ClientMessage::Query { query_id, request } => {
                // behavior.md §12.2：启用 token 时，未通过认证 hello 之前拒绝一切 query，
                // 防止跳过 hello 直接携带 connection_id 执行 SQL。拒绝后关闭连接。
                if session.token.is_some() && !session.authed {
                    let _ = session.send(ServerMessage::QueryError {
                        query_id,
                        error: PolyDBError::new(
                            codes::UNAUTHORIZED,
                            "query requires authenticated hello first",
                        ),
                    });
                    return;
                }
                session.handle_query(query_id, request);
            }
            ClientMessage::QueryCancel { query_id } => {
                session.handle_cancel(query_id);
            }
        }
    }
}

impl Session {
    fn send(&self, msg: ServerMessage) -> bool {
        self.tx.send(msg).is_ok()
    }

    async fn handle_hello(&mut self, connection_id: ConnectionId) -> bool {
        match self.app.get_connection_info(connection_id) {
            Ok(Some(_info)) => {
                self.connection_id = Some(connection_id);
                self.authed = true;
                let _ = self.send(ServerMessage::HelloAck {
                    server_version: SERVER_VERSION.to_string(),
                    db_version: None,
                });
                true
            }
            Ok(None) | Err(_) => {
                let _ = self.send(ServerMessage::QueryError {
                    query_id: Uuid::nil(),
                    error: PolyDBError::new(
                        codes::CONNECTION_NOT_FOUND,
                        format!("connection not found: {connection_id}"),
                    ),
                });
                true
            }
        }
    }

    fn handle_query(&mut self, query_id: Uuid, request: QueryRequest) {
        let conn_id = match request.connection_id.or(self.connection_id) {
            Some(id) => id,
            None => {
                let _ = self.send(ServerMessage::QueryError {
                    query_id,
                    error: PolyDBError::new(
                        codes::INVALID_PARAM,
                        "query requires hello handshake first or explicit connection_id",
                    ),
                });
                return;
            }
        };

        let exists = self
            .app
            .get_connection_info(conn_id)
            .map(|o| o.is_some())
            .unwrap_or(false);
        if !exists {
            let _ = self.send(ServerMessage::QueryError {
                query_id,
                error: PolyDBError::new(
                    codes::CONNECTION_NOT_FOUND,
                    format!("connection not found: {conn_id}"),
                ),
            });
            return;
        }

        // behavior.md §6/§12：同一连接可并发多个查询。注册表满时如实拒绝，
        // 不让失控客户端把 goroutine 堆满。
        let Some(cancel) = register_in_flight(&self.in_flight, query_id) else {
            let _ = self.send(ServerMessage::QueryError {
                query_id,
                error: PolyDBError::new(
                    codes::INVALID_PARAM,
                    format!("too many concurrent queries (max {WS_MAX_IN_FLIGHT})"),
                ),
            });
            return;
        };

        let _ = self.send(ServerMessage::QueryStarted { query_id });

        let app = Arc::clone(&self.app);
        let tx = self.tx.clone();
        let sql = request.sql.clone();
        let params = request.params.clone();
        let in_flight = Arc::clone(&self.in_flight);
        // §2.3：timeout_ms>0 生效，超时回 POLYDB_ERR_TIMEOUT。此前 WS 路径
        // 完全忽略该字段，一条慢查询永不返回。
        let timeout = request
            .timeout_ms
            .filter(|ms| *ms > 0)
            .map(Duration::from_millis);
        let max_rows = crate::query_row_limit(&request);

        tokio::spawn(async move {
            let outcome = tokio::select! {
                r = app.execute(conn_id, &sql, &params) => Some(r),
                _ = cancel.notified() => None,
                _ = async {
                    match timeout {
                        Some(d) => tokio::time::sleep(d).await,
                        None => std::future::pending().await,
                    }
                } => {
                    retire_in_flight(&in_flight, query_id);
                    let _ = tx.send(ServerMessage::QueryError {
                        query_id,
                        error: PolyDBError::new(
                            codes::TIMEOUT,
                            format!("query timed out: {query_id}"),
                        )
                        .retryable(),
                    });
                    return;
                }
            };
            // 出口注销：查询自然完成也必须离开注册表。
            retire_in_flight(&in_flight, query_id);
            match outcome {
                Some(Ok(result)) => {
                    // 与 HTTP 同一限行（§5）：无上限的 SELECT 会在序列化进
                    // socket 前先打爆内存。max_rows 帧字段与 REST 参数同义。
                    let _ = tx.send(ServerMessage::QueryResult {
                        query_id,
                        result: crate::apply_max_rows(result, max_rows),
                    });
                }
                Some(Err(e)) => {
                    let _ = tx.send(ServerMessage::QueryError {
                        query_id,
                        error: e.to_protocol(),
                    });
                }
                None => {
                    let _ = tx.send(ServerMessage::QueryCancelled {
                        query_id,
                        rows_returned: None,
                    });
                }
            }
        });
    }

    fn handle_cancel(&mut self, query_id: Uuid) {
        if let Some(cancel) = self
            .in_flight
            .lock()
            .expect("in_flight poisoned")
            .remove(&query_id)
        {
            cancel.notify_one();
            return;
        }
        let _ = self.send(ServerMessage::QueryError {
            query_id,
            error: PolyDBError::new(codes::INVALID_PARAM, format!("query not found: {query_id}")),
        });
    }

    fn cancel_all(&mut self) {
        for (_, cancel) in self.in_flight.lock().expect("in_flight poisoned").drain() {
            cancel.notify_one();
        }
    }
}

fn encode(v: &ServerMessage) -> Result<Vec<u8>, Box<dyn std::error::Error + Send + Sync>> {
    let mut buf = Vec::new();
    v.serialize(
        &mut rmp_serde::Serializer::new(&mut buf)
            .with_struct_map()
            .with_human_readable(),
    )?;
    Ok(buf)
}

fn decode<T: serde::de::DeserializeOwned>(bytes: &[u8]) -> Result<T, String> {
    let mut de = rmp_serde::Deserializer::new(bytes).with_human_readable();
    T::deserialize(&mut de).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderMap;

    fn headers(origin: Option<&str>, host: Option<&str>) -> HeaderMap {
        let mut h = HeaderMap::new();
        if let Some(o) = origin {
            h.insert(header::ORIGIN, o.parse().unwrap());
        }
        if let Some(hv) = host {
            h.insert(header::HOST, hv.parse().unwrap());
        }
        h
    }

    #[test]
    fn origin_absent_is_allowed() {
        assert!(check_origin(&headers(None, Some("127.0.0.1:8080"))));
    }

    #[test]
    fn same_origin_is_allowed() {
        assert!(check_origin(&headers(
            Some("http://127.0.0.1:8080"),
            Some("127.0.0.1:8080")
        )));
    }

    #[test]
    fn cross_origin_is_rejected() {
        assert!(!check_origin(&headers(
            Some("https://evil.example.com"),
            Some("127.0.0.1:8080")
        )));
    }

    #[test]
    fn malformed_origin_is_rejected() {
        assert!(!check_origin(&headers(
            Some("not-a-url"),
            Some("127.0.0.1:8080")
        )));
        assert!(!check_origin(&headers(
            Some("https://"),
            Some("127.0.0.1:8080")
        )));
    }

    #[test]
    fn origin_host_strips_scheme_and_suffixes() {
        assert_eq!(origin_host("http://127.0.0.1:8080"), Some("127.0.0.1:8080"));
        assert_eq!(origin_host("https://example.com"), Some("example.com"));
        assert_eq!(origin_host("example.com"), Some("example.com"));
        assert_eq!(origin_host("https://"), None);
    }
}
