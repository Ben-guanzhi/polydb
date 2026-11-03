use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use parking_lot::Mutex;
use rusqlite::Connection as RusqliteConnection;

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyAction, ForeignKeyInfo, IndexInfo,
    QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo, TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};
use polydb_core::error_classify::driver_error;

// Quote an identifier for interpolation into a PRAGMA statement. PRAGMA has no
// bound-parameter form for its identifier argument, so the name goes in as
// text — and a name containing `"` must have its quotes doubled, or the
// statement is a syntax error (e.g. a table literally named `my"table`).
fn quote_ident(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

pub struct SqliteConn {
    conn: Arc<Mutex<RusqliteConnection>>,
    _path: String,
}

/// SQLite 内置函数里没有 REGEXP：`x REGEXP y` 只是 `regexp(y, x)` 的语法糖，
/// 标量函数必须由驱动注册（behavior.md §13.5）。这里注册的实现与 Go 侧
/// （modernc + regexp 包）语义对齐：同为 RE2 家族（无后向引用、无环视）、
/// 任一参数 NULL 则结果为 NULL、非法正则报错冒泡为 POLYDB_ERR_QUERY_FAILED。
fn register_regexp_fn(conn: &RusqliteConnection) -> CoreResult<()> {
    use rusqlite::functions::{Context, FunctionFlags};
    conn.create_scalar_function(
        "regexp",
        2,
        FunctionFlags::SQLITE_UTF8 | FunctionFlags::SQLITE_DETERMINISTIC,
        |ctx: &Context| -> rusqlite::Result<Option<bool>> {
            let Some(pattern) = arg_text(ctx.get_raw(0)) else {
                return Ok(None);
            };
            let Some(text) = arg_text(ctx.get_raw(1)) else {
                return Ok(None);
            };
            let re = regex::Regex::new(&pattern).map_err(|e| {
                rusqlite::Error::UserFunctionError(format!("invalid regex {pattern:?}: {e}").into())
            })?;
            Ok(Some(re.is_match(&text)))
        },
    )
    .map_err(|e| CoreError::Driver(format!("sqlite register regexp udf failed: {e}")))?;
    Ok(())
}

fn arg_text(v: rusqlite::types::ValueRef) -> Option<String> {
    use rusqlite::types::ValueRef as R;
    Some(match v {
        R::Text(b) | R::Blob(b) => String::from_utf8_lossy(b).into_owned(),
        R::Integer(i) => i.to_string(),
        R::Real(f) => f.to_string(),
        R::Null => return None,
    })
}

/// `PRAGMA foreign_key_list` 的 on_update/on_delete 是 SQL 文本（如 `NO ACTION`、
/// `SET NULL`），未识别的值一律视为「无信息」而不是猜一个默认动作。
fn fk_action(raw: &str) -> Option<ForeignKeyAction> {
    Some(match raw.trim().to_ascii_uppercase().as_str() {
        "CASCADE" => ForeignKeyAction::Cascade,
        "SET NULL" => ForeignKeyAction::SetNull,
        "SET DEFAULT" => ForeignKeyAction::SetDefault,
        "RESTRICT" => ForeignKeyAction::Restrict,
        "NO ACTION" => ForeignKeyAction::NoAction,
        _ => return None,
    })
}

/// 事务句柄。SQLite 本身没有独立的事务对象，事务由底层 Connection 的
/// BEGIN IMMEDIATE 句柄只是不透明标记；实际 begin/commit/rollback 与执行
/// 都通过 self.conn 上的 execute_batch 完成。
#[derive(Clone, Debug)]
pub struct SqliteTxHandle;

impl SqliteConn {
    pub fn open(path: &str) -> CoreResult<Self> {
        let conn = RusqliteConnection::open(path)
            .map_err(|e| CoreError::Driver(format!("sqlite open failed: {e}")))?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            .map_err(|e| CoreError::Driver(format!("sqlite pragma failed: {e}")))?;
        register_regexp_fn(&conn)?;
        Ok(Self {
            conn: Arc::new(Mutex::new(conn)),
            _path: path.to_string(),
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：底层连接通过 `Arc::clone` 共享，
    /// 路径字段单独克隆。用于 app-core 持有第二个 `Arc<dyn SqlDriver>`，
    /// 与 `Connection` 内的驱动共享同一份连接资源。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            conn: Arc::clone(&self.conn),
            _path: self._path.clone(),
        })
    }

    fn value_to_rusqlite(v: &Value) -> Box<dyn rusqlite::types::ToSql> {
        match v {
            Value::Null => Box::new(rusqlite::types::Null),
            Value::Bool(b) => Box::new(*b),
            Value::Integer(i) => Box::new(*i),
            Value::Float(f) => Box::new(*f),
            Value::String(s) => Box::new(s.clone()),
            _ => Box::new(serde_json::to_string(v).unwrap_or_default()),
        }
    }

    fn row_to_values(row: &rusqlite::Row, col_count: usize) -> rusqlite::Result<Vec<Value>> {
        let mut values = Vec::with_capacity(col_count);
        for i in 0..col_count {
            let val: Value = match row.get_ref(i)? {
                rusqlite::types::ValueRef::Null => Value::Null,
                rusqlite::types::ValueRef::Integer(i) => Value::Integer(i),
                rusqlite::types::ValueRef::Real(f) => Value::Float(f),
                rusqlite::types::ValueRef::Text(b) => {
                    Value::String(String::from_utf8_lossy(b).into_owned())
                }
                rusqlite::types::ValueRef::Blob(b) => polydb_db_core::blob_value(b),
            };
            values.push(val);
        }
        Ok(values)
    }

    /// 语句类型判定（收敛到 db-core 公共版，方言固定为 SQLite）。
    ///
    /// WITH 前缀统一判 select（与 Go 侧 dbcore.DetectStatementType 规则一致，
    /// 双实现行为约定见 spec/behavior.md §11）：包括 `WITH ... INSERT` 形态。
    /// 公共版按 token 类别判型（整词而非前缀）。
    fn detect_statement_type(sql: &str) -> StatementType {
        polydb_db_core::detect_statement_type(sql, Some(DatabaseKind::Sqlite))
    }

    /// 在给定 rusqlite::Connection 上跑一条 SQL。事务内外通用；
    /// execute 与 execute_in_tx 都走该实现。
    fn execute_on(
        conn: &RusqliteConnection,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);

        let mut stmt = conn
            .prepare(sql)
            .map_err(|e| db_err(&e, "prepare failed"))?;

        let col_count = stmt.column_count();
        let columns: Vec<ResultColumn> = (0..col_count)
            .map(|i| ResultColumn {
                name: stmt.column_name(i).unwrap_or("?").to_string(),
                table: None,
                data_type: "TEXT".to_string(),
                generic_type: None,
                nullable: Some(true),
            })
            .collect();

        let param_refs: Vec<Box<dyn rusqlite::types::ToSql>> =
            params.iter().map(Self::value_to_rusqlite).collect();
        let param_slices: Vec<&dyn rusqlite::types::ToSql> =
            param_refs.iter().map(|b| b.as_ref()).collect();

        if stmt_type == StatementType::Select || !columns.is_empty() {
            let rows_result = stmt
                .query_map(param_slices.as_slice(), |row| {
                    Self::row_to_values(row, col_count)
                })
                .map_err(|e| db_err(&e, "query failed"))?;

            let mut rows = Vec::new();
            let mut truncated = false;
            for row_result in rows_result {
                if rows.len() >= polydb_db_core::row_cap() {
                    truncated = true;
                    break;
                }
                let row = row_result.map_err(|e| db_err(&e, "row failed"))?;
                rows.push(row);
            }

            Ok(QueryResult {
                columns,
                rows,
                affected_rows: 0,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated,
                total_rows: None,
                has_more: truncated,
                statement_type: Some(stmt_type),
            })
        } else {
            let affected = stmt
                .execute(param_slices.as_slice())
                .map_err(|e| db_err(&e, "execute failed"))?;

            Ok(QueryResult {
                columns: vec![],
                rows: vec![],
                affected_rows: affected as u64,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        }
    }

    /// 开启事务：BEGIN IMMEDIATE。SQLite 不支持任意隔离级别，ReadCommitted
    /// 与驱动默认行为一致（IMMEDIATE 事务），其他级别同样映射到 IMMEDIATE。
    // Transaction primitives: rusqlite is synchronous, so each one runs in
    // spawn_blocking — the same discipline as execute. Running BEGIN/COMMIT
    // on a runtime worker blocks it for the whole statement, starving the
    // WS cancel/timeout timers (the non-tx execute already moved off-worker;
    // these were the last holdouts).
    pub async fn begin_tx_impl(&self) -> CoreResult<SqliteTxHandle> {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            guard
                .execute_batch("BEGIN IMMEDIATE")
                .map_err(|e| CoreError::Driver(format!("begin tx failed: {e}")))?;
            Ok(SqliteTxHandle)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    pub async fn execute_in_tx_impl(
        &self,
        _tx: &mut SqliteTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let conn = Arc::clone(&self.conn);
        let sql = sql.to_string();
        let params = params.to_vec();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            Self::execute_on(&guard, &sql, &params)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    pub async fn commit_impl(&self, _tx: &mut SqliteTxHandle) -> CoreResult<()> {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            guard
                .execute_batch("COMMIT")
                .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    pub async fn rollback_impl(&self, _tx: &mut SqliteTxHandle) -> CoreResult<()> {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            guard
                .execute_batch("ROLLBACK")
                .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }
}

#[async_trait]
impl DatabaseDriver for SqliteConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Sqlite
    }

    async fn ping(&self) -> CoreResult<()> {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            guard
                .execute_batch("SELECT 1")
                .map_err(|e| CoreError::Driver(format!("ping failed: {e}")))?;
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn close(&self) -> CoreResult<()> {
        // rusqlite closes on drop
        Ok(())
    }

    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for SqliteConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_SQLITE, schema, table, req)?;
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
            polydb_db_core::DIALECT_SQLITE,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<SqliteConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, _mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(self.begin_tx_impl().await?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<SqliteTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        self.execute_in_tx_impl(h, sql, params).await
    }

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<SqliteTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        self.commit_impl(&mut h).await
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<SqliteTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        self.rollback_impl(&mut h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        // rusqlite is synchronous: running it on a runtime worker blocks that
        // worker for the whole query. On a current-thread runtime — and with
        // the GUI's four workers under a few slow queries — every other task
        // (WS cancel/timeout timers, heartbeat) starves. Hand the work to the
        // blocking pool: this future then yields, the timers fire, and the
        // statement itself keeps running to completion on the blocking thread
        // (best-effort cancel, the same semantics the Go drivers have).
        let conn = Arc::clone(&self.conn);
        let sql = sql.to_string();
        let params = params.to_vec();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            Self::execute_on(&guard, &sql, &params)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        // SQLite has a single main schema
        Ok(vec![SchemaInfo {
            name: "main".to_string(),
        }])
    }

    async fn list_tables(&self, _schema: &str) -> CoreResult<Vec<TableInfo>> {
        // Metadata introspection is synchronous rusqlite work: on a runtime
        // worker it blocks the thread (and the WS cancel/timeout timers that
        // depend on it). Same spawn_blocking discipline as execute.
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard.prepare(
                "SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name"
            ).map_err(|e| CoreError::Driver(format!("list_tables prepare: {e}")))?;

            let tables = stmt
                .query_map([], |row| {
                    let name: String = row.get(0)?;
                    let type_str: String = row.get(1)?;
                    Ok((name, type_str))
                })
                .map_err(|e| CoreError::Driver(format!("list_tables query: {e}")))?;

            let mut result = Vec::new();
            for t in tables {
                let (name, type_str) = t.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                result.push(TableInfo {
                    name,
                    schema: "main".to_string(),
                    table_type: if type_str == "view" {
                        TableType::View
                    } else {
                        TableType::Table
                    },
                    row_count: None,
                    comment: None,
                });
            }
            Ok(result)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn list_columns(&self, _schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        // Metadata introspection is synchronous rusqlite work: on a runtime
        // worker it blocks the thread (and the WS cancel/timeout timers that
        // depend on it). Same spawn_blocking discipline as execute.
        let conn = Arc::clone(&self.conn);
        let table = table.to_string();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard
                .prepare(&format!("PRAGMA table_info({})", quote_ident(&table)))
                .map_err(|e| CoreError::Driver(format!("list_columns prepare: {e}")))?;

            let cols = stmt
                .query_map([], |row| {
                    let cid: i32 = row.get(0)?;
                    let name: String = row.get(1)?;
                    let data_type: String = row.get(2)?;
                    let notnull: bool = row.get(3)?;
                    let default: Option<String> = row.get(4)?;
                    let pk: bool = row.get(5)?;
                    Ok((cid, name, data_type, notnull, default, pk))
                })
                .map_err(|e| CoreError::Driver(format!("list_columns query: {e}")))?;

            let mut result = Vec::new();
            for c in cols {
                let (cid, name, data_type, notnull, default, pk) =
                    c.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                result.push(ColumnInfo {
                    name,
                    data_type: data_type.clone(),
                    generic_type: None,
                    nullable: !notnull,
                    default_value: default,
                    max_length: None,
                    precision: None,
                    scale: None,
                    is_primary_key: pk,
                    is_auto_increment: false,
                    comment: None,
                    ordinal_position: cid + 1,
                });
            }
            Ok(result)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn list_indexes(&self, _schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        // Metadata introspection is synchronous rusqlite work: on a runtime
        // worker it blocks the thread (and the WS cancel/timeout timers that
        // depend on it). Same spawn_blocking discipline as execute.
        let conn = Arc::clone(&self.conn);
        let table = table.to_string();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard
                .prepare(&format!("PRAGMA index_list({})", quote_ident(&table)))
                .map_err(|e| CoreError::Driver(format!("list_indexes prepare: {e}")))?;

            let indexes = stmt
                .query_map([], |row| {
                    let name: String = row.get(1)?;
                    let unique: bool = row.get(2)?;
                    // origin：'pk' 主键隐式索引 / 'u' UNIQUE 约束 / 'c' CREATE INDEX。
                    let origin: Option<String> = row.get(3).ok();
                    Ok((name, unique, origin))
                })
                .map_err(|e| CoreError::Driver(format!("list_indexes query: {e}")))?;

            let mut result = Vec::new();
            for idx in indexes {
                let (name, unique, origin) =
                    idx.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                let mut info_stmt = guard
                    .prepare(&format!("PRAGMA index_info({})", quote_ident(&name)))
                    .map_err(|e| CoreError::Driver(format!("index_info prepare: {e}")))?;
                let columns = info_stmt
                    .query_map([], |row| {
                        let col_name: String = row.get(2)?;
                        Ok(col_name)
                    })
                    .map_err(|e| CoreError::Driver(format!("index_info query: {e}")))?;

                let mut idx_cols = Vec::new();
                for (i, col) in columns.enumerate() {
                    let col_name = col.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                    idx_cols.push(polydb_core::IndexColumn {
                        name: col_name,
                        position: (i + 1) as i32,
                        order: None,
                        prefix_length: None,
                    });
                }

                result.push(IndexInfo {
                    name,
                    unique,
                    primary: origin.as_deref() == Some("pk"),
                    index_type: None,
                    columns: idx_cols,
                    comment: None,
                });
            }
            Ok(result)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn list_foreign_keys(
        &self,
        _schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        // Metadata introspection is synchronous rusqlite work: on a runtime
        // worker it blocks the thread (and the WS cancel/timeout timers that
        // depend on it). Same spawn_blocking discipline as execute.
        let conn = Arc::clone(&self.conn);
        let table = table.to_string();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard
                .prepare(&format!("PRAGMA foreign_key_list({})", quote_ident(&table)))
                .map_err(|e| CoreError::Driver(format!("fk prepare: {e}")))?;

            let fks = stmt
                .query_map([], |row| {
                    let id: i32 = row.get(0)?;
                    let seq: i32 = row.get(1)?;
                    let ref_table: String = row.get(2)?;
                    let from: String = row.get(3)?;
                    let to: String = row.get(4)?;
                    let on_update: String = row.get(5)?;
                    let on_delete: String = row.get(6)?;
                    Ok((id, seq, ref_table, from, to, on_update, on_delete))
                })
                .map_err(|e| CoreError::Driver(format!("fk query: {e}")))?;

            use std::collections::BTreeMap;
            // PRAGMA 的 id 是外键约束在表定义中的序号、seq 是多列内的次序，两者都必须按数值排。
            // 原先用 HashMap 收集，同一次 DDL 的两次采集可能拿到不同的 fk_i 命名，
            // 于是比对凭空产出一对 drop/add 外键（E2.0 真快照复核抓到）。
            let mut agg: BTreeMap<i32, (String, Vec<(i32, String, String)>, String, String)> =
                BTreeMap::new();
            for fk in fks {
                let (id, seq, ref_table, from, to, on_update, on_delete) =
                    fk.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                let entry = agg
                    .entry(id)
                    .or_insert_with(|| (ref_table, Vec::new(), on_update, on_delete));
                entry.1.push((seq, from, to));
            }

            let result = agg
                .into_iter()
                .map(|(_id, (ref_table, mut pairs, on_update, on_delete))| {
                    pairs.sort_by_key(|(seq, _, _)| *seq);
                    ForeignKeyInfo {
                        // SQLite 的外键约束本来就没有名字（PRAGMA foreign_key_list 也不返回名字）。
                        // 填任何合成名都会让「新增一条外键」把其余外键的编号整体错位，
                        // 于是凭空产出一堆 drop/add 对。留空，由 diff 按内容配对。
                        name: String::new(),
                        columns: pairs.iter().map(|(_, from, _)| from.clone()).collect(),
                        referenced_schema: "main".to_string(),
                        referenced_table: ref_table,
                        referenced_columns: pairs.iter().map(|(_, _, to)| to.clone()).collect(),
                        on_update: fk_action(&on_update),
                        on_delete: fk_action(&on_delete),
                    }
                })
                .collect();

            Ok(result)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn create_table_sql(&self, _schema: &str, table: &str) -> CoreResult<String> {
        let conn = Arc::clone(&self.conn);
        let table = table.to_string();
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let sql: String = guard
                .query_row(
                    "SELECT sql FROM sqlite_master WHERE type='table' AND name=?1",
                    [&table],
                    |row| row.get(0),
                )
                .map_err(|e| CoreError::Driver(format!("create_table_sql: {e}")))?;
            Ok(sql)
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }
}

// rusqlite errors carry no structured code: the wordings are the whole
// evidence, so the text-pattern layer of the kernel classifier does the
// mapping (mirrors Go dberror's third evidence layer).
fn db_err<E: std::fmt::Display>(e: &E, what: &str) -> CoreError {
    driver_error(None, None, None, &format!("{what}: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn quote_ident_escapes_embedded_quotes() {
        // A table literally named `my"table` must come back as a valid
        // quoted identifier: the inner quote is doubled, or the PRAGMA
        // statement that interpolates it is a syntax error.
        assert_eq!(quote_ident("plain"), "\"plain\"");
        assert_eq!(quote_ident("my\"table"), "\"my\"\"table\"");
        assert_eq!(quote_ident("a\"b\"c"), "\"a\"\"b\"\"c\"");
    }

    #[test]
    fn pragma_metadata_survives_quote_in_name() {
        // End to end: a table with a quote in its name must still be
        // introspectable through the PRAGMA paths that interpolate it.
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        polydb_rt::block_on(
            sql_driver.execute(
                "CREATE TABLE \"my\"\"table\" (id INTEGER PRIMARY KEY, x INTEGER)",
                &[],
            ),
        )
        .unwrap();
        let cols = polydb_rt::block_on(sql_driver.list_columns("main", "my\"table")).unwrap();
        assert_eq!(cols.len(), 2);
        assert_eq!(cols[0].name, "id");
        assert_eq!(cols[1].name, "x");
    }

    use polydb_core::{FilterCondition, FilterOperator as Op, TableRowsRequest};

    fn cond(column: &str, op: Op, value: Value) -> FilterCondition {
        FilterCondition {
            column: column.to_string(),
            op,
            value,
            second_value: Value::Null,
            values: Vec::new(),
            case_sensitive: None,
        }
    }

    /// 建一张三行文本表：Alice / alice / Bob。
    fn fixture_conn() -> SqliteConn {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        polydb_rt::block_on(
            sql_driver.execute("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)", &[]),
        )
        .unwrap();
        for sql in [
            "INSERT INTO t (name) VALUES ('Alice')",
            "INSERT INTO t (name) VALUES ('alice')",
            "INSERT INTO t (name) VALUES ('Bob')",
        ] {
            polydb_rt::block_on(sql_driver.execute(sql, &[])).unwrap();
        }
        conn
    }

    fn browse_names(conn: &SqliteConn, conditions: Vec<FilterCondition>) -> Vec<String> {
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        let page = polydb_rt::block_on(sql_driver.browse_rows(
            "",
            "t",
            &TableRowsRequest {
                columns: Vec::new(),
                conditions,
                logic: None,
                order_by: Vec::new(),
                offset: 0,
                limit: 0,
            },
        ))
        .unwrap();
        page.rows
            .iter()
            .map(|r| match &r[1] {
                Value::String(s) => s.clone(),
                other => format!("{other:?}"),
            })
            .collect()
    }

    #[test]
    fn regexp_udf_is_registered() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        let one = |pattern: Value| {
            polydb_rt::block_on(sql_driver.execute(
                "SELECT CASE WHEN 'hello' REGEXP ?1 THEN 1 ELSE 0 END",
                &[pattern],
            ))
            .unwrap()
            .rows[0][0]
                .clone()
        };
        assert_eq!(one(Value::String("^h".into())), Value::Integer(1));
        assert_eq!(one(Value::String("^H".into())), Value::Integer(0));
        // (?i) 内联标志是两端 UDF 的统一不区分大小写写法
        assert_eq!(one(Value::String("(?i)^H".into())), Value::Integer(1));
        // RE2 家族：后向引用不受支持，必须报错而不是静默通过
        assert!(polydb_rt::block_on(
            sql_driver.execute("SELECT 'a' REGEXP ?1", &[Value::String("(a)\\1".into())])
        )
        .is_err());
    }

    #[test]
    fn regexp_udf_propagates_null() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        for sql in [
            "SELECT (NULL REGEXP '^h') IS NULL",
            "SELECT ('hello' REGEXP NULL) IS NULL",
        ] {
            let res = polydb_rt::block_on(sql_driver.execute(sql, &[])).unwrap();
            assert_eq!(res.rows[0][0], Value::Integer(1), "sql: {sql}");
        }
    }

    #[test]
    fn browse_rows_supports_new_ops() {
        let conn = fixture_conn();
        // contains 走 LIKE + ESCAPE：SQLite 的 LIKE 对 ASCII 不区分，ALI 两种大小写都中
        assert_eq!(
            browse_names(
                &conn,
                vec![cond("name", Op::Contains, Value::String("ALI".into()))]
            ),
            vec!["Alice", "alice"]
        );
        // 强制区分走 GLOB：小写 ali 只命中 alice
        let mut cs = cond("name", Op::Contains, Value::String("ali".into()));
        cs.case_sensitive = Some(true);
        assert_eq!(browse_names(&conn, vec![cs]), vec!["alice"]);
        // 显式强制不区分仍走 LIKE
        let mut ci = cond("name", Op::Contains, Value::String("ali".into()));
        ci.case_sensitive = Some(false);
        assert_eq!(browse_names(&conn, vec![ci]), vec!["Alice", "alice"]);
        // regex：默认区分，不区分靠 (?i)
        assert_eq!(
            browse_names(
                &conn,
                vec![cond("name", Op::Regex, Value::String("^A".into()))]
            ),
            vec!["Alice"]
        );
        let mut re_ci = cond("name", Op::Regex, Value::String("^a".into()));
        re_ci.case_sensitive = Some(false);
        assert_eq!(browse_names(&conn, vec![re_ci]), vec!["Alice", "alice"]);
        // starts_with / ends_with / empty
        assert_eq!(
            browse_names(
                &conn,
                vec![cond("name", Op::StartsWith, Value::String("Bo".into()))]
            ),
            vec!["Bob"]
        );
        assert_eq!(
            browse_names(
                &conn,
                vec![cond("name", Op::EndsWith, Value::String("lice".into()))]
            ),
            vec!["Alice", "alice"]
        );
        polydb_rt::block_on(
            conn.as_sql()
                .unwrap()
                .execute("INSERT INTO t (name) VALUES ('')", &[]),
        )
        .unwrap();
        assert_eq!(
            browse_names(&conn, vec![cond("name", Op::Empty, Value::Null)]).len(),
            1
        );
    }

    #[test]
    fn browse_rows_count_matches_new_ops() {
        let conn = fixture_conn();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        let req = TableRowsRequest {
            columns: Vec::new(),
            conditions: vec![cond("name", Op::Regex, Value::String("^[AB]".into()))],
            logic: None,
            order_by: Vec::new(),
            offset: 0,
            limit: 0,
        };
        assert_eq!(
            polydb_rt::block_on(sql_driver.browse_rows_count("", "t", &req)).unwrap(),
            2
        );
    }

    #[test]
    fn open_and_ping() {
        let conn = SqliteConn::open(":memory:").unwrap();
        assert_eq!(conn.kind(), DatabaseKind::Sqlite);
        polydb_rt::block_on(conn.ping()).unwrap();
    }

    #[test]
    fn execute_select_and_dml() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();

        let result = polydb_rt::block_on(sql_driver.execute(
            "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, age INTEGER)",
            &[],
        ))
        .unwrap();
        assert_eq!(result.affected_rows, 0);

        for sql in [
            "INSERT INTO users (name, age) VALUES ('alice', 30)",
            "INSERT INTO users (name, age) VALUES ('bob', 25)",
        ] {
            let result = polydb_rt::block_on(sql_driver.execute(sql, &[])).unwrap();
            assert_eq!(result.affected_rows, 1, "executing {sql}");
        }

        let result =
            polydb_rt::block_on(sql_driver.execute("SELECT * FROM users ORDER BY id", &[]))
                .unwrap();
        assert_eq!(result.columns.len(), 3);
        assert_eq!(result.rows.len(), 2);
        assert_eq!(result.rows[0][0], Value::Integer(1));
        assert_eq!(result.rows[0][1], Value::String("alice".into()));
        assert_eq!(result.rows[0][2], Value::Integer(30));

        let result =
            polydb_rt::block_on(sql_driver.execute("SELECT COUNT(*) AS c FROM users", &[]))
                .unwrap();
        assert_eq!(result.rows[0][0], Value::Integer(2));

        let result = polydb_rt::block_on(sql_driver.execute(
            "SELECT * FROM users WHERE name = ?1",
            &[Value::String("bob".into())],
        ))
        .unwrap();
        assert_eq!(result.rows.len(), 1);
        assert_eq!(result.rows[0][0], Value::Integer(2));

        let result = polydb_rt::block_on(sql_driver.execute("DELETE FROM users", &[])).unwrap();
        assert_eq!(result.affected_rows, 2);
    }

    #[test]
    fn metadata_introspection() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();

        polydb_rt::block_on(sql_driver.execute(
            "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b TEXT NOT NULL)",
            &[],
        ))
        .unwrap();
        polydb_rt::block_on(sql_driver.execute("CREATE INDEX idx_t1_b ON t1(b)", &[])).unwrap();

        let schemas = polydb_rt::block_on(sql_driver.list_schemas()).unwrap();
        assert_eq!(schemas.len(), 1);
        assert_eq!(schemas[0].name, "main");

        let tables = polydb_rt::block_on(sql_driver.list_tables("main")).unwrap();
        assert!(tables.iter().any(|t| t.name == "t1"));

        let columns = polydb_rt::block_on(sql_driver.list_columns("main", "t1")).unwrap();
        assert_eq!(columns.len(), 2);
        assert_eq!(columns[0].name, "a");
        assert!(columns[0].is_primary_key);
        assert_eq!(columns[1].name, "b");
        assert!(!columns[1].nullable);

        let indexes = polydb_rt::block_on(sql_driver.list_indexes("main", "t1")).unwrap();
        let idx = indexes.iter().find(|i| i.name == "idx_t1_b").unwrap();
        assert_eq!(idx.columns.len(), 1);
        assert_eq!(idx.columns[0].name, "b");

        let ddl = polydb_rt::block_on(sql_driver.create_table_sql("main", "t1")).unwrap();
        assert!(ddl.contains("CREATE TABLE"));
    }
}
