use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use parking_lot::Mutex;
use rusqlite::Connection as RusqliteConnection;

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, QueryResult,
    ResultColumn, SchemaInfo, StatementType, TableInfo, TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

pub struct SqliteConn {
    conn: Arc<Mutex<RusqliteConnection>>,
    _path: String,
}

/// 事务句柄。SQLite 本身没有独立的事务对象，事务由底层 Connection 的
/// BEGIN IMMEDIATE 语句开启；因此句柄只携带 TxMode 用于诊断。
/// 实际 begin/commit/rollback 与执行都通过 self.conn 上的 execute_batch 完成。
#[derive(Clone, Debug)]
pub struct SqliteTxHandle {
    pub(crate) mode: TxMode,
}

impl SqliteConn {
    pub fn open(path: &str) -> CoreResult<Self> {
        let conn = RusqliteConnection::open(path)
            .map_err(|e| CoreError::Driver(format!("sqlite open failed: {e}")))?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            .map_err(|e| CoreError::Driver(format!("sqlite pragma failed: {e}")))?;
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
                rusqlite::types::ValueRef::Blob(b) => {
                    Value::String(format!("<blob {} bytes>", b.len()))
                }
            };
            values.push(val);
        }
        Ok(values)
    }

    /// 剥离 SQL 开头的前导注释（-- 行注释与 /* */ 块注释），
    /// 使带注释的 SQL 仍能命中语句类型检测（与 Go 实现一致）。
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

    fn detect_statement_type(sql: &str) -> StatementType {
        let trimmed = Self::strip_leading_comments(sql).trim().to_uppercase();
        if trimmed.starts_with("SELECT") {
            StatementType::Select
        } else if trimmed.starts_with("INSERT") {
            StatementType::Insert
        } else if trimmed.starts_with("UPDATE") {
            StatementType::Update
        } else if trimmed.starts_with("DELETE") {
            StatementType::Delete
        } else if trimmed.starts_with("CREATE")
            || trimmed.starts_with("ALTER")
            || trimmed.starts_with("DROP")
        {
            StatementType::Ddl
        } else {
            StatementType::Other
        }
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
            .map_err(|e| CoreError::Driver(format!("prepare failed: {e}")))?;

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
                .map_err(|e| CoreError::Driver(format!("query failed: {e}")))?;

            let mut rows = Vec::new();
            for row_result in rows_result {
                let row = row_result.map_err(|e| CoreError::Driver(format!("row failed: {e}")))?;
                rows.push(row);
            }

            Ok(QueryResult {
                columns,
                rows,
                affected_rows: 0,
                execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                truncated: false,
                total_rows: None,
                has_more: false,
                statement_type: Some(stmt_type),
            })
        } else {
            let affected = stmt
                .execute(param_slices.as_slice())
                .map_err(|e| CoreError::Driver(format!("execute failed: {e}")))?;

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
    pub fn begin_tx_impl(&self, mode: TxMode) -> CoreResult<SqliteTxHandle> {
        let conn = self.conn.lock();
        conn.execute_batch("BEGIN IMMEDIATE")
            .map_err(|e| CoreError::Driver(format!("begin tx failed: {e}")))?;
        Ok(SqliteTxHandle { mode })
    }

    pub fn execute_in_tx_impl(
        &self,
        _tx: &mut SqliteTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let conn = self.conn.lock();
        Self::execute_on(&conn, sql, params)
    }

    pub fn commit_impl(&self, _tx: &mut SqliteTxHandle) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.execute_batch("COMMIT")
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        Ok(())
    }

    pub fn rollback_impl(&self, _tx: &mut SqliteTxHandle) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.execute_batch("ROLLBACK")
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        Ok(())
    }
}

#[async_trait]
impl DatabaseDriver for SqliteConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Sqlite
    }

    async fn ping(&self) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.execute_batch("SELECT 1")
            .map_err(|e| CoreError::Driver(format!("ping failed: {e}")))?;
        Ok(())
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
    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<SqliteConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(self.begin_tx_impl(mode)?))
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
        self.execute_in_tx_impl(h, sql, params)
    }

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<SqliteTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        self.commit_impl(&mut h)
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<SqliteTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        self.rollback_impl(&mut h)
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        let conn = self.conn.lock();
        Self::execute_on(&conn, sql, params)
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        // SQLite has a single main schema
        Ok(vec![SchemaInfo {
            name: "main".to_string(),
        }])
    }

    async fn list_tables(&self, _schema: &str) -> CoreResult<Vec<TableInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn.prepare(
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
    }

    async fn list_columns(&self, _schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn
            .prepare(&format!("PRAGMA table_info(\"{table}\")"))
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
    }

    async fn list_indexes(&self, _schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn
            .prepare(&format!("PRAGMA index_list(\"{table}\")"))
            .map_err(|e| CoreError::Driver(format!("list_indexes prepare: {e}")))?;

        let indexes = stmt
            .query_map([], |row| {
                let name: String = row.get(1)?;
                let unique: bool = row.get(2)?;
                Ok((name, unique))
            })
            .map_err(|e| CoreError::Driver(format!("list_indexes query: {e}")))?;

        let mut result = Vec::new();
        for idx in indexes {
            let (name, unique) = idx.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
            let mut info_stmt = conn
                .prepare(&format!("PRAGMA index_info(\"{name}\")"))
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
                primary: false,
                index_type: None,
                columns: idx_cols,
                comment: None,
            });
        }
        Ok(result)
    }

    async fn list_foreign_keys(
        &self,
        _schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        let conn = self.conn.lock();
        let mut stmt = conn
            .prepare(&format!("PRAGMA foreign_key_list(\"{table}\")"))
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

        use std::collections::HashMap;
        let mut fk_map: HashMap<i32, (String, Vec<String>, Vec<String>, String, String)> =
            HashMap::new();
        for fk in fks {
            let (id, _seq, ref_table, from, to, on_update, on_delete) =
                fk.map_err(|e| CoreError::Driver(format!("row: {e}")))?;
            let entry = fk_map
                .entry(id)
                .or_insert_with(|| (ref_table, Vec::new(), Vec::new(), on_update, on_delete));
            entry.1.push(from);
            entry.2.push(to);
        }

        let result = fk_map
            .into_iter()
            .enumerate()
            .map(
                |(i, (_id, (ref_table, cols, ref_cols, _on_update, _on_delete)))| ForeignKeyInfo {
                    name: format!("fk_{i}"),
                    columns: cols,
                    referenced_schema: "main".to_string(),
                    referenced_table: ref_table,
                    referenced_columns: ref_cols,
                    on_update: None,
                    on_delete: None,
                },
            )
            .collect();

        Ok(result)
    }

    async fn create_table_sql(&self, _schema: &str, table: &str) -> CoreResult<String> {
        let conn = self.conn.lock();
        let sql: String = conn
            .query_row(
                "SELECT sql FROM sqlite_master WHERE type='table' AND name=?1",
                [table],
                |row| row.get(0),
            )
            .map_err(|e| CoreError::Driver(format!("create_table_sql: {e}")))?;
        Ok(sql)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn open_and_ping() {
        let conn = SqliteConn::open(":memory:").unwrap();
        assert_eq!(conn.kind(), DatabaseKind::Sqlite);
        smol::block_on(conn.ping()).unwrap();
    }

    #[test]
    fn execute_select_and_dml() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();

        let result = smol::block_on(sql_driver.execute(
            "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, age INTEGER)",
            &[],
        ))
        .unwrap();
        assert_eq!(result.affected_rows, 0);

        for sql in [
            "INSERT INTO users (name, age) VALUES ('alice', 30)",
            "INSERT INTO users (name, age) VALUES ('bob', 25)",
        ] {
            let result = smol::block_on(sql_driver.execute(sql, &[])).unwrap();
            assert_eq!(result.affected_rows, 1, "executing {sql}");
        }

        let result =
            smol::block_on(sql_driver.execute("SELECT * FROM users ORDER BY id", &[])).unwrap();
        assert_eq!(result.columns.len(), 3);
        assert_eq!(result.rows.len(), 2);
        assert_eq!(result.rows[0][0], Value::Integer(1));
        assert_eq!(result.rows[0][1], Value::String("alice".into()));
        assert_eq!(result.rows[0][2], Value::Integer(30));

        let result =
            smol::block_on(sql_driver.execute("SELECT COUNT(*) AS c FROM users", &[])).unwrap();
        assert_eq!(result.rows[0][0], Value::Integer(2));

        let result = smol::block_on(sql_driver.execute(
            "SELECT * FROM users WHERE name = ?1",
            &[Value::String("bob".into())],
        ))
        .unwrap();
        assert_eq!(result.rows.len(), 1);
        assert_eq!(result.rows[0][0], Value::Integer(2));

        let result = smol::block_on(sql_driver.execute("DELETE FROM users", &[])).unwrap();
        assert_eq!(result.affected_rows, 2);
    }

    #[test]
    fn metadata_introspection() {
        let conn = SqliteConn::open(":memory:").unwrap();
        let sql_driver: &dyn SqlDriver = conn.as_sql().unwrap();

        smol::block_on(sql_driver.execute(
            "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b TEXT NOT NULL)",
            &[],
        ))
        .unwrap();
        smol::block_on(sql_driver.execute("CREATE INDEX idx_t1_b ON t1(b)", &[])).unwrap();

        let schemas = smol::block_on(sql_driver.list_schemas()).unwrap();
        assert_eq!(schemas.len(), 1);
        assert_eq!(schemas[0].name, "main");

        let tables = smol::block_on(sql_driver.list_tables("main")).unwrap();
        assert!(tables.iter().any(|t| t.name == "t1"));

        let columns = smol::block_on(sql_driver.list_columns("main", "t1")).unwrap();
        assert_eq!(columns.len(), 2);
        assert_eq!(columns[0].name, "a");
        assert!(columns[0].is_primary_key);
        assert_eq!(columns[1].name, "b");
        assert!(!columns[1].nullable);

        let indexes = smol::block_on(sql_driver.list_indexes("main", "t1")).unwrap();
        let idx = indexes.iter().find(|i| i.name == "idx_t1_b").unwrap();
        assert_eq!(idx.columns.len(), 1);
        assert_eq!(idx.columns[0].name, "b");

        let ddl = smol::block_on(sql_driver.create_table_sql("main", "t1")).unwrap();
        assert!(ddl.contains("CREATE TABLE"));
    }
}
