//! polydb-db-mysql：基于 sqlx 的 MySQL 驱动实现（对应 Go 侧 dbmysql）。

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use bigdecimal::BigDecimal;
use chrono::{NaiveDate, NaiveDateTime, NaiveTime};
use sqlx::mysql::{MySqlPool, MySqlPoolOptions, MySqlRow};
use sqlx::{Column, Row, TypeInfo, ValueRef};

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

pub struct MySqlConn {
    pool: MySqlPool,
    _dsn: String,
}

/// 事务句柄，包装 sqlx::Transaction。Transaction 需自持所有权（commit/rollback 消费 self），
/// 因此 handle 也按值传递；app-core 的 ActiveTx::commit/rollback 通过 take() 从 Option 取出。
#[derive(Debug)]
pub struct MySqlTxHandle(pub sqlx::Transaction<'static, sqlx::MySql>);

impl MySqlConn {
    /// DSN 形如 mysql://user@host:port/dbname（不含密码）。懒连接。
    pub fn open(dsn: &str) -> CoreResult<Self> {
        let pool = MySqlPoolOptions::new()
            .max_connections(4)
            .acquire_timeout(std::time::Duration::from_secs(5))
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

    fn bind_value<'q>(
        q: sqlx::query::Query<'q, sqlx::MySql, sqlx::mysql::MySqlArguments>,
        v: &Value,
    ) -> sqlx::query::Query<'q, sqlx::MySql, sqlx::mysql::MySqlArguments> {
        match v {
            Value::Null => q.bind(Option::<i64>::None),
            Value::Bool(b) => q.bind(*b),
            Value::Integer(i) => q.bind(*i),
            Value::Float(f) => q.bind(*f),
            Value::String(s) => q.bind(s.clone()),
            _ => q.bind(format!("{v:?}")),
        }
    }

    /// 与 Go 侧 DriverToValue 对齐：整数列保持 i64（MySQL 无独立 BOOL 类型）。
    fn my_value(row: &MySqlRow, i: usize) -> Value {
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
        if let Ok(v) = row.try_get::<f64, _>(i) {
            return Value::Float(v);
        }
        if let Ok(v) = row.try_get::<BigDecimal, _>(i) {
            return Value::String(v.to_string());
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
            return Value::String(format!("<blob {} bytes>", v.len()));
        }
        Value::String(format!("<{}>", raw.type_info().name()))
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

    fn begin_statement(mode: TxMode) -> Option<String> {
        match mode.isolation_level {
            IsolationLevel::ReadCommitted => Some(
                "BEGIN ISOLATION LEVEL READ COMMITTED".to_string(),
            ),
            IsolationLevel::Serializable => Some(
                "BEGIN ISOLATION LEVEL SERIALIZABLE".to_string(),
            ),
            IsolationLevel::ReadUncommitted => Some(
                "BEGIN ISOLATION LEVEL READ UNCOMMITTED".to_string(),
            ),
            IsolationLevel::RepeatableRead => Some(
                "BEGIN ISOLATION LEVEL REPEATABLE READ".to_string(),
            ),
        }
    }

    pub async fn begin_tx(&self, mode: TxMode) -> CoreResult<MySqlTxHandle> {
        let tx = match Self::begin_statement(mode) {
            Some(st) => self.pool.begin_with(st).await,
            None => self.pool.begin().await,
        };
        let tx = tx.map_err(|e| CoreError::Driver(format!("begin tx failed: {e}")))?;
        Ok(MySqlTxHandle(tx))
    }

    pub async fn commit(&self, tx: MySqlTxHandle) -> CoreResult<()> {
        tx.0.commit().await
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        Ok(())
    }

    pub async fn rollback(&self, tx: MySqlTxHandle) -> CoreResult<()> {
        tx.0.rollback().await
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        Ok(())
    }

    async fn execute_on<'e, E>(
        executor: E,
        sql: &str,
        params: &[Value],
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

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
            let rows = q
                .fetch_all(executor)
                .await
                .map_err(|e| CoreError::Driver(format!("query failed: {e}")))?;
            let columns = Self::columns_of(&rows);
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::my_value(r, i)).collect())
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
            let res = q
                .execute(executor)
                .await
                .map_err(|e| CoreError::Driver(format!("execute failed: {e}")))?;
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
        Self::execute_on(tx.0.as_mut(), sql, params).await
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

#[async_trait]
impl SqlDriver for MySqlConn {
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

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast::<MySqlTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        MySqlConn::commit(self, *h).await
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast::<MySqlTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        MySqlConn::rollback(self, *h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        Self::execute_on(&self.pool, sql, params).await
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
        let rows = sqlx::query(
            "SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES \
             WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME",
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
                table_type: if r.get::<String, _>(1) == "VIEW" {
                    TableType::View
                } else {
                    TableType::Table
                },
                row_count: None,
                comment: None,
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let rows = sqlx::query(
            "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES', COLUMN_DEFAULT, EXTRA, ORDINAL_POSITION \
             FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION",
        )
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
                    comment: None,
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
        let row = sqlx::query("SHOW CREATE TABLE ??")
            .bind(format!("{schema}.{table}"))
            .fetch_one(&self.pool)
            .await
            .map_err(|e| CoreError::Driver(format!("create_table_sql: {e}")))?;
        Ok(row.get::<String, _>(1))
    }
}
