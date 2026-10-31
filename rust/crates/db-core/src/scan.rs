//! 方言感知的 SQL 词法扫描器。
//!
//! 与 Go 侧 `pkg/dbcore/scan.go` 逐字一致（spec/behavior.md §11 双后端一致性红线）。
//!
//! 词法层只做一件事：把输入切成 token 并标出类别；「这是不是写语句」的判断
//! 一律读 token 的类别，绝不在「清洗过的文本」上做子串搜索——后者每次都要
//! 重新发明一遍词边界判断，且总有漏网的引号形式。

use polydb_protocol::common::DatabaseKind;

/// 词法单元的类别。
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum TokenKind {
    /// 裸词：标识符或关键字。
    Word,
    /// 引号标识符："x"（标准 SQL）、`x`（MySQL）、[x]（T-SQL）。
    Quoted,
    /// 字符串字面量：'x'（含 PostgreSQL 的 dollar-quote 与 Oracle 的 q'…'）。
    Literal,
    /// 注释：-- 行、# 行（仅 MySQL/MariaDB）、/* */ 块。
    Comment,
    /// 语句分隔符 ';'。
    Semicolon,
    /// 其余记号（运算符、括号、逗号等）。
    Other,
}

/// 一个词法单元。
#[derive(Clone, Copy, Debug)]
pub struct Token {
    pub kind: TokenKind,
    /// token 在原文中的字节区间 [start, end)。边界总落在 UTF-8 字符边界上。
    pub start: usize,
    pub end: usize,
}

impl Token {
    /// token 的原文切片。
    pub fn text<'a>(&self, sql: &'a str) -> &'a str {
        &sql[self.start..self.end]
    }
}

/// 描述一个方言的词法差异。
///
/// 只读红线的正确性取决于「我们认为在哪结束一个字面量 / 注释」与「数据库
/// 认为在哪结束」一致。两者不一致时，后面的写语句会被当成字面量吞掉——
/// 这不是理论风险，而是已实测过的真实绕过：
///
///   - '#' 在 MySQL/MariaDB 是行注释，在 PostgreSQL 是位异或运算符。
///     一律当注释 → PG 上 `SELECT 1 # 2; DROP TABLE t` 的 DROP 被吞掉。
///     一律不当注释 → MySQL 上 `SELECT 1 # it's ok` 的撇号开启字面量，
///     把后面的写语句一起吞掉。
///   - 反斜杠转义只在 MySQL 恒成立；PostgreSQL 的普通字符串里反斜杠是普通
///     字符（standard_conforming_strings=on），只有 E'…' / U&'…' 才转义。
///   - '--' 在 MySQL 必须跟空白才是注释（`1--2` 是减法），其他方言无条件成立。
///   - PostgreSQL 的 $$…$$ 与 Oracle 的 q'…' 都是「字面量里可以藏引号」的
///     形式；不认识它们，后面的写语句会被前一个引号吞掉。
///   - MySQL 的 /*! … */ 内容会被服务端执行，当普通注释吞掉等于放行里面的写语句。
#[derive(Clone, Copy, Debug, Default)]
pub struct LexProfile {
    /// '#' 是行注释（仅 MySQL/MariaDB）。
    pub hash_comment: bool,
    /// '--' 后必须跟空白/控制符才是注释（仅 MySQL）。
    pub dash_dash_needs_space: bool,
    /// '…' 与 "…" 里的反斜杠是转义符（仅 MySQL）。
    pub backslash_escape: bool,
    /// E'…' / U&'…' 里反斜杠是转义符（仅 PostgreSQL）。
    pub e_string: bool,
    /// $$…$$ / $tag$…$tag$ 是字符串字面量（仅 PostgreSQL）。
    pub dollar_quote: bool,
    /// /*! … */ 与 /*M! … */ 的内容会被服务端执行（MySQL/MariaDB）。
    pub exec_comment: bool,
    /// q'…' 是备选引用（仅 Oracle），内容里可以含引号。
    pub q_quote: bool,
}

/// MySQL / MariaDB 的词法剖面。
pub const MYSQL_PROFILE: LexProfile = LexProfile {
    hash_comment: true,
    dash_dash_needs_space: true,
    backslash_escape: true,
    e_string: false,
    dollar_quote: false,
    exec_comment: true,
    q_quote: false,
};

/// MySQL 的第二变体：反斜杠**不是**转义符（服务端开了 NO_BACKSLASH_ESCAPES
/// 时就是这个语义）。
pub const MYSQL_PROFILE_NO_ESCAPE: LexProfile = LexProfile {
    hash_comment: true,
    dash_dash_needs_space: true,
    backslash_escape: false,
    e_string: false,
    dollar_quote: false,
    exec_comment: true,
    q_quote: false,
};

/// PostgreSQL 的词法剖面（standard_conforming_strings=on：普通字符串里反斜杠
/// 是普通字符，只有 E'…' / U&'…' 才转义）。
pub const POSTGRES_PROFILE: LexProfile = LexProfile {
    hash_comment: false,
    dash_dash_needs_space: false,
    backslash_escape: false,
    e_string: true,
    dollar_quote: true,
    exec_comment: false,
    q_quote: false,
};

/// PostgreSQL 的第二变体：standard_conforming_strings=off 时普通字符串也认
/// 反斜杠转义。
pub const POSTGRES_PROFILE_ESCAPE: LexProfile = LexProfile {
    hash_comment: false,
    dash_dash_needs_space: false,
    backslash_escape: true,
    e_string: true,
    dollar_quote: true,
    exec_comment: false,
    q_quote: false,
};

/// Oracle 的词法剖面。
pub const ORACLE_PROFILE: LexProfile = LexProfile {
    hash_comment: false,
    dash_dash_needs_space: false,
    backslash_escape: false,
    e_string: false,
    dollar_quote: false,
    exec_comment: false,
    q_quote: true,
};

/// SQLite / SQL Server 的词法剖面。
pub const STANDARD_PROFILE: LexProfile = LexProfile {
    hash_comment: false,
    dash_dash_needs_space: false,
    backslash_escape: false,
    e_string: false,
    dollar_quote: false,
    exec_comment: false,
    q_quote: false,
};

/// 全部已知剖面：方言未知时取它们的并集（任一解释命中即判写）。
pub const ALL_PROFILES: [LexProfile; 6] = [
    MYSQL_PROFILE,
    MYSQL_PROFILE_NO_ESCAPE,
    POSTGRES_PROFILE,
    POSTGRES_PROFILE_ESCAPE,
    ORACLE_PROFILE,
    STANDARD_PROFILE,
];

/// 返回该方言的词法剖面；None 表示方言未知。
pub fn profile_for(kind: Option<DatabaseKind>) -> Option<LexProfile> {
    match kind {
        Some(DatabaseKind::MySql) => Some(MYSQL_PROFILE),
        Some(DatabaseKind::Postgres) => Some(POSTGRES_PROFILE),
        Some(DatabaseKind::Oracle) => Some(ORACLE_PROFILE),
        Some(DatabaseKind::Sqlite) | Some(DatabaseKind::Mssql) => Some(STANDARD_PROFILE),
        _ => None,
    }
}

/// 返回判定该方言时应当扫描的词法剖面集合。
///
/// MySQL 与 PostgreSQL 各给两个变体，差别只在「反斜杠是不是转义符」。
/// 这个语义由**服务端**设置决定（MySQL 的 NO_BACKSLASH_ESCAPES、
/// PostgreSQL 的 standard_conforming_strings），而连接配置里看不到这些设置。
/// 两种解释都扫一遍：认错的后果不是「多问一次」，而是把后面的写语句当成
/// 字面量吞掉——直接丢数据。Oracle / SQLite / SQL Server 从不把反斜杠当
/// 转义符，只有一个变体。
///
/// 方言未知时取全部（并集 = fail-closed）。
pub fn profiles_for(kind: Option<DatabaseKind>) -> Vec<LexProfile> {
    match kind {
        Some(DatabaseKind::MySql) => vec![MYSQL_PROFILE, MYSQL_PROFILE_NO_ESCAPE],
        Some(DatabaseKind::Postgres) => vec![POSTGRES_PROFILE, POSTGRES_PROFILE_ESCAPE],
        Some(DatabaseKind::Oracle) => vec![ORACLE_PROFILE],
        Some(DatabaseKind::Sqlite) | Some(DatabaseKind::Mssql) => vec![STANDARD_PROFILE],
        _ => ALL_PROFILES.to_vec(),
    }
}

/// 未知方言时的兜底剖面（只影响「剥前导注释」这类非安全判定）。
pub fn profile_for_kind(kind: Option<DatabaseKind>) -> LexProfile {
    profile_for(kind).unwrap_or(STANDARD_PROFILE)
}

/// 把 SQL 切成 token 序列（一次左到右扫描）。
pub fn scan_tokens(sql: &str, p: LexProfile) -> Vec<Token> {
    scan_range(sql.as_bytes(), 0, sql.len(), p)
}

/// 把 sql[from:to) 切成 token 序列。
/// 嵌套扫描（MySQL 的 /*! … */ 内容）用它把子区间按同一剖面重新切一遍。
fn scan_range(b: &[u8], from: usize, to: usize, p: LexProfile) -> Vec<Token> {
    let mut out: Vec<Token> = Vec::new();
    let mut i = from;
    while i < to {
        let c = b[i];
        if is_space_byte(c) {
            i += 1;
            continue;
        }
        // '--'：MySQL 的 '--' 必须跟空白才是注释；`1--2` 是减法。
        if c == b'-' && i + 1 < to && b[i + 1] == b'-' {
            if p.dash_dash_needs_space && i + 2 < to && !is_space_byte(b[i + 2]) {
                out.push(Token {
                    kind: TokenKind::Other,
                    start: i,
                    end: i + 1,
                });
                i += 1;
                continue;
            }
            let j = line_end(b, i, to);
            out.push(Token {
                kind: TokenKind::Comment,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // '#'：仅 MySQL/MariaDB 是行注释。
        if c == b'#' && p.hash_comment {
            let j = line_end(b, i, to);
            out.push(Token {
                kind: TokenKind::Comment,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // '/* */'：MySQL/MariaDB 的 /*! … */（与 /*M! … */）内容会被服务端
        // 执行：按注释吞掉等于放行里面的写语句，所以改成按代码再扫一遍。
        if c == b'/' && i + 1 < to && b[i + 1] == b'*' {
            let j = block_end(b, i, to);
            if p.exec_comment && j > i + 2 {
                if let Some((s, e)) = exec_comment_body(b, i, j) {
                    out.extend(scan_range(b, s, e, p));
                    i = j;
                    continue;
                }
            }
            out.push(Token {
                kind: TokenKind::Comment,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // '\''：Oracle 的 q'…'：内容里可以含引号，别让前一个引号把后面吞掉。
        if c == b'\'' {
            if p.q_quote && !out.is_empty() {
                let prev = out[out.len() - 1];
                if prev.kind == TokenKind::Word
                    && prev.end == i
                    && is_single_q(&b[prev.start..prev.end])
                {
                    let j = skip_q_quoted(b, i, to);
                    out.push(Token {
                        kind: TokenKind::Literal,
                        start: i,
                        end: j,
                    });
                    i = j;
                    continue;
                }
            }
            let escape = p.backslash_escape || (p.e_string && is_e_string_prefix(b, i));
            let j = skip_quoted(b, i, b'\'', escape, to);
            out.push(Token {
                kind: TokenKind::Literal,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // '"' '`' '['：引号标识符。
        if c == b'"' || c == b'`' || c == b'[' {
            let j = skip_quoted(b, i, c, p.backslash_escape, to);
            out.push(Token {
                kind: TokenKind::Quoted,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // '$'：仅 PostgreSQL 认 dollar-quote。
        if c == b'$' && p.dollar_quote {
            if let Some(j) = skip_dollar_quoted(b, i, to) {
                out.push(Token {
                    kind: TokenKind::Literal,
                    start: i,
                    end: j,
                });
                i = j;
                continue;
            }
            out.push(Token {
                kind: TokenKind::Other,
                start: i,
                end: i + 1,
            });
            i += 1;
            continue;
        }
        // ';'：语句分隔符。
        if c == b';' {
            out.push(Token {
                kind: TokenKind::Semicolon,
                start: i,
                end: i + 1,
            });
            i += 1;
            continue;
        }
        // 裸词：标识符或关键字。
        if is_word_byte(c) {
            let mut j = i;
            while j < to && is_word_byte(b[j]) {
                j += 1;
            }
            out.push(Token {
                kind: TokenKind::Word,
                start: i,
                end: j,
            });
            i = j;
            continue;
        }
        // 其余记号：按整个 UTF-8 字符推进，保证 token 边界落在字符边界上。
        let j = advance_char(b, i);
        out.push(Token {
            kind: TokenKind::Other,
            start: i,
            end: j,
        });
        i = j;
    }
    out
}

/// 判定 sql[i:j] 是否是 MySQL 的可执行注释（/*! … */ 或 /*M! … */），
/// 返回其中「会被服务端执行」的正文区间。
///
/// MySQL 的注释体形如 `/*!50000 SELECT 1 */`：'!' 之后可跟 5 位版本号。
/// 正文从版本号之后开始，到对应的 "*/" 之前结束。
fn exec_comment_body(b: &[u8], i: usize, j: usize) -> Option<(usize, usize)> {
    let mut inner = i + 2;
    if inner >= j {
        return None;
    }
    if b[inner] == b'!' {
        inner += 1;
    } else if b[inner] == b'M' && inner + 1 < j && b[inner + 1] == b'!' {
        inner += 2;
    } else {
        return None;
    }
    // 可选的 5 位版本号（MySQL 会剥掉它）。
    let mut digits = 0usize;
    while inner + digits < j && digits < 5 && b[inner + digits].is_ascii_digit() {
        digits += 1;
    }
    inner += digits;
    let mut end = j;
    if j >= 2 && &b[j - 2..j] == b"*/" {
        end = j - 2;
    }
    if inner > end {
        return None;
    }
    Some((inner, end))
}

/// 返回 sql[i:] 处块注释的结束位置（不含 "*/"）。
/// 未闭合时返回 limit（服务端会报语法错，整条不会执行）。
fn block_end(b: &[u8], i: usize, limit: usize) -> usize {
    let mut k = i + 2;
    while k + 1 < limit {
        if b[k] == b'*' && b[k + 1] == b'/' {
            return k + 2;
        }
        k += 1;
    }
    limit
}

/// 返回从 start 起的行注释结束位置（不含换行符；到区间末则返回 limit）。
fn line_end(b: &[u8], start: usize, limit: usize) -> usize {
    let mut k = start;
    while k < limit {
        if b[k] == b'\n' {
            return k;
        }
        k += 1;
    }
    limit
}

/// 越过从 b[start] 开始的引号区域，返回结束引号之后的下标。
/// 处理 ''（双写）；backslash 为真时还处理反斜杠转义。
/// [] 标识符只按 ] 结束。
fn skip_quoted(b: &[u8], start: usize, quote: u8, backslash: bool, limit: usize) -> usize {
    let mut i = start + 1;
    while i < limit {
        if backslash && b[i] == b'\\' {
            i = advance_char(b, i + 1);
            continue;
        }
        if b[i] == quote {
            if quote == b'[' {
                return i + 1;
            }
            if i + 1 < limit && b[i + 1] == quote {
                i += 2;
            } else {
                return i + 1;
            }
            continue;
        }
        i = advance_char(b, i);
    }
    limit
}

/// 越过 Oracle 的 q'…' 备选引用（b[start] 是开引号）。
///
/// 形式是 q'<定界符>正文<定界符>'：定界符成对时（[ ] ( ) { } < >）按配对计数，
/// 不成对时按「定界符后紧跟引号」结束。正文里可以含引号。
fn skip_q_quoted(b: &[u8], start: usize, limit: usize) -> usize {
    if start + 1 >= limit {
        return limit;
    }
    let d = b[start + 1];
    let close = match d {
        b'[' => b']',
        b'(' => b')',
        b'{' => b'}',
        b'<' => b'>',
        _ => d,
    };
    if close == d {
        // 非配对定界符：找到「定界符 + '」才算结束。
        let mut j = start + 2;
        while j < limit {
            if b[j] == d && j + 1 < limit && b[j + 1] == b'\'' {
                return j + 2;
            }
            j += 1;
        }
        return limit;
    }
    let mut depth = 1i32;
    let mut j = start + 2;
    while j < limit {
        if b[j] == d {
            depth += 1;
        } else if b[j] == close {
            depth -= 1;
            if depth == 0 {
                if j + 1 < limit && b[j + 1] == b'\'' {
                    return j + 2;
                }
                return j + 1;
            }
        }
        j += 1;
    }
    limit
}

/// 报告 b[start] 处的单引号是否属于 E'…' / U&'…' 前缀。
/// PostgreSQL 只有这两种前缀的字面量才认反斜杠转义。
fn is_e_string_prefix(b: &[u8], start: usize) -> bool {
    if start == 0 {
        return false;
    }
    let c = b[start - 1];
    if c == b'e' || c == b'E' {
        return start == 1 || !is_word_byte(b[start - 2]);
    }
    if c == b'&' && start >= 2 {
        let u = b[start - 2];
        return (u == b'u' || u == b'U') && (start == 2 || !is_word_byte(b[start - 3]));
    }
    false
}

/// 越过从 b[start] 开始的 PostgreSQL dollar-quote，返回结束位置。
///
/// 三个约束缺一不可：
///   - 定界符必须是 $$ 或 $tag$（标签首字符是字母或下划线）——$1 是占位符；
///   - '$' 前不能紧邻标识符字符——PG 规则：紧跟关键字/标识符的 $tag$ 会被
///     当成标识符的一部分（`a$b$c` 是一个标识符，不是 dollar-quote）；
///   - 内容可以含任意字符（含撇号），但必须由**同一个**定界符闭合。
///
/// 未闭合时取到区间末（定界符缺失本身就是语法错误，不会多执行别的语句）。
fn skip_dollar_quoted(b: &[u8], start: usize, limit: usize) -> Option<usize> {
    if start > 0 && is_ident_byte(b[start - 1]) {
        return None;
    }
    let tag_len = dollar_tag_len(b, start, limit)?;
    let body_start = start + tag_len;
    let delim = &b[start..start + tag_len];
    let mut i = body_start;
    while i + tag_len <= limit {
        if &b[i..i + tag_len] == delim {
            return Some(i + tag_len);
        }
        i += 1;
    }
    Some(limit)
}

/// 返回 b[i:] 处的 dollar-quote 定界符长度（$$ 为 2，$tag$ 为 len(tag)+2），
/// 不是定界符则返回 None。
///
/// 标签首字符必须是字母或下划线：$1 这类占位符不算 dollar-quote
/// （与 db-dev internal/db 的 dollarQuoteAt 同约定）。
fn dollar_tag_len(b: &[u8], i: usize, limit: usize) -> Option<usize> {
    if i >= limit || b[i] != b'$' {
        return None;
    }
    let mut j = i + 1;
    if j < limit && b[j] == b'$' {
        // 空标签定界符 $$：长度 2。这里必须提前返回——再往下走一次
        // 「必须以 $ 收尾」的检查会把 $$ 误判成非定界符。
        return Some(2);
    }
    if j < limit && (b[j] == b'_' || is_alpha_byte(b[j])) {
        j += 1;
        while j < limit && (b[j] == b'_' || is_alpha_byte(b[j]) || is_digit_byte(b[j])) {
            j += 1;
        }
        if j < limit && b[j] == b'$' {
            return Some(j + 1 - i);
        }
    }
    None
}

/// 报告字节是否是裸词字符（字母、数字、下划线）。
fn is_word_byte(c: u8) -> bool {
    c == b'_' || is_alpha_byte(c) || is_digit_byte(c)
}

/// 报告字节是否是标识符字符（裸词字符或 '$'）。
/// PG 的标识符可以含 '$'，所以 dollar-quote 的前置检查用它。
fn is_ident_byte(c: u8) -> bool {
    is_word_byte(c) || c == b'$'
}

fn is_alpha_byte(c: u8) -> bool {
    c.is_ascii_alphabetic()
}

fn is_digit_byte(c: u8) -> bool {
    c.is_ascii_digit()
}

/// 报告字节是否是空白或控制符（MySQL 的 '--' 注释要求其后是这类字符）。
fn is_space_byte(c: u8) -> bool {
    c.is_ascii_whitespace() || c < 0x20
}

/// 报告裸词是不是单个 'q'（Oracle 的 q'…' 前缀）。
fn is_single_q(w: &[u8]) -> bool {
    w.len() == 1 && (w[0] == b'q' || w[0] == b'Q')
}

/// 返回 b[i] 处字符之后的下标（按 UTF-8 字符推进）。
/// token 边界必须落在字符边界上，否则 Rust 的切片会 panic。
fn advance_char(b: &[u8], i: usize) -> usize {
    if i >= b.len() {
        return i;
    }
    let mut j = i + 1;
    while j < b.len() && (b[j] & 0xC0) == 0x80 {
        j += 1;
    }
    j
}
