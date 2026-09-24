//! G4 结果集导出：纯文本序列化，语义对齐 Web `web/src/lib/exporters.ts`（独立实现）。
//!
//! 单元格来源为 `polydb_core::Value`（网格原值）。SQL 方言/字面量复用 `changes` 模块，
//! 保证与编辑器提交的引号规则一致。

use polydb_core::{DatabaseKind, Value};

use crate::changes::{quote_ident, value_to_literal};

/// 单元格 → 导出文本（CSV/TSV/Markdown 用）。NULL 在 CSV/TSV 渲染为空串。
fn cell_text(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::Bool(b) => {
            if *b {
                "true".to_string()
            } else {
                "false".to_string()
            }
        }
        Value::Integer(i) => i.to_string(),
        Value::Float(f) => format!("{}", f),
        Value::String(s) => s.clone(),
        other => serde_json::to_string(other).unwrap_or_default(),
    }
}

fn csv_escape(v: &Value) -> String {
    let s = cell_text(v);
    if s.contains(['"', ',', '\n']) {
        format!("\"{}\"", s.replace('"', "\"\""))
    } else {
        s
    }
}

/// CSV：默认带 UTF-8 BOM（Excel 友好）；表头按原样不转义（对齐 Web）。
pub(crate) fn to_csv(columns: &[String], rows: &[Vec<Value>], with_bom: bool) -> String {
    let mut out = String::new();
    if with_bom {
        out.push('\u{FEFF}');
    }
    out.push_str(&columns.join(","));
    for row in rows {
        out.push('\n');
        out.push_str(
            &columns
                .iter()
                .enumerate()
                .map(|(i, _)| csv_escape(row.get(i).unwrap_or(&Value::Null)))
                .collect::<Vec<_>>()
                .join(","),
        );
    }
    out
}

fn tsv_escape(v: &Value) -> String {
    cell_text(v).replace(['\t', '\n'], " ")
}

pub(crate) fn to_tsv(columns: &[String], rows: &[Vec<Value>]) -> String {
    let mut out = columns.join("\t");
    for row in rows {
        out.push('\n');
        out.push_str(
            &columns
                .iter()
                .enumerate()
                .map(|(i, _)| tsv_escape(row.get(i).unwrap_or(&Value::Null)))
                .collect::<Vec<_>>()
                .join("\t"),
        );
    }
    out
}

fn json_obj(cols: &[String], row: &[Value], indent: bool) -> String {
    let pad = if indent { "  " } else { "" };
    let parts: Vec<String> = cols
        .iter()
        .enumerate()
        .map(|(i, c)| {
            let key = serde_json::to_string(c).unwrap_or_else(|_| "\"\"".to_string());
            let val = serde_json::to_string(row.get(i).unwrap_or(&Value::Null))
                .unwrap_or_else(|_| "null".to_string());
            format!("{key}: {val}")
        })
        .collect();
    format!("{pad}{{ {} }}", parts.join(", "))
}

/// JSON：对象数组，保留列顺序，2 空格缩进（对齐 Web JSON.stringify(obj,null,2)）。
pub(crate) fn to_json(columns: &[String], rows: &[Vec<Value>]) -> String {
    if rows.is_empty() {
        return "[]".to_string();
    }
    let body = rows
        .iter()
        .map(|r| json_obj(columns, r, true))
        .collect::<Vec<_>>()
        .join(",\n");
    format!("[\n{body}\n]")
}

/// NDJSON：每行一个紧凑对象，无尾换行。
pub(crate) fn to_ndjson(columns: &[String], rows: &[Vec<Value>]) -> String {
    rows.iter()
        .map(|r| json_obj(columns, r, false).trim_start().to_string())
        .collect::<Vec<_>>()
        .join("\n")
}

fn md_cell(v: &Value) -> String {
    if matches!(v, Value::Null) {
        return "NULL".to_string();
    }
    cell_text(v).replace('|', "\\|").replace('\n', "<br>")
}

pub(crate) fn to_markdown(columns: &[String], rows: &[Vec<Value>]) -> String {
    let mut out = format!("| {} |", columns.join(" | "));
    out.push('\n');
    out.push_str(
        &columns
            .iter()
            .map(|_| " --- ")
            .collect::<Vec<_>>()
            .join("|"),
    );
    out.push('|');
    for row in rows {
        out.push('\n');
        out.push_str(&format!(
            "| {} |",
            columns
                .iter()
                .enumerate()
                .map(|(i, _)| md_cell(row.get(i).unwrap_or(&Value::Null)))
                .collect::<Vec<_>>()
                .join(" | ")
        ));
    }
    out
}

/// 表名限定：含 `.` 时按段分别引用再以 `.` 重连（对齐 Web）。
fn q_table(kind: DatabaseKind, table: &str) -> String {
    if table.contains('.') {
        table
            .split('.')
            .map(|s| quote_ident(kind, s))
            .collect::<Vec<_>>()
            .join(".")
    } else {
        quote_ident(kind, table)
    }
}

/// INSERT：每行一条独立语句，`\n` 连接（不做多值批）。
pub(crate) fn to_sql_insert(
    kind: DatabaseKind,
    table: &str,
    columns: &[String],
    rows: &[Vec<Value>],
) -> String {
    let qt = q_table(kind, table);
    let qcols = columns
        .iter()
        .map(|c| quote_ident(kind, c))
        .collect::<Vec<_>>()
        .join(", ");
    rows.iter()
        .map(|row| {
            let vals = columns
                .iter()
                .enumerate()
                .map(|(i, _)| {
                    let v = row.get(i).unwrap_or(&Value::Null);
                    if matches!(v, Value::Null) {
                        "NULL".to_string()
                    } else {
                        value_to_literal(kind, v)
                    }
                })
                .collect::<Vec<_>>()
                .join(", ");
            format!("INSERT INTO {qt} ({qcols}) VALUES ({vals});")
        })
        .collect::<Vec<_>>()
        .join("\n")
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum ExportFormat {
    Csv,
    Tsv,
    Json,
    Ndjson,
    Markdown,
    Insert,
}

impl ExportFormat {
    pub(crate) const ALL: [ExportFormat; 6] = [
        ExportFormat::Csv,
        ExportFormat::Tsv,
        ExportFormat::Json,
        ExportFormat::Ndjson,
        ExportFormat::Markdown,
        ExportFormat::Insert,
    ];

    pub(crate) fn label(&self) -> &'static str {
        match self {
            ExportFormat::Csv => "CSV",
            ExportFormat::Tsv => "TSV",
            ExportFormat::Json => "JSON",
            ExportFormat::Ndjson => "NDJSON",
            ExportFormat::Markdown => "Markdown",
            ExportFormat::Insert => "INSERT",
        }
    }

    pub(crate) fn extension(&self) -> &'static str {
        match self {
            ExportFormat::Csv => "csv",
            ExportFormat::Tsv => "tsv",
            ExportFormat::Json => "json",
            ExportFormat::Ndjson => "ndjson",
            ExportFormat::Markdown => "md",
            ExportFormat::Insert => "insert.sql",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cols(names: &[&str]) -> Vec<String> {
        names.iter().map(|s| s.to_string()).collect()
    }
    fn s(x: &str) -> Value {
        Value::String(x.to_string())
    }
    fn i(x: i64) -> Value {
        Value::Integer(x)
    }

    #[test]
    fn csv_quotes_and_bom() {
        let out = to_csv(
            &cols(&["a", "b"]),
            &[vec![i(2), s("comma, x")], vec![Value::Null, s("plain")]],
            true,
        );
        assert_eq!(out, "\u{FEFF}a,b\n2,\"comma, x\"\n,plain");
    }

    #[test]
    fn tsv_flattens_tabs_and_newlines() {
        let out = to_tsv(&cols(&["a", "b"]), &[vec![s("x\ty"), s("p\nq")]]);
        assert_eq!(out, "a\tb\nx y\tp q");
    }

    #[test]
    fn json_preserves_order_and_null() {
        let out = to_json(&cols(&["id", "name"]), &[vec![i(1), Value::Null]]);
        assert_eq!(out, "[\n  { \"id\": 1, \"name\": null }\n]");
    }

    #[test]
    fn ndjson_compact() {
        let out = to_ndjson(&cols(&["a"]), &[vec![i(1)], vec![s("x")]]);
        assert_eq!(out, "{ \"a\": 1 }\n{ \"a\": \"x\" }");
    }

    #[test]
    fn markdown_escapes_pipe_and_null() {
        let out = to_markdown(&cols(&["a", "b"]), &[vec![Value::Null, s("x|y")]]);
        let lines: Vec<&str> = out.split('\n').collect();
        assert_eq!(lines[0], "| a | b |");
        assert_eq!(lines[2], "| NULL | x\\|y |");
    }

    #[test]
    fn insert_postgres_dotted_and_mysql_backtick() {
        let pg = to_sql_insert(
            DatabaseKind::Postgres,
            "main.users",
            &cols(&["id", "name"]),
            &[vec![i(1), s("a'b")]],
        );
        assert_eq!(
            pg,
            "INSERT INTO \"main\".\"users\" (\"id\", \"name\") VALUES (1, 'a''b');"
        );
        let my = to_sql_insert(
            DatabaseKind::MySql,
            "users",
            &cols(&["id"]),
            &[vec![Value::Null]],
        );
        assert_eq!(my, "INSERT INTO `users` (`id`) VALUES (NULL);");
    }
}
