use std::any::Any;
use std::sync::Arc;

use async_trait::async_trait;
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexInfo, QueryResult,
    RedisKeyType, RedisReply, RedisScanPage, RedisValue, SchemaInfo, TableInfo, TableRowsRequest,
    TableRowsResult, Value,
};
use serde::{Deserialize, Serialize};

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
    async fn batch_columns(&self, _schema: &str) -> CoreResult<Option<Vec<(String, ColumnInfo)>>> {
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

/// 有界读取限额（对齐内核 `dbcore.RedisValueLimit`，wave 19）。
/// 零值字段 = 驱动缺省（见 [`DEFAULT_MAX_STRING_BYTES`] / [`DEFAULT_MAX_ENTRIES`]）；
/// 不存在"无界"读法——无界读是 [`KvDriver::get_value`]。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct RedisValueLimit {
    /// 字符串值按字节截断到此数（回退到 UTF-8 行边界）；集合类型忽略。
    #[serde(default)]
    pub max_string_bytes: i64,
    /// 集合条目上限（list 项 / set 成员 / zset 成员 / hash 字段 / stream 条目）。
    #[serde(default)]
    pub max_entries: i64,
}

impl RedisValueLimit {
    /// 把零值字段解析为驱动缺省，得到真正生效的限额。
    pub fn resolved(&self) -> RedisValueLimit {
        RedisValueLimit {
            max_string_bytes: if self.max_string_bytes <= 0 {
                DEFAULT_MAX_STRING_BYTES
            } else {
                self.max_string_bytes
            },
            max_entries: if self.max_entries <= 0 {
                DEFAULT_MAX_ENTRIES
            } else {
                self.max_entries
            },
        }
    }
}

/// 首窗缺省字符串字节上限（64 KiB，对齐内核 `DefaultMaxStringBytes`）。
pub const DEFAULT_MAX_STRING_BYTES: i64 = 64 << 10;
/// 首窗缺省集合条目上限（1000，对齐内核 `DefaultMaxEntries`）。
pub const DEFAULT_MAX_ENTRIES: i64 = 1000;

/// 有界窗口读取结果（对齐内核 `BoundedRedisValue` + `KeyPageReader` 续读语义，
/// wave 19 / wave 3.5）：窗口内的值 + 完整规模 + 是否截断 + 续读游标。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RedisValueWindow {
    /// 窗口内的值（与 [`KvDriver::get_value`] 同形；截断字符串不做数值解析）。
    pub value: RedisValue,
    /// 完整规模：字符串为字节数（STRLEN），集合为条目数
    /// （LLEN/SCARD/ZCARD/HLEN/XLEN）。
    pub total: i64,
    /// Value 是否在限额处被截断（Total > Value 实际持有）。
    pub truncated: bool,
    /// 续读游标；空串 = 已到末尾。驱动私有，调用方原样回传。
    #[serde(default)]
    pub next_cursor: String,
}

#[async_trait]
pub trait KvDriver: DatabaseDriver {
    async fn select_db(&self, index: u32) -> CoreResult<()>;
    async fn scan_keys(&self, cursor: u64, pattern: &str, count: u32) -> CoreResult<RedisScanPage>;
    async fn key_type(&self, key: &str) -> CoreResult<RedisKeyType>;
    async fn get_value(&self, key: &str) -> CoreResult<RedisValue>;
    async fn set_value(&self, key: &str, value: RedisValue) -> CoreResult<()>;
    async fn exec_command(&self, args: &[String]) -> CoreResult<RedisReply>;

    /// 有界窗口读取（对齐内核 `BoundedValueReader` + `KeyPageReader` 续读语义）：
    /// 一次调用只取窗口内的值，附完整规模与截断标记，百万成员 key 首窗毫秒级返回。
    ///
    /// `cursor` 为空串 = 从头读第一窗；非空 = 驱动私有续读游标（调用方原样回传
    /// [`RedisValueWindow::next_cursor`]）。缺省返回 `NotSupported`：仅 KV 驱动
    /// 可选覆写，调用方据此回落到 [`get_value`]（无界读）。
    async fn get_value_window(
        &self,
        _key: &str,
        _limit: RedisValueLimit,
        _cursor: &str,
    ) -> CoreResult<RedisValueWindow> {
        Err(CoreError::NotSupported(
            "get_value_window not supported by this driver".into(),
        ))
    }
}
