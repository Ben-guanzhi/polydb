//! polydb-db-mssql：基于 tiberius 的 SQL Server 驱动实现（对应 Go 侧 dbmssql）。
//! tiberius Client 内部含非 Send 句柄，且 close 消费自身：所有交互经
//! smol::block_on 同步执行，外层 async 方法不跨 await 持有 guard，满足 Send。

use std::any::Any;
use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, NaiveDate, NaiveDateTime, NaiveTime, Utc};
use parking_lot::Mutex;
use smol::block_on;
use smol::future::or as race;
use smol::Timer;
use tiberius::numeric::Decimal;
use tiberius::{AuthMethod, Client, Config, Row, ToSql};
use tokio::net::TcpStream;
use tokio_util::compat::{Compat, TokioAsyncWriteCompatExt};

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

// tiberius 0.12 无内置连接/查询超时；不可达主机（防火墙丢包）会让 TCP 握手
// 挂到系统超时（本机实测 ~4s/地址，tokio 会串行尝试多个地址）。所有 block_on
// 一律套超时，保证契约测试中无服务时快速失败而非挂死 tokio worker。
const IO_TIMEOUT: Duration = Duration::from_secs(6);

pub struct MssqlConn {
    client: Arc<Mutex<Option<Client<Compat<TcpStream>>>>>,
    _dsn: String,
}

/// 以 IO_TIMEOUT 上限同步执行一个异步操作（tiberius 无内置超时）。
/// 用 Timer 与操作竞速：超时分支获胜时，操作 future 被直接丢弃（对
/// 连接/查询来说是安全的取消，底层 socket 一并关闭）。
fn timed<T, E>(what: &str, fut: impl Future<Output = Result<T, E>>) -> CoreResult<T>
where
    E: std::fmt::Display,
{
    enum Out<T, E> {
        Done(Result<T, E>),
        TimedOut,
    }
    let out = block_on(race(async { Out::Done(fut.await) }, async {
        let _ = Timer::after(IO_TIMEOUT).await;
        Out::TimedOut
    }));
    match out {
        Out::Done(Ok(v)) => Ok(v),
        Out::Done(Err(e)) => Err(CoreError::Driver(format!("{what}: {e}"))),
        Out::TimedOut => Err(CoreError::Driver(format!(
            "{what}: timeout after {IO_TIMEOUT:?}"
        ))),
    }
}

impl MssqlConn {
    /// host:port/database 形式定位 SQL Server；password 为可选连接密码
    /// （明文来自 keyring，连接时一次性注入，不落库；与 Go 侧 mssqlDSN 对齐）。
    pub fn open(
        host: &str,
        port: u16,
        database: Option<&str>,
        username: &str,
        password: &str,
    ) -> CoreResult<Self> {
        let mut config = Config::new();
        config.host(host);
        config.port(port);
        config.authentication(AuthMethod::sql_server(username, password));
        if let Some(db) = database {
            config.database(db);
        }
        config.trust_cert();

        let tcp = timed("connect mssql", TcpStream::connect(config.get_addr()))?;
        tcp.set_nodelay(true).ok();
        let client = timed("login mssql", Client::connect(config, tcp.compat_write()))?;

        Ok(Self {
            client: Arc::new(Mutex::new(Some(client))),
            _dsn: format!("{host}:{port}"),
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：tiberius `Client` 不可克隆，
    /// 因此内部包 `Arc<Mutex<...>>`，两个 `Arc<Self>` 共享同一个 client。
    /// 用于 app-core 持有第二个 `Arc<dyn SqlDriver>`，与 `Connection`
    /// 内的驱动共享同一底层连接。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            client: Arc::clone(&self.client),
            _dsn: self._dsn.clone(),
        })
    }

    fn tsql_params(v: &[Value]) -> Vec<Box<dyn ToSql>> {
        v.iter()
            .map(|val| match val {
                Value::Integer(i) => Box::new(*i) as Box<dyn ToSql>,
                Value::Float(f) => Box::new(*f) as Box<dyn ToSql>,
                Value::Bool(b) => Box::new(*b) as Box<dyn ToSql>,
                Value::String(s) => Box::new(s.clone()) as Box<dyn ToSql>,
                _ => Box::new(0i64) as Box<dyn ToSql>,
            })
            .collect()
    }

    fn param_refs(boxes: &[Box<dyn ToSql>]) -> Vec<&dyn ToSql> {
        boxes.iter().map(|b| b.as_ref()).collect()
    }

    /// tiberius 的 try_get 返回 Result<Option<T>>（None 即 SQL NULL）。
    fn mssql_value(row: &Row, i: usize) -> Value {
        if let Ok(Some(v)) = row.try_get::<bool, usize>(i) {
            return Value::Bool(v);
        }
        if let Ok(Some(v)) = row.try_get::<i64, usize>(i) {
            return Value::Integer(v);
        }
        if let Ok(Some(v)) = row.try_get::<i32, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<i16, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<u8, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<f64, usize>(i) {
            return Value::Float(v);
        }
        if let Ok(Some(v)) = row.try_get::<f32, usize>(i) {
            return Value::Float(v as f64);
        }
        if let Ok(Some(v)) = row.try_get::<Decimal, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveDateTime, usize>(i) {
            return Value::String(v.format("%Y-%m-%d %H:%M:%S").to_string());
        }
        if let Ok(Some(v)) = row.try_get::<DateTime<Utc>, usize>(i) {
            return Value::String(v.format("%Y-%m-%d %H:%M:%S").to_string());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveDate, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveTime, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<&str, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<&[u8], usize>(i) {
            return Value::String(format!("<blob {} bytes>", v.len()));
        }
        Value::Null
    }

    fn detect_statement_type(sql: &str) -> StatementType {
        let t = strip_leading_comments(sql).trim().to_uppercase();
        if t.starts_with("SELECT") || t.starts_with("WITH") {
            StatementType::Select
        } else if t.starts_with("INSERT") {
            StatementType::Insert
        } else if t.starts_with("UPDATE") {
            StatementType::Update
        } else if t.starts_with("DELETE") {
            StatementType::Delete
        } else if t.starts_with("CREATE") || t.starts_with("ALTER") || t.starts_with("DROP") {
            StatementType::Ddl
        } else {
            StatementType::Other
        }
    }

    fn columns_of(rows: &[Row]) -> Vec<ResultColumn> {
        let Some(first) = rows.first() else {
            return vec![];
        };
        first
            .columns()
            .iter()
            .map(|c| ResultColumn {
                name: c.name().to_string(),
                table: None,
                data_type: format!("{:?}", c.column_type()),
                generic_type: None,
                nullable: Some(true),
            })
            .collect()
    }

    fn query_rows(&self, sql: &str, refs: &[&dyn ToSql], what: &str) -> CoreResult<Vec<Row>> {
        let mut client = self.client.lock();
        let stream = timed(what, client.as_mut().unwrap().query(sql, refs))?;
        timed(what, stream.into_first_result())
    }
}

fn strip_leading_comments(sql: &str) -> &str {
    let mut s = sql;
    loop {
        s = s.trim_start();
        if let Some(rest) = s.strip_prefix("--") {
            s = match rest.find('\n') {
                Some(i) => &rest[i + 1..],
                None => return "",
            };
        } else if let Some(rest) = s.strip_prefix("/*") {
            s = match rest.find("*/") {
                Some(i) => &rest[i + 2..],
                None => return "",
            };
        } else {
            return s;
        }
    }
}

fn quote_ident(s: &str) -> String {
    format!("[{}]", s.replace(']', "]]"))
}

/// MSSQL 事务句柄。tiberius Client 无独立事务对象，事务状态由
/// BEGIN/COMMIT/ROLLBACK TRAN 在连接上隐式维护；本结构仅作不透明标记。
#[derive(Clone, Debug)]
pub struct MssqlTxHandle;

impl MssqlConn {
    fn begin_stmt(mode: TxMode) -> Option<&'static str> {
        match mode.isolation_level {
            IsolationLevel::ReadCommitted => None,
            IsolationLevel::ReadUncommitted => {
                Some("SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; BEGIN TRAN")
            }
            IsolationLevel::RepeatableRead => {
                Some("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ; BEGIN TRAN")
            }
            IsolationLevel::Serializable => {
                Some("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE; BEGIN TRAN")
            }
        }
    }

    fn run_batch(&self, sql: &str, what: &str) -> CoreResult<()> {
        let mut client = self.client.lock();
        let stream = timed(what, client.as_mut().unwrap().simple_query(sql))?;
        timed(what, stream.into_first_result())?;
        Ok(())
    }

    pub fn begin_tx(&self, mode: TxMode) -> CoreResult<MssqlTxHandle> {
        let sql = match Self::begin_stmt(mode) {
            Some(s) => s.to_string(),
            None => "BEGIN TRAN".to_string(),
        };
        self.run_batch(&sql, "begin tx failed")?;
        Ok(MssqlTxHandle)
    }

    pub fn commit(&self, _tx: &mut MssqlTxHandle) -> CoreResult<()> {
        self.run_batch("COMMIT TRAN", "commit failed")
    }

    pub fn rollback(&self, _tx: &mut MssqlTxHandle) -> CoreResult<()> {
        self.run_batch("ROLLBACK TRAN", "rollback failed")
    }

    /// 事务内执行一条 SQL。MSSQL 事务状态由连接上的 BEGIN/COMMIT/ROLLBACK
    /// TRAN 隐式维护，因此 execute_in_tx 与 execute 完全等价（同一 client）。
    pub async fn execute_in_tx(
        &self,
        _tx: &mut MssqlTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::tsql_params(params);
        let refs = Self::param_refs(&boxes);

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
            let rows = self.query_rows(sql, &refs, "query failed")?;
            let columns = Self::columns_of(&rows);
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::mssql_value(r, i)).collect())
                .collect();
            Ok(QueryResult {
                columns,
                rows: data,
                affected_rows: 0,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        } else {
            let mut client = self.client.lock();
            let result = timed(
                "execute failed",
                client.as_mut().unwrap().execute(sql, &refs),
            )?;
            let affected = result.rows_affected().first().copied().unwrap_or(0);
            Ok(QueryResult {
                columns: vec![],
                rows: vec![],
                affected_rows: affected,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        }
    }
}

#[async_trait]
impl DatabaseDriver for MssqlConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Mssql
    }

    async fn ping(&self) -> CoreResult<()> {
        let mut client = self.client.lock();
        let stream = timed(
            "ping failed",
            client.as_mut().unwrap().simple_query("SELECT 1"),
        )?;
        timed("ping failed", stream.into_first_result())?;
        Ok(())
    }

    async fn close(&self) -> CoreResult<()> {
        if let Some(client) = self.client.lock().take() {
            timed("close", client.close())?;
        }
        Ok(())
    }

    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for MssqlConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_MSSQL, schema, table, req)?;
        let limit = polydb_db_core::browse_rows_limits(req.limit);
        let res = self.execute(&sql, &args).await?;
        Ok(polydb_db_core::rows_result_to_browse_page(
            res, req.offset, limit,
        ))
    }

    async fn browse_rows_count(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<u64> {
        let (sql, args) = polydb_db_core::build_rows_count_query(
            polydb_db_core::DIALECT_MSSQL,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<MssqlConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(MssqlConn::begin_tx(self, mode)?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<MssqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::execute_in_tx(self, h, sql, params).await
    }

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<MssqlTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::commit(self, &mut h)
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<MssqlTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::rollback(self, &mut h)
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::tsql_params(params);
        let refs = Self::param_refs(&boxes);

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
            let rows = self.query_rows(sql, &refs, "query failed")?;
            let columns = Self::columns_of(&rows);
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::mssql_value(r, i)).collect())
                .collect();
            Ok(QueryResult {
                columns,
                rows: data,
                affected_rows: 0,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        } else {
            let mut client = self.client.lock();
            let result = timed(
                "execute failed",
                client.as_mut().unwrap().execute(sql, &refs),
            )?;
            let affected = result.rows_affected().first().copied().unwrap_or(0);
            Ok(QueryResult {
                columns: vec![],
                rows: vec![],
                affected_rows: affected,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        }
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        let rows = self.query_rows(
            "SELECT s.name FROM sys.schemas s \
             WHERE s.name NOT IN ('sys','INFORMATION_SCHEMA','guest') \
               AND s.name NOT LIKE 'db[_]%' AND s.name NOT LIKE '##%' ORDER BY s.name",
            &[],
            "list_schemas",
        )?;
        Ok(rows
            .iter()
            .map(|r| SchemaInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            })
            .collect())
    }

    async fn list_tables(&self, schema: &str) -> CoreResult<Vec<TableInfo>> {
        let rows = self.query_rows(
            "SELECT t.name, 'table' AS type FROM sys.tables t WHERE t.schema_id = SCHEMA_ID(@P1) \
             UNION ALL \
             SELECT v.name, 'view' FROM sys.views v WHERE v.schema_id = SCHEMA_ID(@P1) \
             ORDER BY name",
            &[&schema],
            "list_tables",
        )?;
        Ok(rows
            .iter()
            .map(|r| TableInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                schema: schema.to_string(),
                table_type: match r.try_get::<&str, usize>(1).ok().flatten() {
                    Some("view") => TableType::View,
                    _ => TableType::Table,
                },
                row_count: None,
                comment: None,
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let rows = self.query_rows(
            "SELECT c.name, \
                    CASE WHEN t.name IN ('nvarchar','nchar','varchar','char','binary','varbinary') \
                         THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length AS varchar) END + ')' \
                         WHEN t.name IN ('decimal','numeric') \
                         THEN t.name + '(' + CAST(c.precision AS varchar) + ',' + CAST(c.scale AS varchar) + ')' \
                         ELSE t.name END AS data_type, \
                    c.is_nullable, d.definition, c.is_identity, c.column_id \
             FROM sys.columns c \
             JOIN sys.types t ON t.user_type_id = c.user_type_id \
             JOIN sys.tables tb ON tb.object_id = c.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             LEFT JOIN sys.default_constraints d ON d.object_id = c.default_object_id \
             WHERE s.name = @P1 AND tb.name = @P2 ORDER BY c.column_id",
            &[&schema, &table],
            "list_columns",
        )?;

        let mut cols: Vec<ColumnInfo> = rows
            .iter()
            .map(|r| ColumnInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                data_type: r
                    .try_get::<&str, usize>(1)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                generic_type: None,
                nullable: r.try_get::<bool, usize>(2).ok().flatten().unwrap_or(false),
                default_value: r
                    .try_get::<&str, usize>(3)
                    .ok()
                    .flatten()
                    .map(|s| s.to_string()),
                max_length: None,
                precision: None,
                scale: None,
                is_primary_key: false,
                is_auto_increment: r.try_get::<bool, usize>(4).ok().flatten().unwrap_or(false),
                comment: None,
                ordinal_position: r.try_get::<i32, usize>(5).ok().flatten().unwrap_or(0),
            })
            .collect();

        let pk_rows = self.query_rows(
            "SELECT col.name FROM sys.indexes i \
             JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id \
             JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id \
             JOIN sys.tables tb ON tb.object_id = i.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE i.is_primary_key = 1 AND s.name = @P1 AND tb.name = @P2 \
             ORDER BY ic.key_ordinal",
            &[&schema, &table],
            "list_columns pk",
        )?;
        let pk_set: std::collections::HashSet<String> = pk_rows
            .iter()
            .map(|r| {
                r.try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string()
            })
            .collect();
        for c in cols.iter_mut() {
            if pk_set.contains(&c.name) {
                c.is_primary_key = true;
            }
        }
        Ok(cols)
    }

    async fn list_indexes(&self, schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        let rows = self.query_rows(
            "SELECT i.name, i.is_unique, i.is_primary_key, i.type_desc, \
                    ic.key_ordinal, col.name \
             FROM sys.indexes i \
             JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id \
             JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id \
             JOIN sys.tables tb ON tb.object_id = i.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE s.name = @P1 AND tb.name = @P2 ORDER BY i.name, ic.key_ordinal",
            &[&schema, &table],
            "list_indexes",
        )?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (bool, bool, String, Vec<IndexColumn>)> = HashMap::new();
        for r in &rows {
            let name = r
                .try_get::<&str, usize>(0)
                .ok()
                .flatten()
                .unwrap_or("")
                .to_string();
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.try_get::<bool, usize>(1).ok().flatten().unwrap_or(false),
                        r.try_get::<bool, usize>(2).ok().flatten().unwrap_or(false),
                        r.try_get::<&str, usize>(3)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        Vec::new(),
                    ),
                );
            }
            groups.get_mut(&name).unwrap().3.push(IndexColumn {
                name: r
                    .try_get::<&str, usize>(5)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                position: r.try_get::<i32, usize>(4).ok().flatten().unwrap_or(0),
                order: None,
                prefix_length: None,
            });
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (unique, primary, type_desc, columns) = groups.remove(&name).unwrap();
                IndexInfo {
                    name,
                    unique,
                    primary,
                    index_type: Some(
                        if type_desc.contains("CLUSTERED") || type_desc.contains("NONCLUSTERED") {
                            IndexType::Btree
                        } else {
                            IndexType::Other
                        },
                    ),
                    columns,
                    comment: None,
                }
            })
            .collect())
    }

    async fn list_foreign_keys(
        &self,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        let rows = self.query_rows(
            "SELECT fk.name, c.name, rs.name, rt.name, rc.name \
             FROM sys.foreign_keys fk \
             JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id \
             JOIN sys.columns c ON c.object_id = fkc.parent_object_id AND c.column_id = fkc.parent_column_id \
             JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id \
             JOIN sys.schemas rs ON rs.schema_id = rt.schema_id \
             JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id \
             JOIN sys.tables tb ON tb.object_id = fk.parent_object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE s.name = @P1 AND tb.name = @P2 \
             ORDER BY fk.name, fkc.constraint_column_id",
            &[&schema, &table],
            "list_foreign_keys",
        )?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (String, String, Vec<String>, Vec<String>)> =
            HashMap::new();
        for r in &rows {
            let name = r
                .try_get::<&str, usize>(0)
                .ok()
                .flatten()
                .unwrap_or("")
                .to_string();
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.try_get::<&str, usize>(2)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        r.try_get::<&str, usize>(3)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        Vec::new(),
                        Vec::new(),
                    ),
                );
            }
            let group = groups.get_mut(&name).unwrap();
            group.2.push(
                r.try_get::<&str, usize>(1)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            );
            group.3.push(
                r.try_get::<&str, usize>(4)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            );
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (ref_schema, ref_table, cols, ref_cols) = groups.remove(&name).unwrap();
                ForeignKeyInfo {
                    name,
                    columns: cols,
                    referenced_schema: ref_schema,
                    referenced_table: ref_table,
                    referenced_columns: ref_cols,
                    on_update: None,
                    on_delete: None,
                }
            })
            .collect())
    }

    async fn create_table_sql(&self, schema: &str, table: &str) -> CoreResult<String> {
        let cols = self.list_columns(schema, table).await?;
        if cols.is_empty() {
            return Err(CoreError::Driver(format!(
                "table not found: {schema}.{table}"
            )));
        }
        let mut b = format!(
            "CREATE TABLE {}.{} (\n",
            quote_ident(schema),
            quote_ident(table)
        );
        for (i, col) in cols.iter().enumerate() {
            let suffix = if col.is_primary_key {
                " PRIMARY KEY"
            } else if col.is_auto_increment {
                " IDENTITY(1,1)"
            } else if !col.nullable {
                " NOT NULL"
            } else {
                ""
            };
            if i < cols.len() - 1 {
                b.push_str(&format!(
                    "  {} {}{},\n",
                    quote_ident(&col.name),
                    col.data_type,
                    suffix
                ));
            } else {
                b.push_str(&format!(
                    "  {} {}{}\n",
                    quote_ident(&col.name),
                    col.data_type,
                    suffix
                ));
            }
        }
        b.push_str(");");
        Ok(b)
    }
}
