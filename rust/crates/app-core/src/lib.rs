use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use chrono::{DateTime, Utc};
use parking_lot::{Mutex, RwLock};
use polydb_core::{
    BeginTransactionRequest, ColumnInfo, ConnectionId, ConnectionInfo, ConnectionStatus, CoreError,
    CoreResult, CreateConnectionRequest, DatabaseKind, ForeignKeyInfo, IndexInfo, IsolationLevel,
    QueryResult, RedisKeyType, RedisReply, RedisScanPage, RedisValue, SchemaInfo, TableInfo,
    TableRowsRequest, TableRowsResult, TransactionInfo, TransactionStatus, UpdateConnectionRequest,
    Value,
};
use polydb_db_core::{Connection, DatabaseDriver, SqlDriver, TxMode};
use polydb_protocol::common::SshTunnelConfig;
use polydb_storage::{keyring_ref, FileKeyring, Keyring, Storage};

mod sshtunnel;

use sshtunnel::Tunnel;

/// 活动事务条目。driver 是通过 `Connection::sql_driver_arc()` 得到的共享 Arc，
/// 与 Connection 内的驱动共享底层池/连接；handle 由具体驱动内部决定形态
/// （sqlx::Transaction / 隐式事务标记结构等），以 `Box<dyn Any + Send>` 承载，
/// 由 SqlDriver::execute_in_tx / commit / rollback 负责 downcast。
struct TxEntry {
    id: uuid::Uuid,
    connection_id: ConnectionId,
    driver: Arc<dyn SqlDriver>,
    handle: Option<Box<dyn Any + Send>>,
    isolation_level: IsolationLevel,
    started_at: DateTime<Utc>,
}

impl TxEntry {
    fn info(&self) -> TransactionInfo {
        TransactionInfo {
            id: self.id,
            connection_id: self.connection_id,
            status: TransactionStatus::Active,
            started_at: self.started_at,
            isolation_level: Some(self.isolation_level),
        }
    }

    fn info_finalized(&self, status: TransactionStatus) -> TransactionInfo {
        TransactionInfo {
            id: self.id,
            connection_id: self.connection_id,
            status,
            started_at: self.started_at,
            isolation_level: Some(self.isolation_level),
        }
    }
}

pub struct AppCore {
    storage: Storage,
    kr: Arc<dyn Keyring>,
    connections: RwLock<HashMap<ConnectionId, Connection>>,
    tunnels: RwLock<HashMap<ConnectionId, Arc<Tunnel>>>,
    txs: Mutex<HashMap<String, TxEntry>>,
    // 已打开连接的只读标志快照（behavior.md §12.3）；disconnect 时移除。
    read_only: RwLock<std::collections::HashSet<ConnectionId>>,
}

impl AppCore {
    /// 从环境变量构造 keyring（POLYDB_MASTER_PASSWORD / POLYDB_DATA_DIR），
    /// 失败即 panic（与 Go 侧 log.Fatal 对齐：主密码错误属启动期致命错误）。
    pub fn new(storage: Storage) -> Self {
        if std::env::var("POLYDB_KEYRING").as_deref() == Ok("os") {
            tracing::warn!(
                "POLYDB_KEYRING=os is not supported by the Rust build; using file backend"
            );
        }
        let master = std::env::var("POLYDB_MASTER_PASSWORD").unwrap_or_default();
        let data_dir = data_dir();
        let kr: Arc<dyn Keyring> = match FileKeyring::open(&data_dir, &master) {
            Ok(k) => Arc::new(k),
            Err(e) => panic!("open keyring {data_dir} failed: {e}"),
        };
        Self::with_keyring(storage, kr)
    }

    /// 显式指定 keyring（测试用）。
    pub fn with_keyring(storage: Storage, kr: Arc<dyn Keyring>) -> Self {
        Self {
            storage,
            kr,
            connections: RwLock::new(HashMap::new()),
            tunnels: RwLock::new(HashMap::new()),
            txs: Mutex::new(HashMap::new()),
            read_only: RwLock::new(std::collections::HashSet::new()),
        }
    }

    fn get_conn(&self, id: ConnectionId) -> CoreResult<Connection> {
        self.connections
            .read()
            .get(&id)
            .cloned()
            .ok_or_else(|| CoreError::ConnectionNotFound(id.to_string()))
    }

    // ensure_connected：懒连接。查询/元数据入口必须先调用，保证未显式 connect 也能工作。
    fn ensure_connected(&self, id: &ConnectionId) -> CoreResult<()> {
        if self.connections.read().contains_key(id) {
            return Ok(());
        }
        self.connect(*id)
    }

    pub fn create_connection(&self, req: &CreateConnectionRequest) -> CoreResult<ConnectionInfo> {
        let mut req = req.clone();
        if let Some(pw) = req.password.take() {
            self.store_conn_secrets(&pw, &mut req.password_ref, "conn")?;
        }
        self.store_ssh_secrets(&mut req.ssh_tunnel)?;
        self.storage.connections().create(&req)
    }

    pub fn list_connections(&self) -> CoreResult<Vec<ConnectionInfo>> {
        self.storage.connections().list()
    }

    pub fn get_connection_info(&self, id: ConnectionId) -> CoreResult<Option<ConnectionInfo>> {
        self.storage.connections().get(id)
    }

    pub fn update_connection(
        &self,
        id: ConnectionId,
        req: &UpdateConnectionRequest,
    ) -> CoreResult<Option<ConnectionInfo>> {
        let mut req = req.clone();
        if let Some(pw) = req.password.take() {
            if !pw.is_empty() {
                self.store_conn_secrets(&pw, &mut req.password_ref, "conn")?;
            }
        }
        self.store_ssh_secrets_update(id, &mut req.ssh_tunnel)?;
        self.storage.connections().update(id, &req)
    }

    pub fn delete_connection(&self, id: ConnectionId) -> CoreResult<bool> {
        self.disconnect(id);
        self.storage.connections().delete(id)
    }

    pub fn connect(&self, id: ConnectionId) -> CoreResult<()> {
        let info = self
            .storage
            .connections()
            .get(id)?
            .ok_or_else(|| CoreError::ConnectionNotFound(id.to_string()))?;

        // password_ref 不随 ConnectionInfo 下发（红线）：单独查询后到 keyring 取明文。
        let stored_ref = self.storage.connections().get_password_ref(id)?;
        let password = self.secret(stored_ref.as_deref().unwrap_or(""))?;

        // SSH 隧道：本地端口转发后把目标地址重写为隧道本地地址（SQLite 忽略隧道）。
        let mut conn_info = info.clone();
        let mut tunnel: Option<Arc<Tunnel>> = None;
        if let Some(cfg) = &info.ssh_tunnel {
            if info.kind != DatabaseKind::Sqlite {
                let ssh_pwd = self.secret(cfg.password_ref.as_deref().unwrap_or(""))?;
                let passphrase =
                    self.secret(cfg.private_key_passphrase_ref.as_deref().unwrap_or(""))?;
                let (target_host, target_port) = target_host_port(&info);
                // SSH 主机密钥 known_hosts TOFU 校验（与 Go 侧 SetKnownHostsPath 对齐）：
                // 条目落在数据目录；首次使用自动记录，主机密钥变更即拒绝。
                let known_hosts = std::path::Path::new(&data_dir()).join("known_hosts");
                let t = Tunnel::open(
                    cfg,
                    &target_host,
                    target_port,
                    &ssh_pwd,
                    &passphrase,
                    Some(&known_hosts),
                )?;
                let (host, port) = split_local_addr(t.local_addr())?;
                conn_info.host = Some(host);
                conn_info.port = Some(port);
                tunnel = Some(Arc::new(t));
            }
        }

        let driver = match self.open_driver(&conn_info, &info, &password) {
            Ok(d) => d,
            Err(e) => {
                if let Some(t) = &tunnel {
                    t.close();
                }
                return Err(e);
            }
        };

        self.connections.write().insert(id, Connection::new(driver));
        if info.read_only == Some(true) {
            self.read_only.write().insert(id);
        } else {
            self.read_only.write().remove(&id);
        }
        if let Some(t) = tunnel {
            self.tunnels.write().insert(id, t);
        }
        Ok(())
    }

    /// 只读判定（behavior.md §12.3）：优先取已打开连接的快照；
    /// 未连接时回读存储配置，保证懒连接路径也能被拦截。
    fn is_read_only(&self, id: &ConnectionId) -> bool {
        if self.read_only.read().contains(id) {
            return true;
        }
        if self.connections.read().contains_key(id) {
            return false;
        }
        self.storage
            .connections()
            .get(*id)
            .ok()
            .flatten()
            .and_then(|info| info.read_only)
            .unwrap_or(false)
    }

    /// 只读连接上的写语句拦截：insert/update/delete/ddl 返回 POLYDB_ERR_READ_ONLY。
    fn reject_write_on_read_only(&self, id: &ConnectionId, sql: &str) -> CoreResult<()> {
        use polydb_db_core::detect_statement_type;
        use polydb_protocol::StatementType;
        if self.is_read_only(id)
            && matches!(
                detect_statement_type(sql),
                StatementType::Insert
                    | StatementType::Update
                    | StatementType::Delete
                    | StatementType::Ddl
            )
        {
            return Err(polydb_protocol::PolyDBError::new(
                polydb_protocol::error::codes::READ_ONLY,
                "connection is read-only: write statements are rejected",
            )
            .into());
        }
        Ok(())
    }

    fn open_driver(
        &self,
        conn_info: &ConnectionInfo,
        orig: &ConnectionInfo,
        password: &str,
    ) -> CoreResult<Arc<dyn DatabaseDriver>> {
        match orig.kind {
            DatabaseKind::Sqlite => {
                let path = orig.database.as_deref().unwrap_or(":memory:");
                Ok(Arc::new(polydb_db_sqlite::SqliteConn::open(path)?))
            }
            DatabaseKind::Postgres => Ok(Arc::new(polydb_db_postgres::PostgresConn::open(
                &postgres_dsn(conn_info, password),
            )?)),
            DatabaseKind::MySql => Ok(Arc::new(polydb_db_mysql::MySqlConn::open(&mysql_dsn(
                conn_info, password,
            ))?)),
            DatabaseKind::Mssql => {
                let (host, port) = net_addr(conn_info, 1433);
                Ok(Arc::new(polydb_db_mssql::MssqlConn::open(
                    &host,
                    port,
                    conn_info.database.as_deref(),
                    conn_info.username.as_deref().unwrap_or(""),
                    password,
                )?))
            }
            DatabaseKind::Oracle => {
                let (host, port) = net_addr(conn_info, 1521);
                Ok(Arc::new(polydb_db_oracle::OracleConn::open(
                    &host,
                    port,
                    conn_info.database.as_deref().unwrap_or(""),
                    conn_info.username.as_deref().unwrap_or(""),
                    password,
                )?))
            }
            DatabaseKind::Redis => {
                let (host, port) = net_addr(conn_info, 6379);
                Ok(Arc::new(polydb_db_redis::RedisConn::open(
                    &format!("{host}:{port}"),
                    redis_db_index(orig),
                    password,
                )?))
            }
        }
    }

    pub fn disconnect(&self, id: ConnectionId) {
        // 先回收该连接上的所有活动事务（避免悬挂事务；驱动 drop 后 tx 会静默失败）。
        let tx_ids: Vec<String> = self
            .txs
            .lock()
            .iter()
            .filter(|(_, t)| t.connection_id == id)
            .map(|(k, _)| k.clone())
            .collect();
        for tid in tx_ids {
            // 静默失败：driver 已被 drop 或连接断开，直接丢弃即可。
            self.txs.lock().remove(&tid);
        }
        self.connections.write().remove(&id);
        self.read_only.write().remove(&id);
        if let Some(t) = self.tunnels.write().remove(&id) {
            t.close();
        }
    }

    pub fn connection_status(&self, id: ConnectionId) -> CoreResult<ConnectionStatus> {
        let exists = self.storage.connections().get(id)?;
        if exists.is_none() {
            return Err(CoreError::ConnectionNotFound(id.to_string()));
        }

        let connected = self.connections.read().contains_key(&id);

        Ok(ConnectionStatus {
            id,
            connected,
            server_version: None,
            latency_ms: None,
            error: None,
        })
    }

    pub async fn ping(&self, id: ConnectionId) -> CoreResult<()> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.driver().ping().await
    }

    pub async fn execute(
        &self,
        id: ConnectionId,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        self.reject_write_on_read_only(&id, sql)?;
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.execute(sql, params).await
    }

    pub async fn list_schemas(&self, id: ConnectionId) -> CoreResult<Vec<SchemaInfo>> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.list_schemas().await
    }

    pub async fn list_tables(&self, id: ConnectionId, schema: &str) -> CoreResult<Vec<TableInfo>> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.list_tables(schema).await
    }

    pub async fn list_columns(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ColumnInfo>> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.list_columns(schema, table).await
    }

    pub async fn list_indexes(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<IndexInfo>> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.list_indexes(schema, table).await
    }

    pub async fn list_foreign_keys(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.list_foreign_keys(schema, table).await
    }

    pub async fn create_table_sql(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<String> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.create_table_sql(schema, table).await
    }

    // ─── 表数据浏览（M11，behavior.md §13）──────────────────

    /// 按表浏览行。只读操作，不受 read_only 影响。
    pub async fn browse_rows(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<TableRowsResult> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.browse_rows(schema, table, req).await
    }

    /// 对同条件执行精确 COUNT(*)。
    pub async fn browse_rows_count(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<u64> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_sql()?.browse_rows_count(schema, table, req).await
    }

    // ─── KV（Redis）─────────────────────────────────────────

    pub async fn select_db(&self, id: ConnectionId, index: u32) -> CoreResult<()> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.select_db(index).await
    }

    pub async fn scan_keys(
        &self,
        id: ConnectionId,
        cursor: u64,
        pattern: &str,
        count: u32,
    ) -> CoreResult<RedisScanPage> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.scan_keys(cursor, pattern, count).await
    }

    pub async fn key_type(&self, id: ConnectionId, key: &str) -> CoreResult<RedisKeyType> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.key_type(key).await
    }

    pub async fn get_value(&self, id: ConnectionId, key: &str) -> CoreResult<RedisValue> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.get_value(key).await
    }

    pub async fn set_value(
        &self,
        id: ConnectionId,
        key: &str,
        value: RedisValue,
    ) -> CoreResult<()> {
        if self.is_read_only(&id) {
            return Err(polydb_protocol::PolyDBError::new(
                polydb_protocol::error::codes::READ_ONLY,
                "connection is read-only: KV writes are rejected",
            )
            .into());
        }
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.set_value(key, value).await
    }

    pub async fn exec_command(&self, id: ConnectionId, args: &[String]) -> CoreResult<RedisReply> {
        if self.is_read_only(&id) {
            return Err(polydb_protocol::PolyDBError::new(
                polydb_protocol::error::codes::READ_ONLY,
                "connection is read-only: KV commands are rejected",
            )
            .into());
        }
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.exec_command(args).await
    }

    // ─── 事务（M25）─────────────────────────────────────────

    pub async fn begin_transaction(
        &self,
        req: &BeginTransactionRequest,
    ) -> CoreResult<TransactionInfo> {
        self.ensure_connected(&req.connection_id)?;
        let conn = self.get_conn(req.connection_id)?;
        let driver = conn.sql_driver_arc()?;
        let iso = req.isolation_level.unwrap_or(IsolationLevel::ReadCommitted);
        let handle = driver.begin_tx(TxMode::new(iso)).await?;
        let entry = TxEntry {
            id: uuid::Uuid::new_v4(),
            connection_id: req.connection_id,
            driver,
            handle: Some(handle),
            isolation_level: iso,
            started_at: Utc::now(),
        };
        let info = entry.info();
        self.txs.lock().insert(info.id.to_string(), entry);
        Ok(info)
    }

    pub async fn execute_in_transaction(
        &self,
        txn_id: &str,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        // 取出 entry 释放锁，避免 execute 期间持锁。
        // spec §10：execute 中 SQL 失败事务保持 active；无论成败都把 entry 放回，
        // 让客户端可自行 rollback 或重试 commit。
        let mut entry = {
            let mut txs = self.txs.lock();
            txs.remove(txn_id)
                .ok_or_else(|| CoreError::TransactionNotFound(txn_id.to_string()))?
        };
        // 只读拦截（behavior.md §12.3）：按事务所属连接判定。
        if let Err(e) = self.reject_write_on_read_only(&entry.connection_id, sql) {
            self.txs.lock().insert(txn_id.to_string(), entry);
            return Err(e);
        }
        let result = match entry.handle.as_mut() {
            Some(handle) => entry.driver.execute_in_tx(handle, sql, params).await,
            None => Err(CoreError::Internal("tx already committed".into())),
        };
        self.txs.lock().insert(txn_id.to_string(), entry);
        result
    }

    pub async fn commit_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut entry = {
            let mut txs = self.txs.lock();
            txs.remove(txn_id)
                .ok_or_else(|| CoreError::TransactionNotFound(txn_id.to_string()))?
        };
        let info = entry.info_finalized(TransactionStatus::Committed);
        let handle = entry
            .handle
            .take()
            .ok_or_else(|| CoreError::Internal("tx already committed".into()))?;
        entry.driver.commit(handle).await?;
        Ok(info)
    }

    pub async fn rollback_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut entry = {
            let mut txs = self.txs.lock();
            txs.remove(txn_id)
                .ok_or_else(|| CoreError::TransactionNotFound(txn_id.to_string()))?
        };
        let info = entry.info_finalized(TransactionStatus::RolledBack);
        let handle = entry
            .handle
            .take()
            .ok_or_else(|| CoreError::Internal("tx already rolled back".into()))?;
        entry.driver.rollback(handle).await?;
        Ok(info)
    }

    // ─── 机密辅助（与 Go 侧 appcore 对齐） ───────────────────

    // secret 从 keyring 取回机密；ref 为空返回空串（连接可能不需要密码）。
    fn secret(&self, ref_: &str) -> CoreResult<String> {
        if ref_.is_empty() {
            return Ok(String::new());
        }
        self.kr.get(ref_)
    }

    // store_conn_secrets 把一次性明文密码写入 keyring 并回填 ref（secret 为空则不动 ref）。
    fn store_conn_secrets(
        &self,
        secret: &str,
        ref_out: &mut Option<String>,
        scope: &str,
    ) -> CoreResult<()> {
        if secret.is_empty() {
            return Ok(());
        }
        if ref_out.is_none() {
            *ref_out = Some(keyring_ref(scope));
        }
        let r = ref_out.as_ref().unwrap().clone();
        self.kr.set(&r, secret)
    }

    // store_ssh_secrets 处理创建请求的 SSH 一次性明文（密码/私钥口令）并清空明文。
    fn store_ssh_secrets(&self, ssh: &mut Option<SshTunnelConfig>) -> CoreResult<()> {
        let Some(ssh) = ssh else {
            return Ok(());
        };
        if let Some(pw) = ssh.password.take() {
            self.store_conn_secrets(&pw, &mut ssh.password_ref, "ssh")?;
        }
        if let Some(pp) = ssh.private_key_passphrase.take() {
            self.store_conn_secrets(&pp, &mut ssh.private_key_passphrase_ref, "ssh-pass")?;
        }
        Ok(())
    }

    // store_ssh_secrets_update 处理更新请求的 SSH 一次性明文；复用已有 ref（改密码时保留同一把 key）。
    fn store_ssh_secrets_update(
        &self,
        id: ConnectionId,
        ssh: &mut Option<SshTunnelConfig>,
    ) -> CoreResult<()> {
        let Some(ssh) = ssh else {
            return Ok(());
        };
        let existing = self.storage.connections().get(id)?;
        let existing_ssh = existing.as_ref().and_then(|e| e.ssh_tunnel.clone());
        if let Some(pw) = ssh.password.take() {
            if !pw.is_empty() {
                let mut ref_out = ssh.password_ref.clone();
                if ref_out.as_deref().unwrap_or("").is_empty() {
                    ref_out = existing_ssh.as_ref().and_then(|s| s.password_ref.clone());
                }
                self.store_conn_secrets(&pw, &mut ref_out, "ssh")?;
                ssh.password_ref = ref_out;
            }
        }
        if let Some(pp) = ssh.private_key_passphrase.take() {
            if !pp.is_empty() {
                let mut ref_out = ssh.private_key_passphrase_ref.clone();
                if ref_out.as_deref().unwrap_or("").is_empty() {
                    ref_out = existing_ssh
                        .as_ref()
                        .and_then(|s| s.private_key_passphrase_ref.clone());
                }
                self.store_conn_secrets(&pp, &mut ref_out, "ssh-pass")?;
                ssh.private_key_passphrase_ref = ref_out;
            }
        }
        Ok(())
    }
}

// ─── DSN 构建（密码来自 keyring，连接时一次性注入，不落库；与 Go 侧 appcore 对齐） ───

fn data_dir() -> String {
    if let Ok(d) = std::env::var("POLYDB_DATA_DIR") {
        return d;
    }
    let base = std::env::var("APPDATA")
        .or_else(|_| std::env::var("HOME"))
        .unwrap_or_else(|_| ".".into());
    std::path::Path::new(&base)
        .join("polydb")
        .to_string_lossy()
        .into_owned()
}

fn host_of(info: &ConnectionInfo) -> String {
    info.host.clone().unwrap_or_else(|| "localhost".to_string())
}

fn net_addr(info: &ConnectionInfo, default_port: u16) -> (String, u16) {
    (host_of(info), info.port.unwrap_or(default_port))
}

// pct_encode：URL 组件百分号编码（用户名/密码可能含 : @ / 等保留字符）。
fn pct_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn postgres_dsn(info: &ConnectionInfo, password: &str) -> String {
    let db = info
        .database
        .clone()
        .unwrap_or_else(|| "postgres".to_string());
    let user = pct_encode(info.username.as_deref().unwrap_or(""));
    let auth = if password.is_empty() {
        user
    } else {
        format!("{user}:{}", pct_encode(password))
    };
    format!(
        "postgres://{auth}@{}:{}/{}?sslmode=disable",
        host_of(info),
        info.port.unwrap_or(5432),
        db
    )
}

fn mysql_dsn(info: &ConnectionInfo, password: &str) -> String {
    let db = info.database.clone().unwrap_or_else(|| "mysql".to_string());
    let user = pct_encode(info.username.as_deref().unwrap_or(""));
    let auth = if password.is_empty() {
        user
    } else {
        format!("{user}:{}", pct_encode(password))
    };
    format!(
        "mysql://{auth}@{}:{}/{}",
        host_of(info),
        info.port.unwrap_or(3306),
        db
    )
}

// redis_db_index：info.database 视为 db 编号（与 Go 侧一致），非数字视为 0。
fn redis_db_index(info: &ConnectionInfo) -> u32 {
    info.database
        .as_deref()
        .and_then(|d| d.trim().parse::<u32>().ok())
        .unwrap_or(0)
}

// target_host_port 返回隧道目标地址（含各驱动默认端口；与 Go 侧一致）。
fn target_host_port(info: &ConnectionInfo) -> (String, u16) {
    let host = host_of(info);
    let port = info.port.unwrap_or(match info.kind {
        DatabaseKind::Postgres => 5432,
        DatabaseKind::MySql => 3306,
        DatabaseKind::Mssql => 1433,
        DatabaseKind::Oracle => 1521,
        DatabaseKind::Redis => 6379,
        DatabaseKind::Sqlite => 0,
    });
    (host, port)
}

// split_local_addr 解析 "127.0.0.1:<port>"。
fn split_local_addr(addr: &str) -> CoreResult<(String, u16)> {
    let (host, port) = addr
        .rsplit_once(':')
        .ok_or_else(|| CoreError::SshTunnel(format!("bad tunnel local addr: {addr}")))?;
    let port = port
        .parse::<u16>()
        .map_err(|e| CoreError::SshTunnel(format!("bad tunnel local port {port}: {e}")))?;
    Ok((host.to_string(), port))
}
