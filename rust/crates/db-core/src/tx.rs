use polydb_core::IsolationLevel;

/// TxMode 是各 SQL 驱动的 begin_tx 选项。与 Go 侧 dbcore.TxMode 对齐：
/// 驱动自行把 IsolationLevel 翻译成数据库方言；不支持任意隔离级别的驱动
/// 可忽略该字段。
#[derive(Debug, Clone, Copy, Default)]
pub struct TxMode {
    pub isolation_level: IsolationLevel,
}

impl TxMode {
    pub fn new(isolation_level: IsolationLevel) -> Self {
        Self { isolation_level }
    }
}
