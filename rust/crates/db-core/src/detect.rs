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

/// 会变更数据或结构的 SQL 关键字（整词、大小写不敏感）。
/// 只读拦截（behavior.md §12.3）据此判定，不复用 [`detect_statement_type`]——
/// 后者把数据修改 CTE（`WITH … DELETE/UPDATE/INSERT`）与 TRUNCATE/GRANT 等
/// 归入 select/other，会让这些写语句漏过只读红线。
const MUTATING_KEYWORDS: &[&str] = &[
    "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "TRUNCATE", "GRANT", "REVOKE", "CREATE",
    "ALTER", "DROP", "CALL", "EXEC", "EXECUTE", "COPY", "VACUUM", "REINDEX", "CLUSTER", "LOCK",
    "COMMENT",
    // Writes whose leading keyword is none of the above, so the §11 classifier
    // calls them select/other and they would slip past the read-only gate.
    "SETVAL", "REFRESH", "INTO", "LOAD", "DO", "OPTIMIZE",
];

/// 判断语句是否包含变更关键字。扫描整条语句（含 WITH 子树与多语句），先剥离
/// `--` / `/* */` 注释与 `'…'`、`"…"`、`` `…` ``、`[…]` 字面量再整词匹配：
/// 字符串里的 "delete"、名为 inserted_at 的列都不会误判。与 Go 侧
/// `dbcore.MutatesData` 逐字一致。
pub fn mutates_data(sql: &str) -> bool {
    stripped_words(sql)
        .iter()
        .any(|word| MUTATING_KEYWORDS.contains(&word.as_str()))
}

/// 把注释与引号字面量替换为空格后，按整词切出大写 token 序列。
fn stripped_words(sql: &str) -> Vec<String> {
    let chars: Vec<char> = sql.chars().collect();
    let mut stripped = String::with_capacity(sql.len());
    let mut i = 0usize;
    while i < chars.len() {
        let c = chars[i];
        match c {
            '\'' | '"' | '`' | '[' => {
                i = skip_quoted(&chars, i, c);
                stripped.push(' ');
            }
            '-' if chars.get(i + 1) == Some(&'-') => {
                while i < chars.len() && chars[i] != '\n' {
                    i += 1;
                }
                stripped.push(' ');
            }
            '/' if chars.get(i + 1) == Some(&'*') => {
                let rest: String = chars[i + 2..].iter().collect();
                match rest.find("*/") {
                    Some(end) => i += 2 + end + 2,
                    None => i = chars.len(),
                }
                stripped.push(' ');
            }
            _ => {
                stripped.push(c);
                i += 1;
            }
        }
    }
    stripped
        .split(|c: char| !(c.is_ascii_alphanumeric() || c == '_'))
        .filter(|w| !w.is_empty())
        .map(|w| w.to_uppercase())
        .collect()
}

/// 越过从 bytes[start] 开始的引号字面量，返回结束引号之后的下标。
/// 处理 `''`（双写）与 MySQL 反斜杠转义；`[]` 标识符只按 `]` 结束。
fn skip_quoted(bytes: &[char], start: usize, quote: char) -> usize {
    let mut i = start + 1;
    while i < bytes.len() {
        match bytes[i] {
            '\\' if quote == '\'' => i += 2,
            c if c == quote && quote == '[' => return i + 1,
            c if c == quote => {
                if bytes.get(i + 1) == Some(&quote) {
                    i += 2;
                } else {
                    return i + 1;
                }
            }
            _ => i += 1,
        }
    }
    bytes.len()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 与 Go 侧 detect_test.go 的 TestMutatesData 用例逐条镜像。
    #[test]
    fn mutates_data_scans_whole_statement() {
        let cases: &[(&str, bool)] = &[
            ("SELECT 1", false),
            ("SELECT * FROM users WHERE name = 'delete'", false),
            ("-- DROP TABLE users\nSELECT 1", false),
            ("/* update */ SELECT 1", false),
            ("SELECT 'insert' AS note", false),
            (
                "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
                true,
            ),
            (
                "WITH u AS (UPDATE t SET x = 1 RETURNING x) SELECT * FROM u",
                true,
            ),
            (
                "WITH i AS (INSERT INTO t VALUES (1) RETURNING id) SELECT * FROM i",
                true,
            ),
            ("TRUNCATE TABLE t", true),
            ("GRANT SELECT ON t TO u", true),
            ("CALL do_write()", true),
            ("EXEC sp_adduser", true),
            ("SELECT 1; DROP TABLE t", true),
            ("SELECT * FROM inserted_events", false),
            ("SELECT updated_at FROM t", false),
            ("SELECT \"delete\" FROM t", false),
            ("SELECT [delete] FROM t", false),
            ("select * from t where note = 'it''s not a delete'", false),
            // Writes reached through a leading keyword the §11 classifier calls
            // select/other.
            ("SELECT setval('seq1', 100)", true),
            ("REFRESH MATERIALIZED VIEW mv", true),
            ("REFRESH TABLE t", true),
            ("SELECT * INTO newt FROM t", true),
            ("SELECT * FROM t INTO OUTFILE '/tmp/dump.csv'", true),
            ("LOAD DATA INFILE '/tmp/x' INTO TABLE t", true),
            ("DO $$ BEGIN UPDATE t SET x = 1; END $$", true),
            ("OPTIMIZE TABLE t", true),
            ("SELECT nextval('seq1') AS n", false),
            (
                "SELECT * FROM \"intoval\" WHERE note = 'into the woods'",
                false,
            ),
            ("SELECT * FROM setval_log WHERE load_status = 1", false),
            ("SELECT do_count, refresher, optimize_time FROM t", false),
        ];
        for (sql, want) in cases {
            assert_eq!(mutates_data(sql), *want, "mutates_data({sql:?})");
        }
    }

    #[test]
    fn detect_statement_type_keeps_with_as_select() {
        assert_eq!(
            detect_statement_type("WITH d AS (DELETE FROM t) SELECT * FROM d"),
            StatementType::Select
        );
    }
}
