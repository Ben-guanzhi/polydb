use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use polydb_core::{
    ColumnInfo, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, QueryResult, RedisKeyType,
    RedisReply, RedisScanPage, RedisValue, SchemaInfo, TableInfo, Value,
};

use crate::tx::TxMode;

#[async_trait]
pub trait DatabaseDriver: Send + Sync {
    fn kind(&self) -> DatabaseKind;
    async fn ping(&self) -> CoreResult<()>;
    async fn close(&self) -> CoreResult<()>;
    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        None
    }
    fn as_kv(&self) -> Option<&dyn KvDriver> {
        None
    }
}

#[async_trait]
pub trait SqlDriver: DatabaseDriver {
    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult>;
    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>>;
    async fn list_tables(&self, schema: &str) -> CoreResult<Vec<TableInfo>>;
    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>>;
    async fn list_indexes(&self, schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>>;
    async fn list_foreign_keys(&self, schema: &str, table: &str)
        -> CoreResult<Vec<ForeignKeyInfo>>;
    async fn create_table_sql(&self, schema: &str, table: &str) -> CoreResult<String>;

    /// 事务接口。句柄以 Box<dyn Any + Send> 承载，具体类型由驱动内部决定
    /// （sqlx::Transaction 或驱动自持的 TxHandle 结构）；调用方通过 downcast
    /// 拿到内部具体引用。所有方法都是 async（由 async_trait 统一包装）；
    /// 各驱动的实现内部可能直接调用同步 inherent 或 await 异步 inherent。
    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>>;
    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult>;
    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()>;
    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()>;

    /// 返回指向 self 的共享 Arc<dyn SqlDriver>。各驱动在 clone_arc 里克隆自身
    /// （共享底层池/连接），向上转型为 trait object，供 app-core 在脱离
    /// Connection 借用的生命周期内继续持有具体驱动的连接引用。
    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver>;
}

#[async_trait]
pub trait KvDriver: DatabaseDriver {
    async fn select_db(&self, index: u32) -> CoreResult<()>;
    async fn scan_keys(&self, cursor: u64, pattern: &str, count: u32) -> CoreResult<RedisScanPage>;
    async fn key_type(&self, key: &str) -> CoreResult<RedisKeyType>;
    async fn get_value(&self, key: &str) -> CoreResult<RedisValue>;
    async fn set_value(&self, key: &str, value: RedisValue) -> CoreResult<()>;
    async fn exec_command(&self, args: &[String]) -> CoreResult<RedisReply>;
}
