//! SQL 补全提供器（自 dbstudio `crates/ui/src/components/sql_completion.rs` 移植，
//! A3 契约锁定：字母/下划线触发、词中不触发、前缀大小写不敏感、单次最多 10 条）。
//!
//! 与 dbstudio 版的差异仅在于并发容器（UI 单线程 `Rc<RefCell>` 取代
//! `Arc<RwLock>`）与 schema 注入 API（本仓库以 `(表, 列…)` 对注入，CLASS/FIELD
//! 条目在此构建）。

use std::rc::Rc;

use anyhow::Result;
use gpui::{App, Task, Window};
use gpui_component::input::{CompletionProvider, Rope};
use lsp_types::{CompletionContext, CompletionItem, CompletionItemKind, CompletionResponse};

/// Common SQL keywords offered by the completion menu.
const SQL_KEYWORDS: &[&str] = &[
    "SELECT",
    "FROM",
    "WHERE",
    "INSERT",
    "INTO",
    "VALUES",
    "UPDATE",
    "SET",
    "DELETE",
    "CREATE",
    "TABLE",
    "DROP",
    "ALTER",
    "ADD",
    "COLUMN",
    "INDEX",
    "VIEW",
    "DISTINCT",
    "AS",
    "ON",
    "JOIN",
    "INNER",
    "LEFT",
    "RIGHT",
    "FULL",
    "OUTER",
    "CROSS",
    "GROUP",
    "BY",
    "ORDER",
    "HAVING",
    "LIMIT",
    "OFFSET",
    "ASC",
    "DESC",
    "AND",
    "OR",
    "NOT",
    "NULL",
    "IS",
    "IN",
    "EXISTS",
    "BETWEEN",
    "LIKE",
    "ILIKE",
    "CASE",
    "WHEN",
    "THEN",
    "ELSE",
    "END",
    "UNION",
    "ALL",
    "EXPLAIN",
    "ANALYZE",
    "BEGIN",
    "COMMIT",
    "ROLLBACK",
    "CAST",
    "COALESCE",
    "COUNT",
    "SUM",
    "AVG",
    "MIN",
    "MAX",
    "NOW",
    "CURRENT_DATE",
    "CURRENT_TIMESTAMP",
    "PRIMARY",
    "KEY",
    "FOREIGN",
    "REFERENCES",
    "UNIQUE",
    "CONSTRAINT",
    "DEFAULT",
    "TRUNCATE",
    "GRANT",
    "REVOKE",
    "USE",
    "SHOW",
    "DATABASE",
    "DATABASES",
    "TABLES",
    "SCHEMA",
    "SCHEMAS",
    "RETURNING",
    "INTERSECT",
    "EXCEPT",
    "WITH",
    "RECURSIVE",
    "OVER",
    "PARTITION",
    "FILTER",
    "WINDOW",
    "ROW",
    "ROWS",
    "PRECEDING",
    "FOLLOWING",
    "CASCADE",
    "RESTRICT",
    "IF",
];

/// 单次补全弹出的条目上限。
const MAX_RESULTS: usize = 10;

/// Provides SQL keyword and schema-derived completions to the editor.
pub struct SqlCompletionProvider {
    /// Each item carries its lowercased label. The prefix filter runs on
    /// every keystroke; pre-lowering the list once keeps keywords and schema
    /// items on the same cheap path.
    completions: Rc<std::cell::RefCell<Vec<(CompletionItem, String)>>>,
}

impl SqlCompletionProvider {
    pub fn new() -> Self {
        let completions = SQL_KEYWORDS
            .iter()
            .map(|keyword| {
                indexed(CompletionItem {
                    label: (*keyword).to_string(),
                    kind: Some(CompletionItemKind::KEYWORD),
                    detail: Some("keyword".to_string()),
                    ..Default::default()
                })
            })
            .collect();
        Self {
            completions: Rc::new(std::cell::RefCell::new(completions)),
        }
    }

    /// Replaces the schema-derived items (tables as CLASS, columns as FIELD).
    pub fn set_schema(&self, tables: Vec<(String, Vec<String>)>) {
        let mut guard = self.completions.borrow_mut();
        guard.retain(|(item, _)| {
            item.kind != Some(CompletionItemKind::CLASS)
                && item.kind != Some(CompletionItemKind::FIELD)
        });
        for (table, cols) in tables {
            guard.push(indexed(CompletionItem {
                label: table.clone(),
                kind: Some(CompletionItemKind::CLASS),
                detail: Some("table".to_string()),
                ..Default::default()
            }));
            for c in cols {
                guard.push(indexed(CompletionItem {
                    label: c,
                    kind: Some(CompletionItemKind::FIELD),
                    detail: Some(format!("column of {table}")),
                    ..Default::default()
                }));
            }
        }
    }

    /// Check if the cursor is in the middle of a word (not at word start).
    fn is_mid_word(text: &Rope, offset: usize, trigger_len: usize) -> bool {
        if offset <= trigger_len {
            return false;
        }
        let prev_char_offset = offset - trigger_len - 1;
        let prev_char = text
            .slice(prev_char_offset..prev_char_offset + 1)
            .to_string();
        prev_char
            .chars()
            .next()
            .is_some_and(|ch| ch.is_ascii_alphanumeric() || ch == '_')
    }

    /// Filter completions by prefix, case-insensitive.
    fn filter_by_prefix(
        items: &[(CompletionItem, String)],
        prefix: &str,
        max_results: usize,
    ) -> Vec<CompletionItem> {
        let query = prefix.to_lowercase();
        items
            .iter()
            .filter(|(_, label_lower)| label_lower.starts_with(&query))
            .take(max_results)
            .map(|(item, _)| item.clone())
            .collect()
    }
}

fn indexed(item: CompletionItem) -> (CompletionItem, String) {
    let label_lower = item.label.to_lowercase();
    (item, label_lower)
}

impl Default for SqlCompletionProvider {
    fn default() -> Self {
        Self::new()
    }
}

impl CompletionProvider for SqlCompletionProvider {
    fn completions(
        &self,
        text: &Rope,
        offset: usize,
        trigger: CompletionContext,
        _: &mut Window,
        _: &mut App,
    ) -> Task<Result<CompletionResponse>> {
        let Some(trigger_character) = trigger.trigger_character else {
            return Task::ready(Ok(CompletionResponse::Array(vec![])));
        };
        if trigger_character.is_empty() || Self::is_mid_word(text, offset, trigger_character.len())
        {
            return Task::ready(Ok(CompletionResponse::Array(vec![])));
        }

        let items = self.completions.borrow();
        let matches = Self::filter_by_prefix(&items, &trigger_character, MAX_RESULTS);
        Task::ready(Ok(CompletionResponse::Array(matches)))
    }

    fn is_completion_trigger(&self, _offset: usize, new_text: &str, _: &mut App) -> bool {
        new_text
            .chars()
            .next()
            .is_some_and(|ch| ch.is_ascii_alphabetic() || ch == '_')
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn labels(provider: &SqlCompletionProvider, kind: CompletionItemKind) -> Vec<String> {
        provider
            .completions
            .borrow()
            .iter()
            .filter(|(item, _)| item.kind == Some(kind))
            .map(|(item, _)| item.label.clone())
            .collect()
    }

    #[test]
    fn schema_replacement_keeps_the_index() {
        let provider = SqlCompletionProvider::default();
        provider.set_schema(vec![("orders".into(), vec!["id".into()])]);
        provider.set_schema(vec![("Orders".into(), vec![])]);
        assert_eq!(
            labels(&provider, CompletionItemKind::CLASS).join(","),
            "Orders"
        );
        assert_eq!(
            labels(&provider, CompletionItemKind::KEYWORD).len(),
            SQL_KEYWORDS.len()
        );
        let guard = provider.completions.borrow();
        let indexed = guard
            .iter()
            .find(|(item, _)| item.label == "Orders")
            .expect("schema item missing");
        assert_eq!(indexed.1, "orders");
    }

    #[test]
    fn columns_come_in_as_field_items() {
        let provider = SqlCompletionProvider::default();
        provider.set_schema(vec![("orders".into(), vec!["id".into(), "user_id".into()])]);
        assert_eq!(
            labels(&provider, CompletionItemKind::FIELD).join(","),
            "id,user_id"
        );
        // 换 schema 后旧列必须被替换，不得残留。
        provider.set_schema(vec![("users".into(), vec!["name".into()])]);
        assert_eq!(
            labels(&provider, CompletionItemKind::FIELD).join(","),
            "name"
        );
    }

    #[test]
    fn prefix_filter_is_case_insensitive_and_capped() {
        let provider = SqlCompletionProvider::default();
        provider.set_schema(vec![(
            "t".into(),
            ("a0 a1 a2 a3 a4 a5 a6 a7 a8 a9 b0 b1")
                .split(' ')
                .map(|s| s.to_string())
                .collect(),
        )]);
        let items = provider.completions.borrow();
        let matched = SqlCompletionProvider::filter_by_prefix(&items, "A", MAX_RESULTS);
        // 命中池里关键字（ALL/AND/…）与 schema 列同池竞争，顺序不保证；
        // 契约是"全部命中且恰好截断到上限"。
        assert_eq!(matched.len(), MAX_RESULTS);
        assert!(matched
            .iter()
            .all(|i| i.label.to_lowercase().starts_with('a')));
        // 关键字与 schema 条目同池过滤。
        let sel = SqlCompletionProvider::filter_by_prefix(&items, "sel", MAX_RESULTS);
        assert_eq!(sel.len(), 1);
        assert_eq!(sel[0].label, "SELECT");
    }
}
