//! U6.1 列画像（语义对齐 Web lib/tableProfile.ts）：单条聚合 SQL 统计
//! 行数 + 每列 NULL 数/去重数/数值 MIN·MAX·AVG；LOB 列跳过 DISTINCT，数值列才出 MIN/MAX/AVG。
//! 纯函数放这里便于单测；组件侧只负责执行与展示。

use polydb_core::Value;

pub(crate) const MAX_PROFILE_COLS: usize = 24;

pub(crate) struct ProfileColumn {
    pub name: String,
    pub data_type: String,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum MetricKind {
    Nulls,
    Distinct,
    Min,
    Max,
    Avg,
}

#[derive(Debug)]
pub(crate) struct MetricSlot {
    pub idx: usize,
    pub column: String,
    pub kind: MetricKind,
}

#[derive(Debug)]
pub(crate) struct ProfilePlan {
    pub sql: String,
    pub metrics: Vec<MetricSlot>,
}

const NUMERIC_WORDS: [&str; 15] = [
    "int",
    "integer",
    "serial",
    "smallint",
    "bigint",
    "numeric",
    "decimal",
    "float",
    "double",
    "real",
    "money",
    "smallmoney",
    "number",
    "binary_float",
    "binary_double",
];

// 对齐 Web 的 \b(int|...)\b：按非字母数字分词后查表（year 罕见，从略不入歧义集）。
pub(crate) fn is_numeric_type(t: &str) -> bool {
    t.to_lowercase()
        .split(|c: char| !c.is_ascii_alphanumeric() && c != '_')
        .any(|w| NUMERIC_WORDS.contains(&w))
}

// 对齐 Web LOB_RE 的子串语义（不带词边界）。
pub(crate) fn is_lob_type(t: &str) -> bool {
    const LOBS: [&str; 10] = [
        "text",
        "clob",
        "blob",
        "bytea",
        "json",
        "xml",
        "image",
        "geometry",
        "geography",
        "interval",
    ];
    let lt = t.to_lowercase();
    // year 型在 Web 数值集里；这里保持与其"numeric 优先"一致：数值判定不含 lob。
    LOBS.iter().any(|p| lt.contains(p))
}

/// 标识符引用：语义对齐 Web alterSql.quoteIdent——统一双引号，oracle 小写。
pub(crate) fn quote_ident(kind: &str, s: &str) -> String {
    if kind == "oracle" {
        format!("\"{}\"", s.to_lowercase())
    } else {
        format!("\"{s}\"")
    }
}

pub(crate) fn build_profile_plan(
    kind: &str,
    table_expr: &str,
    columns: &[ProfileColumn],
) -> ProfilePlan {
    let mut selects: Vec<String> = vec!["COUNT(*)".to_string()];
    let mut metrics: Vec<MetricSlot> = Vec::new();
    let mut idx = 1usize;
    for col in columns {
        let q = quote_ident(kind, &col.name);
        let numeric = is_numeric_type(&col.data_type);
        let lob = !numeric && is_lob_type(&col.data_type);

        selects.push(format!("SUM(CASE WHEN {q} IS NULL THEN 1 ELSE 0 END)"));
        metrics.push(MetricSlot {
            idx,
            column: col.name.clone(),
            kind: MetricKind::Nulls,
        });
        idx += 1;

        if !lob {
            selects.push(format!("COUNT(DISTINCT {q})"));
            metrics.push(MetricSlot {
                idx,
                column: col.name.clone(),
                kind: MetricKind::Distinct,
            });
            idx += 1;
        }
        if numeric {
            selects.push(format!("MIN({q})"));
            metrics.push(MetricSlot {
                idx,
                column: col.name.clone(),
                kind: MetricKind::Min,
            });
            idx += 1;
            selects.push(format!("MAX({q})"));
            metrics.push(MetricSlot {
                idx,
                column: col.name.clone(),
                kind: MetricKind::Max,
            });
            idx += 1;
            selects.push(format!("AVG({q})"));
            metrics.push(MetricSlot {
                idx,
                column: col.name.clone(),
                kind: MetricKind::Avg,
            });
            idx += 1;
        }
    }
    ProfilePlan {
        sql: format!("SELECT {} FROM {table_expr}", selects.join(", ")),
        metrics,
    }
}

#[derive(Debug, Default, Clone)]
pub(crate) struct ColumnProfile {
    pub nulls: Option<i64>,
    pub distinct: Option<i64>,
    pub min: Option<Value>,
    pub max: Option<Value>,
    pub avg: Option<Value>,
}

#[derive(Debug, Default)]
pub(crate) struct ProfileResult {
    pub row_count: Option<i64>,
    pub by_column: Vec<(String, ColumnProfile)>,
}

fn as_i64(v: &Value) -> Option<i64> {
    match v {
        Value::Integer(i) => Some(*i),
        Value::Float(f) => Some(*f as i64),
        Value::String(s) => s.trim().parse().ok(),
        _ => None,
    }
}

/// 用聚合结果首行按计划索引还原画像；row 缺失（查询失败/空）时返回全 None。
pub(crate) fn parse_profile(row: Option<&[Value]>, plan: &ProfilePlan) -> ProfileResult {
    let mut out = ProfileResult::default();
    let Some(row) = row else { return out };
    out.row_count = row.first().and_then(as_i64);
    let mut by: Vec<(String, ColumnProfile)> = Vec::new();
    for m in &plan.metrics {
        let entry = match by.iter_mut().find(|(n, _)| *n == m.column) {
            Some((_, p)) => p,
            None => {
                by.push((m.column.clone(), ColumnProfile::default()));
                &mut by.last_mut().unwrap().1
            }
        };
        let v = row.get(m.idx);
        match m.kind {
            MetricKind::Nulls => entry.nulls = v.and_then(as_i64),
            MetricKind::Distinct => entry.distinct = v.and_then(as_i64),
            MetricKind::Min => entry.min = v.cloned().filter(|x| !matches!(x, Value::Null)),
            MetricKind::Max => entry.max = v.cloned().filter(|x| !matches!(x, Value::Null)),
            MetricKind::Avg => entry.avg = v.cloned().filter(|x| !matches!(x, Value::Null)),
        }
    }
    out.by_column = by;
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn col(name: &str, t: &str) -> ProfileColumn {
        ProfileColumn {
            name: name.into(),
            data_type: t.into(),
        }
    }

    #[test]
    fn type_classification() {
        assert!(is_numeric_type("INTEGER"));
        assert!(is_numeric_type("double precision"));
        assert!(is_numeric_type("numeric(10,2)"));
        assert!(!is_numeric_type("varchar(20)"));
        assert!(!is_numeric_type("point"));
        assert!(is_lob_type("TEXT"));
        assert!(is_lob_type("jsonb"));
        assert!(is_lob_type("BYTEA"));
        assert!(!is_lob_type("varchar"));
    }

    #[test]
    fn oracle_ident_lowercased() {
        assert_eq!(quote_ident("oracle", "USER"), "\"user\"");
        assert_eq!(quote_ident("postgres", "User"), "\"User\"");
    }

    #[test]
    fn plan_metric_slots_match_web_semantics() {
        let cols = vec![
            col("id", "INTEGER"),
            col("name", "TEXT"),
            col("data", "BLOB"),
        ];
        let plan = build_profile_plan("sqlite", "\"main\".\"t\"", &cols);
        // id: nulls@1 distinct@2 min@3 max@4 avg@5；name(TEXT)/data(BLOB) 均为 LOB → 只出 nulls@6/@7。
        let slots: Vec<(usize, &str, MetricKind)> = plan
            .metrics
            .iter()
            .map(|m| (m.idx, m.column.as_str(), m.kind))
            .collect();
        assert_eq!(
            slots,
            vec![
                (1, "id", MetricKind::Nulls),
                (2, "id", MetricKind::Distinct),
                (3, "id", MetricKind::Min),
                (4, "id", MetricKind::Max),
                (5, "id", MetricKind::Avg),
                (6, "name", MetricKind::Nulls),
                (7, "data", MetricKind::Nulls),
            ]
        );
        assert!(plan
            .sql
            .starts_with("SELECT COUNT(*), SUM(CASE WHEN \"id\" IS NULL"));
        assert!(plan.sql.ends_with("FROM \"main\".\"t\""));
    }

    #[test]
    fn parse_roundtrip() {
        let cols = vec![col("id", "INTEGER"), col("name", "TEXT")];
        let plan = build_profile_plan("sqlite", "t", &cols);
        // COUNT(*)=10；id: nulls@1=2 distinct@2=9 min@3=1 max@4=10 avg@5=5.5；name(TEXT,LOB): nulls@6=0
        let row = vec![
            Value::Integer(10),
            Value::Integer(2),
            Value::Integer(9),
            Value::Integer(1),
            Value::Integer(10),
            Value::Float(5.5),
            Value::Integer(0),
        ];
        let r = parse_profile(Some(&row), &plan);
        assert_eq!(r.row_count, Some(10));
        let id = &r.by_column[0].1;
        assert_eq!(
            (
                id.nulls,
                id.distinct,
                id.min.clone(),
                id.max.clone(),
                id.avg.clone()
            ),
            (
                Some(2),
                Some(9),
                Some(Value::Integer(1)),
                Some(Value::Integer(10)),
                Some(Value::Float(5.5))
            )
        );
        let name = &r.by_column[1].1;
        assert_eq!(name.nulls, Some(0));
        assert_eq!(name.distinct, None); // LOB 列跳过 DISTINCT
                                         // 空行（失败）不 panic、全 None。
        assert!(parse_profile(None, &plan).by_column.is_empty());
    }
}
