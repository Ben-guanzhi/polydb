use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use chrono::{DateTime, Utc};
use parking_lot::{Mutex, RwLock};
use polydb_core::{
    BeginTransactionRequest, ColumnInfo, ConnectionId, ConnectionInfo, ConnectionStatus, CoreError,
    CoreResult, CreateConnectionRequest, DatabaseKind, DdlScript, DdlStatement, DiffColumnFlag,
    ForeignKeyInfo, IndexInfo, IsolationLevel, QueryResult, RedisKeyType, RedisReply,
    RedisScanPage, RedisValue, SchemaDdlRequest, SchemaDiffEntry, SchemaDiffOp, SchemaDiffOptions,
    SchemaDiffPlan, SchemaDiffRequest, SchemaDiffTarget, SchemaInfo, TableInfo, TableRowsRequest,
    TableRowsResult, TableType, TransactionInfo, TransactionStatus, UpdateConnectionRequest, Value,
};
use polydb_db_core::{set_blob_sink, BlobSink, Connection, DatabaseDriver, SqlDriver, TxMode};
use polydb_protocol::common::SshTunnelConfig;
use polydb_protocol::error::{codes, PolyDBError};
use polydb_schema_diff as schema_diff;
use polydb_storage::{keyring_ref, BlobStore, FileKeyring, Keyring, Storage};

type Snapshot = schema_diff::Snapshot;
type TableSnapshot = schema_diff::TableSnapshot;
/// `schema` 省略时按种类取默认（behavior.md §14.5）。SQLite 驱动不做 schema 寻址，恒 `main`；
/// 其余方言把空串直传给驱动，由驱动用自己的当前库/模式。
fn default_schema(kind: DatabaseKind) -> &'static str {
    match kind {
        DatabaseKind::Sqlite => "main",
        DatabaseKind::Postgres => "public",
        _ => "",
    }
}

/// 线路选项 → 纯函数层选项：整体省略与逐字段省略都按 false 处理。
fn diff_options(opts: Option<SchemaDiffOptions>) -> schema_diff::Options {
    let o = opts.unwrap_or_default();
    schema_diff::Options {
        ignore_defaults: o.ignore_defaults.unwrap_or(false),
        select_destructive: o.select_destructive.unwrap_or(false),
    }
}

/// 纯函数计算结果 → 线路 DTO（两侧类型分属两个 crate，孤儿规则下只能写自由函数）。
fn to_wire_entry(e: schema_diff::Entry) -> SchemaDiffEntry {
    SchemaDiffEntry {
        id: e.id,
        op: match e.op {
            schema_diff::Op::CreateTable => SchemaDiffOp::CreateTable,
            schema_diff::Op::DropTable => SchemaDiffOp::DropTable,
            schema_diff::Op::AddColumn => SchemaDiffOp::AddColumn,
            schema_diff::Op::DropColumn => SchemaDiffOp::DropColumn,
            schema_diff::Op::AlterColumn => SchemaDiffOp::AlterColumn,
            schema_diff::Op::AddIndex => SchemaDiffOp::AddIndex,
            schema_diff::Op::DropIndex => SchemaDiffOp::DropIndex,
            schema_diff::Op::AddForeignKey => SchemaDiffOp::AddForeignKey,
            schema_diff::Op::DropForeignKey => SchemaDiffOp::DropForeignKey,
        },
        phase: e.phase,
        table: e.table,
        object: e.object,
        from: e.from,
        to: e.to,
        flags: e
            .flags
            .into_iter()
            .map(|f| match f {
                schema_diff::ColumnFlag::Type => DiffColumnFlag::Type,
                schema_diff::ColumnFlag::Nullability => DiffColumnFlag::Nullability,
                schema_diff::ColumnFlag::Default => DiffColumnFlag::Default,
                schema_diff::ColumnFlag::AutoIncrement => DiffColumnFlag::AutoIncrement,
            })
            .collect(),
        destructive: e.destructive,
        selected: e.selected,
        reason: e.reason,
    }
}

fn to_wire_statement(s: schema_diff::Statement) -> DdlStatement {
    DdlStatement {
        entry_ids: s.entry_ids,
        sql: s.sql,
        phase: s.phase,
        table: s.table,
        warnings: s.warnings,
        non_reversible: s.non_reversible,
    }
}

fn to_wire_script(sc: schema_diff::Script) -> DdlScript {
    DdlScript {
        statements: sc.statements.into_iter().map(to_wire_statement).collect(),
        rollback: sc.rollback.into_iter().map(to_wire_statement).collect(),
        pragmas_outside_tx: sc.pragmas_outside_tx,
        ddl_in_tx: sc.ddl_in_tx,
    }
}

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

/// `txs` map 的借出守卫。不变量是「entry 要么在 map 里，要么被唯一借出者持有」：
/// 借出期间释放锁，让并发 commit/rollback 拿到 TRANSACTION_NOT_FOUND 而不是
/// 并发驱动同一个事务句柄。
///
/// 回插落在 `Drop` 里而不是手写语句，这样三条退出路径都成立：正常返回、`?`
/// 提前返回、future 在 `await` 点被取消/drop。旧写法是 remove → await →
/// insert，取消时最后的 insert 不执行——事务永久失踪、handle drop 静默回滚。
/// `txs` 用 `Arc` 承载，让守卫可以 clone 一份跨过 `await`（`&Mutex` 不可 Send）。
struct TxEntryGuard {
    txs: Arc<Mutex<HashMap<String, TxEntry>>>,
    id: String,
    entry: Option<TxEntry>,
}

impl TxEntryGuard {
    fn entry(&mut self) -> &mut TxEntry {
        self.entry
            .as_mut()
            .expect("transaction entry already finalized")
    }

    /// 事务已终结（commit/rollback 成功）：不再放回 map。
    fn finish(&mut self) {
        self.entry = None;
    }
}

impl Drop for TxEntryGuard {
    fn drop(&mut self) {
        if let Some(entry) = self.entry.take() {
            self.txs.lock().insert(self.id.clone(), entry);
        }
    }
}

/// 把 storage 的 [`BlobStore`] 适配为 db-core 的 [`BlobSink`]：trait 属
/// db-core、类型属 storage，包装层按孤儿规则落在 app-core。
struct StoreSink(Arc<BlobStore>);

impl BlobSink for StoreSink {
    fn put_blob(&self, data: &[u8]) -> Option<String> {
        self.0.put_blob(data).ok()
    }
}

pub struct AppCore {
    storage: Storage,
    /// 旁路大字段存储（spec/encoding.md §5.2）；目录跟随库文件所在目录。
    blobs: Arc<BlobStore>,
    kr: Arc<dyn Keyring>,
    connections: RwLock<HashMap<ConnectionId, Connection>>,
    tunnels: RwLock<HashMap<ConnectionId, Arc<Tunnel>>>,
    txs: Arc<Mutex<HashMap<String, TxEntry>>>,
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

    /// 显式指定 keyring（测试用）。构造期同时注入大字段 sink（§5.2）：
    /// 目录取库文件所在目录，启动时清扫一次过期引用。
    pub fn with_keyring(storage: Storage, kr: Arc<dyn Keyring>) -> Self {
        let blobs = Arc::new(BlobStore::new(storage.data_dir()));
        blobs.sweep();
        set_blob_sink(Arc::new(StoreSink(blobs.clone())));
        Self {
            storage,
            blobs,
            kr,
            connections: RwLock::new(HashMap::new()),
            tunnels: RwLock::new(HashMap::new()),
            txs: Arc::new(Mutex::new(HashMap::new())),
            read_only: RwLock::new(std::collections::HashSet::new()),
        }
    }

    /// 旁路大字段存储（server Range 取回与测试播种用）。
    pub fn blob_store(&self) -> &BlobStore {
        &self.blobs
    }

    /// 取回旁路大字段（§5.2 进程内路径；HTTP 走 server 的 Range handler）。
    /// `blob_id` 为完整引用 `$polydb_blob:<uuid>` 去掉前缀后的 UUID，
    /// 非法值报 INVALID_PARAM，未知值报 BLOB_NOT_FOUND。
    pub fn get_blob(&self, blob_id: &str) -> CoreResult<Vec<u8>> {
        if uuid::Uuid::parse_str(blob_id).is_err() {
            return Err(PolyDBError::new(
                codes::INVALID_PARAM,
                format!("invalid blob id: {blob_id}"),
            )
            .into());
        }
        self.blobs.get(blob_id)
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

    /// 只读连接上的写语句拦截：对整条语句（含 WITH 子树与多语句）做变更关键字
    /// 扫描（db_core::mutates_data），命中返回 POLYDB_ERR_READ_ONLY。不复用
    /// detect_statement_type——数据修改 CTE（WITH…DELETE/UPDATE/INSERT）与
    /// TRUNCATE/GRANT/CALL 等会被判成 select/other 而漏拦。
    fn reject_write_on_read_only(&self, id: &ConnectionId, sql: &str) -> CoreResult<()> {
        use polydb_db_core::mutates_data;
        if self.is_read_only(id) && mutates_data(sql) {
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

    // ─── 结构比对（Compare & Sync E2，behavior.md §14）───────

    /// 采集一个（连接, schema）的结构快照。视图/物化视图**不参与比对**，但名字要进快照
    /// （只带名字与类型、不发元数据查询）——生成器要用它避让临时表名、出视图告警（§14.1）。
    async fn collect_snapshot(&self, target: &SchemaDiffTarget) -> CoreResult<Snapshot> {
        self.ensure_connected(&target.connection_id)?;
        let conn = self.get_conn(target.connection_id)?;
        let kind = conn.kind();
        if kind == DatabaseKind::Redis {
            return Err(CoreError::NotSupported(
                "Redis 无模式概念，不参与结构比对".into(),
            ));
        }
        // 空串与缺省同义：Go 侧 `omitempty` 会把显式空串抹成缺席，两端必须同一解释。
        let schema = target
            .schema
            .as_deref()
            .filter(|s| !s.trim().is_empty())
            .map(str::to_string)
            .unwrap_or_else(|| default_schema(kind).to_string());
        let sql = conn.as_sql()?;
        let mut tables = Vec::new();
        for t in sql.list_tables(&schema).await? {
            // 视图（E6.5，取证 A.15）：只带**名字与类型**进快照——它不参与比对，但生成器要用
            // 这些名字避让临时表、并据此挂视图警告。零元数据查询 ⇒ 采集扇出仍是每表 3 次。
            if t.table_type != TableType::Table {
                tables.push(TableSnapshot {
                    name: t.name,
                    table_type: Some(t.table_type),
                    columns: Vec::new(),
                    indexes: Vec::new(),
                    foreign_keys: Vec::new(),
                });
                continue;
            }
            tables.push(TableSnapshot {
                name: t.name.clone(),
                table_type: Some(t.table_type),
                columns: sql.list_columns(&schema, &t.name).await?,
                indexes: sql.list_indexes(&schema, &t.name).await?,
                foreign_keys: sql.list_foreign_keys(&schema, &t.name).await?,
            });
        }
        Ok(Snapshot::new(kind, schema, tables))
    }

    /// 比对两个连接的结构差异，产出有序计划（不含 SQL，语句生成属 E3）。
    pub async fn schema_diff(&self, req: &SchemaDiffRequest) -> CoreResult<SchemaDiffPlan> {
        let source = self.collect_snapshot(&req.source).await?;
        let target = self.collect_snapshot(&req.target).await?;
        let entries = schema_diff::diff(&source, &target, &diff_options(req.options))?;
        Ok(SchemaDiffPlan {
            entries: entries.into_iter().map(to_wire_entry).collect(),
        })
    }

    // ─── 结构同步语句生成（Compare & Sync E3，behavior.md §15）───

    /// 由「连接 + 勾选条目 id」产出可执行语句脚本。服务端**重算**比对（§15.1）：
    /// 计划可能已过期，重算顺带做了勾选有效性校验（陈旧 id / 重建组未整组勾选都会报错）。
    pub async fn schema_ddl(&self, req: &SchemaDdlRequest) -> CoreResult<DdlScript> {
        let source = self.collect_snapshot(&req.source).await?;
        let target = self.collect_snapshot(&req.target).await?;
        let opts = diff_options(req.options);
        let plan = schema_diff::diff(&source, &target, &opts)?;
        let picked = schema_diff::select_entries(&source, &target, &plan, &req.selected)?;
        let script = schema_diff::generate(&source, &target, &picked, &opts)?;
        Ok(to_wire_script(script))
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

    /// 从 map 里借出事务条目并释放锁；不存在报 TRANSACTION_NOT_FOUND。
    fn take_tx(&self, txn_id: &str) -> CoreResult<TxEntryGuard> {
        let entry = self
            .txs
            .lock()
            .remove(txn_id)
            .ok_or_else(|| CoreError::TransactionNotFound(txn_id.to_string()))?;
        Ok(TxEntryGuard {
            txs: self.txs.clone(),
            id: txn_id.to_string(),
            entry: Some(entry),
        })
    }

    pub async fn execute_in_transaction(
        &self,
        txn_id: &str,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        // 借出 entry 释放锁，避免 execute 期间持锁。
        // spec §10：execute 中 SQL 失败事务保持 active；无论成败都把 entry 放回，
        // 让客户端可自行 rollback 或重试 commit。回插在守卫的 Drop 里执行，
        // future 在 await 点被取消时也照样放回。
        let mut guard = self.take_tx(txn_id)?;
        let entry = guard.entry();
        // 只读拦截（behavior.md §12.3）：按事务所属连接判定。
        self.reject_write_on_read_only(&entry.connection_id, sql)?;
        match entry.handle.as_mut() {
            Some(handle) => entry.driver.execute_in_tx(handle, sql, params).await,
            None => Err(CoreError::Internal("tx already committed".into())),
        }
    }

    pub async fn commit_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut guard = self.take_tx(txn_id)?;
        let info = guard.entry().info_finalized(TransactionStatus::Committed);
        let driver = guard.entry().driver.clone();
        let handle = guard
            .entry()
            .handle
            .take()
            .ok_or_else(|| CoreError::Internal("tx already committed".into()))?;
        // 事务终结：守卫不再回插（与 Go 侧一致，commit/rollback 只 remove）。
        // 放在 await 之前，取消或失败都不会把已终结的事务留在 map 里。
        guard.finish();
        driver.commit(handle).await?;
        Ok(info)
    }

    pub async fn rollback_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut guard = self.take_tx(txn_id)?;
        let info = guard.entry().info_finalized(TransactionStatus::RolledBack);
        let driver = guard.entry().driver.clone();
        let handle = guard
            .entry()
            .handle
            .take()
            .ok_or_else(|| CoreError::Internal("tx already rolled back".into()))?;
        guard.finish();
        driver.rollback(handle).await?;
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
