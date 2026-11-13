use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use parking_lot::Mutex;
use rusqlite::Connection as RusqliteConnection;

use polydb_core::error_classify::driver_error;
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyAction, ForeignKeyInfo,
    IndexInfo, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType,
    TableInfo, TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

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
            let Some(pattern) = arg_text(ctx.get_raw(0))? else {
                return Ok(None);
            };
            let Some(text) = arg_text(ctx.get_raw(1))? else {
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

fn arg_text(v: rusqlite::types::ValueRef) -> rusqlite::Result<Option<String>> {
    use rusqlite::types::ValueRef as R;
    Ok(Some(match v {
        // P2-6 回归：此前用 from_utf8_lossy 静默替换非法 UTF-8 为 U+FFFD，
        // 与 F5（row_to_values）修复前的同一缺陷。SQLite TEXT/BLOB 列不强制
        // UTF-8，非法字节若静默替换会改变正则匹配结果而用户无法察觉。
        R::Text(b) | R::Blob(b) => String::from_utf8(b.to_vec()).map_err(|e| {
            rusqlite::Error::UserFunctionError(
                format!("invalid UTF-8 in REGEXP argument: {e}").into(),
            )
        })?,
        R::Integer(i) => i.to_string(),
        R::Real(f) => f.to_string(),
        R::Null => return Ok(None),
    }))
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
///
/// `active` 标记事务是否仍处于活跃状态：commit/rollback 成功后置为 false。
/// execute_in_tx / commit / rollback 在 handle 已不活跃时返回错误而非静默
/// 操作——commit 后 execute_in_tx 会以自动提交模式运行 SQL（BEGIN 已结束），
/// 调用方误以为事务性执行而实际不在事务内。
#[derive(Clone, Debug)]
pub struct SqliteTxHandle {
    active: bool,
}

impl SqliteConn {
    pub fn open(path: &str) -> CoreResult<Self> {
        let conn = RusqliteConnection::open(path)
            .map_err(|e| CoreError::Driver(format!("sqlite open failed: {e}")))?;
        // busy_timeout makes a second writer wait (up to 5s) instead of failing
        // immediately with SQLITE_BUSY, and synchronous=NORMAL is the WAL-mode
        // pairing the Go kernel uses (dbcore/sqlite/spec.go). All queries on
        // this driver serialize through the single connection's mutex, but a
        // concurrent writer from another process (or another client of the
        // same file) still shares the file lock — the timeout is what keeps
        // that contention a wait instead of an error.
        conn.execute_batch(
            "PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA synchronous=NORMAL;",
        )
        .map_err(|e| CoreError::Driver(format!("sqlite pragma failed: {e}")))?;
        // WAL can silently fall back to the rollback journal on media that
        // cannot host the -wal file (network shares, read-only images), and then
        // synchronous=NORMAL is the wrong pairing: the durability it assumes
        // comes from the WAL. Read the mode back and, when WAL did not take,
        // keep the rollback journal's safe setting instead of pretending.
        let mode: String = conn
            .query_row("PRAGMA journal_mode", [], |r| r.get(0))
            .map_err(|e| CoreError::Driver(format!("sqlite journal_mode probe failed: {e}")))?;
        if !mode.eq_ignore_ascii_case("wal") {
            conn.execute_batch("PRAGMA synchronous=FULL")
                .map_err(|e| CoreError::Driver(format!("sqlite pragma failed: {e}")))?;
        }
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
            Value::U64(u) => {
                // SQLite 整数是 i64——u64 值 > i64::MAX 无法原生存储。
                // ≤ i64::MAX 绑为 i64（保持整数精度）；> i64::MAX 绑为
                // f64（SQLite 唯一能容纳的更大数值类型，≥2^53 丢失精度）。
                if *u <= i64::MAX as u64 {
                    Box::new(*u as i64)
                } else {
                    Box::new(*u as f64)
                }
            }
            Value::Float(f) => Box::new(*f),
            Value::String(s) => Box::new(s.clone()),
            // Array/Object（含 $blob Object）跨线协议统一为 JSON 文本——与 Go
            // 侧 ValueToDriver 的 string(v.Raw()) 对齐。serde_json 默认把
            // NaN/Inf 序列化为 null，导致 Array/Object 中的 NaN/Inf 静默
            // 丢失——序列化前先将 NaN/Inf 替换为字符串值。
            _ => Box::new(Self::value_to_json_text(v)),
        }
    }

    /// 将 Value 序列化为 JSON 文本，NaN/Inf 先替换为字符串值再序列化。
    /// 实现统一在 polydb_db_core::value_json，五个驱动共用同一口径。
    fn value_to_json_text(v: &Value) -> String {
        polydb_db_core::value_to_json_text(v)
    }

    fn row_to_values(row: &rusqlite::Row, col_count: usize) -> rusqlite::Result<Vec<Value>> {
        let mut values = Vec::with_capacity(col_count);
        for i in 0..col_count {
            let val: Value = match row.get_ref(i)? {
                rusqlite::types::ValueRef::Null => Value::Null,
                rusqlite::types::ValueRef::Integer(i) => Value::Integer(i),
                rusqlite::types::ValueRef::Real(f) => Value::Float(f),
                rusqlite::types::ValueRef::Text(b) => {
                    // F5 回归：此前用 from_utf8_lossy 静默替换非法 UTF-8 为
                    // U+FFFD——与 Go 侧保留原始字节的行为不对称，且用户无法
                    // 察觉数据已被篡改。SQLite TEXT 列虽不强制 UTF-8，但
                    // 跨线协议要求 UTF-8 文本，非法字节应显式报错而非静默替换。
                    Value::String(String::from_utf8(b.to_vec()).map_err(|e| {
                        rusqlite::Error::InvalidColumnType(
                            i,
                            format!("invalid UTF-8 in TEXT column: {e}"),
                            rusqlite::types::Type::Text,
                        )
                    })?)
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
                // 真实声明类型（PD-21，2026-11-10）：此前硬编码 "TEXT"，而 Go 侧
                // 走 modernc 的 decltypes 报真实类型（dbsqlite.go:180
                // `pdb.DeclTypeOf(ct)`），两端对同一查询给出的 type 不同——
                // 契约测试为此专门做了 `onlyColumnNames` 归一化容忍，把列类型
                // 这一可观测行为整个放弃。
                //
                // 开启 rusqlite 的 `column_decltype` feature 后可读
                // sqlite3_column_decltype（rusqlite-0.32.1 src/column.rs:27
                // `Column::decl_type`）。表达式/函数结果没有声明类型 ⇒ None，
                // 此时回落 "TEXT"（与 Go 侧 DeclTypeOf 的空串回落同形）。
                data_type: stmt
                    .columns()
                    .get(i)
                    .and_then(|c| c.decl_type())
                    .unwrap_or("TEXT")
                    .to_string(),
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
            Ok(SqliteTxHandle { active: true })
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    pub async fn execute_in_tx_impl(
        &self,
        tx: &mut SqliteTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        // P2 F11：commit/rollback 后 handle 的 active=false，此时执行会以
        // 自动提交模式跑 SQL（BEGIN 已结束），调用方误以为事务性执行而实际
        // 不在事务内。拒绝而非静默。
        if !tx.active {
            return Err(CoreError::TransactionNotFound(
                "transaction already committed or rolled back".into(),
            ));
        }
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

    pub async fn commit_impl(&self, tx: &mut SqliteTxHandle) -> CoreResult<()> {
        // P2 F11：commit 后置 active=false——防止二次 commit/execute_in_tx
        // 以自动提交模式静默运行。
        if !tx.active {
            return Err(CoreError::TransactionNotFound(
                "transaction already committed or rolled back".into(),
            ));
        }
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || -> CoreResult<()> {
            let guard = conn.lock();
            guard
                .execute_batch("COMMIT")
                .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))??;
        tx.active = false;
        Ok(())
    }

    pub async fn rollback_impl(&self, tx: &mut SqliteTxHandle) -> CoreResult<()> {
        // P2 F11：rollback 成功后置 active=false——防止二次 rollback/execute_in_tx
        // 以自动提交模式静默运行。commit 失败路径不置 false（P1-2：commit 失败后
        // rollback 仍须成功——事务在 SQL 层仍然活跃）。
        if !tx.active {
            return Err(CoreError::TransactionNotFound(
                "transaction already committed or rolled back".into(),
            ));
        }
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || -> CoreResult<()> {
            let guard = conn.lock();
            guard
                .execute_batch("ROLLBACK")
                .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
            Ok(())
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))??;
        tx.active = false;
        Ok(())
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

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        // SQLite only ever runs `BEGIN IMMEDIATE`: one writer, and readers see
        // a consistent snapshot — that is at least REPEATABLE READ, i.e. as
        // strong as every level the protocol offers *except* READ UNCOMMITTED,
        // which would need PRAGMA read_uncommitted and is not implemented here.
        // The old signature discarded the mode (`_mode`), so a caller asking
        // for dirty reads silently received serializable semantics. Refuse the
        // one level the driver cannot honor instead of pretending it did; the
        // stronger-than-requested levels below are safe (no anomaly the caller
        // did not already ask to be protected from).
        if matches!(mode.isolation_level, IsolationLevel::ReadUncommitted) {
            return Err(CoreError::NotSupported(
                "sqlite cannot run READ UNCOMMITTED: transactions execute BEGIN IMMEDIATE (serializable)"
                    .into(),
            ));
        }
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

    async fn commit(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<SqliteTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        self.commit_impl(h).await
    }

    async fn rollback(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<SqliteTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        self.rollback_impl(h).await
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

    async fn batch_columns(&self, _schema: &str) -> CoreResult<Option<Vec<(String, ColumnInfo)>>> {
        // 批量列配方 = 逐表 `list_columns` 去掉表名参数、改用 `pragma_table_info`
        // 表值函数一次取全库（Go SQLite `CatalogBatchColumns` 的 Rust 对应物）。
        // 表达式与逐表逐列一致，故批量结果与逐表结果行对行相等。
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard
                .prepare(
                    "SELECT t.name, c.name, c.type, c.\"notnull\", c.dflt_value, c.pk, c.cid + 1 \
                     FROM sqlite_master t \
                     JOIN pragma_table_info(t.name) c \
                     WHERE t.type IN ('table', 'view') AND t.name NOT LIKE 'sqlite_%' \
                     ORDER BY t.name, c.cid",
                )
                .map_err(|e| CoreError::Driver(format!("batch_columns prepare: {e}")))?;

            let cols = stmt
                .query_map([], |row| {
                    let table: String = row.get(0)?;
                    let name: String = row.get(1)?;
                    let data_type: String = row.get(2)?;
                    let notnull: bool = row.get(3)?;
                    let default: Option<String> = row.get(4)?;
                    let pk: bool = row.get(5)?;
                    let ord: i32 = row.get(6)?;
                    Ok((table, name, data_type, notnull, default, pk, ord))
                })
                .map_err(|e| CoreError::Driver(format!("batch_columns query: {e}")))?;

            let mut out = Vec::new();
            for c in cols {
                let (table, name, data_type, notnull, default, pk, ord) =
                    c.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                let col = ColumnInfo {
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
                    ordinal_position: ord,
                };
                out.push((table, col));
            }
            Ok(Some(out))
        })
        .await
        .map_err(|e| CoreError::Internal(format!("sqlite task join: {e}")))?
    }

    async fn batch_foreign_keys(
        &self,
        _schema: &str,
    ) -> CoreResult<Option<Vec<(String, Vec<ForeignKeyInfo>)>>> {
        // 批量外键配方 = 逐表 `list_foreign_keys` 去掉表名参数、改用
        // `pragma_foreign_key_list` 表值函数一次取全库（Go SQLite
        // `CatalogBatchForeignKeys` 的 Rust 对应物）。按 (表, id) 分组，
        // 分组/列序/动作/名字与逐表路径逐字一致（等价契约见 equivalence 测试）。
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            let guard = conn.lock();
            let mut stmt = guard
                .prepare(
                    "SELECT t.name, fkl.id, fkl.seq, fkl.\"table\", fkl.\"from\", fkl.\"to\", \
                             fkl.on_update, fkl.on_delete \
                     FROM sqlite_master t \
                     JOIN pragma_foreign_key_list(t.name) fkl ON true \
                     WHERE t.type = 'table' AND t.name NOT LIKE 'sqlite_%' \
                     ORDER BY t.name, fkl.id, fkl.seq",
                )
                .map_err(|e| CoreError::Driver(format!("batch_fk prepare: {e}")))?;

            let rows = stmt
                .query_map([], |row| {
                    let table: String = row.get(0)?;
                    let id: i32 = row.get(1)?;
                    let seq: i32 = row.get(2)?;
                    let ref_table: String = row.get(3)?;
                    let from: String = row.get(4)?;
                    let to: String = row.get(5)?;
                    let on_update: String = row.get(6)?;
                    let on_delete: String = row.get(7)?;
                    Ok((table, id, seq, ref_table, from, to, on_update, on_delete))
                })
                .map_err(|e| CoreError::Driver(format!("batch_fk query: {e}")))?;

            use std::collections::BTreeMap;
            // (表, id) → (ref_table, [(seq, from, to)], on_update, on_delete)。
            // id 是表内 FK 约束序号，seq 是多列内次序，两者都按数值排（同逐表）。
            let mut by_table: BTreeMap<
                String,
                BTreeMap<i32, (String, Vec<(i32, String, String)>, String, String)>,
            > = BTreeMap::new();
            for r in rows {
                let (table, id, seq, ref_table, from, to, on_update, on_delete) =
                    r.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
                let entry = by_table
                    .entry(table)
                    .or_default()
                    .entry(id)
                    .or_insert_with(|| (ref_table, Vec::new(), on_update, on_delete));
                entry.1.push((seq, from, to));
            }

            let mut out: Vec<(String, Vec<ForeignKeyInfo>)> = Vec::new();
            for (table, agg) in &by_table {
                let mut fks = Vec::with_capacity(agg.len());
                for (ref_table, pairs, on_update, on_delete) in agg.values() {
                    let mut pairs = pairs.clone();
                    pairs.sort_by_key(|(seq, _, _)| *seq);
                    fks.push(ForeignKeyInfo {
                        // SQLite 的外键约束本来没有名字（同逐表）：留空由 diff 按内容配对。
                        name: String::new(),
                        columns: pairs.iter().map(|(_, from, _)| from.clone()).collect(),
                        referenced_schema: "main".to_string(),
                        referenced_table: ref_table.clone(),
                        referenced_columns: pairs.iter().map(|(_, _, to)| to.clone()).collect(),
                        on_update: fk_action(on_update),
                        on_delete: fk_action(on_delete),
                    });
                }
                out.push((table.clone(), fks));
            }
            Ok(Some(out))
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
        polydb_rt::block_on(sql_driver.execute(
            "CREATE TABLE \"my\"\"table\" (id INTEGER PRIMARY KEY, x INTEGER)",
            &[],
        ))
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
    fn regexp_udf_rejects_invalid_utf8() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();
        // X'FF' 是单字节 BLOB（0xFF 非法 UTF-8）。此前 from_utf8_lossy
        // 会静默替换为 U+FFFD，正则匹配结果被篡改而用户无法察觉。
        // 修复后应显式报错。pattern 侧和 text 侧都要测。
        for sql in ["SELECT 'hello' REGEXP X'FF'", "SELECT X'FF' REGEXP '.'"] {
            assert!(
                polydb_rt::block_on(sql_driver.execute(sql, &[])).is_err(),
                "sql: {sql} should error on invalid UTF-8"
            );
        }
        // 合法 UTF-8 的 BLOB 仍应正常工作：X'68' 是 'h'，应匹配 'hello'。
        let res = polydb_rt::block_on(sql_driver.execute(
            "SELECT CASE WHEN 'hello' REGEXP X'68' THEN 1 ELSE 0 END",
            &[],
        ))
        .unwrap();
        assert_eq!(res.rows[0][0], Value::Integer(1));
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
