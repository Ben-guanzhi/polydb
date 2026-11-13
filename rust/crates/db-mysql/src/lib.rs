//! polydb-db-mysql：基于 sqlx 的 MySQL 驱动实现（对应 Go 侧 dbmysql）。

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use bigdecimal::BigDecimal;
use chrono::{NaiveDate, NaiveDateTime, NaiveTime};
use sqlx::mysql::{MySqlPool, MySqlPoolOptions, MySqlRow};
use sqlx::{Column, Executor, Row, Statement, TransactionManager, TypeInfo, ValueRef};

use polydb_core::error_classify::{driver_error, DriverClass};
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{has_returning_clause, non_empty, DatabaseDriver, SqlDriver, TxMode};

pub struct MySqlConn {
    pool: MySqlPool,
    _dsn: String,
}

/// 事务句柄：专用池连接上的手动事务。MySQL 的隔离级别没有 BEGIN 语法糖，
/// 必须先 `SET TRANSACTION ISOLATION LEVEL …`（下一事务作用域）再
/// `START TRANSACTION`，且两条语句必须落在同一条连接上——pool.begin_with
/// 只接一条语句，因此拿专用连接走 [`sqlx::mysql::MySqlTransactionManager`]。
/// commit/rollback 按值消费句柄；app-core 的 ActiveTx::commit/rollback
/// 通过 take() 从 Option 取出。
#[derive(Debug)]
pub struct MySqlTxHandle {
    conn: Option<sqlx::pool::PoolConnection<sqlx::MySql>>,
    in_tx: bool,
}

impl Drop for MySqlTxHandle {
    fn drop(&mut self) {
        // 未显式 commit/rollback 就丢弃句柄：sqlx 的 Transaction 原本靠 Drop
        // 回滚，这里等价地让连接关闭而不是回池——开着事务回池会污染后续
        // 租用者（无 async 上下文可用的 Drop 里做不了 await，关连接是确定性的）。
        if self.in_tx {
            if let Some(mut conn) = self.conn.take() {
                conn.close_on_drop();
            }
        }
    }
}

impl MySqlConn {
    /// DSN 形如 mysql://user@host:port/dbname（不含密码）。懒连接。
    pub fn open(dsn: &str) -> CoreResult<Self> {
        let pool = MySqlPoolOptions::new()
            // PR-01：4 槽与「钉住到提交」的手动事务共用时，4 个事务就吃光整池，
            // 之后连元数据刷新都只能在 5s 后报 "pool timed out"。8 槽 + app-core
            // 的每连接 4 个事务上限，保证事务最多占一半容量。
            .max_connections(8)
            .acquire_timeout(std::time::Duration::from_secs(5))
            // 服务端（或代理）会按 wait_timeout 杀空闲会话；主动回收空闲连接，
            // 别把半死的连接留在池里等下一个请求踩（sqlx 默认 idle_timeout=None）。
            .idle_timeout(Some(std::time::Duration::from_secs(300)))
            // 语句级墙钟（PR-01，best-effort）：MySQL 5.7.8+ 的
            // max_execution_time 只约束 SELECT。MariaDB 用另一个变量名
            // （max_statement_time，单位秒），SET 会失败——那是兼容性差异而不
            // 是连接故障，所以这里吞掉错误，只让支持的引擎拿到超时。
            .after_connect(|conn, _meta| {
                Box::pin(async move {
                    let _ = sqlx::query("SET SESSION max_execution_time = 300000")
                        .execute(&mut *conn)
                        .await;
                    Ok(())
                })
            })
            .connect_lazy(dsn)
            .map_err(|e| CoreError::Driver(format!("open mysql: {e}")))?;
        Ok(Self {
            pool,
            _dsn: dsn.to_string(),
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：`MySqlPool` 是廉价 handle 克隆，
    /// 两个 `Arc<Self>` 共享同一个池。用于 app-core 持有第二个
    /// `Arc<dyn SqlDriver>`，与 `Connection` 内的驱动共享池资源。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            pool: self.pool.clone(),
            _dsn: self._dsn.clone(),
        })
    }

    pub fn bind_value<'q>(
        q: sqlx::query::Query<'q, sqlx::MySql, sqlx::mysql::MySqlArguments>,
        v: &Value,
    ) -> sqlx::query::Query<'q, sqlx::MySql, sqlx::mysql::MySqlArguments> {
        match v {
            Value::Null => q.bind(Option::<i64>::None),
            Value::Bool(b) => q.bind(*b),
            Value::Integer(i) => q.bind(*i),
            Value::U64(u) => {
                // sqlx MySQL 原生支持 u64（BIGINT UNSIGNED），但为与
                // SQLite/Oracle/MSSQL 一致——这些引擎的整数是 i64——
                // ≤ i64::MAX 绑为 i64，> i64::MAX 绑为 f64。
                if *u <= i64::MAX as u64 {
                    q.bind(*u as i64)
                } else {
                    q.bind(*u as f64)
                }
            }
            Value::Float(f) => q.bind(*f),
            Value::String(s) => q.bind(s.clone()),
            // Array/Object（含 $blob Object）跨线协议统一为 JSON 文本——与 Go
            // 侧 ValueToDriver 的 string(v.Raw()) 对齐。此前用 Debug repr
            // （format!("{v:?}")，形如 Object {"$blob": …}），与 JSON 同形不同
            // 义，且与其他引擎（SQLite/Oracle 同为 JSON 文本、MSSQL 绑 0）漂移。
            Value::Array(_) | Value::Object(_) => q.bind(json_text_param(v)),
        }
    }

    /// 与 Go 侧 DriverToValue 对齐：整数列保持 i64（MySQL 无独立 BOOL 类型）。
    pub fn my_value(row: &MySqlRow, i: usize) -> Value {
        let raw = match row.try_get_raw(i) {
            Ok(r) => r,
            Err(_) => return Value::Null,
        };
        if raw.is_null() {
            return Value::Null;
        }
        if let Ok(v) = row.try_get::<i64, _>(i) {
            return Value::Integer(v);
        }
        // BIGINT UNSIGNED（u64）：sqlx 的 i64 兼容检查明确排除 UNSIGNED flag
        // （sqlx-mysql types/int.rs:int_compatible 的 `&& !ty.flags.contains(
        // ColumnFlags::UNSIGNED)`），故 try_get::<i64> 对该列必然失败；而
        // try_get::<f64> 也失败（float.rs:real_compatible 只接受 Float|Double）。
        // 此前整条读取链对该列全部落空，最终掉到下面的 `<LongLong>` 占位符
        // ——用户看到的是类型名，**数据完全丢失**。
        //
        // Go 侧同场景走 DriverToValueTyped（go/pkg/dbcore/valueconv.go）：
        // ParseInt 超 i64 失败 ⇒ 末尾 NewStringValue(string(b)) ⇒
        // **精确十进制字符串**。故此处同样产出字符串形态，保持两端一致
        // （AGENTS.md §14 红线）。
        if let Ok(v) = row.try_get::<u64, _>(i) {
            return unsigned_bigint_value(v);
        }
        if let Ok(v) = row.try_get::<f64, _>(i) {
            return Value::Float(v);
        }
        if let Ok(v) = row.try_get::<BigDecimal, _>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(v) = row.try_get::<NaiveDateTime, _>(i) {
            // RFC3339Nano —— 与 Go 侧同形（PD-20，2026-11-10）。
            //
            // 旧的 "%Y-%m-%d %H:%M:%S" 是 Rust 侧独有的形态。sqlx 的
            // MySQL NaiveDateTime 注释明写「assumes the connection's
            // `time_zone` is set to `+00:00` (UTC)」
            // （sqlx-mysql-0.8.6 src/types/chrono.rs:23），故此处
            // and_utc() 断言的 UTC 与驱动假设一致，不是编造。
            //
            // 权威侧依据见 WORKSPACE §132：元数据时间两端本就是 RFC3339，
            // spec 的 format:date-time 亦同源；数据列跟元数据走。
            return Value::String(v.and_utc().to_rfc3339());
        }
        if let Ok(v) = row.try_get::<NaiveDate, _>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(v) = row.try_get::<NaiveTime, _>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(v) = row.try_get::<String, _>(i) {
            return Value::String(v);
        }
        if let Ok(v) = row.try_get::<Vec<u8>, _>(i) {
            return polydb_db_core::blob_value(&v);
        }
        Value::String(format!("<{}>", raw.type_info().name()))
    }

    /// 语句类型判定（收敛到 db-core 公共版，方言固定为 MySQL）。
    ///
    /// 公共版按 token 类别判型（整词而非前缀），并认 MySQL 的方言词法：
    /// '#' 行注释、'--' 需跟空白、反斜杠转义、/*! … */ 可执行注释。
    pub fn detect_statement_type(sql: &str) -> StatementType {
        polydb_db_core::detect_statement_type(sql, Some(DatabaseKind::MySql))
    }

    fn columns_of(rows: &[MySqlRow]) -> Vec<ResultColumn> {
        let Some(first) = rows.first() else {
            return vec![];
        };
        first
            .columns()
            .iter()
            .map(|c| ResultColumn {
                name: c.name().to_string(),
                table: None,
                data_type: c.type_info().name().to_string(),
                generic_type: None,
                nullable: Some(true),
            })
            .collect()
    }

    /// 隔离级别 → MySQL 的 `SET TRANSACTION ISOLATION LEVEL` 取值。
    /// 四个级别 MySQL 全部支持（与 PG 侧不同，无需映射）。
    pub fn isolation_clause(level: IsolationLevel) -> &'static str {
        match level {
            IsolationLevel::ReadUncommitted => "READ UNCOMMITTED",
            IsolationLevel::ReadCommitted => "READ COMMITTED",
            IsolationLevel::RepeatableRead => "REPEATABLE READ",
            IsolationLevel::Serializable => "SERIALIZABLE",
        }
    }

    pub async fn begin_tx(&self, mode: TxMode) -> CoreResult<MySqlTxHandle> {
        let mut conn = self
            .pool
            .acquire()
            .await
            .map_err(|e| CoreError::Driver(format!("acquire conn: {e}")))?;
        // MySQL 没有 `BEGIN ISOLATION LEVEL …`（旧实现发的正是这句，对真实
        // 服务端必然语法错误）：SET TRANSACTION 的"下一事务"作用域绑定在
        // 连接上，所以必须先 acquire、SET、再 BEGIN，全程同一连接。
        sqlx::query(&format!(
            "SET TRANSACTION ISOLATION LEVEL {}",
            Self::isolation_clause(mode.isolation_level)
        ))
        .execute(&mut *conn)
        .await
        .map_err(|e| CoreError::Driver(format!("set isolation: {e}")))?;
        sqlx::mysql::MySqlTransactionManager::begin(&mut *conn, None)
            .await
            .map_err(|e| CoreError::Driver(format!("begin tx failed: {e}")))?;
        Ok(MySqlTxHandle {
            conn: Some(conn),
            in_tx: true,
        })
    }

    pub async fn commit(&self, tx: &mut MySqlTxHandle) -> CoreResult<()> {
        let conn = tx.conn.as_mut().expect("tx conn present");
        sqlx::mysql::MySqlTransactionManager::commit(conn)
            .await
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        tx.in_tx = false;
        Ok(())
    }

    pub async fn rollback(&self, tx: &mut MySqlTxHandle) -> CoreResult<()> {
        let conn = tx.conn.as_mut().expect("tx conn present");
        sqlx::mysql::MySqlTransactionManager::rollback(conn)
            .await
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        tx.in_tx = false;
        Ok(())
    }

    async fn execute_on<'e, E>(
        executor: E,
        sql: &str,
        params: &[Value],
        describe_pool: Option<&sqlx::MySqlPool>,
    ) -> CoreResult<QueryResult>
    where
        E: sqlx::Executor<'e, Database = sqlx::MySql>,
    {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let mut q = sqlx::query::<sqlx::MySql>(sql);
        for v in params {
            q = Self::bind_value(q, v);
        }

        let wants_rows = stmt_type == StatementType::Select
            || stmt_type == StatementType::Other
            || has_returning_clause(sql);
        if wants_rows {
            // 物化上限（polydb_db_core::row_cap，behavior.md §5）：流式 fetch
            // 到上限即停——fetch_all 会先把整个结果集拉进内存，大表在 server
            // 层截断之前就 OOM。truncated/has_more 与 apply_max_rows 同义。
            let mut stream = q.fetch(executor);
            let mut rows: Vec<sqlx::mysql::MySqlRow> = Vec::new();
            let mut truncated = false;
            use futures_util::StreamExt;
            while let Some(row) = stream.next().await {
                let row = row.map_err(|e| db_err(&e, "query failed"))?;
                if rows.len() >= polydb_db_core::row_cap() {
                    truncated = true;
                    break;
                }
                rows.push(row);
            }
            let mut columns = Self::columns_of(&rows);
            if columns.is_empty() {
                // A zero-row result still carries its column list (see the PG
                // driver): only the pool path can prepare-after-fetch, since
                // `fetch` consumes the executor.
                if let Some(pool) = describe_pool {
                    if let Ok(stmt) = pool.prepare(sql).await {
                        columns = stmt
                            .columns()
                            .iter()
                            .map(|c| ResultColumn {
                                name: c.name().to_string(),
                                table: None,
                                data_type: c.type_info().name().to_string(),
                                generic_type: None,
                                nullable: Some(true),
                            })
                            .collect();
                    }
                }
            }
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::my_value(r, i)).collect())
                .collect();
            Ok(QueryResult {
                columns,
                rows: data,
                affected_rows: 0,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated,
                total_rows: None,
                has_more: truncated,
                statement_type: Some(stmt_type),
            })
        } else {
            let res = q
                .execute(executor)
                .await
                .map_err(|e| db_err(&e, "execute failed"))?;
            Ok(QueryResult {
                columns: vec![],
                rows: vec![],
                affected_rows: res.rows_affected(),
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        }
    }

    pub async fn execute_in_tx(
        &self,
        tx: &mut MySqlTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let conn = tx.conn.as_mut().expect("tx conn present");
        Self::execute_on(&mut **conn, sql, params, None).await
    }
}

/// 剥离 SQL 开头的前导注释（收敛到 db-core 公共版，方言固定为 MySQL）。
/// 公共版认 MySQL 的 '#' 行注释（旧副本只认 '--' 与 '/* */'）。
pub fn strip_leading_comments(sql: &str) -> &str {
    polydb_db_core::strip_leading_comments(sql, Some(DatabaseKind::MySql))
}

#[async_trait]
impl DatabaseDriver for MySqlConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::MySql
    }

    async fn ping(&self) -> CoreResult<()> {
        sqlx::query("SELECT 1")
            .execute(&self.pool)
            .await
            .map_err(|e| CoreError::Driver(format!("ping failed: {e}")))?;
        Ok(())
    }

    async fn close(&self) -> CoreResult<()> {
        self.pool.close().await;
        Ok(())
    }

    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

/// 目录配方（C3）。`pub` 让本 crate 的 `catalog_contract` 测试直接断言 SQL 内容；
/// 跨语言契约由内核仓库的 `dbcore/spec/vectors/catalog-contract.json` 持有。
pub const TABLES_SQL: &str = "SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES \
     WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME";

pub const COLUMNS_SQL: &str = "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES', COLUMN_DEFAULT, EXTRA, ORDINAL_POSITION, \
            COLUMN_COMMENT \
     FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION";

#[async_trait]
impl SqlDriver for MySqlConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_MYSQL, schema, table, req)?;
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
            polydb_db_core::DIALECT_MYSQL,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<MySqlConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(MySqlConn::begin_tx(self, mode).await?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<MySqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MySqlConn::execute_in_tx(self, h, sql, params).await
    }

    async fn commit(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<MySqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MySqlConn::commit(self, h).await
    }

    async fn rollback(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<MySqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MySqlConn::rollback(self, h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        Self::execute_on(&self.pool, sql, params, Some(&self.pool)).await
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        let rows =
            sqlx::query("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
                .fetch_all(&self.pool)
                .await
                .map_err(|e| CoreError::Driver(format!("list_schemas: {e}")))?;
        Ok(rows
            .iter()
            .map(|r| SchemaInfo {
                name: r.get::<String, _>(0),
            })
            .collect())
    }

    async fn list_tables(&self, schema: &str) -> CoreResult<Vec<TableInfo>> {
        // C1：表注释与内核 `CatalogTables` 同源（`information_schema.TABLES.TABLE_COMMENT`）。
        let rows = sqlx::query(TABLES_SQL)
        .bind(schema)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_tables: {e}")))?;
        Ok(rows
            .iter()
            .map(|r| TableInfo {
                name: r.get::<String, _>(0),
                schema: schema.to_string(),
                table_type: if r.get::<String, _>(1) == "VIEW" {
                    TableType::View
                } else {
                    TableType::Table
                },
                row_count: None,
                comment: non_empty(r.get::<Option<String>, _>(2)),
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        // C1：列注释同理（`COLUMN_COMMENT`，内核 `CatalogColumns` 的同一列）。
        let rows = sqlx::query(COLUMNS_SQL)
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_columns: {e}")))?;

        let mut cols: Vec<ColumnInfo> = rows
            .iter()
            .map(|r| {
                let extra = r.get::<String, _>(4);
                ColumnInfo {
                    name: r.get::<String, _>(0),
                    data_type: r.get::<String, _>(1),
                    generic_type: None,
                    nullable: r.get::<bool, _>(2),
                    default_value: r.get::<Option<String>, _>(3),
                    max_length: None,
                    precision: None,
                    scale: None,
                    is_primary_key: false,
                    is_auto_increment: extra.to_lowercase().contains("auto_increment"),
                    comment: non_empty(r.get::<Option<String>, _>(6)),
                    ordinal_position: r.get::<u32, _>(5) as i32,
                }
            })
            .collect();

        let pk_rows = sqlx::query(
            "SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE \
             WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' ORDER BY ORDINAL_POSITION",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_columns pk: {e}")))?;

        let pk_set: std::collections::HashSet<String> =
            pk_rows.iter().map(|r| r.get::<String, _>(0)).collect();
        for c in cols.iter_mut() {
            if pk_set.contains(&c.name) {
                c.is_primary_key = true;
            }
        }
        Ok(cols)
    }

    async fn list_indexes(&self, schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        let rows = sqlx::query(
            "SELECT INDEX_NAME, NON_UNIQUE, INDEX_TYPE, SEQ_IN_INDEX, COLUMN_NAME \
             FROM information_schema.STATISTICS \
             WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_indexes: {e}")))?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (bool, bool, String, Vec<IndexColumn>)> = HashMap::new();
        for r in &rows {
            let name = r.get::<String, _>(0);
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.get::<i64, _>(1) == 0,
                        name == "PRIMARY",
                        r.get::<String, _>(2),
                        Vec::new(),
                    ),
                );
            }
            groups.get_mut(&name).unwrap().3.push(IndexColumn {
                name: r.get::<String, _>(4),
                position: r.get::<u64, _>(3) as i32,
                order: None,
                prefix_length: None,
            });
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (unique, primary, idx_type, columns) = groups.remove(&name).unwrap();
                IndexInfo {
                    name,
                    unique,
                    primary,
                    index_type: Some(match idx_type.to_uppercase().as_str() {
                        "HASH" => IndexType::Hash,
                        "FULLTEXT" => IndexType::Fulltext,
                        "SPATIAL" => IndexType::Spatial,
                        "BTREE" => IndexType::Btree,
                        _ => IndexType::Other,
                    }),
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
        let rows = sqlx::query(
            "SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME \
             FROM information_schema.KEY_COLUMN_USAGE \
             WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL \
             ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_foreign_keys: {e}")))?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (String, String, Vec<String>, Vec<String>)> =
            HashMap::new();
        for r in &rows {
            let name = r.get::<String, _>(0);
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.get::<String, _>(2),
                        r.get::<String, _>(3),
                        Vec::new(),
                        Vec::new(),
                    ),
                );
            }
            let group = groups.get_mut(&name).unwrap();
            group.2.push(r.get::<String, _>(1));
            group.3.push(r.get::<String, _>(4));
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
        // Two identifier binds, not one `schema.table` string: sqlx's `??`
        // escapes each bound value as a single SQL identifier, so binding
        // "mydb.mytable" produced `` `mydb.mytable` `` — one identifier named
        // literally "mydb.mytable", not a qualified schema.table reference.
        let row = sqlx::query("SHOW CREATE TABLE ??.??")
            .bind(schema)
            .bind(table)
            .fetch_one(&self.pool)
            .await
            .map_err(|e| CoreError::Driver(format!("create_table_sql: {e}")))?;
        Ok(row.get::<String, _>(1))
    }
}

// sqlx errors carry SQLSTATE + ErrorKind: the strongest evidence of the six.
fn db_err(e: &sqlx::Error, what: &str) -> CoreError {
    let text = format!("{what}: {e}");
    if let Some(db) = e.as_database_error() {
        let kind = match db.kind() {
            sqlx::error::ErrorKind::UniqueViolation => Some(DriverClass::Duplicate),
            sqlx::error::ErrorKind::ForeignKeyViolation
            | sqlx::error::ErrorKind::NotNullViolation
            | sqlx::error::ErrorKind::CheckViolation => Some(DriverClass::Constraint),
            _ => None,
        };
        return driver_error(db.code().as_deref(), None, kind, &text);
    }
    driver_error(None, None, None, &text)
}

/// Array/Object（含 $blob Object）参数的绑定形态：跨线协议统一为 JSON 文本——
/// 与 Go 侧 ValueToDriver 的 string(v.Raw()) 对齐。此前用 Debug repr
/// （format!("{v:?}")，形如 Object {"$blob": …}），与 JSON 同形不同义。
/// NaN/Inf 先经 polydb_db_core::value_to_json_text 替换为字符串，避免
/// serde_json 静默转 null（P2-3：非 SQLite 驱动此前缺此 sanitize）。
/// 独立成纯函数供口径测试下探。
fn json_text_param(v: &Value) -> String {
    polydb_db_core::value_to_json_text(v)
}

/// unsigned_bigint_value 把 MySQL `BIGINT UNSIGNED` 的 u64 值转成协议 Value。
///
/// 形态纪律（PD-17，2026-11-09）：**精确十进制字符串**，与 Go 侧
/// `DriverToValueTyped`（`go/pkg/dbcore/valueconv.go`）对齐——Go 侧对
/// 超 i64 的 BIGINT UNSIGNED，`ParseInt` 失败后落到末尾的
/// `NewStringValue(string(b))`，即字符串形态。
///
/// 为什么不返回 `Value::U64`：`Value::U64` 在 wire 上是**数字**，而 Go 侧
/// 同一场景发的是**字符串**；两端 wire 形态必须一致（AGENTS.md §14 双实现
/// 一致性红线），故此处用字符串。
///
/// 为什么不返回 `Value::Float`：f64 只有 53 位尾数，`u64::MAX`
/// （18446744073709551615）会被舍入成 18446744073709552000——静默丢精度。
fn unsigned_bigint_value(v: u64) -> Value {
    Value::String(v.to_string())
}

#[cfg(test)]
mod bind_contract_tests {
    use super::*;

    /// P1-4 口径钉子：Array 绑 JSON 文本（[1,"x"]），不是 Debug repr
    /// （[Integer(1), String("x")]）；$blob Object 同样以 JSON 文本走。
    #[test]
    fn array_and_object_bind_as_json_text() {
        let arr = Value::Array(vec![Value::Integer(1), Value::String("x".into())]);
        assert_eq!(json_text_param(&arr), r#"[1,"x"]"#);
        let blob = polydb_db_core::blob_value(&[0u8, 1, 2]);
        let s = json_text_param(&blob);
        assert!(
            s.starts_with('{') && s.contains("$blob"),
            "blob bind = {s:?}"
        );
        // Debug repr 的特征串不得出现：那是修复前的漂移形态。
        assert!(
            !json_text_param(&arr).contains("Integer"),
            "debug repr leaked"
        );
    }

    /// P2-3 回归：Array/Object 中的 NaN/Inf 必须序列化为字符串值，
    /// 不能被 serde_json 静默转为 null（数据丢失）。
    #[test]
    fn array_with_nan_inf_serializes_as_string() {
        let arr = Value::Array(vec![
            Value::Float(1.5),
            Value::Float(f64::NAN),
            Value::Float(f64::INFINITY),
        ]);
        let json = json_text_param(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed[0], 1.5);
        assert_eq!(
            parsed[1], "NaN",
            "NaN must be preserved as string, not null"
        );
        assert_eq!(parsed[2], "Infinity");
    }

    #[test]
    fn object_with_nan_serializes_as_string() {
        let mut map = std::collections::HashMap::new();
        map.insert("v".to_string(), Value::Float(f64::NAN));
        let json = json_text_param(&Value::Object(map));
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed["v"], "NaN");
    }

    /// PD-17 回归：`BIGINT UNSIGNED` 超 i64::MAX 的值必须以**精确十进制
    /// 字符串**产出，不得丢精度、不得落回类型名占位符。
    ///
    /// 缺陷背景：sqlx 的 i64 兼容检查排除 UNSIGNED flag
    /// （sqlx-mysql types/int.rs:int_compatible 的
    /// `&& !ty.flags.contains(ColumnFlags::UNSIGNED)`），try_get::<f64>
    /// 又只接受 Float|Double（float.rs:real_compatible）——整条读取链对
    /// 该列全部落空，旧实现最终返回 `Value::String("<LongLong>")`
    /// （类型名占位符，**数据完全丢失**）。
    ///
    /// Go 侧同场景产出精确十进制字符串（DriverToValueTyped ⇒
    /// NewStringValue），故 Rust 侧必须一致（AGENTS.md §14 红线）。
    ///
    /// 本用例锁定**产出形态**（不依赖真实 MySQL：本机无 MySQL 服务，
    /// 端到端由 test/contract 的 MySQL 场景覆盖，无服务时 soft-skip）。
    #[test]
    fn bigint_unsigned_overflow_keeps_exact_decimal_text() {
        // u64::MAX —— 超出 i64::MAX，且远超 f64 的 53 位尾数精度。
        let got = unsigned_bigint_value(u64::MAX);
        assert_eq!(
            got,
            Value::String("18446744073709551615".to_string()),
            "BIGINT UNSIGNED 超 i64 必须保留精确十进制文本"
        );

        // 不得是浮点（会丢低位）：u64::MAX 转 f64 得 18446744073709552000。
        assert_ne!(
            got,
            Value::Float(u64::MAX as f64),
            "不得用 f64 承载超 i64 的整数（静默丢精度）"
        );

        // 不得是类型名占位符（旧实现返回 "<LongLong>"）。
        let s = match &got {
            Value::String(s) => s.clone(),
            other => panic!("expected String, got {other:?}"),
        };
        assert!(
            !s.starts_with('<') || !s.ends_with('>'),
            "不得落回类型名占位符：{s}"
        );

        // i64::MAX + 1（恰好多 1，f64 已无法区分）也要精确。
        let just_over = i64::MAX as u64 + 1;
        assert_eq!(
            unsigned_bigint_value(just_over),
            Value::String("9223372036854775808".to_string()),
            "i64::MAX+1 必须精确（f64 在此已丢精度）"
        );

        // 范围内的值同样走字符串形态（与 Go 侧对 BIGINT UNSIGNED 的
        // 处理一致：Go 侧 ParseInt 成功 ⇒ NewIntValue 数字形态；
        // 但 Rust 侧 u64 分支只在 UNSIGNED 列命中，范围内值会被上面的
        // i64 分支先接住——故此处只断言超界值）。
    }

    /// PD-17 回归（读取链）：`my_value` 的读取链必须**真的**含 u64 分支。
    ///
    /// 上一条只测纯函数，把调用点删掉它仍会绿——变异证明实测如此。故此处
    /// 直接钉住读取链的源码形态：u64 分支必须在 i64 分支之后、f64 分支
    /// 之前，且其返回值必须经 `unsigned_bigint_value`（字符串形态）。
    #[test]
    fn my_value_read_chain_has_u64_branch() {
        let src = include_str!("lib.rs");
        let branch = "if let Ok(v) = row.try_get::<u64, _>(i) {\n            return unsigned_bigint_value(v);\n        }";
        let pos = src
            .find(branch)
            .unwrap_or_else(|| panic!("读取链缺少 u64 分支（或未走 unsigned_bigint_value）"));

        // 顺序：i64 分支 < u64 分支 < f64 分支。
        // （顺序有意义：u64 必须在 f64 之前，否则超界值会被 f64 先抢走。）
        let i64_pos = src.find("try_get::<i64, _>").expect("缺少 i64 分支");
        let f64_pos = src.find("try_get::<f64, _>").expect("缺少 f64 分支");
        assert!(
            i64_pos < pos,
            "u64 分支必须排在 i64 分支之后（否则普通整数会被 u64 抢走）"
        );
        assert!(
            pos < f64_pos,
            "u64 分支必须排在 f64 分支之前（否则超界值会被 f64 先抢走并丢精度）"
        );

        // 兜底占位符仍在（未匹配任何类型时的最后手段）——但 u64 分支必须
        // 在它之前，避免超界整数落到占位符。
        let placeholder = src
            .find("format!(\"<{}>\", raw.type_info().name())")
            .expect("缺少类型名占位符兜底");
        assert!(
            pos < placeholder,
            "u64 分支必须排在类型名占位符之前（否则超界整数落回占位符，数据全丢）"
        );
    }
}

#[cfg(test)]
mod catalog_contract {
    use super::*;

    fn collapse(s: &str) -> String {
        s.split_whitespace().collect::<Vec<_>>().join(" ").to_lowercase()
    }

    /// 契约向量（内核仓库持有）：本 crate 只断言自己的目录 SQL 常量。
    fn markers(engine: &str, kind: &str) -> Vec<String> {
        let p = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../../dbcore/spec/vectors/catalog-contract.json");
        let raw = std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()));
        let doc: serde_json::Value = serde_json::from_str(&raw).expect("parse catalog contract");
        doc["engines"][engine][kind]
            .as_array()
            .unwrap_or_else(|| panic!("向量缺 {engine}/{kind}"))
            .iter()
            .map(|m| collapse(m.as_str().expect("marker string")))
            .collect()
    }

    fn assert_markers(engine: &str, kind: &str, sql: &str) {
        let hay = collapse(sql);
        for m in markers(engine, kind) {
            assert!(
                hay.contains(&m),
                "{engine}/{kind}: 目录 SQL 缺 marker {m:?}（C1 注释 / C2 serial 自增这类漂移）\nSQL={sql}"
            );
        }
    }

    #[test]
    fn keeps_the_catalog_contract() {
        assert_markers("mysql", "tables", TABLES_SQL);
        assert_markers("mysql", "columns", COLUMNS_SQL);
    }
}