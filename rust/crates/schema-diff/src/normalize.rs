//! 类型与默认值归一：把「同一语义、不同写法」的元数据折叠成可比较的字面量。
//!
//! 两端（Rust / Go）必须逐字等价，因此这里**只用 ASCII 规则**：
//! 小写折叠只处理 ASCII 字母，空白只认 ASCII 空白集。非 ASCII 标识符
//! （如带重音的引号标识符）不做大小写折叠，属 v1 已知限制。

use polydb_core::DatabaseKind;

/// ASCII 空白集（两端一致，不含 Unicode 空白）。
#[inline]
fn is_ws(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\r' | 0x0b | 0x0c)
}

/// ASCII 小写折叠（非 ASCII 字节原样保留）。
pub fn lower_ascii(s: &str) -> String {
    s.chars().map(|c| c.to_ascii_lowercase()).collect()
}

/// 把连续空白压成单个空格并去掉首尾空白。
pub fn fold_spaces(s: &str) -> String {
    let src = s.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(src.len());
    let mut pending = false;
    for &b in src {
        if is_ws(b) {
            if !out.is_empty() {
                pending = true;
            }
            continue;
        }
        if pending {
            out.push(b' ');
            pending = false;
        }
        out.push(b);
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// 去掉标点旁的空白：`numeric (10, 2)` → `numeric(10,2)`，`text [ ]` → `text[]`。
fn squeeze_punct(s: &str) -> String {
    let src = s.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(src.len());
    let mut i = 0;
    while i < src.len() {
        if !is_ws(src[i]) {
            out.push(src[i]);
            i += 1;
            continue;
        }
        if out.is_empty() {
            i += 1;
            continue;
        }
        let prev = *out.last().unwrap_or(&b' ');
        let mut j = i;
        while j < src.len() && is_ws(src[j]) {
            j += 1;
        }
        let next = if j < src.len() { src[j] } else { b' ' };
        let drop = prev == b'('
            || prev == b'['
            || prev == b','
            || next == b'('
            || next == b')'
            || next == b'['
            || next == b']'
            || next == b',';
        if !drop {
            out.push(b' ');
        }
        i = j;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn is_word_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// 取 `res` 末尾单词的起始下标（单词 = [a-z0-9_]）。
fn tail_word_start(res: &[u8]) -> usize {
    let mut k = res.len();
    while k > 0 && is_word_byte(res[k - 1]) {
        k -= 1;
    }
    k
}

/// 找到与 `b[i] == '('` 匹配的 `)` 下标；找不到返回 `None`。
/// 引号内的括号不计（单引号成对切换，`''` 转义按两次切换处理）。
fn matching_close(b: &[u8], open: usize) -> Option<usize> {
    let mut depth = 0i32;
    let mut in_quote = false;
    let mut i = open;
    while i < b.len() {
        let c = b[i];
        if c == b'\'' {
            in_quote = !in_quote;
        } else if !in_quote {
            if c == b'(' {
                depth += 1;
            } else if c == b')' {
                depth -= 1;
                if depth == 0 {
                    return Some(i);
                }
            }
        }
        i += 1;
    }
    None
}

const MYSQL_INT_TYPES: &[&str] = &[
    "tinyint",
    "smallint",
    "mediumint",
    "int",
    "integer",
    "bigint",
];

/// MySQL 整数显示宽度：`int(11)` → `int`（保留类型名，只删括号宽度）。
fn strip_mysql_int_width(s: &str) -> String {
    let b = s.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'(' {
            if let Some(close) = matching_close(b, i) {
                let inner = &b[i + 1..close];
                let word = &out[tail_word_start(&out)..];
                let is_int = MYSQL_INT_TYPES.iter().any(|t| t.as_bytes() == word);
                let width_only = !inner.is_empty() && inner.iter().all(u8::is_ascii_digit);
                let boundary =
                    tail_word_start(&out) == 0 || !is_word_byte(out[tail_word_start(&out) - 1]);
                if is_int && width_only && boundary {
                    i = close + 1;
                    continue;
                }
            }
        }
        out.push(b[i]);
        i += 1;
    }
    fold_spaces(&String::from_utf8_lossy(&out))
}

/// 按空格分词处理（词 = 类型名 + 可选 `(...)` / `[]` 尾巴）。
fn split_token(tok: &str) -> (&str, &str) {
    let b = tok.as_bytes();
    let mut k = 0;
    while k < b.len() && is_word_byte(b[k]) {
        k += 1;
    }
    (&tok[..k], &tok[k..])
}

fn drop_word(s: &str, word: &str) -> String {
    s.split(' ')
        .filter(|t| split_token(t).0 != word)
        .collect::<Vec<_>>()
        .join(" ")
}

fn replace_word(s: &str, from: &str, to: &str) -> String {
    s.split(' ')
        .map(|t| {
            let (base, rest) = split_token(t);
            if base == from {
                format!("{to}{rest}")
            } else {
                t.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

/// Oracle 精确数字：`number(10,0)` → `number(10)`（scale 为全 0 时等价）。
fn strip_oracle_scale0(s: &str) -> String {
    s.split(' ')
        .map(|t| {
            let (base, rest) = split_token(t);
            if (base != "number" && base != "numeric") || rest.len() < 4 {
                return t.to_string();
            }
            if !rest.starts_with('(') || !rest.ends_with(')') {
                return t.to_string();
            }
            let inner = &rest[1..rest.len() - 1];
            let parts: Vec<&str> = inner.split(',').collect();
            if parts.len() == 2
                && !parts[0].is_empty()
                && parts[0].bytes().all(|c| c.is_ascii_digit())
                && !parts[1].is_empty()
                && parts[1].bytes().all(|c| c == b'0')
            {
                format!("{base}({})", parts[0])
            } else {
                t.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

/// 类型字面量归一：小写 + 折空白 + 去标点旁空白，再按种类去噪声。
///
/// 规则（只折叠「同一数据库内部的不同写法」，不做跨种类等价）：
/// - 通用：`NUMERIC (10, 2)` → `numeric(10,2)`；`text [ ]` → `text[]`
/// - mysql：整数显示宽度 `int(11)` → `int`；丢弃 `zerofill`（MySQL 8 已弃用，`unsigned` 保留）
/// - oracle：`bpchar` → `char`（Oracle 内部名）；`number(p,0)` → `number(p)`
pub fn normalize_type(kind: DatabaseKind, raw: &str) -> String {
    let mut s = squeeze_punct(&fold_spaces(&lower_ascii(raw.trim())));
    if kind == DatabaseKind::MySql {
        s = fold_spaces(&drop_word(&strip_mysql_int_width(&s), "zerofill"));
    }
    if kind == DatabaseKind::Oracle {
        s = fold_spaces(&strip_oracle_scale0(&replace_word(&s, "bpchar", "char")));
    }
    s
}

/// 引号外小写折叠：字符串字面量内容保持原样（`'ABC'` 与 `'abc'` 是不同默认值）。
fn lower_outside_quotes(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut quote: Option<char> = None;
    for c in s.chars() {
        match quote {
            Some(q) => {
                if c == q {
                    quote = None;
                }
                out.push(c);
            }
            None => match c {
                '\'' | '"' => {
                    quote = Some(c);
                    out.push(c);
                }
                _ => out.push(c.to_ascii_lowercase()),
            },
        }
    }
    out
}

/// 剥掉成对的最外层括号：`(( now() ))` → `now()`；`(a) + (b)` 不动。
fn strip_outer_parens(s: &str) -> String {
    let b = s.as_bytes();
    if b.first() != Some(&b'(') {
        return s.to_string();
    }
    match matching_close(b, 0) {
        Some(idx) if idx + 1 == b.len() => String::from_utf8_lossy(&b[1..idx]).into_owned(),
        _ => s.to_string(),
    }
}

/// 剥掉**表达式末尾、括号深度 0 处**的类型投射：`'0'::character varying` → `'0'`。
/// 括号内的投射（如 PG 的 `nextval('s'::regclass)`）保持原样；多层投射由外层循环逐个剥。
fn strip_trailing_cast(s: &str) -> String {
    let b = s.as_bytes();
    let mut depth = 0i32;
    let mut quote: Option<u8> = None;
    let mut last: Option<usize> = None;
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        match quote {
            Some(q) => {
                if c == q {
                    quote = None;
                }
            }
            None => match c {
                b'\'' | b'"' => quote = Some(c),
                b'(' => depth += 1,
                b')' => depth -= 1,
                b':' if depth == 0 && i + 1 < b.len() && b[i + 1] == b':' => last = Some(i),
                _ => {}
            },
        }
        i += 1;
    }
    match last {
        Some(pos) => {
            let tail = fold_spaces(&String::from_utf8_lossy(&b[pos + 2..]));
            if is_cast_tail(&tail) {
                fold_spaces(&String::from_utf8_lossy(&b[..pos]))
            } else {
                s.to_string()
            }
        }
        None => s.to_string(),
    }
}

/// 投射目标类型字面量：`text`、`character varying`、`numeric(10,2)`、`varchar[]`。
fn is_cast_tail(t: &str) -> bool {
    if t.is_empty() {
        return false;
    }
    t.split(' ').all(|tok| {
        let (base, rest) = split_token(tok);
        let head_ok = base.starts_with(|c: char| c.is_ascii_alphabetic() || c == '_');
        head_ok
            && (rest.is_empty()
                || (rest.starts_with('(') && rest.ends_with(')'))
                || rest == "[]"
                || (rest.starts_with('(') && rest.ends_with(")[]")))
    })
}

/// 默认值表达式归一；`None` 表示「无默认值」。
///
/// 规则：`NULL` 字面量视为无默认；反复剥最外层括号与尾部 `::type` 投射；
/// 引号外小写、折空白；引号内容保持原样。
/// 不做等价改写（`now()` 与 `current_timestamp` 在 v1 视为不同默认值）。
pub fn normalize_default(raw: Option<&str>) -> Option<String> {
    let mut s = fold_spaces(&lower_outside_quotes(raw?.trim()));
    if s.is_empty() {
        return None;
    }
    loop {
        let prev = s.clone();
        s = fold_spaces(&strip_outer_parens(&s));
        s = fold_spaces(&strip_trailing_cast(&s));
        if s == prev {
            break;
        }
    }
    if s.is_empty() || s == "null" {
        None
    } else {
        Some(s)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn t(kind: DatabaseKind, raw: &str) -> String {
        normalize_type(kind, raw)
    }

    #[test]
    fn normalize_type_whitespace_and_case() {
        assert_eq!(
            t(DatabaseKind::Postgres, "NUMERIC (10, 2)"),
            "numeric(10,2)"
        );
        assert_eq!(
            t(DatabaseKind::Postgres, "character\tvarying\n(20)"),
            "character varying(20)"
        );
        assert_eq!(t(DatabaseKind::Postgres, "TEXT [ ]"), "text[]");
        assert_eq!(
            t(DatabaseKind::Postgres, "  int   unsigned  "),
            "int unsigned"
        );
    }

    #[test]
    fn normalize_type_mysql_display_width_and_zerofill() {
        assert_eq!(t(DatabaseKind::MySql, "int(11)"), "int");
        assert_eq!(
            t(DatabaseKind::MySql, "INT(10) UNSIGNED ZEROFILL"),
            "int unsigned"
        );
        assert_eq!(
            t(DatabaseKind::MySql, "bigint(20) unsigned"),
            "bigint unsigned"
        );
        // 非整数宽度、非纯数字宽度必须保留
        assert_eq!(t(DatabaseKind::MySql, "decimal(10,2)"), "decimal(10,2)");
        assert_eq!(
            t(DatabaseKind::MySql, "int(11) unsigned zerofill"),
            "int unsigned"
        );
        assert_eq!(t(DatabaseKind::MySql, "varchar(11)"), "varchar(11)");
        // 其它种类不得去宽度
        assert_eq!(t(DatabaseKind::Sqlite, "int(11)"), "int(11)");
    }

    #[test]
    fn normalize_type_oracle_bpchar_and_scale() {
        assert_eq!(t(DatabaseKind::Oracle, "BPCHAR(3)"), "char(3)");
        assert_eq!(t(DatabaseKind::Oracle, "bpchar"), "char");
        assert_eq!(t(DatabaseKind::Oracle, "NUMBER(10, 0)"), "number(10)");
        assert_eq!(t(DatabaseKind::Oracle, "NUMBER(10,2)"), "number(10,2)");
        assert_eq!(t(DatabaseKind::Oracle, "VARCHAR2(50)"), "varchar2(50)");
    }

    #[test]
    fn normalize_default_collapses_noise() {
        assert_eq!(normalize_default(None), None);
        assert_eq!(normalize_default(Some("   ")), None);
        assert_eq!(normalize_default(Some("NULL")), None);
        assert_eq!(normalize_default(Some("(NULL)")), None);
        assert_eq!(normalize_default(Some("(('0'))")), Some("'0'".into()));
        assert_eq!(
            normalize_default(Some("'0'::character varying")),
            Some("'0'".into())
        );
        assert_eq!(
            normalize_default(Some("'0'::numeric::text")),
            Some("'0'".into())
        );
        assert_eq!(
            normalize_default(Some("((CURRENT_TIMESTAMP))")),
            Some("current_timestamp".into())
        );
        // 括号内投射不动
        assert_eq!(
            normalize_default(Some("nextval('orders_id_seq'::regclass)")),
            Some("nextval('orders_id_seq'::regclass)".into())
        );
    }

    #[test]
    fn normalize_default_preserves_quoted_case() {
        assert_eq!(normalize_default(Some("'ABC'")), Some("'ABC'".into()));
        assert_eq!(normalize_default(Some("'abc'")), Some("'abc'".into()));
        assert_eq!(
            normalize_default(Some("DEFAULT_UPPER('ABC') || 'x'")),
            Some("default_upper('ABC') || 'x'".into())
        );
    }
}
