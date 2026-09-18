//! /ws WebSocket 处理器。契约见 spec/asyncapi.yaml。

use std::collections::HashMap;
use std::sync::Arc;

use axum::extract::ws::{Message, WebSocket, WebSocketUpgrade};
use axum::extract::State;
use axum::response::Response;
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

pub async fn ws_upgrade(State(state): State<AppState>, ws: WebSocketUpgrade) -> Response {
    let app = state.app.clone();
    ws.on_upgrade(move |socket| session(app, socket))
}

async fn session(app: AppRef, socket: WebSocket) {
    let (mut ws_tx, mut ws_rx) = socket.split();
    let (tx, mut rx) = mpsc::unbounded_channel::<ServerMessage>();

    let mut session = Session {
        app,
        tx: tx.clone(),
        connection_id: None,
        in_flight: HashMap::new(),
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
        _ = receive_loop(&mut session, &mut ws_rx) => {
            drop(tx);
            let _ = send.await;
        }
    }

    session.cancel_all();
}

struct Session {
    app: AppRef,
    tx: mpsc::UnboundedSender<ServerMessage>,
    connection_id: Option<ConnectionId>,
    in_flight: HashMap<Uuid, Arc<Notify>>,
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
            ClientMessage::Hello { connection_id, .. } => {
                let ok = session.handle_hello(connection_id).await;
                if !ok {
                    return;
                }
            }
            ClientMessage::Query { query_id, request } => {
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

        let cancel = Arc::new(Notify::new());
        self.in_flight.insert(query_id, Arc::clone(&cancel));

        let _ = self.send(ServerMessage::QueryStarted { query_id });

        let app = Arc::clone(&self.app);
        let tx = self.tx.clone();
        let sql = request.sql.clone();
        let params = request.params.clone();

        tokio::spawn(async move {
            let cancel = cancel;
            let outcome = tokio::select! {
                r = app.execute(conn_id, &sql, &params) => Some(r),
                _ = cancel.notified() => None,
            };
            match outcome {
                Some(Ok(result)) => {
                    let _ = tx.send(ServerMessage::QueryResult { query_id, result });
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
        if let Some(cancel) = self.in_flight.remove(&query_id) {
            cancel.notify_one();
            return;
        }
        let _ = self.send(ServerMessage::QueryError {
            query_id,
            error: PolyDBError::new(codes::INVALID_PARAM, format!("query not found: {query_id}")),
        });
    }

    fn cancel_all(&mut self) {
        for (_, cancel) in self.in_flight.drain() {
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
