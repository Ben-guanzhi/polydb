//! G2 数据浏览过滤：纯函数层（语义对齐 Web `TableDataView.tsx` 的 OPS 表与 `condToWire`）。
//!
//! 独立实现、不移植代码：13 操作符元数据、数值探测（`numish` 的 `^-?\d+(\.\d+)?$`）、
//! 草稿行→`FilterCondition` 线格式转换、以及供 presets 序列化的可 serde 结构。

use polydb_core::{FilterCondition, FilterOperator, Value};
use serde::{Deserialize, Serialize};

/// 界面上的一行过滤草稿（列 + 操作符 + 三个可选值槽）。
#[derive(Clone, PartialEq, Eq, Debug)]
pub(crate) struct FilterDraft {
    pub column: String,
    pub op: FilterOperator,
    pub value: String,
    pub second: String,
    pub values: String,
}

impl FilterDraft {
    pub(crate) fn with_column(column: String) -> Self {
        Self {
            column,
            op: FilterOperator::Eq,
            value: String::new(),
            second: String::new(),
            values: String::new(),
        }
    }
}

/// 操作符元数据（是否与 Web OPS 表逐字段对齐）。
#[derive(Clone, Copy, Debug)]
pub(crate) struct OpMeta {
    pub op: FilterOperator,
    pub label: &'static str,
    pub needs_value: bool,
    pub needs_values: bool,
    pub needs_second: bool,
}

pub(crate) const OPS: [OpMeta; 13] = [
    OpMeta {
        op: FilterOperator::Eq,
        label: "= 等于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Ne,
        label: "≠ 不等于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Lt,
        label: "< 小于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Le,
        label: "≤ 小于等于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Gt,
        label: "> 大于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Ge,
        label: "≥ 大于等于",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Like,
        label: "LIKE 模式",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::NotLike,
        label: "NOT LIKE 模式",
        needs_value: true,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::In,
        label: "IN 列表",
        needs_value: false,
        needs_values: true,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::NotIn,
        label: "NOT IN 列表",
        needs_value: false,
        needs_values: true,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::Between,
        label: "BETWEEN 区间",
        needs_value: true,
        needs_values: false,
        needs_second: true,
    },
    OpMeta {
        op: FilterOperator::Null,
        label: "IS NULL",
        needs_value: false,
        needs_values: false,
        needs_second: false,
    },
    OpMeta {
        op: FilterOperator::NotNull,
        label: "IS NOT NULL",
        needs_value: false,
        needs_values: false,
        needs_second: false,
    },
];

pub(crate) fn op_meta(op: FilterOperator) -> &'static OpMeta {
    OPS.iter().find(|m| m.op == op).unwrap_or(&OPS[0])
}

/// 在 OPS 表内向后循环切换到下一个操作符。
pub(crate) fn next_op(op: FilterOperator) -> FilterOperator {
    let i = OPS.iter().position(|m| m.op == op).unwrap_or(0);
    OPS[(i + 1) % OPS.len()].op
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

/// 草稿行 → `FilterCondition`；无列、IN 列表空集返回 None（该行不参与，对齐 Web condToWire）。
pub(crate) fn draft_to_condition(d: &FilterDraft) -> Option<FilterCondition> {
    if d.column.is_empty() {
        return None;
    }
    let m = op_meta(d.op);
    let mut c = FilterCondition {
        column: d.column.clone(),
        op: d.op,
        value: Value::Null,
        second_value: Value::Null,
        values: Vec::new(),
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
        assert!(draft_to_condition(&draft("", FilterOperator::Eq, "1", "", "")).is_none());
        assert!(draft_to_condition(&draft("id", FilterOperator::In, "", "", " , , ")).is_none());
    }

    #[test]
    fn condition_shapes() {
        let c = draft_to_condition(&draft("id", FilterOperator::Eq, "5", "", "")).unwrap();
        assert_eq!(c.value, Value::Integer(5));
        let b = draft_to_condition(&draft("n", FilterOperator::Between, "1", "9", "")).unwrap();
        assert_eq!(
            (b.value, b.second_value),
            (Value::Integer(1), Value::Integer(9))
        );
        let l = draft_to_condition(&draft("s", FilterOperator::Like, "%a%", "", "")).unwrap();
        assert_eq!(l.value, Value::String("%a%".into()));
        let i = draft_to_condition(&draft("k", FilterOperator::In, "", "", "1, 2, x")).unwrap();
        assert_eq!(
            i.values,
            vec![
                Value::Integer(1),
                Value::Integer(2),
                Value::String("x".into())
            ]
        );
        let nl = draft_to_condition(&draft("t", FilterOperator::Null, "", "", "")).unwrap();
        assert_eq!(nl.value, Value::Null);
    }

    #[test]
    fn op_cycle_wraps() {
        assert_eq!(next_op(FilterOperator::Eq), FilterOperator::Ne);
        assert_eq!(next_op(FilterOperator::NotNull), FilterOperator::Eq);
        assert_eq!(parse_op("between"), FilterOperator::Between);
        assert_eq!(parse_op("bogus"), FilterOperator::Eq);
    }

    #[test]
    fn preset_roundtrip() {
        let drafts = vec![
            draft("id", FilterOperator::Gt, "10", "", ""),
            draft("nm", FilterOperator::In, "", "", "a,b"),
        ];
        let p = FilterPreset::from_drafts("P1", &drafts, false);
        assert_eq!(p.logic, "or");
        let back = p.to_drafts();
        assert_eq!(back, drafts);
        let json = serde_json::to_string(&p).unwrap();
        let p2: FilterPreset = serde_json::from_str(&json).unwrap();
        assert_eq!(p2, p);
    }
}
