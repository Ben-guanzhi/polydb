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
            "INSERT INTO connections (id, name, kind, host, port, database, username, password_ref, options, ssh_tunnel, default_schema, read_only, \"group\", color, created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16)",
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
                req.group.as_deref(),
                req.color.as_deref(),
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
            group: req.group.clone(),
            color: req.color.clone(),
            created_at: now,
            updated_at: now,
        })
    }

    pub fn list(&self) -> CoreResult<Vec<ConnectionInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn.prepare(
            "SELECT id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, \"group\", color, created_at, updated_at FROM connections ORDER BY created_at"
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
                let group: Option<String> = row.get(11)?;
                let color: Option<String> = row.get(12)?;
                let created_at_str: String = row.get(13)?;
                let updated_at_str: String = row.get(14)?;
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
                    group,
                    color,
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
                group,
                color,
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
                group,
                color,
                created_at,
                updated_at,
            });
        }
        Ok(result)
    }

    pub fn get(&self, id: ConnectionId) -> CoreResult<Option<ConnectionInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn.prepare(
            "SELECT id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, \"group\", color, created_at, updated_at FROM connections WHERE id = ?1"
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
            let group: Option<String> = row.get(11)?;
            let color: Option<String> = row.get(12)?;
            let created_at_str: String = row.get(13)?;
            let updated_at_str: String = row.get(14)?;
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
                group,
                color,
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
                group,
                color,
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
                    group,
                    color,
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
        let conn = self.conn.lock();

        // F2 回归：get 与 UPDATE 必须在同一把锁内完成（原子 get-modify-write）。
        // 旧写法先 self.get(id)（获锁→读→释锁）、再 self.get_password_ref(id)
        // （获锁→读→释锁）、最后 conn.lock() 执行 UPDATE——两次读与写之间
        // 另一并发 update 可改行，UPDATE 用陈旧 existing 填充未提供字段，
        // 覆盖并发更新（丢失更新）。

        let now = Utc::now();

        // 读 existing row（与 get() 同一 SELECT，直接用已持有的 conn）
        let existing_row = {
            let mut stmt = conn
                .prepare(
                    "SELECT id, name, kind, host, port, database, username, options, ssh_tunnel, default_schema, read_only, \"group\", color, created_at, updated_at FROM connections WHERE id = ?1",
                )
                .map_err(|e| CoreError::Storage(format!("get connection prepare: {e}")))?;

            match stmt.query_row(rusqlite::params![id.to_string()], |row| {
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
                let group: Option<String> = row.get(11)?;
                let color: Option<String> = row.get(12)?;
                let created_at_str: String = row.get(13)?;
                let updated_at_str: String = row.get(14)?;
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
                    group,
                    color,
                    created_at_str,
                    updated_at_str,
                ))
            }) {
                Ok(row) => Some(row),
                Err(rusqlite::Error::QueryReturnedNoRows) => None,
                Err(e) => return Err(CoreError::Storage(format!("get connection: {e}"))),
            }
        };

        let Some(existing_row) = existing_row else {
            return Ok(None);
        };

        let (
            _id_str,
            name_existing,
            kind_str,
            host_existing,
            port_existing,
            database_existing,
            username_existing,
            options_json_existing,
            ssh_json_existing,
            default_schema_existing,
            read_only_stored,
            group_existing,
            color_existing,
            created_at_str,
            _updated_at_str,
        ) = existing_row;

        let kind = parse_db_kind(&kind_str)?;
        let options_existing: std::collections::HashMap<String, String> =
            serde_json::from_str(&options_json_existing).unwrap_or_default();
        let ssh_tunnel_existing = ssh_json_existing
            .as_deref()
            .map(serde_json::from_str)
            .transpose()
            .map_err(|e| CoreError::Storage(format!("parse ssh: {e}")))?;
        let created_at: DateTime<Utc> = created_at_str
            .parse()
            .map_err(|e| CoreError::Storage(format!("parse created_at: {e}")))?;

        // password_ref 在同一把锁内读取
        let password_ref = match req.password_ref.as_ref() {
            Some(r) => Some(r.clone()),
            None => match conn.query_row(
                "SELECT password_ref FROM connections WHERE id = ?1",
                rusqlite::params![id.to_string()],
                |row| row.get::<_, Option<String>>(0),
            ) {
                Ok(v) => v,
                Err(rusqlite::Error::QueryReturnedNoRows) => None,
                Err(e) => return Err(CoreError::Storage(format!("get password_ref: {e}"))),
            },
        };

        // 用锁内读到的新鲜数据填充未提供字段
        let name = req.name.as_deref().unwrap_or(&name_existing);
        let host = req.host.as_ref().or(host_existing.as_ref());
        let port = req.port.or(port_existing.map(|p| p as u16));
        let database = req.database.as_ref().or(database_existing.as_ref());
        let username = req.username.as_ref().or(username_existing.as_ref());
        let options = req.options.as_ref().unwrap_or(&options_existing);
        let ssh_tunnel = req.ssh_tunnel.as_ref().or(ssh_tunnel_existing.as_ref());
        let default_schema = req
            .default_schema
            .as_ref()
            .or(default_schema_existing.as_ref());
        let read_only = req.read_only.or(bool_from_stored(read_only_stored));
        let group = req.group.clone().or(group_existing.clone());
        let color = req.color.clone().or(color_existing.clone());

        let options_json = serde_json::to_string(options)
            .map_err(|e| CoreError::Storage(format!("serialize options: {e}")))?;
        let ssh_json = ssh_tunnel
            .map(serde_json::to_string)
            .transpose()
            .map_err(|e| CoreError::Storage(format!("serialize ssh: {e}")))?;

        let affected = conn.execute(
            "UPDATE connections SET name=?1, host=?2, port=?3, database=?4, username=?5, password_ref=?6, options=?7, ssh_tunnel=?8, default_schema=?9, read_only=?10, \"group\"=?11, color=?12, updated_at=?13 WHERE id=?14",
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
                group.as_deref(),
                color.as_deref(),
                now.to_rfc3339(),
                id.to_string(),
            ],
        )
        .map_err(|e| CoreError::Storage(format!("update connection: {e}")))?;

        if affected == 0 {
            return Ok(None);
        }

        Ok(Some(ConnectionInfo {
            id,
            name: name.to_string(),
            kind,
            host: host.cloned(),
            port,
            database: database.cloned(),
            username: username.cloned(),
            options: options.clone(),
            ssh_tunnel: ssh_tunnel.cloned(),
            default_schema: default_schema.cloned(),
            read_only,
            group,
            color,
            created_at,
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

#[cfg(test)]
mod tests {
    use super::*;
    use parking_lot::Mutex;
    use polydb_protocol::CreateConnectionRequest;
    use rusqlite::Connection as RusqliteConnection;
    use std::sync::Arc;

    fn test_repo() -> ConnectionRepository {
        let conn = RusqliteConnection::open_in_memory().unwrap();
        conn.execute_batch(
            "CREATE TABLE connections (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                kind TEXT NOT NULL,
                host TEXT,
                port INTEGER,
                database TEXT,
                username TEXT,
                password_ref TEXT,
                options TEXT NOT NULL DEFAULT '{}',
                ssh_tunnel TEXT,
                default_schema TEXT,
                read_only INTEGER NOT NULL DEFAULT 0,
                \"group\" TEXT,
                \"color\" TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );",
        )
        .unwrap();
        ConnectionRepository::new(Arc::new(Mutex::new(conn)))
    }

    fn req(name: &str) -> CreateConnectionRequest {
        CreateConnectionRequest {
            name: name.to_string(),
            kind: DatabaseKind::Sqlite,
            host: None,
            port: None,
            database: Some("/tmp/test.db".to_string()),
            username: None,
            password_ref: None,
            password: None,
            options: std::collections::HashMap::new(),
            ssh_tunnel: None,
            default_schema: None,
            read_only: None,
            group: None,
            color: None,
        }
    }

    #[test]
    fn create_and_get() {
        let repo = test_repo();
        let info = repo.create(&req("my-connection")).unwrap();
        assert_eq!(info.name, "my-connection");
        assert_eq!(info.kind, DatabaseKind::Sqlite);

        let fetched = repo.get(info.id).unwrap().unwrap();
        assert_eq!(fetched.name, "my-connection");
        assert_eq!(fetched.id, info.id);
    }

    #[test]
    fn get_nonexistent_returns_none() {
        let repo = test_repo();
        let id = uuid::Uuid::new_v4();
        assert!(repo.get(id).unwrap().is_none());
    }

    #[test]
    fn list_empty() {
        let repo = test_repo();
        let list = repo.list().unwrap();
        assert!(list.is_empty());
    }

    #[test]
    fn list_multiple() {
        let repo = test_repo();
        repo.create(&req("a")).unwrap();
        repo.create(&req("b")).unwrap();
        repo.create(&req("c")).unwrap();
        let list = repo.list().unwrap();
        assert_eq!(list.len(), 3);
    }

    #[test]
    fn delete_existing() {
        let repo = test_repo();
        let info = repo.create(&req("to-delete")).unwrap();
        assert!(repo.delete(info.id).unwrap());
        assert!(repo.get(info.id).unwrap().is_none());
    }

    #[test]
    fn delete_nonexistent() {
        let repo = test_repo();
        let id = uuid::Uuid::new_v4();
        assert!(!repo.delete(id).unwrap());
    }

    #[test]
    fn update_partial() {
        let repo = test_repo();
        let info = repo.create(&req("original")).unwrap();

        let update = polydb_protocol::UpdateConnectionRequest {
            name: Some("renamed".to_string()),
            ..Default::default()
        };
        let updated = repo.update(info.id, &update).unwrap().unwrap();
        assert_eq!(updated.name, "renamed");
        assert_eq!(updated.database, info.database);
    }

    #[test]
    fn update_nonexistent_returns_none() {
        let repo = test_repo();
        let id = uuid::Uuid::new_v4();
        let update = polydb_protocol::UpdateConnectionRequest {
            name: Some("new".to_string()),
            ..Default::default()
        };
        assert!(repo.update(id, &update).unwrap().is_none());
    }

    // F3 回归：get 与 UPDATE 之间行被删掉时，UPDATE 影响 0 行，
    // update 必须返回 None 而非 Some(基于已删行的内存对象)。
    // 用 BEFORE UPDATE 触发器模拟这个竞态：get 看到行存在，
    // 触发器在 UPDATE 前删除该行，UPDATE 影响 0 行。
    #[test]
    fn update_after_delete_returns_none() {
        let repo = test_repo();
        let info = repo.create(&req("to-update")).unwrap();

        // 触发器在 UPDATE 前删除该行——模拟并发删除
        {
            let conn = repo.conn.lock();
            conn.execute_batch(
                "CREATE TRIGGER delete_before_update BEFORE UPDATE ON connections
                 BEGIN
                     DELETE FROM connections WHERE id = NEW.id;
                 END;",
            )
            .unwrap();
        }

        let update = polydb_protocol::UpdateConnectionRequest {
            name: Some("renamed".to_string()),
            ..Default::default()
        };
        let result = repo.update(info.id, &update).unwrap();
        assert!(
            result.is_none(),
            "update-after-delete 应返回 None（UPDATE 影响 0 行），但返回了 Some"
        );
    }

    // F2 回归：并发 update 不能丢失更新。
    // 旧写法 get(id) 与 UPDATE 不在同一把锁内——两个并发 update 各自
    // 读到同一旧行，UPDATE 用陈旧数据填充未提供字段，后执行的 UPDATE
    // 覆盖先执行的修改（丢失更新）。
    #[test]
    fn concurrent_updates_do_not_lose_changes() {
        let repo = std::sync::Arc::new(test_repo());
        let info = repo.create(&req("original")).unwrap();

        // 线程 A：改 name="renamed"（不提供 color）
        // 线程 B：改 color="blue"（不提供 name）
        // 修复后：两个修改都保留。修复前：取决于执行顺序，
        // 后执行的 UPDATE 用陈旧 existing 覆盖先执行的修改。
        let repo_a = std::sync::Arc::clone(&repo);
        let repo_b = std::sync::Arc::clone(&repo);
        let id = info.id;

        let handle_a = std::thread::spawn(move || {
            let update = polydb_protocol::UpdateConnectionRequest {
                name: Some("renamed".to_string()),
                ..Default::default()
            };
            repo_a.update(id, &update).unwrap();
        });

        let handle_b = std::thread::spawn(move || {
            let update = polydb_protocol::UpdateConnectionRequest {
                color: Some("blue".to_string()),
                ..Default::default()
            };
            repo_b.update(id, &update).unwrap();
        });

        handle_a.join().unwrap();
        handle_b.join().unwrap();

        let final_info = repo.get(info.id).unwrap().unwrap();
        assert_eq!(
            final_info.name, "renamed",
            "线程 A 的 name 修改不能被线程 B 覆盖（丢失更新）"
        );
        assert_eq!(
            final_info.color.as_deref(),
            Some("blue"),
            "线程 B 的 color 修改不能被线程 A 覆盖（丢失更新）"
        );
    }

    #[test]
    fn options_roundtrip() {
        let repo = test_repo();
        let mut r = req("with-options");
        let mut opts = std::collections::HashMap::new();
        opts.insert("sslmode".to_string(), "require".to_string());
        opts.insert("pool_size".to_string(), "10".to_string());
        r.options = opts;
        let info = repo.create(&r).unwrap();

        let fetched = repo.get(info.id).unwrap().unwrap();
        assert_eq!(fetched.options.get("sslmode"), Some(&"require".to_string()));
        assert_eq!(fetched.options.get("pool_size"), Some(&"10".to_string()));
    }

    #[test]
    fn read_only_roundtrip() {
        let repo = test_repo();
        let mut r = req("readonly");
        r.read_only = Some(true);
        let info = repo.create(&r).unwrap();

        let fetched = repo.get(info.id).unwrap().unwrap();
        assert_eq!(fetched.read_only, Some(true));
    }

    #[test]
    fn read_only_default_none() {
        let repo = test_repo();
        let info = repo.create(&req("normal")).unwrap();
        let fetched = repo.get(info.id).unwrap().unwrap();
        assert_eq!(fetched.read_only, None);
    }

    #[test]
    fn ssh_tunnel_roundtrip() {
        let repo = test_repo();
        let mut r = req("with-ssh");
        r.ssh_tunnel = Some(polydb_protocol::common::SshTunnelConfig {
            host: "jump.example.com".to_string(),
            port: 2222,
            username: "admin".to_string(),
            password_ref: None,
            password: None,
            private_key_path: Some("/path/key".to_string()),
            private_key_passphrase_ref: None,
            private_key_passphrase: None,
        });
        let info = repo.create(&r).unwrap();

        let fetched = repo.get(info.id).unwrap().unwrap();
        let ssh = fetched.ssh_tunnel.as_ref().unwrap();
        assert_eq!(ssh.host, "jump.example.com");
        assert_eq!(ssh.port, 2222);
        assert_eq!(ssh.username, "admin");
    }

    #[test]
    fn group_and_color_roundtrip() {
        let repo = test_repo();
        let mut r = req("grouped");
        r.group = Some("work".to_string());
        r.color = Some("#FF0000".to_string());
        let info = repo.create(&r).unwrap();

        let fetched = repo.get(info.id).unwrap().unwrap();
        assert_eq!(fetched.group, Some("work".to_string()));
        assert_eq!(fetched.color, Some("#FF0000".to_string()));
    }

    #[test]
    fn all_database_kinds_roundtrip() {
        let repo = test_repo();
        for (i, kind) in [
            DatabaseKind::Sqlite,
            DatabaseKind::MySql,
            DatabaseKind::Postgres,
            DatabaseKind::Mssql,
            DatabaseKind::Oracle,
            DatabaseKind::Redis,
        ]
        .iter()
        .enumerate()
        {
            let mut r = req(&format!("conn-{i}"));
            r.kind = *kind;
            let info = repo.create(&r).unwrap();
            let fetched = repo.get(info.id).unwrap().unwrap();
            assert_eq!(fetched.kind, *kind);
        }
    }

    #[test]
    fn bool_from_stored_values() {
        assert_eq!(bool_from_stored(0), None);
        assert_eq!(bool_from_stored(1), Some(true));
        assert_eq!(bool_from_stored(42), Some(true));
    }
}
