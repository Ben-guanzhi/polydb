use std::sync::Arc;

use chrono::{DateTime, Utc};
use parking_lot::Mutex;
use polydb_core::{
    ConnectionId, ConnectionInfo, CoreError, CoreResult, CreateConnectionRequest, DatabaseKind,
    UpdateConnectionRequest,
};
use rusqlite::Connection as RusqliteConnection;

pub struct ConnectionRepository {
    conn: Arc<Mutex<RusqliteConnection>>,
}

impl ConnectionRepository {
    pub(crate) fn new(conn: Arc<Mutex<RusqliteConnection>>) -> Self {
        Self { conn }
    }

    pub fn create(&self, req: &CreateConnectionRequest) -> CoreResult<ConnectionInfo> {
        let conn = self.conn.lock();
        let id = ConnectionId::new_v4();
        let now = Utc::now();
        let options_json = serde_json::to_string(&req.options)
            .map_err(|e| CoreError::Storage(format!("serialize options: {e}")))?;
        let ssh_json = req
            .ssh_tunnel
            .as_ref()
            .map(serde_json::to_string)
            .transpose()
            .map_err(|e| CoreError::Storage(format!("serialize ssh: {e}")))?;
        let kind_str = req.kind.to_string();

        conn.execute(
            "INSERT INTO connections (id, name, kind, host, port, database, username, password_ref, options, ssh_tunnel, default_schema, read_only, created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)",
            rusqlite::params![
                id.to_string(),
                req.name,
                kind_str,
                req.host,
                req.port.map(|p| p as i64),
                req.database,
                req.username,
                req.password_ref,
                options_json,
                ssh_json,
                req.default_schema,
                req.read_only.unwrap_or(false),
                now.to_rfc3339(),
                now.to_rfc3339(),
            ],
        ).map_err(|e| CoreError::Storage(format!("insert connection: {e}")))?;

        Ok(ConnectionInfo {
            id,
            name: req.name.clone(),
            kind: req.kind,
            host: req.host.clone(),
            port: req.port,
            database: req.database.clone(),
            username: req.username.clone(),
            options: req.options.clone(),
            ssh_tunnel: req.ssh_tunnel.clone(),
            default_schema: req.default_schema.clone(),
            read_only: req.read_only,
            created_at: now,
            updated_at: now,
        })
    }

    pub fn list(&self) -> CoreResult<Vec<ConnectionInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn.prepare(
            "SELECT id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, created_at, updated_at FROM connections ORDER BY created_at"
        ).map_err(|e| CoreError::Storage(format!("list connections prepare: {e}")))?;

        let rows = stmt
            .query_map([], |row| {
                let id_str: String = row.get(0)?;
                let name: String = row.get(1)?;
                let kind_str: String = row.get(2)?;
                let host: Option<String> = row.get(3)?;
                let port: Option<i64> = row.get(4)?;
                let database: Option<String> = row.get(5)?;
                let username: Option<String> = row.get(6)?;
                let options_json: String = row.get(7)?;
                let ssh_json: Option<String> = row.get(8)?;
                let default_schema: Option<String> = row.get(9)?;
                let read_only: i64 = row.get(10)?;
                let created_at_str: String = row.get(11)?;
                let updated_at_str: String = row.get(12)?;
                Ok((
                    id_str,
                    name,
                    kind_str,
                    host,
                    port,
                    database,
                    username,
                    options_json,
                    ssh_json,
                    default_schema,
                    read_only,
                    created_at_str,
                    updated_at_str,
                ))
            })
            .map_err(|e| CoreError::Storage(format!("list connections query: {e}")))?;

        let mut result = Vec::new();
        for row in rows {
            let (
                id_str,
                name,
                kind_str,
                host,
                port,
                database,
                username,
                options_json,
                ssh_json,
                default_schema,
                read_only,
                created_at_str,
                updated_at_str,
            ) = row.map_err(|e| CoreError::Storage(format!("row: {e}")))?;

            let id: ConnectionId = id_str
                .parse()
                .map_err(|e| CoreError::Storage(format!("parse id: {e}")))?;
            let kind = parse_db_kind(&kind_str)?;
            let options: std::collections::HashMap<String, String> =
                serde_json::from_str(&options_json).unwrap_or_default();
            let ssh_tunnel = ssh_json
                .as_deref()
                .map(serde_json::from_str)
                .transpose()
                .map_err(|e| CoreError::Storage(format!("parse ssh: {e}")))?;
            let created_at: DateTime<Utc> = created_at_str
                .parse()
                .map_err(|e| CoreError::Storage(format!("parse created_at: {e}")))?;
            let updated_at: DateTime<Utc> = updated_at_str
                .parse()
                .map_err(|e| CoreError::Storage(format!("parse updated_at: {e}")))?;

            result.push(ConnectionInfo {
                id,
                name,
                kind,
                host,
                port: port.map(|p| p as u16),
                database,
                username,
                options,
                ssh_tunnel,
                default_schema,
                read_only: bool_from_stored(read_only),
                created_at,
                updated_at,
            });
        }
        Ok(result)
    }

    pub fn get(&self, id: ConnectionId) -> CoreResult<Option<ConnectionInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn.prepare(
            "SELECT id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, created_at, updated_at FROM connections WHERE id = ?1"
        ).map_err(|e| CoreError::Storage(format!("get connection prepare: {e}")))?;

        let result = stmt.query_row(rusqlite::params![id.to_string()], |row| {
            let id_str: String = row.get(0)?;
            let name: String = row.get(1)?;
            let kind_str: String = row.get(2)?;
            let host: Option<String> = row.get(3)?;
            let port: Option<i64> = row.get(4)?;
            let database: Option<String> = row.get(5)?;
            let username: Option<String> = row.get(6)?;
            let options_json: String = row.get(7)?;
            let ssh_json: Option<String> = row.get(8)?;
            let default_schema: Option<String> = row.get(9)?;
            let read_only: i64 = row.get(10)?;
            let created_at_str: String = row.get(11)?;
            let updated_at_str: String = row.get(12)?;
            Ok((
                id_str,
                name,
                kind_str,
                host,
                port,
                database,
                username,
                options_json,
                ssh_json,
                default_schema,
                read_only,
                created_at_str,
                updated_at_str,
            ))
        });

        match result {
            Ok((
                id_str,
                name,
                kind_str,
                host,
                port,
                database,
                username,
                options_json,
                ssh_json,
                default_schema,
                read_only,
                created_at_str,
                updated_at_str,
            )) => {
                let id: ConnectionId = id_str
                    .parse()
                    .map_err(|e| CoreError::Storage(format!("parse id: {e}")))?;
                let kind = parse_db_kind(&kind_str)?;
                let options: std::collections::HashMap<String, String> =
                    serde_json::from_str(&options_json).unwrap_or_default();
                let ssh_tunnel = ssh_json
                    .as_deref()
                    .map(serde_json::from_str)
                    .transpose()
                    .map_err(|e| CoreError::Storage(format!("parse ssh: {e}")))?;
                let created_at: DateTime<Utc> = created_at_str
                    .parse()
                    .map_err(|e| CoreError::Storage(format!("parse created_at: {e}")))?;
                let updated_at: DateTime<Utc> = updated_at_str
                    .parse()
                    .map_err(|e| CoreError::Storage(format!("parse updated_at: {e}")))?;

                Ok(Some(ConnectionInfo {
                    id,
                    name,
                    kind,
                    host,
                    port: port.map(|p| p as u16),
                    database,
                    username,
                    options,
                    ssh_tunnel,
                    default_schema,
                    read_only: bool_from_stored(read_only),
                    created_at,
                    updated_at,
                }))
            }
            Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
            Err(e) => Err(CoreError::Storage(format!("get connection: {e}"))),
        }
    }

    pub fn update(
        &self,
        id: ConnectionId,
        req: &UpdateConnectionRequest,
    ) -> CoreResult<Option<ConnectionInfo>> {
        let existing = self.get(id)?;
        let Some(existing) = existing else {
            return Ok(None);
        };

        let now = Utc::now();

        // password_ref 不随 ConnectionInfo 下发：更新请求未带新 ref 时保留库中已有 ref
        // （与 Go 侧 repo.Update 对齐，避免改名/改 host 等更新把密码引用清空）。
        // 必须在获取锁之前查询：get_password_ref 需要同一把锁，而 parking_lot 不可重入，
        // 持锁时调用会自死锁。
        let password_ref = match req.password_ref.as_ref() {
            Some(r) => Some(r.clone()),
            None => self.get_password_ref(id)?,
        };

        let conn = self.conn.lock();

        let name = req.name.as_deref().unwrap_or(&existing.name);
        let host = req.host.as_ref().or(existing.host.as_ref());
        let port = req.port.or(existing.port);
        let database = req.database.as_ref().or(existing.database.as_ref());
        let username = req.username.as_ref().or(existing.username.as_ref());
        let options = req.options.as_ref().unwrap_or(&existing.options);
        let ssh_tunnel = req.ssh_tunnel.as_ref().or(existing.ssh_tunnel.as_ref());
        let default_schema = req
            .default_schema
            .as_ref()
            .or(existing.default_schema.as_ref());
        let read_only = req.read_only.or(existing.read_only);

        let options_json = serde_json::to_string(options)
            .map_err(|e| CoreError::Storage(format!("serialize options: {e}")))?;
        let ssh_json = ssh_tunnel
            .map(serde_json::to_string)
            .transpose()
            .map_err(|e| CoreError::Storage(format!("serialize ssh: {e}")))?;

        conn.execute(
            "UPDATE connections SET name=?1, host=?2, port=?3, database=?4, username=?5, password_ref=?6, options=?7, ssh_tunnel=?8, default_schema=?9, read_only=?10, updated_at=?11 WHERE id=?12",
            rusqlite::params![
                name,
                host,
                port.map(|p| p as i64),
                database,
                username,
                password_ref.as_deref(),
                options_json,
                ssh_json,
                default_schema,
                read_only.unwrap_or(false),
                now.to_rfc3339(),
                id.to_string(),
            ],
        ).map_err(|e| CoreError::Storage(format!("update connection: {e}")))?;

        Ok(Some(ConnectionInfo {
            id,
            name: name.to_string(),
            kind: existing.kind,
            host: host.cloned(),
            port,
            database: database.cloned(),
            username: username.cloned(),
            options: options.clone(),
            ssh_tunnel: ssh_tunnel.cloned(),
            default_schema: default_schema.cloned(),
            read_only,
            created_at: existing.created_at,
            updated_at: now,
        }))
    }

    pub fn delete(&self, id: ConnectionId) -> CoreResult<bool> {
        let conn = self.conn.lock();
        let affected = conn
            .execute(
                "DELETE FROM connections WHERE id=?1",
                rusqlite::params![id.to_string()],
            )
            .map_err(|e| CoreError::Storage(format!("delete connection: {e}")))?;
        Ok(affected > 0)
    }

    /// 返回连接存储的 password_ref。该引用不随 ConnectionInfo 下发（红线），
    /// 仅 Connect 时单独查询，再到 keyring 取明文。
    pub fn get_password_ref(&self, id: ConnectionId) -> CoreResult<Option<String>> {
        let conn = self.conn.lock();
        match conn.query_row(
            "SELECT password_ref FROM connections WHERE id = ?1",
            rusqlite::params![id.to_string()],
            |row| row.get::<_, Option<String>>(0),
        ) {
            Ok(v) => Ok(v),
            Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
            Err(e) => Err(CoreError::Storage(format!("get password_ref: {e}"))),
        }
    }
}

fn parse_db_kind(s: &str) -> CoreResult<DatabaseKind> {
    match s {
        "sqlite" => Ok(DatabaseKind::Sqlite),
        "mysql" => Ok(DatabaseKind::MySql),
        "postgres" => Ok(DatabaseKind::Postgres),
        "mssql" => Ok(DatabaseKind::Mssql),
        "oracle" => Ok(DatabaseKind::Oracle),
        "redis" => Ok(DatabaseKind::Redis),
        _ => Err(CoreError::Storage(format!("unknown database kind: {s}"))),
    }
}

/// read_only 列以 0/1 存储；0 表示"未设置"（线上省略该字段），1 表示 true。
fn bool_from_stored(v: i64) -> Option<bool> {
    if v != 0 {
        Some(true)
    } else {
        None
    }
}
