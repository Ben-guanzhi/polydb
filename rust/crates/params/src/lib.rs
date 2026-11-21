//! SQL 占位符提取/重写（§384）——dbcore/sqltext 的 Rust 对拍侧。
//! 语义与 Go 内核逐字一致：四式识别（?/$n/:name/@pn）+ 词法安全
//! （字符串/注释/::cast/@@var/反引号）+ 绑定序位。共享向量语料在
//! test/param-vectors/*.json，两侧测试读同一批文件（schema-diff 先例）。

use serde::Deserialize;

/// 一个占位符出现处：区间、绑定序位、具名名。
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct ParamRef {
    pub start: usize,
    pub end: usize,
    pub index: usize,
    #[serde(default)]
    pub name: String,
}

impl ParamRef {
    /// 面板展示标签：具名为裸名，位置式为 1 基序号。
    pub fn label(&self) -> String {
        if self.name.is_empty() {
            (self.index + 1).to_string()
        } else {
            self.name.clone()
        }
    }
}

fn is_digit(b: u8) -> bool { b.is_ascii_digit() }
fn is_ident_start(b: u8) -> bool { b.is_ascii_alphabetic() || b == b'_' }
fn is_ident_char(b: u8) -> bool { is_ident_start(b) || is_digit(b) }

/// 按绑定序列出 sql 的占位符（词法安全，语义与 Go 内核一致）。
pub fn extract_params(sql: &str) -> Vec<ParamRef> {
    let bytes = sql.as_bytes();
    let n = bytes.len();
    let mut refs = Vec::new();
    let mut by_name = std::collections::HashMap::new();
    let mut i = 0;
    while i < n {
        let c = bytes[i];
        match c {
            b'\'' | b'"' | b'`' => {
                let quote = c;
                let mut j = i + 1;
                while j < n {
                    if bytes[j] == quote {
                        if j + 1 < n && bytes[j + 1] == quote {
                            j += 2;
                            continue;
                        }
                        j += 1;
                        break;
                    }
                    j += 1;
                }
                i = j;
            }
            b'-' if i + 1 < n && bytes[i + 1] == b'-' => {
                match sql[i..].find('\n') {
                    Some(j) => i += j + 1,
                    None => i = n,
                }
            }
            b'/' if i + 1 < n && bytes[i + 1] == b'*' => {
                match sql[i + 2..].find("*/") {
                    Some(j) => i += 2 + j + 2,
                    None => i = n,
                }
            }
            b'?' if i == 0 || (bytes[i - 1] != b':' && bytes[i - 1] != b'$') => {
                let index = refs.len();
                refs.push(ParamRef { start: i, end: i + 1, index, name: String::new() });
                i += 1;
            }
            b'$' if i + 1 < n && is_digit(bytes[i + 1]) => {
                let mut j = i + 1;
                while j < n && is_digit(bytes[j]) { j += 1; }
                let num: usize = sql[i + 1..j].parse().unwrap_or(1);
                let index = if num > 0 { num - 1 } else { refs.len() };
                refs.push(ParamRef { start: i, end: j, index, name: String::new() });
                i = j;
            }
            b':' | b'@' if i + 1 < n &&
                (is_ident_start(bytes[i + 1]) || bytes[i + 1] == b':' || bytes[i + 1] == b'@') => {
                if bytes[i + 1] == b':' { i += 2; continue; }
                if c == b'@' && bytes[i + 1] == b'@' {
                    i += 2;
                    while i < n && is_ident_char(bytes[i]) { i += 1; }
                    continue;
                }
                let mut j = i + 1;
                while j < n && is_ident_char(bytes[j]) { j += 1; }
                let name = sql[i + 1..j].to_string();
                let index = *by_name.entry(name.clone()).or_insert(refs.len());
                refs.push(ParamRef { start: i, end: j, index, name });
                i = j;
            }
            _ => i += 1,
        }
    }
    refs
}

/// 目标引擎的原生标记（与 Go sqltext.Placeholder 逐字一致）。
pub fn placeholder(db_type: &str, index: usize) -> String {
    match db_type.to_ascii_lowercase().as_str() {
        "postgres" | "postgresql" | "pg" => format!("${}", index),
        "mssql" | "sqlserver" => format!("@p{}", index),
        "oracle" | "dm" => format!(":p{}", index),
        _ => "?".to_string(), // mysql / sqlite
    }
}

/// 按绑定序重写占位符（跨方言）。
pub fn rewrite_placeholders(sql: &str, refs: &[ParamRef], db_type: &str) -> String {
    if refs.is_empty() { return sql.to_string(); }
    let mut out = String::new();
    let mut prev = 0;
    for r in refs {
        out.push_str(&sql[prev..r.start]);
        out.push_str(&placeholder(db_type, r.index + 1));
        prev = r.end;
    }
    out.push_str(&sql[prev..]);
    out
}
