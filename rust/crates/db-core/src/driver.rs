use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use polydb_core::{
    ColumnInfo, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, QueryResult, RedisKeyType,
    RedisReply, RedisScanPage, RedisValue, SchemaInfo, TableInfo, TableRowsRequest,
    TableRowsResult, Value,
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

    /// 批量读一个 schema 下所有表/视图的列（K-1.2 配方，Go `BatchColumnsReader` 的
    /// Rust 对应物）。一次往返取代逐表 `list_columns` 的 T 次往返——`collect_snapshot`
    /// 的快照 N+1 正是靠它收敛。
    ///
    /// 三态返回，调用方据此决定回落策略（与 Go `schemadiff.Collect` 逐字对应）：
    /// - `Ok(Some(pairs))`：驱动原生支持批量，一次往返拿到全部 `(表名, 列)`；
    /// - `Ok(None)`：驱动不具备批量能力，调用方应回落逐表 `list_columns`；
    /// - `Err(_)`：批量尝试失败，调用方同样回落逐表（错误由逐表路径传播，
    ///   绝不产出半读快照——「半读快照把每个缺失索引/约束报成 DROP」防线保持）。
    ///
    /// 行形状 = 逐表 `list_columns` 的列形状 + 表名前置（对应 Go 的 column 0 =
    /// table name）。默认实现返回 `Ok(None)`；PG / SQLite 驱动覆盖此方法。
    async fn batch_columns(
        &self,
        _schema: &str,
    ) -> CoreResult<Option<Vec<(String, ColumnInfo)>>> {
        Ok(None)
    }

    /// 批量读一个 schema 下所有表的外键（K-1.2 配方，Go `BatchForeignKeysReader` 的
    /// Rust 对应物）。三态语义同 `batch_columns`。
    ///
    /// 与列的关键区别：**空组是真状态**（表无外键），不像列那样需要回落补读。
    /// 返回 `(表名, 该表外键组)`；只含真表（视图无外键）。
    async fn batch_foreign_keys(
        &self,
        _schema: &str,
    ) -> CoreResult<Option<Vec<(String, Vec<ForeignKeyInfo>)>>> {
        Ok(None)
    }

    /// 按表浏览行（服务端分页/排序/过滤，behavior.md §13）。
    /// 实现经 db-core browse 模块构造参数化 SQL 后走自身执行管线。
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<TableRowsResult>;
    /// 对同条件执行 COUNT(*)。
    async fn browse_rows_count(
        &self,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<u64>;

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
