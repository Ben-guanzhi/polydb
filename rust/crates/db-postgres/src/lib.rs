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
use sqlx::{Column, Row, TypeInfo, ValueRef};

use polydb_core::error_classify::{driver_error, DriverClass};
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, IsolationLevel,
    QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo, TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

pub struct PostgresConn {
    pool: PgPool,
    _dsn: String,
}

/// 事务句柄，包装 sqlx::Transaction。
#[derive(Debug)]
pub struct PostgresTxHandle(pub sqlx::Transaction<'static, sqlx::Postgres>);

impl PostgresConn {
    /// DSN 形如 postgres://user@host:port/dbname（不含密码）。懒连接：真正建连发生在首次查询。
    pub fn open(dsn: &str) -> CoreResult<Self> {
        let pool = PgPoolOptions::new()
            .max_connections(4)
            .acquire_timeout(std::time::Duration::from_secs(5))
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
            Value::Float(f) => q.bind(*f),
            Value::String(s) => q.bind(s.clone()),
            _ => q.bind(format!("{v:?}")),
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
            return Value::String(v.format("%Y-%m-%d %H:%M:%S").to_string());
        }
        if let Ok(v) = row.try_get::<NaiveDateTime, _>(i) {
            return Value::String(v.format("%Y-%m-%d %H:%M:%S").to_string());
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
        Ok(PostgresTxHandle(tx))
    }

    pub async fn commit(&self, tx: PostgresTxHandle) -> CoreResult<()> {
        tx.0.commit()
            .await
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        Ok(())
    }

    pub async fn rollback(&self, tx: PostgresTxHandle) -> CoreResult<()> {
        tx.0.rollback()
            .await
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        Ok(())
    }

    /// 在给定 executor（pool 或 transaction）上跑一条 SQL。
    async fn execute_on<'e, E>(executor: E, sql: &str, params: &[Value]) -> CoreResult<QueryResult>
    where
        E: sqlx::Executor<'e, Database = sqlx::Postgres>,
    {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let mut q = sqlx::query::<sqlx::Postgres>(sql);
        for v in params {
            q = Self::bind_value(q, v);
        }

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
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
            let columns = Self::columns_of(&rows);
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
        Self::execute_on(tx.0.as_mut(), sql, params).await
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

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast::<PostgresTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        PostgresConn::commit(self, *h).await
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast::<PostgresTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        PostgresConn::rollback(self, *h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        Self::execute_on(&self.pool, sql, params).await
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
        let rows = sqlx::query(
            "SELECT c.relname, CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' ELSE 'table' END \
             FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace \
             WHERE n.nspname = $1 AND c.relkind IN ('r','v','m','p') ORDER BY c.relname",
        )
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
                comment: None,
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let rows = sqlx::query(
            "SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), \
                    COALESCE(a.attidentity <> '', a.attgenerated <> ''), a.attnum \
             FROM pg_catalog.pg_attribute a \
             JOIN pg_catalog.pg_class cl ON cl.oid = a.attrelid \
             JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
             LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum \
             WHERE n.nspname = $1 AND cl.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped \
             ORDER BY a.attnum",
        )
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
                comment: None,
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
        // auto_increment 口径**照抄逐表**（只 attidentity/attgenerated，不含
        // nextval）——逐表侧就是这么读的，批量若加 nextval 会与逐表不一致。
        let rows = sqlx::query(
            "SELECT cl.relname, a.attname, format_type(a.atttypid, a.atttypmod), \
                     NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), \
                     CASE WHEN EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k \
                                        WHERE k.conrelid = a.attrelid AND k.contype = 'p' \
                                          AND a.attnum = ANY(k.conkey)) THEN 1 ELSE 0 END, \
                     COALESCE(a.attidentity <> '', a.attgenerated <> ''), a.attnum \
              FROM pg_catalog.pg_attribute a \
              JOIN pg_catalog.pg_class cl ON cl.oid = a.attrelid \
              JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace \
              LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum \
              WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped \
              ORDER BY cl.relname, a.attnum",
        )
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
                        comment: None,
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
        let mut b = format!(
            "CREATE TABLE {}.{} (\n",
            quote_ident(schema),
            quote_ident(table)
        );
        for (i, col) in cols.iter().enumerate() {
            let suffix = if col.is_primary_key {
                " PRIMARY KEY"
            } else if col.is_auto_increment {
                " GENERATED ALWAYS AS IDENTITY"
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
