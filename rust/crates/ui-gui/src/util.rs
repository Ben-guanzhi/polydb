//! 纯函数工具：值渲染、单元格预览辅助、光标字节定位、多语句拆分、Redis 写回门禁。

use gpui::Keystroke;
use polydb_core::{CoreError, RedisValue, Value};
use polydb_protocol::{codes, BLOB_REF_PREFIX};

// 单行文本编辑器：把一次按键作用到字符串 + 光标（字节下标）上，返回是否有改动。
// Ctrl/平台键交回上层（剪贴板等）处理，此处直接返回 false。
pub(crate) fn edit_line(s: &mut String, cursor: &mut usize, ks: &Keystroke) -> bool {
    if ks.modifiers.control || ks.modifiers.platform {
        return false;
    }
    match ks.key.as_str() {
        "backspace" => {
            if *cursor > 0 {
                let start = char_floor(s, *cursor - 1);
                s.replace_range(start..*cursor, "");
                *cursor = start;
            }
        }
        "delete" => {
            if *cursor < s.len() {
                let end = char_ceil(s, *cursor + 1);
                s.replace_range(*cursor..end, "");
            }
        }
        "left" => {
            if *cursor > 0 {
                *cursor = char_floor(s, *cursor - 1);
            }
        }
        "right" => {
            if *cursor < s.len() {
                *cursor = char_ceil(s, *cursor + 1);
            }
        }
        "home" => *cursor = 0,
        "end" => *cursor = s.len(),
        _ => {
            if let Some(ch) = ks.key_char.as_deref() {
                if ch.chars().count() == 1 {
                    let at = char_ceil(s, (*cursor).min(s.len()));
                    s.insert_str(at, ch);
                    *cursor = at + ch.len();
                }
            }
        }
    }
    true
}

// 与 Web 端 tryParseJson 对齐：仅对象/数组字面量视为可美化 JSON。

// 结果表格最多渲染的行数（超出显示提示，与 TUI 行为一致）。
pub(crate) const MAX_RESULT_ROWS: usize = 200;

// 网格单元格显示截断长度（完整内容看预览面板）。
pub(crate) const CELL_CLIP: usize = 60;

pub(crate) fn value_str(v: &Value) -> String {
    match v {
        Value::Null => "NULL".to_string(),
        Value::Bool(b) => b.to_string(),
        Value::Integer(i) => i.to_string(),
        Value::Float(f) => f.to_string(),
        Value::String(s) => s.clone(),
        Value::Array(_) | Value::Object(_) => {
            serde_json::to_string(v).unwrap_or_else(|_| "<complex>".to_string())
        }
    }
}

/// spec/encoding.md §5.1：`$blob` 值的字节长度（内联与旁路引用两种形状都带 len）。
pub(crate) fn blob_len(v: &Value) -> Option<u64> {
    let Value::Object(map) = v else { return None };
    let Value::Object(blob) = map.get("$blob")? else {
        return None;
    };
    match blob.get("len")? {
        Value::Integer(n) if *n >= 0 => Some(*n as u64),
        _ => None,
    }
}

/// §5.2 旁路引用：返回 (len, uuid)。非 $blob / 内联 base64 形状 → None。
pub(crate) fn blob_ref_of(v: &Value) -> Option<(u64, String)> {
    let Value::Object(map) = v else { return None };
    let Value::Object(blob) = map.get("$blob")? else {
        return None;
    };
    let len = blob_len(v)?;
    let Value::String(r) = blob.get("ref")? else {
        return None;
    };
    let uuid = r.strip_prefix(BLOB_REF_PREFIX)?;
    Some((len, uuid.to_string()))
}

/// 网格单元格显示文本：$blob → `<blob N bytes>`（与 Web/TUI 网格对齐），
/// 其余同 value_str。复制/预览仍走 value_str 保留原始 JSON。
pub(crate) fn cell_str(v: &Value) -> String {
    match blob_len(v) {
        Some(n) => format!("<blob {n} bytes>"),
        None => value_str(v),
    }
}

/// 取回失败的展示文案：未知/过期用 spec 规定的前端固定文案，其余透出错误细节。
pub(crate) fn blob_fetch_err_text(e: &CoreError) -> String {
    let p = e.to_protocol();
    if p.code == codes::BLOB_NOT_FOUND {
        "已过期，请重新执行查询".to_string()
    } else {
        p.to_string()
    }
}

/// 取回内容预览：前 n 字节的 hex（每行 16 字节），二进制安全。
pub(crate) fn hex_bytes(b: &[u8], n: usize) -> String {
    b.iter()
        .take(n)
        .map(|x| format!("{x:02x}"))
        .collect::<Vec<_>>()
        .chunks(16)
        .map(|c| c.join(" "))
        .collect::<Vec<_>>()
        .join("\n")
}

// 与 Web 端 tryParseJson 对齐：仅对象/数组字面量视为可美化 JSON。
pub(crate) fn try_pretty_json(v: &Value) -> Option<String> {
    match v {
        Value::Array(_) | Value::Object(_) => serde_json::to_string_pretty(v).ok(),
        Value::String(s) => {
            let t = s.trim_start();
            if !(t.starts_with('{') || t.starts_with('[')) {
                return None;
            }
            serde_json::from_str::<serde_json::Value>(t)
                .ok()
                .and_then(|j| serde_json::to_string_pretty(&j).ok())
        }
        _ => None,
    }
}

// 16 字节/行的经典 hexdump（偏移 + hex + ASCII gutter）。
pub(crate) fn hex_dump(s: &str) -> String {
    let bytes = s.as_bytes();
    if bytes.is_empty() {
        return "(empty)".to_string();
    }
    let mut lines: Vec<String> = Vec::new();
    for (n, chunk) in bytes.chunks(16).enumerate() {
        let hex = chunk
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<Vec<_>>()
            .join(" ");
        let ascii: String = chunk
            .iter()
            .map(|&b| {
                if (0x20..0x7f).contains(&b) {
                    b as char
                } else {
                    '·'
                }
            })
            .collect();
        lines.push(format!("{:08x}  {:<47}  |{ascii}|", n * 16, hex));
    }
    lines.join("\n")
}

pub(crate) fn clip(s: &str, max: usize) -> String {
    let count = s.chars().count();
    if count <= max {
        return s.to_string();
    }
    let cut: String = s.chars().take(max).collect();
    format!("{cut}…({} 字符)", count)
}

// 把字节下标回退到最近的 char 边界（用于光标左移 / backspace）。
// 注意不能 s[..i] 切片——i 本身可能就在多字节字符中间。
pub(crate) fn char_floor(s: &str, i: usize) -> usize {
    let mut j = i.min(s.len());
    while j > 0 && !s.is_char_boundary(j) {
        j -= 1;
    }
    j
}

// 把字节下标前进到最近的 char 边界（用于光标右移 / 删除）。
pub(crate) fn char_ceil(s: &str, i: usize) -> usize {
    let mut j = i.min(s.len());
    while j < s.len() && !s.is_char_boundary(j) {
        j += 1;
    }
    j
}

// 光标所在行的字节边界 [start, end)，end 不含换行符。
pub(crate) fn line_bounds(s: &str, i: usize) -> (usize, usize) {
    let start = s[..i].rfind('\n').map(|p| p + 1).unwrap_or(0);
    let end = s[i..].find('\n').map(|p| i + p).unwrap_or(s.len());
    (start, end)
}

// 行内按“字符列”定位字节下标（越界钳到行尾，保证 char 边界）。
pub(crate) fn col_to_byte(line: &str, col_chars: usize) -> usize {
    line.char_indices()
        .nth(col_chars)
        .map(|(i, _)| i)
        .unwrap_or(line.len())
}

// ── Smart AutoIndent ──────────────────────────────────────────
// PL/SQL Developer 的 Smart AutoIndent 词表（Tools > Preferences > Editor，
// 随工具发行 SmartIndent.txt）：光标所在行以这些词开头时，换行给下一行缩进一级。
pub(crate) const SMART_INDENT_WORDS: &[&str] = &[
    "BEGIN",
    "IF",
    "THEN",
    "ELSE",
    "ELSIF",
    "PROCEDURE",
    "FUNCTION",
    "CASE",
    "WHEN",
    "LOOP",
    "WHILE",
    "FOR",
    "EXCEPTION",
];

// 裸换行，与「换行 + 一级缩进」：一级缩进是两个空格，与 Go 侧 SQL 模板同口径。
const PLAIN_NEWLINE: &str = "\n";
const INDENTED_NEWLINE: &str = "\n  ";

// 行首第一个词（跳掉前导空白），返回切片引用不分配。以注释或字符串字面量
// 开头的行不是语句，返回 None。
pub(crate) fn first_code_word(line: &str) -> Option<&str> {
    let rest = line.trim_start();
    let end = rest.find(|c: char| c.is_whitespace()).unwrap_or(rest.len());
    let word = &rest[..end];
    if word.is_empty()
        || word.starts_with("--")
        || word.starts_with("/*")
        || word.starts_with('#')
        || word.starts_with('\'')
        || word.starts_with('"')
        || word.starts_with('`')
    {
        return None;
    }
    Some(word)
}

// 行首词是否命中词表（大小写不敏感、整词匹配：FORM 不算 FOR）。
pub(crate) fn smart_indent_line(line: &str) -> bool {
    first_code_word(line)
        .is_some_and(|w| SMART_INDENT_WORDS.iter().any(|k| k.eq_ignore_ascii_case(w)))
}

// 光标处换行应插入的文本：命中词表就带上缩进，否则是裸换行。
pub(crate) fn newline_insert(line: &str) -> &'static str {
    if smart_indent_line(line) {
        INDENTED_NEWLINE
    } else {
        PLAIN_NEWLINE
    }
}

// SQL 编辑器词移动：字母/数字/下划线视为词字符，其余（含空白、标点、换行）为分隔。
pub(crate) fn is_word_char(c: char) -> bool {
    c.is_alphanumeric() || c == '_'
}

// Ctrl+Left：从光标（字节边界）向左跳到一个词的起点。
// 先吞掉紧邻左侧的分隔符，再吞掉词字符；结果保证是 char 边界。
pub(crate) fn word_floor(s: &str, pos: usize) -> usize {
    let chars: Vec<(usize, char)> = s.char_indices().collect();
    // boundary = 位于 chars[i] 之前的下标 i（i==chars.len() 表示串尾）。
    let mut i = chars.len();
    while i > 0 && chars[i - 1].0 + chars[i - 1].1.len_utf8() > pos {
        i -= 1;
    }
    while i > 0 && !is_word_char(chars[i - 1].1) {
        i -= 1;
    }
    while i > 0 && is_word_char(chars[i - 1].1) {
        i -= 1;
    }
    if i == 0 {
        0
    } else {
        chars[i].0
    }
}

// Ctrl+Right：从光标向右跳到一个词的终点。
// 先吞掉紧邻右侧的词字符（若无则先吞分隔符再吞词字符），结果保证是 char 边界。
pub(crate) fn word_ceil(s: &str, pos: usize) -> usize {
    let chars: Vec<(usize, char)> = s.char_indices().collect();
    // i = 第一个起点 >= pos 的字符下标。
    let mut i = 0;
    while i < chars.len() && chars[i].0 < pos {
        i += 1;
    }
    let mut j = i;
    while j < chars.len() && is_word_char(chars[j].1) {
        j += 1;
    }
    if j == i {
        while j < chars.len() && !is_word_char(chars[j].1) {
            j += 1;
        }
        while j < chars.len() && is_word_char(chars[j].1) {
            j += 1;
        }
    }
    if j >= chars.len() {
        s.len()
    } else {
        chars[j].0
    }
}

// 双击选词：返回光标 pos 相邻词的连续区间 [start,end)；两侧都非词字符则 None。
pub(crate) fn word_at(s: &str, pos: usize) -> Option<(usize, usize)> {
    let left = char_floor(s, pos);
    let right = char_ceil(s, pos);
    // 锚点：优先取 pos 左侧紧邻字符，若为词字符则锚到它，否则锚到右侧。
    let anchor = if left > 0 {
        let p = char_floor(s, left - 1);
        if s[p..].chars().next().is_some_and(is_word_char) {
            p
        } else {
            right.min(s.len())
        }
    } else {
        right
    };
    let first = s[anchor..].chars().next();
    if !first.is_some_and(is_word_char) {
        return None;
    }
    let mut start = anchor;
    loop {
        let p = char_floor(s, start.saturating_sub(1));
        if p < start && s[p..].chars().next().is_some_and(is_word_char) {
            start = p;
        } else {
            break;
        }
    }
    let mut end = anchor + first.unwrap().len_utf8();
    loop {
        if end < s.len() && s[end..].chars().next().is_some_and(is_word_char) {
            end = char_ceil(s, end + 1);
        } else {
            break;
        }
    }
    Some((start, end))
}

// 按分号拆分多语句，字符串/注释感知（语义对齐 Web M27 splitSql）：
// 引号内分号不切；-- 与 # 行注释、块注释中的分号不切；纯注释不构成语句。
pub(crate) fn split_statements(sql: &str) -> Vec<String> {
    let cs: Vec<char> = sql.chars().collect();
    let mut out = Vec::new();
    let mut cur = String::new();
    let mut code = false; // 当前缓冲里是否已有真实代码（非空白/注释）
    let mut i = 0;
    while i < cs.len() {
        let c = cs[i];
        match c {
            '\'' | '"' | '`' => {
                let q = c;
                code = true;
                cur.push(q);
                i += 1;
                while i < cs.len() {
                    let d = cs[i];
                    if d == '\\' && i + 1 < cs.len() {
                        cur.push(d);
                        cur.push(cs[i + 1]);
                        i += 2;
                        continue;
                    }
                    cur.push(d);
                    i += 1;
                    if d == q {
                        // 双写转义（'' / "" / ``）继续留在字符串内。
                        if i < cs.len() && cs[i] == q {
                            cur.push(q);
                            i += 1;
                            continue;
                        }
                        break;
                    }
                }
            }
            '-' if i + 1 < cs.len() && cs[i + 1] == '-' => {
                while i < cs.len() && cs[i] != '\n' {
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            '#' => {
                while i < cs.len() && cs[i] != '\n' {
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            '/' if i + 1 < cs.len() && cs[i + 1] == '*' => {
                cur.push('/');
                cur.push('*');
                i += 2;
                while i < cs.len() {
                    if cs[i] == '*' && i + 1 < cs.len() && cs[i + 1] == '/' {
                        cur.push('*');
                        cur.push('/');
                        i += 2;
                        break;
                    }
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            ';' => {
                if code {
                    let t = cur.trim();
                    if !t.is_empty() {
                        out.push(t.to_string());
                    }
                }
                cur.clear();
                code = false;
                i += 1;
            }
            _ => {
                if !c.is_whitespace() {
                    code = true;
                }
                cur.push(c);
                i += 1;
            }
        }
    }
    if code && !cur.trim().is_empty() {
        out.push(cur.trim().to_string());
    }
    out
}

/// Redis 整值写回门禁（D4，与 TUI `kvStartEdit` / Web `redisWriteBlock` 同语义）：
/// 只有**已加载的 string 值**且连接非只读才能整值替换；list/hash/set 需按元素操作，走命令行。
/// 返回提示语，`None` 表示可以写。
pub(crate) fn redis_write_block(
    value: Option<&RedisValue>,
    read_only: bool,
) -> Option<&'static str> {
    let Some(v) = value else {
        return Some("先从左侧点击键加载值");
    };
    if read_only {
        return Some("连接标记为只读，不能写入");
    }
    if !matches!(v, RedisValue::String(_)) {
        return Some("仅 string 支持整值写回，其余类型请用命令行（RPUSH/HSET …）");
    }
    None
}

// 多语句日志用的单行摘要。
pub(crate) fn stmt_preview(s: &str) -> String {
    let one: String = s.chars().map(|c| if c == '\n' { ' ' } else { c }).collect();
    clip(one.trim(), 60)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn split(s: &str) -> Vec<String> {
        split_statements(s)
    }

    #[test]
    fn redis_write_block_gates() {
        use polydb_core::RedisValue;
        let string = RedisValue::String("v".into());
        let list = RedisValue::List(vec!["a".into()]);
        assert_eq!(redis_write_block(None, false), Some("先从左侧点击键加载值"));
        // 只读门禁优先于类型门禁（与 TUI 提示顺序一致）。
        assert_eq!(
            redis_write_block(Some(&list), true),
            Some("连接标记为只读，不能写入")
        );
        assert_eq!(
            redis_write_block(Some(&list), false),
            Some("仅 string 支持整值写回，其余类型请用命令行（RPUSH/HSET …）")
        );
        assert_eq!(
            redis_write_block(Some(&string), true),
            Some("连接标记为只读，不能写入")
        );
        assert_eq!(redis_write_block(Some(&string), false), None);
    }

    #[test]
    fn split_basic_and_trailing() {
        assert_eq!(split("SELECT 1; SELECT 2;"), ["SELECT 1", "SELECT 2"]);
        assert_eq!(split("SELECT 1"), ["SELECT 1"]);
        assert_eq!(split("  ;; ;"), Vec::<String>::new());
    }

    #[test]
    fn split_respects_strings() {
        assert_eq!(
            split("SELECT 'a;b', \"c;d\", `e;f`; SELECT 2"),
            ["SELECT 'a;b', \"c;d\", `e;f`", "SELECT 2"]
        );
        // 双写转义 + 反斜杠转义：引号内的 '' 与 \' 不闭合字符串。
        assert_eq!(
            split("SELECT 'it''s;ok'; SELECT 2"),
            ["SELECT 'it''s;ok'", "SELECT 2"]
        );
        assert_eq!(
            split("SELECT 'a\\';b'; SELECT 2"),
            ["SELECT 'a\\';b'", "SELECT 2"]
        );
    }

    #[test]
    fn split_respects_comments() {
        assert_eq!(
            split("-- up; front\nSELECT 1; /* mid; */ SELECT 2;"),
            ["-- up; front\nSELECT 1", "/* mid; */ SELECT 2"]
        );
        // 纯注释不构成语句；# 行注释（MySQL 风格）。
        assert_eq!(split("-- only a comment;"), Vec::<String>::new());
        assert_eq!(split("SELECT 1; # tail; cmt"), ["SELECT 1"]);
    }

    #[test]
    fn clip_counts_chars_not_bytes() {
        assert_eq!(clip("abc", 10), "abc");
        assert_eq!(clip("一二三四五", 2), "一二…(5 字符)");
    }

    #[test]
    fn char_boundaries_and_lines() {
        let s = "中文ab"; // 中/文 各 3 字节，a/b 各 1
        assert_eq!(char_floor(s, 4), 3);
        assert_eq!(char_ceil(s, 4), 6);
        // char_floor 落在边界上时**原地不动** ⇒ 退格必须先把下标退一格，否则删不掉字符。
        assert_eq!(char_floor("hello", 5), 5);
        assert_eq!(char_floor(s, 5), 3); // 光标在 6（文之后）⇒ 退到 3，删掉整个"文"
        assert_eq!(char_floor(s, 7), 7); // 光标在 8（末尾）⇒ 前一格已是边界，删掉 'b'
        let t = "ab\r\ncd";
        let (ls, le) = line_bounds(t, 2);
        assert_eq!((ls, le), (0, 3)); // \r 留在行内（end 只排除 \n）
        assert_eq!(col_to_byte("中文x", 2), 6);
        assert_eq!(col_to_byte("中文x", 9), 7);
    }

    fn blob_value(pairs: Vec<(&str, Value)>) -> Value {
        let mut inner = std::collections::HashMap::new();
        for (k, v) in pairs {
            inner.insert(k.to_string(), v);
        }
        let mut outer = std::collections::HashMap::new();
        outer.insert("$blob".to_string(), Value::Object(inner));
        Value::Object(outer)
    }

    #[test]
    fn blob_cell_str_and_ref() {
        let big = blob_value(vec![
            ("len", Value::Integer(1_048_577)),
            (
                "ref",
                Value::String(format!(
                    "{BLOB_REF_PREFIX}11111111-1111-1111-1111-111111111111"
                )),
            ),
        ]);
        assert_eq!(blob_len(&big), Some(1_048_577));
        assert_eq!(cell_str(&big), "<blob 1048577 bytes>");
        let (len, uuid) = blob_ref_of(&big).expect("ref shape");
        assert_eq!(len, 1_048_577);
        assert_eq!(uuid, "11111111-1111-1111-1111-111111111111");

        // 内联 base64 形状：有标签但不是引用（复制/预览仍走 value_str 原始 JSON）
        let inline = blob_value(vec![
            ("len", Value::Integer(2)),
            ("base64", Value::String("AQI=".into())),
        ]);
        assert_eq!(cell_str(&inline), "<blob 2 bytes>");
        assert!(blob_ref_of(&inline).is_none());

        // 非 $blob：标签回落 value_str，ref 判空
        assert_eq!(cell_str(&Value::String("x".into())), "x");
        assert!(blob_ref_of(&Value::Null).is_none());
        let no_len = blob_value(vec![("ref", Value::String("x".into()))]);
        assert!(blob_ref_of(&no_len).is_none());
    }

    #[test]
    fn blob_fetch_err_text_maps_404() {
        assert_eq!(
            blob_fetch_err_text(&CoreError::BlobNotFound("22222222".into())),
            "已过期，请重新执行查询"
        );
        assert_eq!(
            blob_fetch_err_text(&CoreError::Internal("boom".into())),
            "[POLYDB_ERR_UNKNOWN] boom"
        );
    }

    #[test]
    fn hex_bytes_lines() {
        assert_eq!(hex_bytes(&[0, 1, 0xff], 16), "00 01 ff");
        assert_eq!(hex_bytes(&[0xab; 17], 16).lines().count(), 1);
        assert_eq!(hex_bytes(&[0xab; 17], 17).lines().count(), 2);
    }

    #[test]
    fn word_movement_boundaries() {
        // "SELECT a;b"：S@0 T@5 空格@6 a@7 ;@8 b@9，len=10
        let s = "SELECT a;b";
        assert_eq!(word_floor(s, 10), 9);
        assert_eq!(word_floor(s, 9), 7);
        assert_eq!(word_floor(s, 8), 7);
        assert_eq!(word_ceil(s, 0), 6);
        assert_eq!(word_ceil(s, 7), 8);
        assert_eq!(word_ceil(s, 9), 10);
        assert_eq!(word_at(s, 9), Some((9, 10))); // b
        assert_eq!(word_at(s, 8), Some((7, 8))); // a（左侧紧邻词字符）
        assert_eq!(word_at(s, 4), Some((0, 6))); // SELECT 内部
    }

    #[test]
    fn preview_helpers() {
        assert!(
            try_pretty_json(&polydb_core::Value::String("{\"a\":1}".into()))
                .unwrap()
                .contains("\"a\"")
        );
        assert!(try_pretty_json(&polydb_core::Value::Integer(5)).is_none());
        assert_eq!(hex_dump(""), "(empty)");
        assert!(hex_dump("hi").contains("68 69"));
        assert!(hex_dump("hi").contains("|hi|"));
    }

    #[test]
    fn smart_indent_first_word() {
        assert_eq!(first_code_word("BEGIN"), Some("BEGIN"));
        assert_eq!(first_code_word("  \tif x = 1;"), Some("if"));
        assert_eq!(first_code_word("FOR i IN 1..3 LOOP"), Some("FOR"));
        assert_eq!(first_code_word("x := 1; THEN"), Some("x"));
        assert_eq!(first_code_word("-- IF"), None);
        assert_eq!(first_code_word("/* IF"), None);
        assert_eq!(first_code_word("# IF"), None);
        assert_eq!(first_code_word("'IF'"), None);
        assert_eq!(first_code_word("`IF`"), None);
        assert_eq!(first_code_word("   "), None);
        assert_eq!(first_code_word(""), None);
    }

    #[test]
    fn smart_indent_line_matches() {
        for w in SMART_INDENT_WORDS {
            assert!(smart_indent_line(&format!("  {w}")), "{w}");
            assert!(smart_indent_line(&format!("  {w} x")), "{w}");
        }
        // 整词匹配：FORM / THENISH 不算 FOR / THEN。
        assert!(!smart_indent_line("FORM"));
        assert!(!smart_indent_line("THENISH"));
        assert!(!smart_indent_line("SELECT * FROM t"));
        assert!(!smart_indent_line("-- BEGIN"));
    }

    #[test]
    fn newline_insert_steps_only_on_trigger_lines() {
        assert_eq!(newline_insert("BEGIN"), "\n  ");
        assert_eq!(newline_insert("  when x = 1 then"), "\n  ");
        assert_eq!(newline_insert("SELECT * FROM t"), "\n");
        assert_eq!(newline_insert("-- BEGIN"), "\n");
        assert_eq!(newline_insert(""), "\n");
    }
}
