//! polydb-db-postgres：基于 sqlx 的 PostgreSQL 驱动实现（对应 Go 侧 dbpostgres）。

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use bigdecimal::BigDecimal;
use chrono::{DateTime, NaiveDate, NaiveDateTime, NaiveTime, Utc};
use sqlx::postgres::{PgPool, PgPoolOptions, PgRow};
use sqlx::types::uuid::Uuid;
use sqlx::types::Json;
use sqlx::{Column, Executor, Row, Statement, TypeInfo, ValueRef};

use polydb_core::error_classify::{driver_error, DriverClass};
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, IsolationLevel,
    QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo, TableType, Value,
};
use polydb_db_core::{has_returning_clause, non_empty, DatabaseDriver, SqlDriver, TxMode};

pub struct PostgresConn {
    pool: PgPool,
    _dsn: String,
}

/// 事务句柄，包装 sqlx::Transaction。P1-2 修复：内部用 Option 承载，
/// 使 commit/rollback 在 `&mut` 签名下能用 take() 取出 Transaction
/// （sqlx::Transaction::commit 按值接收，无法在 `&mut` 上直接调用）。
#[derive(Debug)]
pub struct PostgresTxHandle(pub Option<sqlx::Transaction<'static, sqlx::Postgres>>);

impl PostgresConn {
    /// DSN 形如 postgres://user@host:port/dbname（不含密码）。懒连接：真正建连发生在首次查询。
    pub fn open(dsn: &str) -> CoreResult<Self> {
        let pool = PgPoolOptions::new()
            // PR-01：与 MySQL 侧同理——事务钉住连接，4 槽会被 4 个事务吃光。
            // 8 槽 + app-core 的每连接 4 个事务上限，事务最多占一半。
            .max_connections(8)
            .acquire_timeout(std::time::Duration::from_secs(5))
            // 空闲回收：PG 的 idle_session_timeout / 代理都会杀空闲会话，
            // 别把半死连接留在池里（sqlx 默认 idle_timeout=None）。
            .idle_timeout(Some(std::time::Duration::from_secs(300)))
            // 语句级墙钟（PR-01）：PG 原生支持 statement_timeout（毫秒），
            // 与 MSSQL 的 300s 兜底对齐；角色级设置可覆盖它，那是运维选择。
            .after_connect(|conn, _meta| {
                Box::pin(async move {
                    let _ = sqlx::query("SET statement_timeout = 300000")
                        .execute(&mut *conn)
                        .await;
                    Ok(())
                })
            })
            .connect_lazy(dsn)
            .map_err(|e| CoreError::Driver(format!("open postgres: {e}")))?;
        Ok(Self {
            pool,
            _dsn: dsn.to_string(),
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：`PgPool` 是廉价 handle 克隆，
    /// 两个 `Arc<Self>` 共享同一个池。用于 app-core 持有第二个
    /// `Arc<dyn SqlDriver>`，与 `Connection` 内的驱动共享池资源。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            pool: self.pool.clone(),
            _dsn: self._dsn.clone(),
        })
    }

    fn bind_value<'q>(
        q: sqlx::query::Query<'q, sqlx::Postgres, sqlx::postgres::PgArguments>,
        v: &Value,
    ) -> sqlx::query::Query<'q, sqlx::Postgres, sqlx::postgres::PgArguments> {
        match v {
            Value::Null => q.bind(Option::<i64>::None),
            Value::Bool(b) => q.bind(*b),
            Value::Integer(i) => q.bind(*i),
            Value::U64(u) => {
                // sqlx Postgres 的 BIGINT 是 i64——u64 > i64::MAX 无法
                // 原生绑定。≤ i64::MAX 绑为 i64，> i64::MAX 绑为 f64
                // （≥2^53 丢失整数精度，但 Postgres NUMERIC 列的高精值
                // 走 String 路径不受影响）。
                if *u <= i64::MAX as u64 {
                    q.bind(*u as i64)
                } else {
                    q.bind(*u as f64)
                }
            }
            Value::Float(f) => q.bind(*f),
            Value::String(s) => q.bind(s.clone()),
            // Array/Object（含 $blob Object）跨线协议统一为 JSON 文本——与 Go
            // 侧 ValueToDriver 的 string(v.Raw()) 对齐。此前用 Debug repr，
            // 与其他引擎漂移。
            Value::Array(_) | Value::Object(_) => q.bind(json_text_param(v)),
        }
    }

    fn pg_value(row: &PgRow, i: usize) -> Value {
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
        // sqlx maps each wire width to its own Rust int, so only INT8 reaches
        // the i64 above. SERIAL is INT4 and SMALLSERIAL is INT2, which would
        // otherwise fall through to the "<INT4>" placeholder below and the value
        // would be replaced by its type name.
        if let Ok(v) = row.try_get::<i32, _>(i) {
            return Value::Integer(i64::from(v));
        }
        if let Ok(v) = row.try_get::<i16, _>(i) {
            return Value::Integer(i64::from(v));
        }
        if let Ok(v) = row.try_get::<f64, _>(i) {
            return Value::Float(v);
        }
        if let Ok(v) = row.try_get::<bool, _>(i) {
            return Value::Bool(v);
        }
        if let Ok(v) = row.try_get::<BigDecimal, _>(i) {
            return Value::String(v.to_string());
        }
        // uuid / json / jsonb: sqlx's try_get::<String> fails for these OIDs
        // (they don't implement Decode for String), so without explicit
        // branches they fall through to the <type> placeholder — the user
        // sees "<uuid>" instead of the actual value.
        if let Ok(v) = row.try_get::<Uuid, _>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(v) = row.try_get::<Json<serde_json::Value>, _>(i) {
            return Value::String(v.0.to_string());
        }
        if let Ok(v) = row.try_get::<DateTime<Utc>, _>(i) {
            // RFC3339Nano —— 与 Go 侧同形（PD-20，2026-11-10）。
            //
            // 旧的 "%Y-%m-%d %H:%M:%S" 是 Rust 侧独有的形态：Go 侧的
            // time.Time 走 RFC3339Nano（pdb.DriverToValue 的 time.Time 分支），
            // 而 pgx 把无时区的 PG `timestamp` 按 **UTC 挂钟**解读
            // （pgtype/timestamp.go:120 ParseInLocation(..., time.UTC)）
            // —— 故此处 to_rfc3339 断言的 UTC 与 Go 侧是同一语义，不是编造。
            //
            // 权威侧的依据见 WORKSPACE §132：元数据时间（created_at 等）
            // 两端本就是 RFC3339（Rust to_rfc3339 / Go time.Time 序列化），
            // spec 的 format:date-time 亦同源；数据列跟元数据走，
            // 全局只有一个时间拼写。
            return Value::String(v.to_rfc3339());
        }
        if let Ok(v) = row.try_get::<NaiveDateTime, _>(i) {
            // 同上：PG `timestamp`（无时区）被两端都按 UTC 挂钟解读，
            // 故补 Z 后缀是陈述既有假设。
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

    /// 与 Go 侧 dbcore.DetectStatementType 对齐的语句类型检测。
    ///
    /// 收敛到 db-core 公共版（方言固定为 PostgreSQL）：公共版按 token 类别
    /// 判型（整词而非前缀），并认 PG 的方言词法（dollar-quote、E'…' 转义）。
    fn detect_statement_type(sql: &str) -> StatementType {
        polydb_db_core::detect_statement_type(sql, Some(DatabaseKind::Postgres))
    }

    fn columns_of(rows: &[PgRow]) -> Vec<ResultColumn> {
        let Some(first) = rows.first() else {
            return vec![];
        };
        Self::columns_from_row(first)
    }

    /// Column metadata from one row's cursor description. sqlx carries the
    /// column list on every Row even when the result set is empty, so a
    /// zero-row SELECT keeps its columns (the Go kernel reads ColumnTypes()
    /// before iterating — same contract).
    fn columns_from_row(row: &PgRow) -> Vec<ResultColumn> {
        row.columns()
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

    /// 将 TxMode.IsolationLevel 翻译成 sqlx 的 BEGIN 语句；仅当调用方显式请求
    /// 隔离级别时才生成 statement，默认走 sqlx 的 BEGIN 语句。
    fn begin_statement(mode: TxMode) -> Option<String> {
        match mode.isolation_level {
            IsolationLevel::ReadCommitted => {
                Some("BEGIN ISOLATION LEVEL READ COMMITTED".to_string())
            }
            IsolationLevel::Serializable => Some("BEGIN ISOLATION LEVEL SERIALIZABLE".to_string()),
            IsolationLevel::ReadUncommitted => {
                Some("BEGIN ISOLATION LEVEL READ UNCOMMITTED".to_string())
            }
            IsolationLevel::RepeatableRead => {
                Some("BEGIN ISOLATION LEVEL REPEATABLE READ".to_string())
            }
        }
    }

    pub async fn begin_tx(&self, mode: TxMode) -> CoreResult<PostgresTxHandle> {
        let tx = match Self::begin_statement(mode) {
            Some(st) => self.pool.begin_with(st).await,
            None => self.pool.begin().await,
        };
        let tx = tx.map_err(|e| CoreError::Driver(format!("begin tx failed: {e}")))?;
        Ok(PostgresTxHandle(Some(tx)))
    }

    pub async fn commit(&self, tx: &mut PostgresTxHandle) -> CoreResult<()> {
        // P1-2：take() 取出 Transaction（sqlx commit 按值接收）。
        // 失败时 Transaction 已消费，但 handle 仍留在 entry 里（Box 未被
        // 取出），rollback 会得到 "tx handle already consumed" 而非
        // 误导性的 "tx already rolled back"。
        let inner =
            tx.0.take()
                .ok_or_else(|| CoreError::Internal("tx handle already consumed".into()))?;
        inner
            .commit()
            .await
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        Ok(())
    }

    pub async fn rollback(&self, tx: &mut PostgresTxHandle) -> CoreResult<()> {
        let inner =
            tx.0.take()
                .ok_or_else(|| CoreError::Internal("tx handle already consumed".into()))?;
        inner
            .rollback()
            .await
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        Ok(())
    }

    /// 在给定 executor（pool 或 transaction）上跑一条 SQL。
    ///
    /// `describe_pool` 是零行结果集的列元数据回落：sqlx 的 `fetch` 消耗掉
    /// executor，无法在其上再 prepare；但 pool 可以（`&Pool` 实现 Executor）。
    /// 事务路径传 None——空结果的列列表是当时诚实的答案，稍后可以在提交前
    /// 用另一条连接补。
    async fn execute_on<'e, E>(
        executor: E,
        sql: &str,
        params: &[Value],
        describe_pool: Option<&sqlx::PgPool>,
    ) -> CoreResult<QueryResult>
    where
        E: sqlx::Executor<'e, Database = sqlx::Postgres>,
    {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let mut q = sqlx::query::<sqlx::Postgres>(sql);
        for v in params {
            q = Self::bind_value(q, v);
        }

        // SELECT, and any DML carrying RETURNING, produces a result set: the
        // RETURNING rows are the answer, not a side effect. Routing them to
        // q.execute() silently dropped the generated ids (the Go kernel's
        // IsReadQuery routes RETURNING to the query path the same way).
        let wants_rows = stmt_type == StatementType::Select
            || stmt_type == StatementType::Other
            || has_returning_clause(sql);
        if wants_rows {
            // 物化上限（polydb_db_core::row_cap，behavior.md §5）：流式 fetch
            // 到上限即停——fetch_all 会先把整个结果集拉进内存，大表在 server
            // 层截断之前就 OOM。truncated/has_more 与 apply_max_rows 同义。
            let mut stream = q.fetch(executor);
            let mut rows: Vec<sqlx::postgres::PgRow> = Vec::new();
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
                // A zero-row result still carries its column list: the grid
                // needs the shape to render an empty table. sqlx's Executor::
                // prepare returns the statement's column metadata without
                // executing it, so the empty case recovers what the Go kernel
                // reads from rows.ColumnTypes() before iterating. Only the
                // pool path can do this (the executor itself was consumed by
                // fetch); a transaction's empty result keeps its empty column
                // list.
                if let Some(pool) = describe_pool {
                    match pool.prepare(sql).await {
                        Ok(stmt) => {
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
                        Err(_) => {
                            // PREPARE can fail for statements the engine
                            // cannot prepare (e.g. multi-statement text);
                            // the empty column list is then the honest answer.
                        }
                    }
                }
            }
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::pg_value(r, i)).collect())
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
        tx: &mut PostgresTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let inner =
            tx.0.as_mut()
                .ok_or_else(|| CoreError::Internal("tx handle already consumed".into()))?;
        // sqlx 0.8: &mut Transaction 不实现 Executor，但 &mut PgConnection 实现。
        // Transaction 通过 Deref 暴露 PgConnection，所以 &mut **inner 即可。
        Self::execute_on(&mut **inner, sql, params, None).await
    }
}

fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

#[async_trait]
impl DatabaseDriver for PostgresConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Postgres
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

/// 目录配方（C3）。`pub` 是为了让本 crate 的 `catalog_contract` 测试直接断言 SQL
/// 内容——跨语言契约由内核仓库的 `dbcore/spec/vectors/catalog-contract.json` 持有，
/// Go 侧断言**生成 SQL**，这边断言这三个常量。
pub const TABLES_SQL: &str =
    "SELECT c.relname, CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' ELSE 'table' END, \
            COALESCE(obj_description(c.oid, 'pg_class'), '') \
     FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace \
     WHERE n.nspname = $1 AND c.relkind IN ('r','v','m','p') ORDER BY c.relname";

pub const COLUMNS_SQL: &str =
    "SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), \
            CASE WHEN COALESCE(a.attidentity <> '', false) \
                   OR COALESCE(a.attgenerated <> '', false) \
                   OR COALESCE(pg_get_expr(d.adbin, d.adrelid) LIKE 'nextval(%', false) \
                 THEN true ELSE false END, a.attnum, \
            COALESCE(col_description(a.attrelid, a.attnum), '') \
     FROM pg_catalog.pg_attribute a \
     JOIN pg_catalog.pg_class cl ON cl.oid = a.attrelid \
     JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
     LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum \
     WHERE n.nspname = $1 AND cl.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped \
     ORDER BY a.attnum";

pub const BATCH_COLUMNS_SQL: &str =
    "SELECT cl.relname, a.attname, format_type(a.atttypid, a.atttypmod), \
             NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), \
             CASE WHEN EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k \
                                WHERE k.conrelid = a.attrelid AND k.contype = 'p' \
                                  AND a.attnum = ANY(k.conkey)) THEN 1 ELSE 0 END, \
             CASE WHEN COALESCE(a.attidentity <> '', false) \
                    OR COALESCE(a.attgenerated <> '', false) \
                    OR COALESCE(pg_get_expr(d.adbin, d.adrelid) LIKE 'nextval(%', false) \
                  THEN true ELSE false END, a.attnum, \
             COALESCE(col_description(a.attrelid, a.attnum), '') \
      FROM pg_catalog.pg_attribute a \
      JOIN pg_catalog.pg_class cl ON cl.oid = a.attrelid \
      JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
      LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum \
      WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped \
      ORDER BY cl.relname, a.attnum";

#[async_trait]
impl SqlDriver for PostgresConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_DEFAULT, schema, table, req)?;
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
            polydb_db_core::DIALECT_DEFAULT,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<PostgresConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(PostgresConn::begin_tx(self, mode).await?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<PostgresTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        PostgresConn::execute_in_tx(self, h, sql, params).await
    }

    async fn commit(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<PostgresTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        PostgresConn::commit(self, h).await
    }

    async fn rollback(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<PostgresTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        PostgresConn::rollback(self, h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        Self::execute_on(&self.pool, sql, params, Some(&self.pool)).await
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        let rows = sqlx::query(
            "SELECT nspname FROM pg_catalog.pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname",
        )
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
        // C1：表注释与内核 `CatalogTables` 同源——`obj_description(oid,'pg_class')`。
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
                table_type: match r.get::<String, _>(1).as_str() {
                    "view" => TableType::View,
                    "materialized_view" => TableType::MaterializedView,
                    _ => TableType::Table,
                },
                row_count: None,
                comment: non_empty(r.get::<Option<String>, _>(2)),
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let rows = sqlx::query(COLUMNS_SQL)
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_columns: {e}")))?;

        let mut cols: Vec<ColumnInfo> = rows
            .iter()
            .map(|r| ColumnInfo {
                name: r.get::<String, _>(0),
                data_type: r.get::<String, _>(1),
                generic_type: None,
                nullable: r.get::<bool, _>(2),
                default_value: r.get::<Option<String>, _>(3),
                max_length: None,
                precision: None,
                scale: None,
                is_primary_key: false,
                is_auto_increment: r.get::<bool, _>(4),
                // C1：列注释与内核 `CatalogColumns` 同源（col_description）。
                comment: non_empty(r.get::<Option<String>, _>(6)),
                // attnum 在 pg_catalog 里是 int2（不是 int4）：sqlx 严格按 SQL 类型解码，
                // 用 i32 读会 ColumnDecode 失败（探测 p10 钉下的真值）。先读 i16 再转。
                ordinal_position: r.get::<i16, _>(5) as i32,
            })
            .collect();

        let pk_rows = sqlx::query(
            "SELECT a.attname FROM pg_catalog.pg_index i \
             JOIN pg_catalog.pg_class cl ON cl.oid = i.indrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             JOIN pg_catalog.pg_attribute a ON a.attrelid = cl.oid AND a.attnum = ANY(i.indkey) \
             WHERE n.nspname = $1 AND cl.relname = $2 AND i.indisprimary",
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
        // 索引列表与索引列分两次往返（原先是 1 次列表 + 每索引 1 次内层 = N+1）。
        // 第二查询一次取回本表所有索引的列，再按索引名分组。
        //
        // 列 join 的目标是**表**（cl = indrelid），不是索引：`indkey` 存的是被索引
        // 表列的 attnum，而索引自身的 pg_attribute attnum 是索引内位置——只有当被索引
        // 列恰是表第 1 列时两者才巧合相等（p8 取证：p8_pkey 读到 id，p8_code_key 读空）。
        // 表达式索引列（indkey 里的 0）在 pg_attribute 无匹配行，被 INNER JOIN 丢弃——
        // 与逐索引内层查询语义一致（等价契约见 drivertest）。
        let rows = sqlx::query(
            "SELECT c.relname, i.indisunique, i.indisprimary \
             FROM pg_catalog.pg_index i \
             JOIN pg_catalog.pg_class cl ON cl.oid = i.indrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid \
             WHERE n.nspname = $1 AND cl.relname = $2 ORDER BY c.relname",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("list_indexes: {e}")))?;

        let col_rows = sqlx::query(
            "SELECT c.relname, ik.N, a.attname \
             FROM pg_catalog.pg_index i \
             JOIN pg_catalog.pg_class cl ON cl.oid = i.indrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid \
             CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS ik(attnum, N) \
             JOIN pg_catalog.pg_attribute a ON a.attrelid = cl.oid AND a.attnum = ik.attnum \
             WHERE n.nspname = $1 AND cl.relname = $2 ORDER BY c.relname, ik.N",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("index columns: {e}")))?;

        // 按索引名分组：行序由 ORDER BY c.relname, ik.N 保证，故索引序与列序都稳定；
        // 输出顺序仍以第一查询的 rows（按 relname）为准。
        let mut cols_by_index: std::collections::HashMap<String, Vec<polydb_core::IndexColumn>> =
            std::collections::HashMap::new();
        for cr in &col_rows {
            cols_by_index
                .entry(cr.get::<String, _>(0))
                .or_default()
                .push(polydb_core::IndexColumn {
                    name: cr.get::<String, _>(2),
                    // WITH ORDINALITY 的序号列是 bigint（INT8），不是 int4：
                    // sqlx 严格按 SQL 类型解码，读 i32 会 ColumnDecode 失败。
                    position: cr.get::<i64, _>(1) as i32,
                    order: None,
                    prefix_length: None,
                });
        }

        let mut out = Vec::with_capacity(rows.len());
        for r in &rows {
            let name = r.get::<String, _>(0);
            // 先取列再 move name 进 IndexInfo（remove 需要 &name）。
            let columns = cols_by_index.remove(&name).unwrap_or_default();
            out.push(IndexInfo {
                name,
                unique: r.get::<bool, _>(1),
                primary: r.get::<bool, _>(2),
                index_type: None,
                columns,
                comment: None,
            });
        }
        Ok(out)
    }

    async fn list_foreign_keys(
        &self,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        let rows = sqlx::query(
            "SELECT con.conname, fka.attname, ns.nspname, rel.relname, pka.attname \
             FROM pg_catalog.pg_constraint con \
             JOIN pg_catalog.pg_class cl ON cl.oid = con.conrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS fk(attnum, N) \
             JOIN pg_catalog.pg_attribute fka ON fka.attrelid = cl.oid AND fka.attnum = fk.attnum \
             JOIN pg_catalog.pg_class rel ON rel.oid = con.confrelid \
             JOIN pg_catalog.pg_namespace ns ON ns.oid = rel.relnamespace \
             CROSS JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS pk(attnum, N2) \
             JOIN pg_catalog.pg_attribute pka ON pka.attrelid = rel.oid AND pka.attnum = pk.attnum \
             WHERE con.contype = 'f' AND n.nspname = $1 AND cl.relname = $2 AND fk.N = pk.N2 \
             ORDER BY con.conname, fk.N",
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

    async fn batch_columns(&self, schema: &str) -> CoreResult<Option<Vec<(String, ColumnInfo)>>> {
        // 批量列配方 = 逐表 `list_columns` 去掉表过滤、relname 前置（Go PG
        // `CatalogBatchColumns` 的 Rust 对应物）。表达式与 `list_columns` 逐列一致，
        // 故批量结果与逐表结果行对行相等（等价契约见 equivalence 测试）。
        //
        // 主键一次算出（EXISTS pg_constraint contype='p'），免去逐表的第二次查询；
        // auto_increment 口径**照抄逐表**（C2：含 nextval serial 判定）——两侧
        // 表达式必须同形，否则批量与逐表对同一列给出不同的 is_auto_increment
        //（等价契约测试会红）。
        let rows = sqlx::query(BATCH_COLUMNS_SQL)
        .bind(schema)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("batch_columns: {e}")))?;

        Ok(Some(
            rows.iter()
                .map(|r| {
                    // attnum 是 int2（见 list_columns 注释）。
                    let col = ColumnInfo {
                        name: r.get::<String, _>(1),
                        data_type: r.get::<String, _>(2),
                        generic_type: None,
                        nullable: r.get::<bool, _>(3),
                        default_value: r.get::<Option<String>, _>(4),
                        max_length: None,
                        precision: None,
                        scale: None,
                        is_primary_key: r.get::<i32, _>(5) != 0,
                        is_auto_increment: r.get::<bool, _>(6),
                        // C1：与逐表同源的注释列（批量/逐表等价契约会比对这一列）。
                        comment: non_empty(r.get::<Option<String>, _>(8)),
                        ordinal_position: r.get::<i16, _>(7) as i32,
                    };
                    (r.get::<String, _>(0), col)
                })
                .collect(),
        ))
    }

    async fn batch_foreign_keys(
        &self,
        schema: &str,
    ) -> CoreResult<Option<Vec<(String, Vec<ForeignKeyInfo>)>>> {
        // 批量外键配方 = 逐表 `list_foreign_keys` 去掉表过滤、relname 前置，一次
        // 往返拿全 schema。按 (表名, conname) 分组；conname 在 schema 内唯一，
        // 故 (表, conname) 是安全键。列序由 ORDER BY 保证，分组序 = 首次出现序。
        let rows = sqlx::query(
            "SELECT cl.relname, con.conname, fka.attname, ns.nspname, rel.relname, pka.attname \
             FROM pg_catalog.pg_constraint con \
             JOIN pg_catalog.pg_class cl ON cl.oid = con.conrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS fk(attnum, N) \
             JOIN pg_catalog.pg_attribute fka ON fka.attrelid = cl.oid AND fka.attnum = fk.attnum \
             JOIN pg_catalog.pg_class rel ON rel.oid = con.confrelid \
             JOIN pg_catalog.pg_namespace ns ON ns.oid = rel.relnamespace \
             CROSS JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS pk(attnum, N2) \
             JOIN pg_catalog.pg_attribute pka ON pka.attrelid = rel.oid AND pka.attnum = pk.attnum \
             WHERE con.contype = 'f' AND n.nspname = $1 AND fk.N = pk.N2 \
             ORDER BY cl.relname, con.conname, fk.N",
        )
        .bind(schema)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("batch_foreign_keys: {e}")))?;

        // 行序 = ORDER BY (表, conname, fk.N)，故首次出现序即表序、同表内 conname 序。
        let mut order: Vec<(String, String)> = Vec::new();
        let mut groups: HashMap<(String, String), (String, String, Vec<String>, Vec<String>)> =
            HashMap::new();
        for r in &rows {
            let table = r.get::<String, _>(0);
            let conname = r.get::<String, _>(1);
            let key = (table.clone(), conname.clone());
            if !groups.contains_key(&key) {
                order.push(key.clone());
                groups.insert(
                    key.clone(),
                    (
                        r.get::<String, _>(3),
                        r.get::<String, _>(4),
                        Vec::new(),
                        Vec::new(),
                    ),
                );
            }
            let g = groups.get_mut(&key).unwrap();
            g.2.push(r.get::<String, _>(2));
            g.3.push(r.get::<String, _>(5));
        }
        // 按表聚合（表序 = 首次出现序，外键序 = conname 序）。
        // ORDER BY 保证同表行连续，故只需看 out 末项的表名。
        let mut out: Vec<(String, Vec<ForeignKeyInfo>)> = Vec::new();
        for (table, conname) in &order {
            let (ref_schema, ref_table, cols, ref_cols) =
                groups.remove(&(table.clone(), conname.clone())).unwrap();
            let fk = ForeignKeyInfo {
                name: conname.clone(),
                columns: cols,
                referenced_schema: ref_schema,
                referenced_table: ref_table,
                referenced_columns: ref_cols,
                on_update: None,
                on_delete: None,
            };
            match out.last_mut() {
                Some((last_table, fks)) if last_table == table => fks.push(fk),
                _ => out.push((table.clone(), vec![fk])),
            }
        }
        Ok(Some(out))
    }

    async fn create_table_sql(&self, schema: &str, table: &str) -> CoreResult<String> {
        let cols = self.list_columns(schema, table).await?;
        if cols.is_empty() {
            return Err(CoreError::Driver(format!(
                "table not found: {schema}.{table}"
            )));
        }
        // C4：与内核 `CreateTableSQL` 同形状——列里**不**内联主键（复合主键会
        // 生成两个 PRIMARY KEY = 非法 SQL），主键走表级
        // `;\nALTER TABLE … ADD CONSTRAINT <name> PRIMARY KEY (…)`；
        // 收尾不带分号（与 Go 侧逐字一致）。
        let qualified = format!("{}.{}", quote_ident(schema), quote_ident(table));
        let pk = self.primary_key_constraint(schema, table).await?;
        Ok(render_create_table(
            &qualified,
            &cols,
            pk.as_ref().map(|(n, c)| (n.as_str(), c.as_slice())),
        ))
    }
}

impl PostgresConn {
    /// 主键约束名 + 按约束顺序排列的列（C4）。内核用 `CatalogPrimaryKey` 单独
    /// 查这一条；`list_columns` 的 `is_primary_key` 只有列标记，没有约束名。
    async fn primary_key_constraint(
        &self,
        schema: &str,
        table: &str,
    ) -> CoreResult<Option<(String, Vec<String>)>> {
        let rows = sqlx::query(
            "SELECT k.conname, a.attname \
             FROM pg_catalog.pg_constraint k \
             JOIN pg_catalog.pg_class cl ON cl.oid = k.conrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             CROSS JOIN LATERAL unnest(k.conkey) WITH ORDINALITY AS ck(attnum, N) \
             JOIN pg_catalog.pg_attribute a ON a.attrelid = cl.oid AND a.attnum = ck.attnum \
             WHERE k.contype = 'p' AND n.nspname = $1 AND cl.relname = $2 \
             ORDER BY ck.N",
        )
        .bind(schema)
        .bind(table)
        .fetch_all(&self.pool)
        .await
        .map_err(|e| CoreError::Driver(format!("primary key: {e}")))?;
        let Some(first) = rows.first() else {
            return Ok(None);
        };
        let name: String = first.get(0);
        let cols: Vec<String> = rows.iter().map(|r| r.get::<String, _>(1)).collect();
        Ok(Some((name, cols)))
    }
}

/// 整段 DDL 装配（纯函数）：`CREATE TABLE …(列…)`，主键存在时追加表级
/// `;\nALTER TABLE … ADD CONSTRAINT … PRIMARY KEY (…)`。与内核
/// `CreateTableSQL`（`dbcore/postgres/postgres.go:323-362`）同形状。
fn render_create_table(
    qualified: &str,
    cols: &[ColumnInfo],
    pk: Option<(&str, &[String])>,
) -> String {
    let mut out = format!("CREATE TABLE {qualified} (\n{})", render_create_columns(cols));
    if let Some((name, pk_cols)) = pk {
        let quoted: Vec<String> = pk_cols.iter().map(|c| quote_ident(c)).collect();
        out.push_str(&format!(
            ";\nALTER TABLE {qualified} ADD CONSTRAINT {} PRIMARY KEY ({})",
            quote_ident(name),
            quoted.join(", ")
        ));
    }
    out
}

/// 列渲染（纯函数，可无服务端测试）。与内核 `renderCreateColumns`
/// （`dbcore/postgres/postgres.go:371-396`）同形状：
/// `    <ident> <type>[ GENERATED ALWAYS AS IDENTITY][ NULL| NOT NULL][ DEFAULT <expr>]`
///
/// 两条口径不能丢：
/// - 只有**真正的 identity 列**发 `GENERATED ALWAYS AS IDENTITY`——serial 列的
///   默认值已经是序列机制，再套这个子句会被 PostgreSQL 直接拒绝
///   （"cannot create identity column of type serial"）；
/// - 主键**不**在这里内联（C4）：复合主键会让每一列各带一个 `PRIMARY KEY`，
///   PostgreSQL 报 "multiple primary keys"；约束由调用方以表级 ALTER 追加。
fn render_create_columns(cols: &[ColumnInfo]) -> String {
    let mut b = String::new();
    let last = cols.len().saturating_sub(1);
    for (i, col) in cols.iter().enumerate() {
        let serial_default = col
            .default_value
            .as_deref()
            .map(|d| d.trim_start().starts_with("nextval("))
            .unwrap_or(false);
        b.push_str("    ");
        b.push_str(&quote_ident(&col.name));
        b.push(' ');
        b.push_str(&col.data_type);
        if col.is_auto_increment && !serial_default {
            b.push_str(" GENERATED ALWAYS AS IDENTITY");
        }
        if col.nullable {
            b.push_str(" NULL");
        } else {
            b.push_str(" NOT NULL");
        }
        if let Some(d) = col.default_value.as_deref() {
            if !d.is_empty() {
                b.push_str(" DEFAULT ");
                b.push_str(d);
            }
        }
        if i < last {
            b.push(',');
        }
        b.push('\n');
    }
    b
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
/// 与 Go 侧 ValueToDriver 的 string(v.Raw()) 对齐。此前用 Debug repr，与其他
/// 引擎漂移。NaN/Inf 先经 polydb_db_core::value_to_json_text 替换为字符串，
/// 避免 serde_json 静默转 null（P2-3）。
/// 独立成纯函数供口径测试下探。
fn json_text_param(v: &Value) -> String {
    polydb_db_core::value_to_json_text(v)
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
}

#[cfg(test)]
mod create_ddl_tests {
    use super::*;

    fn col(name: &str, ty: &str, auto: bool, default: Option<&str>) -> ColumnInfo {
        ColumnInfo {
            name: name.to_string(),
            data_type: ty.to_string(),
            generic_type: None,
            nullable: true,
            default_value: default.map(|s| s.to_string()),
            max_length: None,
            precision: None,
            scale: None,
            is_primary_key: false,
            is_auto_increment: auto,
            comment: None,
            ordinal_position: 1,
        }
    }

    fn not_null(name: &str, ty: &str) -> ColumnInfo {
        ColumnInfo {
            nullable: false,
            ..col(name, ty, false, None)
        }
    }

    /// C2：serial 列（默认值 nextval(...)）必须**不**发 IDENTITY——PostgreSQL
    /// 对这个组合直接报 "cannot create identity column of type serial"。
    /// 真正的 identity 列（默认值不是 nextval）继续发。
    #[test]
    fn serial_never_emits_identity_clause() {
        let serial = col("id", "integer", true, Some("nextval('t_id_seq'::regclass)"));
        let out = render_create_columns(&[serial]);
        assert!(
            !out.contains("IDENTITY"),
            "serial column must not emit IDENTITY: {out}"
        );

        let identity = col("id", "integer", true, None);
        let out = render_create_columns(&[identity]);
        assert!(
            out.contains("GENERATED ALWAYS AS IDENTITY"),
            "identity column must keep the clause: {out}"
        );
    }

    /// C4：主键不进列定义，改走表级 `ALTER TABLE … ADD CONSTRAINT … PRIMARY KEY`。
    /// 旧实现给每一列各加一个 `PRIMARY KEY`——复合主键直接是非法 SQL
    ///（"multiple primary keys"），而且把 `NOT NULL` 顶掉了。
    #[test]
    fn primary_key_is_table_level_not_inline() {
        let cols = vec![not_null("a", "int"), not_null("b", "int")];
        let pk = vec!["a".to_string(), "b".to_string()];
        let out = render_create_table("\"s\".\"t\"", &cols, Some(("t_pkey", pk.as_slice())));
        assert!(
            !out.contains("int PRIMARY KEY"),
            "primary key must not be inlined into a column: {out}"
        );
        assert!(
            out.contains("ALTER TABLE \"s\".\"t\" ADD CONSTRAINT \"t_pkey\" PRIMARY KEY (\"a\", \"b\")"),
            "table-level constraint missing: {out}"
        );
        // 列本身仍要保留 NOT NULL（旧实现被 PRIMARY KEY 顶掉）。
        assert!(out.contains("\"a\" int NOT NULL"), "{out}");
    }

    /// C4：`DEFAULT` 必须渲染——旧实现整段丢弃，serial 列的序列默认值就丢了。
    #[test]
    fn defaults_are_rendered() {
        let cols = vec![col("note", "text", false, Some("'x'::text"))];
        let out = render_create_table("\"s\".\"t\"", &cols, None);
        assert!(
            out.contains("\"note\" text NULL DEFAULT 'x'::text"),
            "default missing: {out}"
        );
        // 无主键时与内核一致：收尾只有 `)`，不追加 ALTER。
        assert!(!out.contains("ALTER TABLE"), "{out}");
    }
}

#[cfg(test)]
mod catalog_contract {
    use super::*;

    /// 契约向量（内核仓库持有）：本 crate 只断言自己的三个 SQL 常量。
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

    fn collapse(s: &str) -> String {
        s.split_whitespace().collect::<Vec<_>>().join(" ").to_lowercase()
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
        assert_markers("postgres", "tables", TABLES_SQL);
        assert_markers("postgres", "columns", COLUMNS_SQL);
        assert_markers("postgres", "batch_columns", BATCH_COLUMNS_SQL);
    }
}
