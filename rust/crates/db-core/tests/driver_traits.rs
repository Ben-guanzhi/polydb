//! Driver trait + Connection capability-downgrade tests.
//!
//! Pins the as_sql()/as_kv() → ErrNotSupported path and the Connection
//! wrapper's kind() / driver() / driver_arc() accessors.

use std::sync::Arc;

use polydb_core::{CoreError, CoreResult, DatabaseKind, QueryResult, Value};
use polydb_db_core::{Connection, DatabaseDriver, KvDriver, SqlDriver, TxMode};

use async_trait::async_trait;

/// A minimal SQL-only driver for testing.
struct SqlOnlyDriver {
    kind: DatabaseKind,
}

#[async_trait]
impl DatabaseDriver for SqlOnlyDriver {
    fn kind(&self) -> DatabaseKind {
        self.kind
    }
    async fn ping(&self) -> CoreResult<()> {
        Ok(())
    }
    async fn close(&self) -> CoreResult<()> {
        Ok(())
    }
    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for SqlOnlyDriver {
    async fn execute(&self, _sql: &str, _params: &[Value]) -> CoreResult<QueryResult> {
        Ok(QueryResult {
            columns: vec![],
            rows: vec![],
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        })
    }
    async fn list_schemas(&self) -> CoreResult<Vec<polydb_core::SchemaInfo>> {
        Ok(vec![])
    }
    async fn list_tables(&self, _schema: &str) -> CoreResult<Vec<polydb_core::TableInfo>> {
        Ok(vec![])
    }
    async fn list_columns(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::ColumnInfo>> {
        Ok(vec![])
    }
    async fn list_indexes(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::IndexInfo>> {
        Ok(vec![])
    }
    async fn list_foreign_keys(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::ForeignKeyInfo>> {
        Ok(vec![])
    }
    async fn create_table_sql(&self, _schema: &str, _table: &str) -> CoreResult<String> {
        Ok(String::new())
    }
    async fn browse_rows(
        &self,
        _schema: &str,
        _table: &str,
        _req: &polydb_core::TableRowsRequest,
    ) -> CoreResult<polydb_core::TableRowsResult> {
        Ok(polydb_core::TableRowsResult {
            columns: vec![],
            rows: vec![],
            offset: 0,
            has_more: false,
            total_estimate: None,
            execution_time_ms: 0.0,
        })
    }
    async fn browse_rows_count(
        &self,
        _schema: &str,
        _table: &str,
        _req: &polydb_core::TableRowsRequest,
    ) -> CoreResult<u64> {
        Ok(0)
    }
    async fn begin_tx(
        &self,
        _mode: polydb_db_core::TxMode,
    ) -> CoreResult<Box<dyn std::any::Any + Send>> {
        Err(CoreError::Driver("no tx".into()))
    }
    async fn execute_in_tx(
        &self,
        _tx: &mut Box<dyn std::any::Any + Send>,
        _sql: &str,
        _params: &[Value],
    ) -> CoreResult<QueryResult> {
        Err(CoreError::Driver("no tx".into()))
    }
    async fn commit(&self, _tx: &mut Box<dyn std::any::Any + Send>) -> CoreResult<()> {
        Err(CoreError::Driver("no tx".into()))
    }
    async fn rollback(&self, _tx: &mut Box<dyn std::any::Any + Send>) -> CoreResult<()> {
        Err(CoreError::Driver("no tx".into()))
    }
    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        Arc::new(SqlOnlyDriver { kind: self.kind })
    }
}

/// A minimal KV-only driver for testing.
struct KvOnlyDriver {
    kind: DatabaseKind,
}

#[async_trait]
impl DatabaseDriver for KvOnlyDriver {
    fn kind(&self) -> DatabaseKind {
        self.kind
    }
    async fn ping(&self) -> CoreResult<()> {
        Ok(())
    }
    async fn close(&self) -> CoreResult<()> {
        Ok(())
    }
    fn as_kv(&self) -> Option<&dyn KvDriver> {
        Some(self)
    }
}

#[async_trait]
impl KvDriver for KvOnlyDriver {
    async fn select_db(&self, _index: u32) -> CoreResult<()> {
        Ok(())
    }
    async fn scan_keys(
        &self,
        _cursor: u64,
        _pattern: &str,
        _count: u32,
    ) -> CoreResult<polydb_core::RedisScanPage> {
        Ok(polydb_core::RedisScanPage {
            cursor: 0,
            db: None,
            keys: vec![],
        })
    }
    async fn key_type(&self, _key: &str) -> CoreResult<polydb_core::RedisKeyType> {
        Ok(polydb_core::RedisKeyType::String)
    }
    async fn get_value(&self, _key: &str) -> CoreResult<polydb_core::RedisValue> {
        Ok(polydb_core::RedisValue::String("val".to_string()))
    }
    async fn set_value(&self, _key: &str, _value: polydb_core::RedisValue) -> CoreResult<()> {
        Ok(())
    }
    async fn exec_command(&self, _args: &[String]) -> CoreResult<polydb_core::RedisReply> {
        Ok(polydb_core::RedisReply {
            reply_type: polydb_core::RedisReplyType::SimpleString,
            value: Some(polydb_core::RedisReplyValue::String("OK".to_string())),
        })
    }
}

// ── Connection kind() ─────────────────────────────────────────────────────

#[test]
fn connection_kind_reflects_driver() {
    let sql = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Postgres,
    }));
    assert_eq!(sql.kind(), DatabaseKind::Postgres);

    let kv = Connection::new(Arc::new(KvOnlyDriver {
        kind: DatabaseKind::Redis,
    }));
    assert_eq!(kv.kind(), DatabaseKind::Redis);
}

// ── as_sql() on SQL driver ────────────────────────────────────────────────

#[test]
fn as_sql_on_sql_driver_succeeds() {
    let conn = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::MySql,
    }));
    let sql = conn.as_sql().expect("SQL driver should support as_sql");
    let kind = sql.kind();
    assert_eq!(kind, DatabaseKind::MySql);
}

#[test]
fn as_sql_on_kv_driver_returns_not_supported() {
    let conn = Connection::new(Arc::new(KvOnlyDriver {
        kind: DatabaseKind::Redis,
    }));
    let err = conn.as_sql().err().unwrap();
    let msg = err.to_string();
    assert!(msg.contains("not a SQL database"), "unexpected: {msg}");
    assert!(msg.contains("redis"), "unexpected: {msg}");
}

// ── as_kv() on KV driver ──────────────────────────────────────────────────

#[test]
fn as_kv_on_kv_driver_succeeds() {
    let conn = Connection::new(Arc::new(KvOnlyDriver {
        kind: DatabaseKind::Redis,
    }));
    let kv = conn.as_kv().expect("KV driver should support as_kv");
    let kind = kv.kind();
    assert_eq!(kind, DatabaseKind::Redis);
}

#[test]
fn as_kv_on_sql_driver_returns_not_supported() {
    let conn = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Postgres,
    }));
    let err = conn.as_kv().err().unwrap();
    let msg = err.to_string();
    assert!(msg.contains("not a KV database"), "unexpected: {msg}");
    assert!(msg.contains("postgres"), "unexpected: {msg}");
}

// ── sql_driver_arc ────────────────────────────────────────────────────────

#[test]
fn sql_driver_arc_returns_arc() {
    let conn = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Sqlite,
    }));
    let arc = conn.sql_driver_arc().expect("should succeed");
    assert_eq!(arc.kind(), DatabaseKind::Sqlite);
}

#[test]
fn sql_driver_arc_on_kv_driver_returns_error() {
    let conn = Connection::new(Arc::new(KvOnlyDriver {
        kind: DatabaseKind::Redis,
    }));
    assert!(conn.sql_driver_arc().is_err());
}

// ── driver() accessor ─────────────────────────────────────────────────────

#[test]
fn driver_accessor_returns_trait_object() {
    let conn = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Oracle,
    }));
    let driver = conn.driver();
    assert_eq!(driver.kind(), DatabaseKind::Oracle);
}

// ── driver_arc() ──────────────────────────────────────────────────────────

#[test]
fn driver_arc_returns_shared_reference() {
    let conn = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Mssql,
    }));
    let arc = conn.driver_arc();
    assert_eq!(arc.kind(), DatabaseKind::Mssql);
}

// ── Connection is Clone ───────────────────────────────────────────────────

#[test]
fn connection_is_clone() {
    let original = Connection::new(Arc::new(SqlOnlyDriver {
        kind: DatabaseKind::Sqlite,
    }));
    let cloned = original.clone();
    assert_eq!(original.kind(), cloned.kind());
}

// ── P1-2 回归：commit 失败后 rollback 仍能成功 ─────────────────────────

/// 事务状态句柄：记录 committed / rolled_back 标志。
struct TxState {
    committed: bool,
    rolled_back: bool,
}

/// 支持事务的 mock 驱动：commit_fails 控制 commit 是否失败。
struct TxMockDriver {
    kind: DatabaseKind,
    commit_fails: bool,
}

#[async_trait]
impl DatabaseDriver for TxMockDriver {
    fn kind(&self) -> DatabaseKind {
        self.kind
    }
    async fn ping(&self) -> CoreResult<()> {
        Ok(())
    }
    async fn close(&self) -> CoreResult<()> {
        Ok(())
    }
    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for TxMockDriver {
    async fn execute(&self, _sql: &str, _params: &[Value]) -> CoreResult<QueryResult> {
        Ok(QueryResult {
            columns: vec![],
            rows: vec![],
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        })
    }
    async fn list_schemas(&self) -> CoreResult<Vec<polydb_core::SchemaInfo>> {
        Ok(vec![])
    }
    async fn list_tables(&self, _schema: &str) -> CoreResult<Vec<polydb_core::TableInfo>> {
        Ok(vec![])
    }
    async fn list_columns(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::ColumnInfo>> {
        Ok(vec![])
    }
    async fn list_indexes(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::IndexInfo>> {
        Ok(vec![])
    }
    async fn list_foreign_keys(
        &self,
        _schema: &str,
        _table: &str,
    ) -> CoreResult<Vec<polydb_core::ForeignKeyInfo>> {
        Ok(vec![])
    }
    async fn create_table_sql(&self, _schema: &str, _table: &str) -> CoreResult<String> {
        Ok(String::new())
    }
    async fn browse_rows(
        &self,
        _schema: &str,
        _table: &str,
        _req: &polydb_core::TableRowsRequest,
    ) -> CoreResult<polydb_core::TableRowsResult> {
        Ok(polydb_core::TableRowsResult {
            columns: vec![],
            rows: vec![],
            offset: 0,
            has_more: false,
            total_estimate: None,
            execution_time_ms: 0.0,
        })
    }
    async fn browse_rows_count(
        &self,
        _schema: &str,
        _table: &str,
        _req: &polydb_core::TableRowsRequest,
    ) -> CoreResult<u64> {
        Ok(0)
    }
    async fn begin_tx(&self, _mode: TxMode) -> CoreResult<Box<dyn std::any::Any + Send>> {
        Ok(Box::new(TxState {
            committed: false,
            rolled_back: false,
        }))
    }
    async fn execute_in_tx(
        &self,
        _tx: &mut Box<dyn std::any::Any + Send>,
        _sql: &str,
        _params: &[Value],
    ) -> CoreResult<QueryResult> {
        Ok(QueryResult {
            columns: vec![],
            rows: vec![],
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        })
    }
    async fn commit(&self, tx: &mut Box<dyn std::any::Any + Send>) -> CoreResult<()> {
        if self.commit_fails {
            return Err(CoreError::Driver("commit failed (simulated)".into()));
        }
        let state = tx
            .downcast_mut::<TxState>()
            .ok_or_else(|| CoreError::Internal("bad tx handle".into()))?;
        state.committed = true;
        Ok(())
    }
    async fn rollback(&self, tx: &mut Box<dyn std::any::Any + Send>) -> CoreResult<()> {
        let state = tx
            .downcast_mut::<TxState>()
            .ok_or_else(|| CoreError::Internal("bad tx handle".into()))?;
        state.rolled_back = true;
        Ok(())
    }
    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        Arc::new(TxMockDriver {
            kind: self.kind,
            commit_fails: self.commit_fails,
        })
    }
}

/// P1-2 回归：commit 失败时，handle 不被消费（&mut 借用而非 by-value 移动），
/// rollback 可以随后成功。旧签名 `commit(Box<dyn Any + Send>)` 会消费 handle，
/// commit 失败后 handle 消失，rollback 报 "tx already rolled back"。
#[test]
fn commit_failure_does_not_consume_handle_so_rollback_succeeds() {
    let driver = TxMockDriver {
        kind: DatabaseKind::Sqlite,
        commit_fails: true,
    };

    polydb_rt::block_on(async {
        let mut handle = driver.begin_tx(TxMode::default()).await.unwrap();

        // commit 失败
        let commit_err = driver
            .commit(&mut handle)
            .await
            .expect_err("commit should fail");
        assert!(
            commit_err.to_string().contains("commit failed"),
            "unexpected commit error: {commit_err}"
        );

        // rollback 仍能成功——handle 未被 commit 消费
        driver
            .rollback(&mut handle)
            .await
            .expect("rollback must succeed after commit failure");

        // 验证状态：commit 未标记成功，rollback 已标记
        let state = handle.downcast_ref::<TxState>().expect("bad handle");
        assert!(!state.committed, "commit should not have succeeded");
        assert!(state.rolled_back, "rollback should have succeeded");
    });
}

/// commit 成功时，handle 同样不被消费，rollback 仍可执行。
#[test]
fn commit_success_then_rollback_is_allowed() {
    let driver = TxMockDriver {
        kind: DatabaseKind::Sqlite,
        commit_fails: false,
    };

    polydb_rt::block_on(async {
        let mut handle = driver.begin_tx(TxMode::default()).await.unwrap();

        driver
            .commit(&mut handle)
            .await
            .expect("commit should succeed");
        let state = handle.downcast_ref::<TxState>().unwrap();
        assert!(state.committed);

        // rollback 也能执行——&mut 不阻止二次操作
        driver
            .rollback(&mut handle)
            .await
            .expect("rollback should work");
        let state = handle.downcast_ref::<TxState>().unwrap();
        assert!(state.committed);
        assert!(state.rolled_back);
    });
}
