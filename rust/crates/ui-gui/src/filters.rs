//! G2 数据浏览过滤：纯函数层（语义对齐 Web `lib/filterOps.ts` 的 op 表与 `condToWire`）。
//!
//! 独立实现、不移植代码：18 操作符元数据、数值探测（`numish` 的 `^-?\d+(\.\d+)?$`）、
//! 草稿行→`FilterCondition` 线格式转换、`case_sensitive` 三态、按方言的能力矩阵
//! （behavior.md §13.6）以及供 presets 序列化的可 serde 结构。

use polydb_core::{DatabaseKind, FilterCondition, FilterOperator, Value};
use serde::{Deserialize, Serialize};

/// 大小写敏感的三态选择（behavior.md §13.5）。缺席=沿用方言默认排序规则，故线格式省略字段。
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default, Serialize, Deserialize)]
pub(crate) enum CaseMode {
    #[default]
    Default,
    Sensitive,
    Insensitive,
}

impl CaseMode {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            CaseMode::Default => "default",
            CaseMode::Sensitive => "sensitive",
            CaseMode::Insensitive => "insensitive",
        }
    }

    pub(crate) fn label(self) -> &'static str {
        match self {
            CaseMode::Default => "默认",
            CaseMode::Sensitive => "区分",
            CaseMode::Insensitive => "忽略",
        }
    }

    pub(crate) fn parse(s: &str) -> Self {
        match s {
            "sensitive" => CaseMode::Sensitive,
            "insensitive" => CaseMode::Insensitive,
            _ => CaseMode::Default,
        }
    }

    /// 仅文本匹配类 op 下发；其余 op 后端本就忽略，前端不发以免污染对拍。
    pub(crate) fn to_wire(self) -> Option<bool> {
        match self {
            CaseMode::Default => None,
            CaseMode::Sensitive => Some(true),
            CaseMode::Insensitive => Some(false),
        }
    }
}

/// 界面上的一行过滤草稿（列 + 操作符 + 三个可选值槽 + 大小写三态）。
#[derive(Clone, PartialEq, Eq, Debug)]
pub(crate) struct FilterDraft {
    pub column: String,
    pub op: FilterOperator,
    pub value: String,
    pub second: String,
    pub values: String,
    pub case_mode: CaseMode,
}

impl FilterDraft {
    pub(crate) fn with_column(column: String) -> Self {
        Self {
            column,
            op: FilterOperator::Eq,
            value: String::new(),
            second: String::new(),
            values: String::new(),
            case_mode: CaseMode::default(),
        }
    }
}

/// 操作符元数据（是否与 Web `FILTER_OPS` 逐字段对齐）。
#[derive(Clone, Copy, Debug)]
pub(crate) struct OpMeta {
    pub op: FilterOperator,
    pub label: &'static str,
    pub needs_value: bool,
    pub needs_values: bool,
    pub needs_second: bool,
    /// 文本匹配类：唯一接受 `case_sensitive` 的 op 家族（behavior.md §13.5）。
    pub text_match: bool,
}

/// 按 `needs_*` 三标志构造一行元数据。
const fn meta(
    op: FilterOperator,
    label: &'static str,
    needs_value: bool,
    needs_values: bool,
    needs_second: bool,
    text_match: bool,
) -> OpMeta {
    OpMeta {
        op,
        label,
        needs_value,
        needs_values,
        needs_second,
        text_match,
    }
}

pub(crate) const OPS: [OpMeta; 18] = [
    meta(FilterOperator::Eq, "= 等于", true, false, false, false),
    meta(FilterOperator::Ne, "≠ 不等于", true, false, false, false),
    meta(FilterOperator::Lt, "< 小于", true, false, false, false),
    meta(FilterOperator::Le, "≤ 小于等于", true, false, false, false),
    meta(FilterOperator::Gt, "> 大于", true, false, false, false),
    meta(FilterOperator::Ge, "≥ 大于等于", true, false, false, false),
    meta(FilterOperator::Like, "LIKE 模式", true, false, false, true),
    meta(
        FilterOperator::NotLike,
        "NOT LIKE 模式",
        true,
        false,
        false,
        true,
    ),
    meta(
        FilterOperator::Contains,
        "包含子串",
        true,
        false,
        false,
        true,
    ),
    meta(
        FilterOperator::StartsWith,
        "前缀为",
        true,
        false,
        false,
        true,
    ),
    meta(FilterOperator::EndsWith, "后缀为", true, false, false, true),
    meta(FilterOperator::Regex, "正则匹配", true, false, false, true),
    meta(FilterOperator::In, "IN 列表", false, true, false, false),
    meta(
        FilterOperator::NotIn,
        "NOT IN 列表",
        false,
        true,
        false,
        false,
    ),
    meta(
        FilterOperator::Between,
        "BETWEEN 区间",
        true,
        false,
        true,
        false,
    ),
    meta(FilterOperator::Null, "IS NULL", false, false, false, false),
    meta(
        FilterOperator::NotNull,
        "IS NOT NULL",
        false,
        false,
        false,
        false,
    ),
    meta(
        FilterOperator::Empty,
        "为空（NULL 或空串）",
        false,
        false,
        false,
        false,
    ),
];

pub(crate) fn op_meta(op: FilterOperator) -> &'static OpMeta {
    OPS.iter().find(|m| m.op == op).unwrap_or(&OPS[0])
}

/// 当前方言是否支持该 op（behavior.md §13.6：SQL Server 无原生正则）。
pub(crate) fn op_supported(kind: DatabaseKind, op: FilterOperator) -> bool {
    !(op == FilterOperator::Regex && kind == DatabaseKind::Mssql)
}

/// 不支持时的提示文案；可用则 None。
pub(crate) fn op_unsupported_hint(kind: DatabaseKind, op: FilterOperator) -> Option<&'static str> {
    if op_supported(kind, op) {
        return None;
    }
    match (kind, op) {
        (DatabaseKind::Mssql, FilterOperator::Regex) => {
            Some("SQL Server 无原生正则，后端会返回 POLYDB_ERR_NOT_SUPPORTED")
        }
        _ => Some("当前数据库不支持该操作符"),
    }
}

/// 在 OPS 表内向后循环切换到下一个**该方言可用**的操作符。
pub(crate) fn next_op(kind: DatabaseKind, op: FilterOperator) -> FilterOperator {
    let n = OPS.len();
    let start = OPS.iter().position(|m| m.op == op).unwrap_or(0);
    for step in 1..=n {
        let cand = OPS[(start + step) % n].op;
        if op_supported(kind, cand) {
            return cand;
        }
    }
    op
}

/// 字符串→操作符（未知回落 Eq），供预设反序列化。
pub(crate) fn parse_op(s: &str) -> FilterOperator {
    OPS.iter()
        .find(|m| m.op.as_str() == s)
        .map(|m| m.op)
        .unwrap_or(FilterOperator::Eq)
}

// numish 的数值探测：仅 `-?\d+` 或 `-?\d+\.\d+`（无科学计数/前后缀）。
fn numeric_shape(t: &str) -> Option<Value> {
    let b = t.as_bytes();
    if b.is_empty() {
        return None;
    }
    let mut i = 0usize;
    if b[0] == b'-' {
        i = 1;
    }
    let mut digits = 0usize;
    while i < b.len() && b[i].is_ascii_digit() {
        i += 1;
        digits += 1;
    }
    if digits == 0 {
        return None;
    }
    if i == b.len() {
        return t.parse::<i64>().ok().map(Value::Integer);
    }
    if b[i] == b'.' {
        i += 1;
        let mut frac = 0usize;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
            frac += 1;
        }
        if frac >= 1 && i == b.len() {
            return t.parse::<f64>().ok().map(Value::Float);
        }
    }
    None
}

/// 单值线格式转换：数值探测用 trim 后文本，字符串回落保留原文（对齐 Web numish）。
pub(crate) fn parse_wire_value(raw: &str) -> Value {
    match numeric_shape(raw.trim()) {
        Some(v) => v,
        None => Value::String(raw.to_string()),
    }
}

/// 草稿行 → `FilterCondition`；无列、IN 列表空集、当前方言不支持该 op 返回 None
/// （该行不参与请求，对齐 Web `condToWire`）。
pub(crate) fn draft_to_condition(d: &FilterDraft, kind: DatabaseKind) -> Option<FilterCondition> {
    if d.column.is_empty() {
        return None;
    }
    let m = op_meta(d.op);
    if !op_supported(kind, d.op) {
        return None;
    }
    let mut c = FilterCondition {
        column: d.column.clone(),
        op: d.op,
        value: Value::Null,
        second_value: Value::Null,
        values: Vec::new(),
        case_sensitive: None,
    };
    if m.needs_value {
        c.value = parse_wire_value(&d.value);
    }
    if m.needs_second {
        c.second_value = parse_wire_value(&d.second);
    }
    if m.needs_values {
        let vals: Vec<Value> = d
            .values
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .map(parse_wire_value)
            .collect();
        if vals.is_empty() {
            return None;
        }
        c.values = vals;
    }
    // 仅文本匹配类 op 下发；其余 op 后端本就忽略，不发以免污染日志与契约对拍。
    if m.text_match {
        c.case_sensitive = d.case_mode.to_wire();
    }
    Some(c)
}

// ─── 预设持久化的 serde 结构（op 以字符串落盘，避免耦合 serde repr）───

#[derive(Clone, Serialize, Deserialize, Debug, PartialEq)]
pub(crate) struct SavedCondition {
    pub column: String,
    pub op: String,
    #[serde(default)]
    pub value: String,
    #[serde(default)]
    pub second: String,
    #[serde(default)]
    pub values: String,
    /// 旧预设没有该字段，`default` 字符串回落 `CaseMode::Default`（线格式省略=方言默认）。
    #[serde(default = "default_case_mode")]
    pub case_mode: String,
}

fn default_case_mode() -> String {
    CaseMode::Default.as_str().to_string()
}

#[derive(Clone, Serialize, Deserialize, Debug, PartialEq)]
pub(crate) struct FilterPreset {
    pub name: String,
    pub conditions: Vec<SavedCondition>,
    #[serde(default)]
    pub logic: String,
}

impl FilterPreset {
    pub(crate) fn from_drafts(name: &str, drafts: &[FilterDraft], and_logic: bool) -> Self {
        Self {
            name: name.to_string(),
            conditions: drafts
                .iter()
                .map(|d| SavedCondition {
                    column: d.column.clone(),
                    op: d.op.as_str().to_string(),
                    value: d.value.clone(),
                    second: d.second.clone(),
                    values: d.values.clone(),
                    case_mode: d.case_mode.as_str().to_string(),
                })
                .collect(),
            logic: if and_logic { "and".into() } else { "or".into() },
        }
    }

    pub(crate) fn to_drafts(&self) -> Vec<FilterDraft> {
        self.conditions
            .iter()
            .map(|s| FilterDraft {
                column: s.column.clone(),
                op: parse_op(&s.op),
                value: s.value.clone(),
                second: s.second.clone(),
                values: s.values.clone(),
                case_mode: CaseMode::parse(&s.case_mode),
            })
            .collect()
    }

    pub(crate) fn logic_is_and(&self) -> bool {
        self.logic != "or"
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn draft(col: &str, op: FilterOperator, v: &str, second: &str, values: &str) -> FilterDraft {
        FilterDraft {
            column: col.into(),
            op,
            value: v.into(),
            second: second.into(),
            values: values.into(),
            case_mode: CaseMode::Default,
        }
    }

    fn with_case(mut d: FilterDraft, m: CaseMode) -> FilterDraft {
        d.case_mode = m;
        d
    }

    /// 多数单测用 SQLite；MSSQL 仅用于能力矩阵。
    const SQLITE: DatabaseKind = DatabaseKind::Sqlite;

    #[test]
    fn op_table_covers_18_ops_with_text_match_flags() {
        assert_eq!(OPS.len(), 18);
        let mut names: Vec<&str> = OPS.iter().map(|m| m.op.as_str()).collect();
        names.sort_unstable();
        names.dedup();
        assert_eq!(names.len(), 18, "op 表不得重复");
        let text: Vec<&str> = OPS
            .iter()
            .filter(|m| m.text_match)
            .map(|m| m.op.as_str())
            .collect();
        assert_eq!(
            text,
            vec![
                "like",
                "not_like",
                "contains",
                "starts_with",
                "ends_with",
                "regex"
            ]
        );
        // 无值槽的三个 op
        for op in ["null", "not_null", "empty"] {
            let m = OPS.iter().find(|m| m.op.as_str() == op).unwrap();
            assert!(!(m.needs_value || m.needs_values || m.needs_second), "{op}");
        }
        // needsValues 与其余互斥；needsSecond 只随 between 出现
        for m in OPS.iter() {
            if m.needs_values {
                assert!(!m.needs_value && !m.needs_second);
            }
            if m.needs_second {
                assert_eq!(m.op, FilterOperator::Between);
            }
        }
    }

    #[test]
    fn case_mode_three_states() {
        assert_eq!(CaseMode::Default.to_wire(), None);
        assert_eq!(CaseMode::Sensitive.to_wire(), Some(true));
        assert_eq!(CaseMode::Insensitive.to_wire(), Some(false));
        assert_eq!(CaseMode::parse("insensitive"), CaseMode::Insensitive);
        assert_eq!(CaseMode::parse("bogus"), CaseMode::Default);
        for m in [
            CaseMode::Default,
            CaseMode::Sensitive,
            CaseMode::Insensitive,
        ] {
            assert_eq!(CaseMode::parse(m.as_str()), m);
        }
    }

    #[test]
    fn case_sensitive_only_on_text_match_ops() {
        let c = draft_to_condition(
            &with_case(
                draft("nm", FilterOperator::Contains, "ali", "", ""),
                CaseMode::Sensitive,
            ),
            SQLITE,
        )
        .unwrap();
        assert_eq!(c.case_sensitive, Some(true));
        // 比较类 op 即便设了也不下发（后端本就忽略，发了会让契约对拍出现噪声）
        let e = draft_to_condition(
            &with_case(
                draft("id", FilterOperator::Eq, "1", "", ""),
                CaseMode::Sensitive,
            ),
            SQLITE,
        )
        .unwrap();
        assert_eq!(e.case_sensitive, None);
        // default 三态省略字段
        let d =
            draft_to_condition(&draft("nm", FilterOperator::Like, "%a%", "", ""), SQLITE).unwrap();
        assert_eq!(d.case_sensitive, None);
        assert_eq!(d.value, Value::String("%a%".into()));
    }

    #[test]
    fn mssql_regex_is_dropped_not_sent() {
        assert!(!op_supported(DatabaseKind::Mssql, FilterOperator::Regex));
        assert!(op_supported(SQLITE, FilterOperator::Regex));
        assert!(op_supported(DatabaseKind::Postgres, FilterOperator::Regex));
        let d = draft("nm", FilterOperator::Regex, "^a", "", "");
        assert!(draft_to_condition(&d, DatabaseKind::Mssql).is_none());
        assert!(draft_to_condition(&d, SQLITE).is_some());
        assert!(
            op_unsupported_hint(DatabaseKind::Mssql, FilterOperator::Regex)
                .unwrap()
                .contains("NOT_SUPPORTED")
        );
        assert!(op_unsupported_hint(SQLITE, FilterOperator::Regex).is_none());
    }

    #[test]
    fn op_cycle_skips_unsupported_and_wraps() {
        assert_eq!(next_op(SQLITE, FilterOperator::Eq), FilterOperator::Ne);
        assert_eq!(
            next_op(SQLITE, FilterOperator::EndsWith),
            FilterOperator::Regex
        );
        // MSSQL：从后缀直接跳到 IN 列表，跳过正则
        assert_eq!(
            next_op(DatabaseKind::Mssql, FilterOperator::EndsWith),
            FilterOperator::In
        );
        // 回到起点（新表尾 op 为空），且永不落在不支持的 op 上
        assert_eq!(next_op(SQLITE, FilterOperator::Empty), FilterOperator::Eq);
        for m in OPS.iter() {
            assert!(op_supported(
                DatabaseKind::Mssql,
                next_op(DatabaseKind::Mssql, m.op)
            ));
        }
    }

    #[test]
    fn numish_matches_web() {
        assert_eq!(parse_wire_value("42"), Value::Integer(42));
        assert_eq!(parse_wire_value(" -7 "), Value::Integer(-7));
        assert_eq!(parse_wire_value("3.5"), Value::Float(3.5));
        // 无小数位 / 科学计数 / 前导 + → 按字符串原样（不 trim 回落）。
        assert_eq!(parse_wire_value("5."), Value::String("5.".into()));
        assert_eq!(parse_wire_value("1e5"), Value::String("1e5".into()));
        assert_eq!(parse_wire_value("abc"), Value::String("abc".into()));
        assert_eq!(parse_wire_value(""), Value::String("".into()));
    }

    #[test]
    fn no_column_or_empty_in_dropped() {
        assert!(draft_to_condition(&draft("", FilterOperator::Eq, "1", "", ""), SQLITE).is_none());
        assert!(
            draft_to_condition(&draft("id", FilterOperator::In, "", "", " , , "), SQLITE).is_none()
        );
    }

    #[test]
    fn condition_shapes() {
        let c = draft_to_condition(&draft("id", FilterOperator::Eq, "5", "", ""), SQLITE).unwrap();
        assert_eq!(c.value, Value::Integer(5));
        let b =
            draft_to_condition(&draft("n", FilterOperator::Between, "1", "9", ""), SQLITE).unwrap();
        assert_eq!(
            (b.value, b.second_value),
            (Value::Integer(1), Value::Integer(9))
        );
        let l =
            draft_to_condition(&draft("s", FilterOperator::Like, "%a%", "", ""), SQLITE).unwrap();
        assert_eq!(l.value, Value::String("%a%".into()));
        let i =
            draft_to_condition(&draft("k", FilterOperator::In, "", "", "1, 2, x"), SQLITE).unwrap();
        assert_eq!(
            i.values,
            vec![
                Value::Integer(1),
                Value::Integer(2),
                Value::String("x".into())
            ]
        );
        let nl = draft_to_condition(&draft("t", FilterOperator::Null, "", "", ""), SQLITE).unwrap();
        assert_eq!(nl.value, Value::Null);
        // 新增 op 的取值形态：三个包裹类取单值，empty 不取值
        for op in [
            FilterOperator::Contains,
            FilterOperator::StartsWith,
            FilterOperator::EndsWith,
        ] {
            let x = draft_to_condition(&draft("s", op, "a%b", "", ""), SQLITE).unwrap();
            assert_eq!(x.value, Value::String("a%b".into()));
        }
        let e = draft_to_condition(
            &draft("s", FilterOperator::Empty, "ignored", "", ""),
            SQLITE,
        )
        .unwrap();
        assert_eq!(e.value, Value::Null);
    }

    #[test]
    fn op_cycle_wraps() {
        assert_eq!(parse_op("between"), FilterOperator::Between);
        assert_eq!(parse_op("starts_with"), FilterOperator::StartsWith);
        assert_eq!(parse_op("empty"), FilterOperator::Empty);
        assert_eq!(parse_op("bogus"), FilterOperator::Eq);
    }

    #[test]
    fn preset_roundtrip() {
        let drafts = vec![
            draft("id", FilterOperator::Gt, "10", "", ""),
            with_case(
                draft("nm", FilterOperator::In, "", "", "a,b"),
                CaseMode::Insensitive,
            ),
        ];
        let p = FilterPreset::from_drafts("P1", &drafts, false);
        assert_eq!(p.logic, "or");
        let back = p.to_drafts();
        assert_eq!(back, drafts);
        let json = serde_json::to_string(&p).unwrap();
        let p2: FilterPreset = serde_json::from_str(&json).unwrap();
        assert_eq!(p2, p);
    }

    #[test]
    fn legacy_preset_without_case_mode_still_loads() {
        // G2 时期落盘的预设没有 case_mode 字段，必须回落方言默认。
        let legacy = r#"{"name":"P0","conditions":[{"column":"nm","op":"like","value":"%a%","second":"","values":""}],"logic":"and"}"#;
        let p: FilterPreset = serde_json::from_str(legacy).unwrap();
        let d = &p.to_drafts()[0];
        assert_eq!(d.case_mode, CaseMode::Default);
        assert_eq!(draft_to_condition(d, SQLITE).unwrap().case_sensitive, None);
    }
}
