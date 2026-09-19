//! 语句类型检测：与 Go 侧 `dbcore.DetectStatementType` 逐字一致（spec/behavior.md §11）。
//!
//! 各驱动内部保留私有副本用于执行分支；app-core（只读拦截等）使用本公共版本。
//! 收敛副本见 docs/plan-tablepro-parity.md M11。

use polydb_protocol::StatementType;

/// 剥离 SQL 开头的前导注释（`--` 行注释与 `/* */` 块注释）与空白。
pub fn strip_leading_comments(sql: &str) -> &str {
    let mut s = sql.trim_start();
    loop {
        if let Some(rest) = s.strip_prefix("--") {
            match rest.find('\n') {
                Some(i) => s = rest[i + 1..].trim_start(),
                None => return "",
            }
        } else if let Some(rest) = s.strip_prefix("/*") {
            match rest.find("*/") {
                Some(i) => s = rest[i + 2..].trim_start(),
                None => return "",
            }
        } else {
            return s;
        }
    }
}

/// 按前缀规则判定语句类型（判定前剥离前导注释）。
/// `WITH ...` 统一判 select（含 `WITH ... INSERT/UPDATE/DELETE` 形态），两端对齐。
pub fn detect_statement_type(sql: &str) -> StatementType {
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
