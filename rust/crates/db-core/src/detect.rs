//! 语句类型检测：与 Go 侧 `pkg/dbcore/detect.go` 逐字一致（spec/behavior.md §11）。
//!
//! 词法判定一律读 token 的类别（[`scan::TokenKind`]），方言由连接的
//! `DatabaseKind` 决定——「字面量在哪结束」是方言决定的，认错方言会把后面
//! 的写语句当成字面量吞掉（fail-open，直接丢数据）。

use crate::scan::{self, LexProfile, TokenKind};
use polydb_protocol::common::DatabaseKind;
use polydb_protocol::StatementType;

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

/// 判断语句是否包含变更关键字（behavior.md §12.3 只读拦截的判定基础）。
///
/// 扫描整条语句（含 WITH 子树与多语句），**按 token 类别**判定：只认裸词
/// token，关键字藏在 '…'、"…"、`…`、[…]、$$…$$、q'…' 这些字面量里、或藏在
/// -- / # / /* */ 注释里，都不算命中——字符串里的 "delete"、名为 inserted_at
/// 的列都不会误判。
///
/// kind 是连接的方言：词法差异（'#' 行注释、反斜杠转义、PG 的 dollar-quote、
/// Oracle 的 q'…'、MySQL 的 /*! … */）决定「字面量在哪结束」。方言未知时
/// （None）取各剖面并集：任一剖面认为写了就算写了——宁可多问，放过去就是
/// 丢数据。
pub fn mutates_data(sql: &str, kind: Option<DatabaseKind>) -> bool {
    scan::profiles_for(kind).iter().any(|p| {
        scan::scan_tokens(sql, *p).iter().any(|t| {
            t.kind == TokenKind::Word
                && MUTATING_KEYWORDS.contains(&t.text(sql).to_ascii_uppercase().as_str())
        })
    })
}

/// 按方言感知的分隔符切分 SQL：字面量与注释里的 ';' 不是分隔符。
///
/// 一个方言可能有多个词法变体（反斜杠转义开/关），取切得**最细**的那一份：
/// 多切出来的片段只会让只读闸门更严，不会把写语句藏进只读片段里。
/// 方言未知时同样取各变体里最细的一份。
pub fn split_statements(sql: &str, kind: Option<DatabaseKind>) -> Vec<&str> {
    let mut best: Vec<&str> = Vec::new();
    for p in scan::profiles_for(kind) {
        let s = split_under(sql, p);
        if s.len() > best.len() {
            best = s;
        }
    }
    best
}

/// 按给定词法剖面把 SQL 切成语句（跳过空片段）。
fn split_under<'a>(sql: &'a str, p: LexProfile) -> Vec<&'a str> {
    let mut out: Vec<&str> = Vec::new();
    let mut start = 0usize;
    for t in scan::scan_tokens(sql, p) {
        if t.kind != TokenKind::Semicolon {
            continue;
        }
        let seg = sql[start..t.start].trim();
        if !seg.is_empty() {
            out.push(seg);
        }
        start = t.end;
    }
    let seg = sql[start..].trim();
    if !seg.is_empty() {
        out.push(seg);
    }
    out
}

/// 返回语句的首个关键字（大写）；没有关键字时返回空串。
///
/// 方言已知时按该方言判定；方言未知时要求各剖面结论一致，否则返回空串
/// （判不出关键字 → 只读闸门按「不认识」处理，更严）。
pub fn leading_keyword(sql: &str, kind: Option<DatabaseKind>) -> String {
    let profiles = scan::profiles_for(kind);
    let mut kw = String::new();
    for (i, p) in profiles.iter().enumerate() {
        let k = leading_keyword_under(sql, *p);
        if i == 0 {
            kw = k;
            continue;
        }
        if k != kw {
            return String::new();
        }
    }
    kw
}

/// 按给定词法剖面取首个关键字。
fn leading_keyword_under(sql: &str, p: LexProfile) -> String {
    for t in scan::scan_tokens(sql, p) {
        if t.kind == TokenKind::Comment {
            continue;
        }
        if t.kind != TokenKind::Word {
            return String::new();
        }
        return t.text(sql).to_ascii_uppercase();
    }
    String::new()
}

/// 判定语句类型（按首个关键字整词判定），所有 SQL 驱动共用。
///
/// 与 Go 侧 `dbcore.DetectStatementType` 逐字一致（spec/behavior.md §11 双后端一致性红线）。
pub fn detect_statement_type(sql: &str, kind: Option<DatabaseKind>) -> StatementType {
    match leading_keyword(sql, kind).as_str() {
        "SELECT" | "WITH" => StatementType::Select,
        "INSERT" => StatementType::Insert,
        "UPDATE" => StatementType::Update,
        "DELETE" => StatementType::Delete,
        "CREATE" | "ALTER" | "DROP" => StatementType::Ddl,
        _ => StatementType::Other,
    }
}

/// 剥离 SQL 开头的前导注释（-- 行、# 行（MySQL）、/* */ 块）与空白。
/// 各驱动统一走这里，避免带注释的 SELECT 被误判为 OTHER 而走 Exec 分支
/// （双后端一致性红线）。
pub fn strip_leading_comments(sql: &str, kind: Option<DatabaseKind>) -> &str {
    for t in scan::scan_tokens(sql, scan::profile_for_kind(kind)) {
        if t.kind == TokenKind::Comment {
            continue;
        }
        return &sql[t.start..];
    }
    ""
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 与 Go 侧 detect_test.go 的 TestMutatesData 用例逐条镜像。
    /// kind 为 None（方言未知 → 取各剖面并集）。
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
            assert_eq!(mutates_data(sql, None), *want, "mutates_data({sql:?})");
        }
    }

    /// 与 Go 侧 detect_test.go 的 TestMutatesDataDialects 用例逐条镜像。
    /// 「字面量在哪结束」是方言决定的：认错方言 = 把写语句当字面量吞掉。
    #[test]
    fn mutates_data_is_dialect_aware() {
        type Case = (&'static str, &'static str, Option<DatabaseKind>, bool);
        let cases: &[Case] = &[
            // '#'：MySQL/MariaDB 是行注释，PostgreSQL 是位异或运算符。
            (
                "mysql/hash-comment-then-write",
                "SELECT 1 # it's ok\n; DROP TABLE t",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "mysql/hash-comment-swallows-write",
                "SELECT 1 # ok; DROP TABLE t",
                Some(DatabaseKind::MySql),
                false,
            ),
            (
                "pg/hash-is-xor-not-comment",
                "SELECT 1 # ok; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            (
                "pg/hash-then-apostrophe",
                "SELECT 1 # it's ok\n; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                false,
            ),
            // '--'：MySQL 必须跟空白/控制符才是注释，1--2 是减法。
            (
                "mysql/dash-dash-needs-space",
                "SELECT 1--2; DELETE FROM t",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "mysql/dash-dash-with-space-is-comment",
                "SELECT 1 -- 2; DELETE FROM t",
                Some(DatabaseKind::MySql),
                false,
            ),
            (
                "pg/dash-dash-always-comment",
                "SELECT 1--2; DELETE FROM t",
                Some(DatabaseKind::Postgres),
                false,
            ),
            // 反斜杠转义：语义由服务端设置决定，两种解释都扫。
            (
                "mysql/backslash-escape-ambiguous",
                "SELECT 'a\\'; DROP TABLE t",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "pg/plain-string-no-backslash-escape",
                "SELECT 'a\\'; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            (
                "pg/e-string-honors-backslash",
                "SELECT E'a\\'; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                false,
            ),
            // PostgreSQL dollar-quote。
            (
                "pg/dollar-quote-hides-write",
                "SELECT $tag$ DROP TABLE t $tag$",
                Some(DatabaseKind::Postgres),
                false,
            ),
            (
                "pg/dollar-quote-body-has-apostrophe",
                "SELECT $tag$ it's a trap $tag$; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            (
                "pg/dollar-placeholder-not-quote",
                "SELECT * FROM t WHERE id = $1; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            (
                "pg/dollar-tag-needs-letter",
                "SELECT $1$; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            (
                "pg/dollar-not-after-identifier",
                "SELECT a$b$c; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                true,
            ),
            // Oracle q'…'：正文里可以含引号。
            (
                "oracle/q-quote-hides-write",
                "SELECT q'[it's ok]' FROM dual; DROP TABLE t",
                Some(DatabaseKind::Oracle),
                true,
            ),
            (
                "pg/q-quote-is-not-special",
                "SELECT q'[it's ok]' FROM dual; DROP TABLE t",
                Some(DatabaseKind::Postgres),
                false,
            ),
            // MySQL 可执行注释 /*! … */：服务端会执行注释体。
            (
                "mysql/exec-comment-is-executed",
                "/*!50000 DROP TABLE t */",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "mysql/exec-comment-maria",
                "/*M! DELETE FROM t */",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "mysql/exec-comment-no-version",
                "/*! DROP TABLE t */",
                Some(DatabaseKind::MySql),
                true,
            ),
            (
                "pg/exec-comment-is-plain-comment",
                "/*!50000 DROP TABLE t */",
                Some(DatabaseKind::Postgres),
                false,
            ),
            // 未知方言：取各剖面并集（fail-closed）。
            (
                "unknown/dollar-quote-not-recognized",
                "SELECT $tag$ DROP TABLE t $tag$",
                None,
                true,
            ),
        ];
        for (name, sql, kind, want) in cases {
            assert_eq!(
                mutates_data(sql, *kind),
                *want,
                "{name}: mutates_data({sql:?}, {kind:?})"
            );
        }
    }

    /// 与 Go 侧 detect_test.go 的 TestMutatesDataDialectDivergence 逐条镜像：
    /// 同一段 SQL 在不同方言下结论不同——这是「字面量在哪结束由方言决定」的
    /// 必然结果，不是实现 bug。
    #[test]
    fn mutates_data_diverges_across_dialects() {
        let hash: &[(&str, Option<DatabaseKind>, bool)] = &[
            ("SELECT 1 # ok; DROP TABLE t", Some(DatabaseKind::MySql), false),
            ("SELECT 1 # ok; DROP TABLE t", Some(DatabaseKind::Postgres), true),
            ("SELECT 1 # ok; DROP TABLE t", Some(DatabaseKind::Sqlite), true),
        ];
        for (sql, kind, want) in hash {
            assert_eq!(mutates_data(sql, *kind), *want, "hash/{sql:?}/{kind:?}");
        }

        let dash: &[(&str, Option<DatabaseKind>, bool)] = &[
            ("SELECT 1--2; DELETE FROM t", Some(DatabaseKind::MySql), true),
            ("SELECT 1--2; DELETE FROM t", Some(DatabaseKind::Postgres), false),
            ("SELECT 1--2; DELETE FROM t", Some(DatabaseKind::Sqlite), false),
        ];
        for (sql, kind, want) in dash {
            assert_eq!(mutates_data(sql, *kind), *want, "dash/{sql:?}/{kind:?}");
        }

        let dollar: &[(&str, Option<DatabaseKind>, bool)] = &[
            ("SELECT $tag$ DROP TABLE t $tag$", Some(DatabaseKind::Postgres), false),
            ("SELECT $tag$ DROP TABLE t $tag$", Some(DatabaseKind::MySql), true),
            ("SELECT $tag$ DROP TABLE t $tag$", Some(DatabaseKind::Sqlite), true),
        ];
        for (sql, kind, want) in dollar {
            assert_eq!(mutates_data(sql, *kind), *want, "dollar/{sql:?}/{kind:?}");
        }

        let q: &[(&str, Option<DatabaseKind>, bool)] = &[
            ("SELECT q'[it's ok]' FROM dual; DROP TABLE t", Some(DatabaseKind::Oracle), true),
            ("SELECT q'[it's ok]' FROM dual; DROP TABLE t", Some(DatabaseKind::Postgres), false),
            ("SELECT q'[it's ok]' FROM dual; DROP TABLE t", Some(DatabaseKind::MySql), false),
        ];
        for (sql, kind, want) in q {
            assert_eq!(mutates_data(sql, *kind), *want, "q/{sql:?}/{kind:?}");
        }

        let exec: &[(&str, Option<DatabaseKind>, bool)] = &[
            ("/*!50000 DROP TABLE t */", Some(DatabaseKind::MySql), true),
            ("/*!50000 DROP TABLE t */", Some(DatabaseKind::Postgres), false),
            ("/*!50000 DROP TABLE t */", Some(DatabaseKind::Sqlite), false),
        ];
        for (sql, kind, want) in exec {
            assert_eq!(mutates_data(sql, *kind), *want, "exec/{sql:?}/{kind:?}");
        }
    }

    /// 与 Go 侧 detect_test.go 的 TestSplitStatements 逐条镜像。
    #[test]
    fn split_statements_is_dialect_aware() {
        assert_eq!(
            split_statements("SELECT 1; SELECT 2", Some(DatabaseKind::Sqlite)),
            vec!["SELECT 1", "SELECT 2"]
        );
        assert_eq!(
            split_statements("SELECT 'a;b' FROM t", Some(DatabaseKind::Sqlite)),
            vec!["SELECT 'a;b' FROM t"]
        );
        assert_eq!(
            split_statements("SELECT $$a;b$$", Some(DatabaseKind::Postgres)),
            vec!["SELECT $$a;b$$"]
        );
        assert_eq!(
            split_statements("SELECT 1 # a;b\nFROM t", Some(DatabaseKind::MySql)),
            vec!["SELECT 1 # a;b\nFROM t"]
        );
        assert_eq!(
            split_statements("SELECT 1 # 2; SELECT 3", Some(DatabaseKind::Postgres)),
            vec!["SELECT 1 # 2", "SELECT 3"]
        );
        assert_eq!(
            split_statements("SELECT 1--2; SELECT 3", Some(DatabaseKind::MySql)),
            vec!["SELECT 1--2", "SELECT 3"]
        );
        assert_eq!(
            split_statements("SELECT 1--2; SELECT 3", Some(DatabaseKind::Postgres)),
            vec!["SELECT 1--2; SELECT 3"]
        );
        // 未知方言取并集里最细的一份：切得多只会让闸门更严。
        assert_eq!(
            split_statements("SELECT $$a;b$$", None),
            vec!["SELECT $$a", "b$$"]
        );
    }

    /// 与 Go 侧 detect_test.go 的 TestLeadingKeyword 逐条镜像。
    #[test]
    fn leading_keyword_skips_comments() {
        assert_eq!(leading_keyword("SELECT 1", None), "SELECT");
        assert_eq!(leading_keyword("   select 1", None), "SELECT");
        assert_eq!(leading_keyword("-- c\nSELECT 1", None), "SELECT");
        assert_eq!(leading_keyword("/* c */ SELECT 1", None), "SELECT");
        assert_eq!(
            leading_keyword("# c\nSELECT 1", Some(DatabaseKind::MySql)),
            "SELECT"
        );
        assert_eq!(
            leading_keyword("WITH d AS (SELECT 1) SELECT * FROM d", None),
            "WITH"
        );
        assert_eq!(leading_keyword("(SELECT 1)", None), "");
        assert_eq!(leading_keyword("'x'", None), "");
        assert_eq!(leading_keyword("", None), "");
    }

    /// 与 Go 侧 detect_test.go 的 TestDetectStatementType 逐条镜像。
    /// 判型按首个关键字整词判定（不再是前缀匹配）。
    #[test]
    fn detect_statement_type_by_whole_keyword() {
        assert_eq!(
            detect_statement_type("WITH d AS (DELETE FROM t) SELECT * FROM d", None),
            StatementType::Select
        );
        assert_eq!(
            detect_statement_type("SELECT 1", None),
            StatementType::Select
        );
        assert_eq!(
            detect_statement_type("INSERT INTO t VALUES (1)", None),
            StatementType::Insert
        );
        assert_eq!(
            detect_statement_type("UPDATE t SET x = 1", None),
            StatementType::Update
        );
        assert_eq!(
            detect_statement_type("DELETE FROM t", None),
            StatementType::Delete
        );
        assert_eq!(
            detect_statement_type("CREATE TABLE t (id INT)", None),
            StatementType::Ddl
        );
        assert_eq!(
            detect_statement_type("DROP TABLE t", None),
            StatementType::Ddl
        );
        // 前缀匹配的老毛病："SELECTED" 会被旧实现判成 Select。
        assert_eq!(
            detect_statement_type("SELECTED FROM t", None),
            StatementType::Other
        );
        assert_eq!(
            detect_statement_type("SHOW TABLES", Some(DatabaseKind::MySql)),
            StatementType::Other
        );
        assert_eq!(
            detect_statement_type("-- c\nSELECT 1", None),
            StatementType::Select
        );
    }

    /// 与 Go 侧 detect_test.go 的 TestStripLeadingComments 逐条镜像。
    #[test]
    fn strip_leading_comments_skips_leading_comments() {
        assert_eq!(strip_leading_comments("-- c\nSELECT 1", None), "SELECT 1");
        assert_eq!(strip_leading_comments("/* c */ SELECT 1", None), "SELECT 1");
        assert_eq!(
            strip_leading_comments("/* c */ -- d\nSELECT 1", None),
            "SELECT 1"
        );
        assert_eq!(
            strip_leading_comments("# c\nSELECT 1", Some(DatabaseKind::MySql)),
            "SELECT 1"
        );
        assert_eq!(strip_leading_comments("SELECT 1", None), "SELECT 1");
        assert_eq!(strip_leading_comments("  SELECT 1", None), "SELECT 1");
        // 未闭合的注释吃掉整条（服务端会报语法错）。
        assert_eq!(strip_leading_comments("-- no newline", None), "");
        assert_eq!(strip_leading_comments("/* unclosed", None), "");
        assert_eq!(strip_leading_comments("", None), "");
    }
}
