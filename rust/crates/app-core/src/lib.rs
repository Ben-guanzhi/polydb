use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

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
use polydb_db_core::{
    clear_blob_sink, set_blob_sink, BlobSink, Connection, DatabaseDriver, RedisValueLimit,
    RedisValueWindow, SqlDriver, Sweepable, TxManager, TxMode,
};
use polydb_protocol::common::SshTunnelConfig;
use polydb_protocol::error::{codes, PolyDBError};
use polydb_rt::block_on;
use polydb_schema_diff as schema_diff;
use polydb_storage::{keyring_ref, BlobStore, FileKeyring, Keyring, Storage};

type Snapshot = schema_diff::Snapshot;
type TableSnapshot = schema_diff::TableSnapshot;

/// 每条连接同时打开的事务上限（PR-01）。事务把池连接钉到提交/回滚为止，
/// 而 MySQL/PG 的池只有 8 槽（见 db-mysql/db-postgres 的 open）：4 个事务
/// 恰好占一半，另一半留给查询与元数据刷新。超限时回语义化错误，而不是让
/// 第 5 个请求在池上等 5s 后报 "pool timed out"。
const MAX_CONCURRENT_TX_PER_CONNECTION: usize = 4;

/// Per-engine metadata: default port, schema, host requirement, DSN style.
/// Adding a new engine means adding one entry here.
struct DriverSpec {
    default_port: u16,
    default_schema: &'static str,
    needs_host: bool,
}

static SQLITE: DriverSpec = DriverSpec {
    default_port: 0,
    default_schema: "main",
    needs_host: false,
};
static POSTGRES: DriverSpec = DriverSpec {
    default_port: 5432,
    default_schema: "public",
    needs_host: true,
};
static MYSQL: DriverSpec = DriverSpec {
    default_port: 3306,
    default_schema: "",
    needs_host: true,
};
static MSSQL: DriverSpec = DriverSpec {
    default_port: 1433,
    default_schema: "",
    needs_host: true,
};
static ORACLE: DriverSpec = DriverSpec {
    default_port: 1521,
    default_schema: "",
    needs_host: true,
};
static REDIS: DriverSpec = DriverSpec {
    default_port: 6379,
    default_schema: "",
    needs_host: true,
};

fn spec(kind: DatabaseKind) -> &'static DriverSpec {
    match kind {
        DatabaseKind::Sqlite => &SQLITE,
        DatabaseKind::Postgres => &POSTGRES,
        DatabaseKind::MySql => &MYSQL,
        DatabaseKind::Mssql => &MSSQL,
        DatabaseKind::Oracle => &ORACLE,
        DatabaseKind::Redis => &REDIS,
    }
}

/// `schema` 省略时按种类取默认（behavior.md §14.5）。SQLite 驱动不做 schema 寻址，恒 `main`；
/// 其余方言把空串直传给驱动，由驱动用自己的当前库/模式。
fn default_schema(kind: DatabaseKind) -> &'static str {
    spec(kind).default_schema
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
///
/// `last_active` 供无主事务回收器（P2-B）判定 idle 超时：每次
/// execute_in_transaction 成功后刷新，与 Go 侧 txEntry.lastActive 同构。
struct TxEntry {
    id: uuid::Uuid,
    connection_id: ConnectionId,
    driver: Arc<dyn SqlDriver>,
    handle: Option<Box<dyn Any + Send>>,
    isolation_level: IsolationLevel,
    started_at: DateTime<Utc>,
    last_active: DateTime<Utc>,
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
///
/// PD-05（2026-11-09）：注册表与回收器已下沉到内核 `polydb_db_core::TxManager`，
/// 守卫持有的是 `Arc<TxManager<TxEntry>>`；app-core 不再自造 sweeper。
struct TxEntryGuard {
    manager: Arc<TxManager<TxEntry>>,
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
            self.manager.insert(self.id.clone(), entry);
        }
    }
}

/// 内核 `TxManager` 的条目能力实现（PD-05）：回收器只依赖 `last_active` 与
/// `rollback` 两个能力，条目类型与驱动细节留在 app-core。
///
/// `rollback` 在注册表锁外执行（由内核 `TxManager::sweep` 保证）：取走句柄后
/// 调驱动 rollback，失败也照常移除条目——事务已无主，留条目只会让后续
/// commit/rollback 报误导性错误。
impl Sweepable for TxEntry {
    fn last_active(&self) -> DateTime<Utc> {
        self.last_active
    }

    fn rollback(&mut self) {
        if let Some(mut handle) = self.handle.take() {
            let driver = self.driver.clone();
            let _ = block_on(driver.rollback(&mut handle));
        }
    }
}

impl Drop for AppCore {
    fn drop(&mut self) {
        // P2-B / PD-05：停掉后台 sweeper 线程。sweeper 持有 Arc<TxManager> 的
        // 克隆，不持有 Arc<Self>——看到 stop 后退出。字段随后按声明序 drop：
        // txs → connections → ... 各驱动句柄的 Drop 隐式 rollback/close。
        self.txs.stop();
        // P2-E：清除进程级 BLOB_SINK（引用计数递减，归零时真正清除）。
        // 此前 AppCore 构造时 set_blob_sink 但从不 clear——销毁后全局 sink
        // 仍指向已释放的 BlobStore（悬垂），多实例互相覆盖。
        clear_blob_sink();
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

/// 一个懒连接的并发归并flight：leader 发布裁决，follower 等待共享。
/// 裁决的类型化载体（NotFound / Other）——若只用 String，ConnectionNotFound
/// 经桥接会退化成 CoreError::Driver → POLYDB_ERR_QUERY_FAILED → HTTP 500；
/// 状态映射（server error_response）只有 ConnectionNotFound 才落 404。
type ConnectFlight = Arc<std::sync::OnceLock<Result<(), ConnectErr>>>;

/// 连接裁决的错误载体：NotFound 保留连接 id 供重建 ConnectionNotFound，
/// Other 仅承载 Display 文本（CoreError 不可 Clone，只过桥一次）。
#[derive(Clone)]
enum ConnectErr {
    NotFound(String),
    Other(String),
}

impl ConnectErr {
    /// CoreError → 裁决载体：ConnectionNotFound 走 NotFound 分支，其余按文本。
    fn from_core(e: &CoreError) -> Self {
        match e {
            CoreError::ConnectionNotFound(id) => ConnectErr::NotFound(id.clone()),
            e => ConnectErr::Other(e.to_string()),
        }
    }
    fn into_core(self) -> CoreError {
        match self {
            ConnectErr::NotFound(id) => CoreError::ConnectionNotFound(id),
            ConnectErr::Other(msg) => CoreError::Driver(msg),
        }
    }
}

pub struct AppCore {
    storage: Storage,
    /// 旁路大字段存储（spec/encoding.md §5.2）；目录跟随库文件所在目录。
    blobs: Arc<BlobStore>,
    kr: Arc<dyn Keyring>,
    connections: RwLock<HashMap<ConnectionId, Connection>>,
    // 用户主动断开的连接：心跳 ping 不得把它悄悄拉回来（否则状态栏显示
    // "已断开"、后台却每 30s 对同一台死主机重建连接）。显式 connect 清除。
    user_disconnected: RwLock<std::collections::HashSet<ConnectionId>>,
    tunnels: RwLock<HashMap<ConnectionId, Arc<Tunnel>>>,
    // 事务注册表 + 无主事务回收器（PD-05，2026-11-09）：本体在内核
    // `polydb_db_core::TxManager`，与 Go 侧委托 `dbcore.TxManager` 同构。
    // 此前 Rust 端在 app-core 自造 sweeper（txs map + tx_idle_ttl +
    // tx_max_entries + tx_sweeper_stop 四个字段），与 Go 端结构漂移。
    txs: Arc<TxManager<TxEntry>>,
    // 已打开连接的只读标志快照（behavior.md §12.3）；disconnect 时移除。
    read_only: RwLock<std::collections::HashSet<ConnectionId>>,
    // 懒连接并发归并（Go 侧 connectSF 同款）：同一 id 的并发首次使用只拨
    // 一次，其余等待并共享其裁决。没有归并时两个并发冷连各拨一次，落败
    // 方的 Connection 与 SSH 隧道被 insert 覆盖丢弃——隧道监听端口泄漏。
    // 裁决以 String 承载错误：CoreError 含 Box<dyn Error> 不可 Clone，
    // follower 把串重构为 Driver 错误（并发冷连是罕见路径，保真损失可接受）。
    connect_flights: Mutex<HashMap<ConnectionId, ConnectFlight>>,
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
        // 默认 30min idle 超时、1024 容量上限（与 Go TxManager 一致）。
        // PD-05：回收器由内核 TxManager 提供，构造时启动其后台 sweeper。
        let txs = TxManager::<TxEntry>::new();
        txs.spawn();
        Self {
            storage,
            blobs,
            kr,
            connections: RwLock::new(HashMap::new()),
            user_disconnected: RwLock::new(std::collections::HashSet::new()),
            connect_flights: Mutex::new(HashMap::new()),
            tunnels: RwLock::new(HashMap::new()),
            txs,
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
        // 归并登记：返回 (flight, 是否为 leader)。leader 真正拨号并把裁决
        // 发布到 OnceLock；follower 等待并共享同一结果。
        let (flight, leader) = {
            let mut flights = self.connect_flights.lock();
            match flights.get(id) {
                Some(f) => (Arc::clone(f), false),
                None => {
                    let f = Arc::new(std::sync::OnceLock::new());
                    flights.insert(*id, Arc::clone(&f));
                    (f, true)
                }
            }
        };
        if !leader {
            // wait() 阻塞至 leader 发布裁决——leader 侧 catch_unwind 保证
            // 恐慌也会发布（否则 follower 会永久挂在 wait 上）。等待本身也
            // 覆盖整个冷拨号（SSH 隧道、ODPI-C connect，数十秒）：多线程
            // flavor 上 block_in_place 让出 worker——否则 N 个并发首触钉死
            // N-1 个 worker，server 全域停摆。current-thread flavor 没有
            // worker 池可让（block_in_place 会 panic——server 测试套件当场
            // 抓住），直呼 park 是该模型下的唯一形态；runtime 之外同此。
            let outcome: Result<(), ConnectErr> = match tokio::runtime::Handle::try_current() {
                Ok(h) if h.runtime_flavor() == tokio::runtime::RuntimeFlavor::MultiThread => {
                    tokio::task::block_in_place(|| flight.wait().clone())
                }
                _ => flight.wait().clone(),
            };
            return match outcome {
                Ok(()) => Ok(()),
                // 类型化还原：NotFound → ConnectionNotFound（HTTP 404）；
                // 其余退化回 Driver（映射表里落 500，与历史行为一致）。
                Err(e) => Err(e.into_core()),
            };
        }
        // 拨号本身是阻塞的（SSH 隧道、ODPI-C connect，冷路径可达数十秒）：
        // 多线程 flavor 上用 block_in_place 让出 worker（execute 的
        // spawn_blocking 同理）；current-thread flavor 没有 worker 池
        // 可让（block_in_place 会 panic），直呼是唯一合法形态；runtime
        // 之外直呼，同步路径不受影响。
        let outcome: Result<(), ConnectErr> = match tokio::runtime::Handle::try_current() {
            Ok(h) if h.runtime_flavor() == tokio::runtime::RuntimeFlavor::MultiThread => {
                tokio::task::block_in_place(|| {
                    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                        self.connect(*id)
                    })) {
                        Ok(r) => r.map_err(|e| ConnectErr::from_core(&e)),
                        Err(p) => Err(ConnectErr::Other(format!("connect panicked: {p:?}"))),
                    }
                })
            }
            _ => {
                // The sync/current-thread arm carries the same publish
                // guarantee: a panic here must not leave followers blocked
                // on a never-set flight.
                match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| self.connect(*id))) {
                    Ok(r) => r.map_err(|e| ConnectErr::from_core(&e)),
                    Err(p) => Err(ConnectErr::Other(format!("connect panicked: {p:?}"))),
                }
            }
        };
        // 发布裁决并把本连接的结果翻译回 CoreResult 返回。
        flight.set(outcome.clone()).ok();
        self.connect_flights.lock().remove(id);
        outcome.map_err(|e| e.into_core())
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
        let result = self.storage.connections().update(id, &req)?;
        // 同步内存 read_only 快照：update_connection 之前只写 storage，
        // 已连接连接（在 self.connections map 里）的 is_read_only 判定会
        // 直接返回 false（因为 self.read_only set 不含该 id），导致把已连接
        // 连接改成只读后、直到下次重连前，写语句不被 reject_write_on_read_only
        // 拦截——只读红线（behavior.md §12.3）的静默绕过。
        // req.read_only 为 None 表示不修改该字段，storage 里保持原值，
        // 内存快照也不应变动。
        if req.read_only.is_some() {
            if req.read_only == Some(true) {
                self.read_only.write().insert(id);
            } else {
                self.read_only.write().remove(&id);
            }
        }
        Ok(result)
    }

    pub fn delete_connection(&self, id: ConnectionId) -> CoreResult<bool> {
        self.disconnect(id);
        // PD-07（2026-11-09）：先取回凭据引用再删行。keyring 是独立存储，
        // DELETE 不级联——不留这一步的话删除连接后 keyring 里仍残留可复用的
        // 明文密码，等于把「删除」做成了只删元数据的假删除（与 Go 侧
        // appcore.DeleteConnection 的 secretRefs + revokeSecrets 同纪律）。
        let refs = self.secret_refs(id)?;
        let deleted = self.storage.connections().delete(id)?;
        self.revoke_secrets(&refs);
        Ok(deleted)
    }

    // secret_refs 收集连接在 keyring 里的全部凭据引用（连接密码 + SSH 密码/私钥口令）。
    // 连接不存在时返回已收集到的空集合而不是错误——delete 对不存在的 id 幂等返回 false。
    fn secret_refs(&self, id: ConnectionId) -> CoreResult<Vec<String>> {
        let mut refs: Vec<String> = Vec::new();
        if let Some(r) = self.storage.connections().get_password_ref(id)? {
            if !r.is_empty() {
                refs.push(r);
            }
        }
        if let Some(info) = self.storage.connections().get(id)? {
            if let Some(ssh) = info.ssh_tunnel.as_ref() {
                if let Some(r) = ssh.password_ref.clone().filter(|r| !r.is_empty()) {
                    refs.push(r);
                }
                if let Some(r) = ssh
                    .private_key_passphrase_ref
                    .clone()
                    .filter(|r| !r.is_empty())
                {
                    refs.push(r);
                }
            }
        }
        Ok(refs)
    }

    // revoke_secrets 摘除 keyring 里的凭据；删除失败只记录（不掩盖行删除结果），
    // 避免凭据残留静默（与 Go 侧 revokeSecrets 一致）。
    fn revoke_secrets(&self, refs: &[String]) {
        for r in refs {
            if r.is_empty() {
                continue;
            }
            if let Err(e) = self.kr.delete(r) {
                tracing::warn!("pd07: revoke secret {r} failed: {e}");
            }
        }
    }

    pub fn connect(&self, id: ConnectionId) -> CoreResult<()> {
        // 显式 connect 清除"用户断开"意图（心跳恢复工作）。
        self.user_disconnected.write().remove(&id);
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
            if spec(info.kind).needs_host {
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

        // P2-C：重连前回滚并清理该连接上的旧事务。此前 connect 完全不触碰
        // txs，直接重连会留下旧 TxEntry 持有旧驱动的 Arc——旧事务在连接
        // 被覆盖后仍可 commit/rollback，违反"卸载前先失活依赖"的 guard
        // ¬ relied_n(γ)。与 Go 侧 staleMgr.RollbackAll 对齐。
        let tx_ids: Vec<String> = self.txs.ids_matching(|t| t.connection_id == id);
        for tid in tx_ids {
            if let Ok(mut guard) = self.take_tx(&tid) {
                if let Some(mut handle) = guard.entry().handle.take() {
                    let _ = block_on(guard.entry().driver.rollback(&mut handle));
                }
                guard.finish();
            }
        }

        // 旧驱动显式关闭（与 Go 侧 staleDriver.Close 对齐）：先取 Arc 再覆盖，
        // close 幂等，后续 Drop 再触发也安全。
        let stale_driver = self.connections.read().get(&id).map(|c| c.driver_arc());

        self.connections.write().insert(id, Connection::new(driver));
        if info.read_only == Some(true) {
            self.read_only.write().insert(id);
        } else {
            self.read_only.write().remove(&id);
        }
        // 被顶替的旧隧道必须显式摘除：新配置有隧道时下面的 insert 覆盖旧条目
        // （旧 Arc Drop 关闭）；新配置**没有**隧道时旧条目既不 delete 也不
        // close——SSH 会话与 127.0.0.1 监听端口就永久泄漏，每重连一次漏一个。
        // 与 Go 侧 appcore.go 的 prevTunnel 处理（delete + 锁外 Close）同纪律。
        let stale_tunnel = self.tunnels.write().remove(&id);
        if let Some(t) = tunnel {
            self.tunnels.write().insert(id, t);
        }
        // 先关旧驱动（依赖隧道），再关旧隧道。
        if let Some(d) = stale_driver {
            let _ = block_on(d.close());
        }
        if let Some(t) = stale_tunnel {
            // close 起线程断开 SSH 会话，不持锁做（close 自身不锁 tunnels）。
            t.close();
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

    /// 连接的方言（用于挑选词法剖面）。
    ///
    /// 已连接时取驱动上报的 kind（唯一权威）；未连接时取存储配置里的 kind——
    /// 懒连接路径（ensure 之前）也要能判定方言，否则只读闸门会退化成
    /// 「取各剖面并集」，把合法的只读查询也误拦。
    fn connection_kind(&self, id: &ConnectionId) -> Option<DatabaseKind> {
        if let Some(c) = self.connections.read().get(id) {
            return Some(c.kind());
        }
        self.storage
            .connections()
            .get(*id)
            .ok()
            .flatten()
            .map(|info| info.kind)
    }

    /// 只读连接上的写语句拦截：对整条语句（含 WITH 子树与多语句）做变更关键字
    /// 扫描（db_core::mutates_data），命中返回 POLYDB_ERR_READ_ONLY。不复用
    /// detect_statement_type——数据修改 CTE（WITH…DELETE/UPDATE/INSERT）与
    /// TRUNCATE/GRANT/CALL 等会被判成 select/other 而漏拦。
    ///
    /// 方言由连接决定：词法差异（'#' 行注释、反斜杠转义、PG 的 dollar-quote、
    /// Oracle 的 q'…'、MySQL 的 /*! … */）决定「字面量在哪结束」。认错方言会
    /// 把后面的写语句当成字面量吞掉——那是 fail-open，直接丢数据。
    fn reject_write_on_read_only(&self, id: &ConnectionId, sql: &str) -> CoreResult<()> {
        use polydb_db_core::mutates_data;
        if self.is_read_only(id) && mutates_data(sql, self.connection_kind(id)) {
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
                let db_idx = redis_db_index(orig)?;
                Ok(Arc::new(polydb_db_redis::RedisConn::open(
                    &format!("{host}:{port}"),
                    db_idx,
                    password,
                )?))
            }
        }
    }

    pub fn disconnect(&self, id: ConnectionId) {
        // 先回收该连接上的所有活动事务——先 rollback 再移除，避免服务器端
        // 事务悬挂（P2-5：旧写法只从 map 移除不调 driver.rollback，连接
        // 被 drop 后事务靠驱动隐式回滚，行为不保证且错误被静默吞掉）。
        let tx_ids: Vec<String> = self.txs.ids_matching(|t| t.connection_id == id);
        for tid in tx_ids {
            if let Ok(mut guard) = self.take_tx(&tid) {
                let driver = guard.entry().driver.clone();
                if let Some(mut handle) = guard.entry().handle.take() {
                    // rollback 失败也要移除 tx（连接已断开，事务无法重试）。
                    let _ = block_on(driver.rollback(&mut handle));
                }
                guard.finish();
            }
        }
        // P1-A：显式关闭驱动。此前 driver.close() 是死 API（全仓零调用），
        // 连接卸载的逆完全依赖底层库 Drop（sqlx Pool / rusqlite / tiberius
        // / ODPI-C 会话关闭），未被类型系统或应用层审计。先 clone 驱动 Arc
        // 再从 map 移除；close 幂等（Pool::close / client.close / redis drop
        // / oracle close），后续 Drop 再触发也安全。驱动依赖隧道，故先关
        // 驱动再关隧道（依赖序）。
        let driver = self.connections.read().get(&id).map(|c| c.driver_arc());
        self.connections.write().remove(&id);
        self.read_only.write().remove(&id);
        self.user_disconnected.write().insert(id);
        if let Some(d) = driver {
            let _ = block_on(d.close());
        }
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
        // 心跳是断开连接事实上的重连器：ping 不得复活用户主动断开的连接
        // （对不可达主机每 30s 重建一次 SSH 隧道+连接）。显式 connect 恢复；
        // execute 的懒连接语义保持不变（首次查询仍可免显式 connect）。
        if self.user_disconnected.read().contains(&id) {
            return Err(CoreError::Connection("connection is disconnected".into()));
        }
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

        // ── 批量元数据（K-1.2 的 Rust 对应物）：列相位 + FK 相位各一次往返 ──
        // 列的空组不可信（真表必有列 ⇒ 回落单表补读）；FK 的空组是真状态（不回落）。
        // 两组 known 分开管，与 Go schemadiff.Collect 逐字对应。批量失败绝不产出
        // 半读快照：回落逐表后由逐表路径的错误传播兜底（E6.5 防线）。
        let (batch_cols, batch_cols_known): (HashMap<String, Vec<ColumnInfo>>, bool) =
            match sql.batch_columns(&schema).await {
                Ok(Some(pairs)) => {
                    let mut m: HashMap<String, Vec<ColumnInfo>> = HashMap::new();
                    for (t, c) in pairs {
                        m.entry(t).or_default().push(c);
                    }
                    (m, true)
                }
                // Ok(None) = 驱动无批量能力；Err = 批量尝试失败。两者都回落逐表。
                Ok(None) | Err(_) => (HashMap::new(), false),
            };
        let (batch_fks, batch_fks_known): (HashMap<String, Vec<ForeignKeyInfo>>, bool) =
            match sql.batch_foreign_keys(&schema).await {
                Ok(Some(groups)) => (groups.into_iter().collect(), true),
                Ok(None) | Err(_) => (HashMap::new(), false),
            };

        for t in sql.list_tables(&schema).await? {
            // 视图（E6.5，取证 A.15）：只带**名字与类型**进快照——它不参与比对，但生成器要用
            // 这些名字避让临时表、并据此挂视图警告。零元数据查询。
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
            // 列：批量命中且非空才用；否则回落逐表（错误传播，绝不半读）。
            let columns = match batch_cols.get(&t.name) {
                Some(c) if batch_cols_known && !c.is_empty() => c.clone(),
                _ => sql.list_columns(&schema, &t.name).await?,
            };
            let indexes = sql.list_indexes(&schema, &t.name).await?;
            // FK：批量命中即用（空组 = 无外键，真状态）；否则回落逐表。
            let foreign_keys = if batch_fks_known {
                batch_fks.get(&t.name).cloned().unwrap_or_default()
            } else {
                sql.list_foreign_keys(&schema, &t.name).await?
            };
            tables.push(TableSnapshot {
                name: t.name.clone(),
                table_type: Some(t.table_type),
                columns,
                indexes,
                foreign_keys,
            });
        }
        // The snapshot is assembled from several statements with no enclosing
        // transaction. If a DDL lands mid-collect, the pieces can describe
        // states that never existed together (A-table columns against a
        // B-time table list) and the diff then emits a plan pointing at
        // objects that were never real. Re-list the tables and refuse the
        // snapshot when the set moved; the caller retries against the settled
        // schema instead of persisting a phantom.
        let mut before: Vec<&str> = tables.iter().map(|t| t.name.as_str()).collect();
        // Bind the fresh list before borrowing its names: collecting &str out of
        // a temporary Vec would dangle at the end of the statement.
        let tables_now = sql.list_tables(&schema).await?;
        let mut after: Vec<&str> = tables_now.iter().map(|t| t.name.as_str()).collect();
        before.sort_unstable();
        after.sort_unstable();
        if before != after {
            return Err(CoreError::SnapshotMoved(
                "the table list changed while the snapshot was being collected; retry".into(),
            ));
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

    /// 有界窗口读取（对齐内核 `BoundedValueReader`）：首窗只取限额内的值，
    /// 附完整规模与截断标记；`cursor` 非空时续读下一窗（GUI「加载更多」）。
    pub async fn get_value_window(
        &self,
        id: ConnectionId,
        key: &str,
        limit: RedisValueLimit,
        cursor: &str,
    ) -> CoreResult<RedisValueWindow> {
        self.ensure_connected(&id)?;
        let conn = self.get_conn(id)?;
        conn.as_kv()?.get_value_window(key, limit, cursor).await
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

    // ─── 无主事务回收器（P2-B）────────────────────────────────
    //
    // PD-05（2026-11-09）：回收器本体已下沉到内核 `polydb_db_core::TxManager`
    // （`tx.rs`），app-core 不再自造 sweeper 线程——此前 Rust 端有 txs map +
    // ttl + max + stop 四个字段与一段独立线程，与 Go 端委托 `dbcore.TxManager`
    // 的路径结构漂移。现在这些方法只是内核注册表的透传。

    /// 立即回收无主事务（idle 超时 + 容量上限）。测试与手动 GC 可直接调用，
    /// 后台 sweeper 线程调用同一逻辑。返回被回收的事务数。
    pub fn sweep_idle_txs(&self) -> usize {
        self.txs.sweep()
    }

    /// 覆盖 idle 回收阈值（测试/嵌入式调优用）。
    pub fn set_tx_idle_ttl(&self, ttl: Duration) {
        self.txs.set_idle_ttl(ttl)
    }

    /// 覆盖事务容量上限。
    pub fn set_tx_max_entries(&self, max: usize) {
        self.txs.set_max_entries(max)
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
        let now = Utc::now();
        let entry = TxEntry {
            id: uuid::Uuid::new_v4(),
            connection_id: req.connection_id,
            driver,
            handle: Some(handle),
            isolation_level: iso,
            started_at: now,
            last_active: now,
        };
        let info = entry.info();
        // PR-01：事务把连接钉在池里直到提交/回滚，所以这里必须设上限——否则
        // 事务吃光池后，连元数据刷新都只能在 acquire_timeout 后报 "pool timed
        // out"，错误完全指不到真正原因。上限与池容量（8）配套：事务最多占一半。
        if let Err(mut rejected) = self.txs.insert_limited(
            info.id.to_string(),
            entry,
            MAX_CONCURRENT_TX_PER_CONNECTION,
            |t| t.connection_id == req.connection_id,
        ) {
            // 已被拒绝：它刚打开的句柄必须回滚，否则那条连接就漏在池外。
            rejected.rollback();
            return Err(CoreError::Protocol(Box::new(PolyDBError::new(
                codes::TRANSACTION_FAILED,
                format!(
                    "该连接已有 {MAX_CONCURRENT_TX_PER_CONNECTION} 个打开的事务（达到上限）；请先提交或回滚一个再开新事务"
                ),
            ))));
        }
        Ok(info)
    }

    /// 从注册表里借出事务条目并释放锁；不存在报 TRANSACTION_NOT_FOUND。
    fn take_tx(&self, txn_id: &str) -> CoreResult<TxEntryGuard> {
        let entry = self
            .txs
            .take(txn_id)
            .ok_or_else(|| CoreError::TransactionNotFound(txn_id.to_string()))?;
        Ok(TxEntryGuard {
            manager: Arc::clone(&self.txs),
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
        let result = match entry.handle.as_mut() {
            Some(handle) => entry.driver.execute_in_tx(handle, sql, params).await,
            None => Err(CoreError::Internal("tx already committed".into())),
        };
        // P2-B：成功执行刷新 last_active，供回收器判定活跃度（与 Go 侧
        // ExecuteInTx 成功时更新 lastActive 同构）。失败不刷新——事务可能
        // 处于不确定状态，回收器可更安全地将其作为 idle 候选。
        if result.is_ok() {
            entry.last_active = Utc::now();
        }
        result
    }

    pub async fn commit_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut guard = self.take_tx(txn_id)?;
        let info = guard.entry().info_finalized(TransactionStatus::Committed);
        let driver = guard.entry().driver.clone();
        // P1-2：用 &mut 引用而非 take()——handle 在调用期间保持 Some。
        // P3-A：commit 失败后事务终结（从注册表移除），不再留可 rollback 的
        // 条目。PG 的 commit 用 tx.0.take() 消费 Transaction，失败后 sqlx
        // 隐式 rollback，handle 内层已为 None；若留条目在注册表，后续
        // rollback 报 "tx handle already consumed"（误导性），且数据库状态
        // 已不确定（可能已回滚、可能未回滚）。与 Go 侧 CommitTransaction
        // 失败时 delete(a.txMeta, txID) 同构。
        let commit_result = {
            let handle = guard
                .entry()
                .handle
                .as_mut()
                .ok_or_else(|| CoreError::Internal("tx already committed".into()))?;
            driver.commit(handle).await
        };
        match commit_result {
            Ok(()) => {
                guard.entry().handle = None;
                guard.finish();
                Ok(info)
            }
            Err(e) => {
                // commit 失败：事务终结（从注册表移除），错误透传。
                // 不回插条目——PG 的 handle 内层已被消费，留条目只会让
                // 后续 rollback 报误导性错误。
                guard.finish();
                Err(e)
            }
        }
    }

    pub async fn rollback_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        let mut guard = self.take_tx(txn_id)?;
        let info = guard.entry().info_finalized(TransactionStatus::RolledBack);
        let driver = guard.entry().driver.clone();
        // P1-2：用 &mut 引用而非 take()。
        // P3-A：rollback 失败后事务终结（从注册表移除），与 Go 侧同构。
        let rollback_result = {
            let handle = guard
                .entry()
                .handle
                .as_mut()
                .ok_or_else(|| CoreError::Internal("tx already rolled back".into()))?;
            driver.rollback(handle).await
        };
        match rollback_result {
            Ok(()) => {
                guard.entry().handle = None;
                guard.finish();
                Ok(info)
            }
            Err(e) => {
                // rollback 失败：事务终结（从注册表移除），错误透传。
                guard.finish();
                Err(e)
            }
        }
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
        let r = ref_out.clone().expect("ref_out was just set");
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
    // 与 Go 的 os.UserConfigDir() 对齐（dirs::config_dir 等价）。此前用
    // APPDATA/HOME 直接拼 "polydb"，Linux 下解析到 ~/polydb，与 Go 的
    // ~/.config/polydb 不同——Rust server 与 Go TUI/Web 读写不同的
    // polydb.db / keyring.bin / blobs/，连接与密码静默分叉。
    dirs::config_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("."))
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

// redis_db_index：info.database 视为 db 编号（与 Go 侧 dbIndex 一致）。
//
// 解析纪律（PD-16，2026-11-09）：**非法输入必须报错，不得静默回落 0**。
// 静默回落会让用户连到 db 0 而不是他自己填的库——数据写进错误的库，且
// 无任何提示。原实现用 `unwrap_or(0)`：Rust 侧 `parse::<u32>()` 对超出
// u32 上界的输入溢出即 Err，回落 0；而 Go 侧当时用 int 累加，会得到一个
// 巨大的 db 号交给 Redis 报 "ERR DB index is out of range"。两端行为
// 因此不一致（AGENTS.md §14 红线）。
//
// 规则（两端逐字一致）：空串 → 0；纯十进制数字 → 该值；其余（含空白、
// 正负号、小数点、非 ASCII 数字、超出 u32 上界）→ 报错。
fn redis_db_index(info: &ConnectionInfo) -> CoreResult<u32> {
    let raw = match info.database.as_deref() {
        None | Some("") => return Ok(0),
        Some(d) => d,
    };
    // 注意：不 trim——空白不是合法 db 号，Go 侧 strconv.ParseUint 同样
    // 拒绝带空白的输入，两端行为一致。
    //
    // 另：Rust 的 `u32::from_str` **接受前导 '+'**（"+3" -> Ok(3)），
    // 而 Go 的 `strconv.ParseUint` 拒绝 '+'。故此处显式要求「纯 ASCII
    // 十进制数字」，把两端接受集钉成完全一致（AGENTS.md §14 红线）。
    if raw.is_empty() || !raw.bytes().all(|b| b.is_ascii_digit()) {
        return Err(invalid_redis_db_number(raw));
    }
    raw.parse::<u32>().map_err(|_| invalid_redis_db_number(raw))
}

/// 非法 Redis db 编号的统一错误（与 Go 侧 dbIndex 的错误码、文案一致）。
fn invalid_redis_db_number(raw: &str) -> CoreError {
    // 错误码与 Go 侧一致：POLYDB_ERR_INVALID_PARAM（protocol::error::codes::INVALID_PARAM）。
    CoreError::Protocol(Box::new(PolyDBError::new(
        codes::INVALID_PARAM,
        format!("invalid redis db number {raw:?} (expected 0..4294967295)"),
    )))
}
// target_host_port 返回隧道目标地址（含各驱动默认端口；与 Go 侧一致）。
fn target_host_port(info: &ConnectionInfo) -> (String, u16) {
    (
        host_of(info),
        info.port.unwrap_or(spec(info.kind).default_port),
    )
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

#[cfg(test)]
mod tests {
    use super::data_dir;

    /// F1 回归：data_dir 必须与 Go 的 os.UserConfigDir()/polydb 解析一致。
    /// 此前用 APPDATA/HOME 直接拼 "polydb"，Linux 下解析到 ~/polydb
    /// （Go 解析到 ~/.config/polydb），Rust server 与 Go TUI/Web 读写
    /// 不同的 polydb.db / keyring.bin，连接与密码静默分叉。
    #[test]
    fn data_dir_uses_config_dir_not_home() {
        // POLYDB_DATA_DIR 优先
        std::env::set_var("POLYDB_DATA_DIR", "/tmp/polydb-test");
        assert_eq!(data_dir(), "/tmp/polydb-test");
        std::env::remove_var("POLYDB_DATA_DIR");

        // 未设置时必须走 dirs::config_dir()（= Go os.UserConfigDir()）
        let expected = dirs::config_dir()
            .expect("dirs::config_dir should resolve on all platforms")
            .join("polydb");
        assert_eq!(data_dir(), expected.to_string_lossy().into_owned());
    }

    /// PD-07 回归（Rust 端镜像，与 Go 侧 appcore 的 TestDeleteConnectionRevokesKeyringSecrets 对齐）：
    /// 删除连接必须把 keyring 里由 password_ref 指向的凭据一并摘掉。此前
    /// `delete_connection` 只删 `connections` 行，password_ref 指向的明文密码
    /// 留在 keyring 里仍可复用——删除被做成了「只删元数据的假删除」，且两端
    /// 行为分叉（Go 已 revokeSecrets，Rust 未 revoke）。
    #[test]
    fn pd07_delete_connection_revokes_keyring_secrets() {
        use super::*;

        let dir = std::env::temp_dir().join(format!("polydb-pd07-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
        let kr_path = dir.to_string_lossy().to_string();
        let kr = FileKeyring::open(&kr_path, "").unwrap();
        let app = AppCore::with_keyring(storage, std::sync::Arc::new(kr));

        let info = app
            .create_connection(&CreateConnectionRequest {
                name: "pd07".to_string(),
                kind: DatabaseKind::Sqlite,
                host: None,
                port: None,
                database: Some(":memory:".to_string()),
                username: None,
                password_ref: None,
                password: Some("conn-secret".to_string()),
                options: Default::default(),
                ssh_tunnel: Some(SshTunnelConfig {
                    host: "127.0.0.1".to_string(),
                    port: 22,
                    username: "ssh-user".to_string(),
                    password_ref: None,
                    password: Some("ssh-secret".to_string()),
                    private_key_path: None,
                    private_key_passphrase_ref: None,
                    private_key_passphrase: Some("passphrase-secret".to_string()),
                }),
                default_schema: None,
                read_only: None,
                group: None,
                color: None,
            })
            .unwrap();

        // 第二个句柄用于删除后断言凭据是否被摘掉。必须在 create 之后打开：
        // FileKeyring::open 在文件尚不存在时生成随机 salt，先打开的观察句柄
        // 会拿到与写入方不同的 salt 而无法解密。FileKeyring 每次读写都重载文件，
        // 因此后开的句柄能读到 create 写入的内容。
        let observer = FileKeyring::open(&kr_path, "").unwrap();

        let conn_ref = app
            .storage
            .connections()
            .get_password_ref(info.id)
            .unwrap()
            .filter(|r| !r.is_empty())
            .expect("conn password_ref must be non-empty");
        let ssh = info.ssh_tunnel.as_ref().unwrap();
        let refs: Vec<String> = vec![
            conn_ref,
            ssh.password_ref.clone().unwrap_or_default(),
            ssh.private_key_passphrase_ref.clone().unwrap_or_default(),
        ];
        assert!(
            refs.iter().all(|r| !r.is_empty()),
            "refs must be non-empty: {refs:?}"
        );
        for r in &refs {
            assert!(observer.get(r).is_ok(), "secret {r} readable before delete");
        }

        assert!(app.delete_connection(info.id).unwrap());

        for r in &refs {
            assert!(
                observer.get(r).is_err(),
                "secret {r} still readable after delete (PD-07: 凭据必须随行删除)"
            );
        }
    }
}
