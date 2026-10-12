//! 表数据浏览的共享查询构造器（behavior.md §13）：与 Go 侧 `dbcore/browse.go` 逐条对应。
//! 条件值一律走绑定参数，标识符由各方言的 quote 函数引用（内部引号翻倍）。

use polydb_core::{CoreError, CoreResult, TableRowsRequest, Value};

/// 文本匹配类 op 的方言形态差异（behavior.md §13.5）。与引号/分页无关，单独建模。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BrowseFlavor {
    /// ANSI：LIKE 默认区分大小写，正则用 `~` / `~*`（postgres）。
    Pg,
    /// 反引号 + `REGEXP`，LIKE 默认按排序规则不区分（mysql）。
    MySql,
    /// 方括号 + `LIKE … COLLATE …_CS_AS`，**无原生正则**（mssql）。
    MsSql,
    /// `REGEXP_LIKE`，LIKE 默认区分大小写（oracle）。
    Oracle,
    /// 严格大小写走 `GLOB`，正则走驱动注册的 `REGEXP` UDF（sqlite）。
    Sqlite,
}

/// 正则能力：None 的方言遇 `regex` op 直接 NOT_SUPPORTED，不静默降级。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RegexSupport {
    Unsupported,
    Supported,
}

/// 浏览查询需要的方言差异。
#[derive(Clone, Copy)]
pub struct BrowseDialect {
    pub quote: fn(&str) -> String,
    pub limit_clause: fn(u64, u64) -> String,
    /// 分页子句要求 ORDER BY 且请求未带排序时使用（MSSQL）；None 表示无需兜底。
    pub order_by_fallback: Option<&'static str>,
    pub flavor: BrowseFlavor,
}

impl BrowseDialect {
    pub fn regex_support(&self) -> RegexSupport {
        match self.flavor {
            BrowseFlavor::MsSql => RegexSupport::Unsupported,
            _ => RegexSupport::Supported,
        }
    }
}

/// ANSI 双引号标识符 + LIMIT/OFFSET（postgres）。
pub const DIALECT_DEFAULT: BrowseDialect = BrowseDialect {
    quote: quote_double,
    limit_clause: limit_offset_clause,
    order_by_fallback: None,
    flavor: BrowseFlavor::Pg,
};

/// SQLite：引号/分页同 ANSI，但大小写与正则形态不同。
pub const DIALECT_SQLITE: BrowseDialect = BrowseDialect {
    quote: quote_double,
    limit_clause: limit_offset_clause,
    order_by_fallback: None,
    flavor: BrowseFlavor::Sqlite,
};

/// 反引号标识符 + LIMIT/OFFSET（mysql）。
pub const DIALECT_MYSQL: BrowseDialect = BrowseDialect {
    quote: quote_backtick,
    limit_clause: limit_offset_clause,
    order_by_fallback: None,
    flavor: BrowseFlavor::MySql,
};

/// 方括号标识符 + OFFSET/FETCH + ORDER BY 兜底（mssql）。
pub const DIALECT_MSSQL: BrowseDialect = BrowseDialect {
    quote: quote_bracket,
    limit_clause: offset_fetch_clause,
    order_by_fallback: Some("(SELECT NULL)"),
    flavor: BrowseFlavor::MsSql,
};

/// 双引号标识符 + OFFSET/FETCH（oracle 12c+）。
pub const DIALECT_ORACLE: BrowseDialect = BrowseDialect {
    quote: quote_double,
    limit_clause: offset_fetch_clause,
    order_by_fallback: None,
    flavor: BrowseFlavor::Oracle,
};

fn quote_double(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

fn quote_backtick(name: &str) -> String {
    format!("`{}`", name.replace('`', "``"))
}

fn quote_bracket(name: &str) -> String {
    format!("[{}]", name.replace(']', "]]"))
}

fn limit_offset_clause(offset: u64, limit: u64) -> String {
    format!(" LIMIT {limit} OFFSET {offset}")
}

fn offset_fetch_clause(offset: u64, limit: u64) -> String {
    format!(" OFFSET {offset} ROWS FETCH NEXT {limit} ROWS ONLY")
}

/// 分页钳制（§13.2），上限 10000。缺省值当前取 200，而 spec 声明 100——偏差已
/// 登记在 AGENTS.md §16 第 9 条。
pub fn browse_rows_limits(limit: u32) -> u64 {
    match limit {
        0 => 200,
        n if n > 10000 => 10000,
        n => n as u64,
    }
}

fn invalid_param(msg: impl Into<String>) -> CoreError {
    CoreError::from(polydb_protocol::PolyDBError::new(
        polydb_protocol::error::codes::INVALID_PARAM,
        msg.into(),
    ))
}

/// 校验请求的公共部分（列名/条件/op）。
pub fn browse_rows_validate(req: &TableRowsRequest) -> CoreResult<()> {
    for c in &req.columns {
        if c.trim().is_empty() {
            return Err(invalid_param("empty column name"));
        }
    }
    for (i, cond) in req.conditions.iter().enumerate() {
        if cond.column.trim().is_empty() {
            return Err(invalid_param(format!("empty column name in condition {i}")));
        }
        use polydb_protocol::FilterOperator as Op;
        match cond.op {
            Op::Unknown => {
                return Err(invalid_param(format!("condition {i}: unknown filter op")));
            }
            Op::In | Op::NotIn => {
                if cond.values.is_empty() {
                    return Err(invalid_param(format!(
                        "condition {i}: op {} requires non-empty values",
                        cond.op.as_str()
                    )));
                }
            }
            Op::Between => {
                if value_is_null(&cond.value) || value_is_null(&cond.second_value) {
                    return Err(invalid_param(format!(
                        "condition {i}: op between requires value and second_value"
                    )));
                }
            }
            Op::Empty | Op::Null | Op::NotNull => {}
            other => {
                if other.needs_value() && value_is_null(&cond.value) {
                    return Err(invalid_param(format!(
                        "condition {i}: op {} requires value",
                        other.as_str()
                    )));
                }
            }
        }
    }
    Ok(())
}

fn value_is_null(v: &Value) -> bool {
    matches!(v, Value::Null)
}

fn not_supported(msg: impl Into<String>) -> CoreError {
    CoreError::from(polydb_protocol::PolyDBError::new(
        polydb_protocol::error::codes::NOT_SUPPORTED,
        msg.into(),
    ))
}

/// 文本匹配类 op 的转义符（behavior.md §13.3）：不用 `\`，MySQL 字面量里它自身要转义。
const LIKE_ESCAPE: char = '!';

/// 标量值转文本模式；数组/对象不是合法的匹配值 → None。
fn value_to_text(v: &Value) -> Option<String> {
    Some(match v {
        Value::String(s) => s.clone(),
        Value::Integer(i) => i.to_string(),
        Value::Float(f) => f.to_string(),
        Value::Bool(b) => b.to_string(),
        Value::Null | Value::Array(_) | Value::Object(_) => return None,
    })
}

/// 转义用户文本里的 LIKE 通配符，供 contains/starts_with/ends_with 包裹后使用。
pub fn escape_like_text(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for ch in s.chars() {
        match ch {
            '!' => out.push_str("!!"),
            '%' => out.push_str("!%"),
            '_' => out.push_str("!_"),
            _ => out.push(ch),
        }
    }
    out
}

/// SQL LIKE 模式 → SQLite GLOB 模式（GLOB 区分大小写，元字符用 `\` 转义）。
/// 只用于把**用户提供的 LIKE 模式**翻译过去，故 `%`/`_` 视为通配符。
pub fn like_pattern_to_glob(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for ch in s.chars() {
        match ch {
            '%' => out.push('*'),
            '_' => out.push('?'),
            '\\' | '*' | '?' | '[' => {
                out.push('\\');
                out.push(ch);
            }
            _ => out.push(ch),
        }
    }
    out
}

/// 字面文本 → GLOB 模式片段：只转义 GLOB 元字符，不当通配符解释（`%`/`_` 在此是普通字符）。
pub fn glob_quote_text(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for ch in s.chars() {
        match ch {
            '\\' | '*' | '?' | '[' => {
                out.push('\\');
                out.push(ch);
            }
            _ => out.push(ch),
        }
    }
    out
}

/// LIKE 家族条件：同一份值的两种模式形态 + 是否取反 + 是否带 ESCAPE 子句。
/// SQLite 强制区分大小写时走 `glob`，其余方言走 `like`——两套转义符互不通用。
struct TextCond {
    like: String,
    glob: String,
    neg: bool,
    escaped: bool,
}

/// 由 op 与值构造 [`TextCond`]。`like`/`not_like` 原样透传（既有语义不变），
/// 包裹类 op 先按各自方言的转义规则处理字面值，再加通配符。
fn text_cond(op: polydb_protocol::FilterOperator, v: &Value) -> CoreResult<TextCond> {
    use polydb_protocol::FilterOperator as Op;
    let raw = value_to_text(v)
        .ok_or_else(|| invalid_param(format!("op {} requires a scalar value", op.as_str())))?;
    let e = escape_like_text(&raw);
    let g = glob_quote_text(&raw);
    Ok(match op {
        Op::Like => TextCond {
            like: raw.clone(),
            glob: like_pattern_to_glob(&raw),
            neg: false,
            escaped: false,
        },
        Op::NotLike => TextCond {
            like: raw.clone(),
            glob: like_pattern_to_glob(&raw),
            neg: true,
            escaped: false,
        },
        Op::Contains => TextCond {
            like: format!("%{e}%"),
            glob: format!("*{g}*"),
            neg: false,
            escaped: true,
        },
        Op::StartsWith => TextCond {
            like: format!("{e}%"),
            glob: format!("{g}*"),
            neg: false,
            escaped: true,
        },
        Op::EndsWith => TextCond {
            like: format!("%{e}"),
            glob: format!("*{g}"),
            neg: false,
            escaped: true,
        },
        _ => return Err(invalid_param("not a like-family op")),
    })
}

/// 渲染 LIKE 家族条件（behavior.md §13.5）：返回 SQL 片段与绑定值。
fn render_like(d: BrowseDialect, col: &str, tc: &TextCond, cs: Option<bool>) -> (String, Value) {
    let not = if tc.neg { "NOT " } else { "" };
    if d.flavor == BrowseFlavor::Sqlite && cs == Some(true) {
        return (format!("{col} {not}GLOB ?"), Value::String(tc.glob.clone()));
    }
    let esc_clause = if tc.escaped {
        format!(" ESCAPE '{LIKE_ESCAPE}'")
    } else {
        String::new()
    };
    let sql = match (d.flavor, cs) {
        (BrowseFlavor::Pg, Some(false)) => {
            let op = if tc.neg { "NOT ILIKE" } else { "ILIKE" };
            format!("{col} {op} ?{esc_clause}")
        }
        // SQLite 的 LIKE 对 ASCII 本就不区分，无需再套 LOWER。
        (BrowseFlavor::Sqlite, Some(false)) => format!("{col} {not}LIKE ?{esc_clause}"),
        (_, Some(false)) => format!("LOWER({col}) {not}LIKE LOWER(?){esc_clause}"),
        (BrowseFlavor::MySql, Some(true)) => {
            format!("CAST({col} AS BINARY) {not}LIKE CAST(? AS BINARY){esc_clause}")
        }
        (BrowseFlavor::MsSql, Some(true)) => {
            format!("{col} COLLATE Latin1_General_CS_AS {not}LIKE ?{esc_clause}")
        }
        (_, _) => format!("{col} {not}LIKE ?{esc_clause}"),
    };
    (sql, Value::String(tc.like.clone()))
}

/// 渲染 `regex` 条件；无原生正则的方言显式 NOT_SUPPORTED，绝不静默降级。
fn render_regex(
    d: BrowseDialect,
    col: &str,
    v: &Value,
    cs: Option<bool>,
) -> CoreResult<(String, Value)> {
    let raw = value_to_text(v).ok_or_else(|| invalid_param("op regex requires a scalar value"))?;
    let val = Value::String(raw);
    Ok(match (d.flavor, cs) {
        (BrowseFlavor::MsSql, _) => {
            return Err(not_supported(
                "op regex is not supported: MSSQL has no native regular expressions",
            ))
        }
        (BrowseFlavor::Pg, Some(false)) => (format!("{col} ~* ?"), val),
        (BrowseFlavor::Pg, _) => (format!("{col} ~ ?"), val),
        (BrowseFlavor::MySql, Some(true)) => (
            format!("CAST({col} AS BINARY) REGEXP CAST(? AS BINARY)"),
            val,
        ),
        (BrowseFlavor::MySql, Some(false)) => (format!("LOWER({col}) REGEXP LOWER(?)"), val),
        (BrowseFlavor::MySql, _) => (format!("{col} REGEXP ?"), val),
        (BrowseFlavor::Oracle, Some(false)) => (format!("REGEXP_LIKE({col}, ?, 'i')"), val),
        (BrowseFlavor::Oracle, _) => (format!("REGEXP_LIKE({col}, ?)"), val),
        (BrowseFlavor::Sqlite, Some(false)) => (format!("{col} REGEXP '(?i)' || ?"), val),
        (BrowseFlavor::Sqlite, _) => (format!("{col} REGEXP ?"), val),
    })
}

/// 渲染 WHERE 子句（不含 "WHERE" 前缀）与绑定参数。
pub fn build_rows_where(
    d: BrowseDialect,
    req: &TableRowsRequest,
) -> CoreResult<(String, Vec<Value>)> {
    use polydb_protocol::FilterOperator as Op;
    if req.conditions.is_empty() {
        return Ok((String::new(), Vec::new()));
    }
    let joiner = if req.logic == Some(polydb_protocol::FilterLogic::Or) {
        " OR "
    } else {
        " AND "
    };
    let mut sql = String::new();
    let mut args = Vec::new();
    for (i, cond) in req.conditions.iter().enumerate() {
        if i > 0 {
            sql.push_str(joiner);
        }
        let col = (d.quote)(&cond.column);
        match cond.op {
            Op::Unknown => {
                return Err(invalid_param("unknown filter op"));
            }
            Op::Null => sql.push_str(&format!("{col} IS NULL")),
            Op::NotNull => sql.push_str(&format!("{col} IS NOT NULL")),
            Op::Empty => sql.push_str(&format!("({col} IS NULL OR {col} = '')")),
            Op::In | Op::NotIn => {
                let op = if cond.op == Op::NotIn { "NOT IN" } else { "IN" };
                sql.push_str(&format!("{col} {op} ("));
                for (j, v) in cond.values.iter().enumerate() {
                    if j > 0 {
                        sql.push_str(", ");
                    }
                    sql.push('?');
                    args.push(v.clone());
                }
                sql.push(')');
            }
            Op::Between => {
                sql.push_str(&format!("{col} BETWEEN ? AND ?"));
                args.push(cond.value.clone());
                args.push(cond.second_value.clone());
            }
            Op::Eq | Op::Ne | Op::Lt | Op::Le | Op::Gt | Op::Ge => {
                let op = match cond.op {
                    Op::Eq => "=",
                    Op::Ne => "<>",
                    Op::Lt => "<",
                    Op::Le => "<=",
                    Op::Gt => ">",
                    _ => ">=",
                };
                sql.push_str(&format!("{col} {op} ?"));
                args.push(cond.value.clone());
            }
            Op::Regex => {
                let (s, v) = render_regex(d, &col, &cond.value, cond.case_sensitive)?;
                sql.push_str(&s);
                args.push(v);
            }
            Op::Like | Op::NotLike | Op::Contains | Op::StartsWith | Op::EndsWith => {
                let tc = text_cond(cond.op, &cond.value)?;
                let (s, v) = render_like(d, &col, &tc, cond.case_sensitive);
                sql.push_str(&s);
                args.push(v);
            }
        }
    }
    Ok((sql, args))
}

/// 构造浏览查询（limit+1 供 has_more 判定）与绑定参数。
pub fn build_rows_query(
    d: BrowseDialect,
    schema: &str,
    table: &str,
    req: &TableRowsRequest,
) -> CoreResult<(String, Vec<Value>)> {
    browse_rows_validate(req)?;
    let limit = browse_rows_limits(req.limit);
    let mut sql = String::from("SELECT ");
    if req.columns.is_empty() {
        sql.push('*');
    } else {
        let cols: Vec<String> = req.columns.iter().map(|c| (d.quote)(c)).collect();
        sql.push_str(&cols.join(", "));
    }
    sql.push_str(" FROM ");
    sql.push_str(&qualify_table(d, schema, table));
    let (where_sql, args) = build_rows_where(d, req)?;
    if !where_sql.is_empty() {
        sql.push_str(" WHERE ");
        sql.push_str(&where_sql);
    }
    let order_by = build_order_by(d, req);
    if order_by.is_empty() {
        if let Some(fb) = d.order_by_fallback {
            sql.push_str(" ORDER BY ");
            sql.push_str(fb);
        }
    } else {
        sql.push_str(&order_by);
    }
    sql.push_str(&(d.limit_clause)(req.offset, limit + 1));
    Ok((sql, args))
}

/// 构造同条件的 COUNT(*) 查询与绑定参数。
pub fn build_rows_count_query(
    d: BrowseDialect,
    schema: &str,
    table: &str,
    req: &TableRowsRequest,
) -> CoreResult<(String, Vec<Value>)> {
    browse_rows_validate(req)?;
    let mut sql = format!("SELECT COUNT(*) FROM {}", qualify_table(d, schema, table));
    let (where_sql, args) = build_rows_where(d, req)?;
    if !where_sql.is_empty() {
        sql.push_str(" WHERE ");
        sql.push_str(&where_sql);
    }
    Ok((sql, args))
}

fn build_order_by(d: BrowseDialect, req: &TableRowsRequest) -> String {
    if req.order_by.is_empty() {
        return String::new();
    }
    use polydb_protocol::SortDirection;
    let parts: Vec<String> = req
        .order_by
        .iter()
        .map(|o| {
            let dir = match o.dir {
                SortDirection::Asc => "asc",
                SortDirection::Desc => "desc",
            };
            format!("{} {}", (d.quote)(&o.column), dir)
        })
        .collect();
    format!(" ORDER BY {}", parts.join(", "))
}

fn qualify_table(d: BrowseDialect, schema: &str, table: &str) -> String {
    if schema.is_empty() {
        return (d.quote)(table);
    }
    format!("{}.{}", (d.quote)(schema), (d.quote)(table))
}

/// 把「按 limit+1 取回」的查询结果映射为浏览页：超出 limit 截断并置 has_more。
pub fn rows_result_to_browse_page(
    res: polydb_protocol::QueryResult,
    offset: u64,
    limit: u64,
) -> polydb_protocol::TableRowsResult {
    let mut rows = res.rows;
    let has_more = rows.len() as u64 > limit;
    if has_more {
        rows.truncate(limit as usize);
    }
    polydb_protocol::TableRowsResult {
        columns: res.columns,
        rows,
        offset,
        has_more,
        total_estimate: None,
        execution_time_ms: res.execution_time_ms,
    }
}

/// 从单行单列结果提取 COUNT(*)。
pub fn count_result_to_u64(res: polydb_protocol::QueryResult) -> CoreResult<u64> {
    let v = res
        .rows
        .into_iter()
        .next()
        .and_then(|mut r| {
            if r.is_empty() {
                None
            } else {
                Some(r.remove(0))
            }
        })
        .ok_or_else(|| CoreError::Driver("count query returned no rows".into()))?;
    match v {
        Value::Integer(i) if i >= 0 => Ok(i as u64),
        Value::Float(f) if f >= 0.0 => Ok(f as u64),
        Value::String(s) => s
            .trim()
            .parse::<u64>()
            .map_err(|_| CoreError::Driver("count query returned non-numeric value".into())),
        _ => Err(CoreError::Driver(
            "count query returned non-numeric value".into(),
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use polydb_core::{FilterCondition, FilterOperator as Op};

    fn s(t: &str) -> Value {
        Value::String(t.to_string())
    }

    fn cond(column: &str, op: Op, value: Value) -> FilterCondition {
        FilterCondition {
            column: column.to_string(),
            op,
            value,
            second_value: Value::Null,
            values: Vec::new(),
            case_sensitive: None,
        }
    }

    fn with_cs(mut c: FilterCondition, v: Option<bool>) -> FilterCondition {
        c.case_sensitive = v;
        c
    }

    fn where_of(d: BrowseDialect, c: FilterCondition) -> CoreResult<(String, Vec<Value>)> {
        build_rows_where(
            d,
            &TableRowsRequest {
                columns: Vec::new(),
                conditions: vec![c],
                logic: None,
                order_by: Vec::new(),
                offset: 0,
                limit: 0,
            },
        )
    }

    /// 断言 SQL 形态与唯一绑定值。
    fn one(d: BrowseDialect, c: FilterCondition, sql: &str, arg: Value) {
        let (got, args) = where_of(d, c).expect("render ok");
        assert_eq!(got, sql);
        assert_eq!(args.len(), 1);
        assert_eq!(args[0], arg);
    }

    #[test]
    fn escape_helpers() {
        assert_eq!(escape_like_text("a%b_c!d"), "a!%b!_c!!d");
        assert_eq!(like_pattern_to_glob("a%b_c*d?e[f"), "a*b?c\\*d\\?e\\[f");
        // 字面文本里的 % 对 GLOB 无语义，只有 * ? [ \ 要转义
        assert_eq!(glob_quote_text("50%*a"), "50%\\*a");
    }

    #[test]
    fn like_family_passthrough_keeps_legacy_shape() {
        one(
            DIALECT_DEFAULT,
            cond("name", Op::Like, s("%a_")),
            r#""name" LIKE ?"#,
            s("%a_"),
        );
        one(
            DIALECT_MSSQL,
            cond("name", Op::NotLike, s("%a_")),
            "[name] NOT LIKE ?",
            s("%a_"),
        );
    }

    #[test]
    fn wrapped_ops_escape_and_wrap() {
        one(
            DIALECT_DEFAULT,
            cond("x", Op::Contains, s("a%b")),
            r#""x" LIKE ? ESCAPE '!'"#,
            s("%a!%b%"),
        );
        one(
            DIALECT_MYSQL,
            cond("x", Op::StartsWith, s("a_b")),
            "`x` LIKE ? ESCAPE '!'",
            s("a!_b%"),
        );
        one(
            DIALECT_ORACLE,
            cond("x", Op::EndsWith, s("a!b")),
            r#""x" LIKE ? ESCAPE '!'"#,
            s("%a!!b"),
        );
    }

    #[test]
    fn case_insensitive_rendering() {
        one(
            DIALECT_DEFAULT,
            with_cs(cond("x", Op::Like, s("a")), Some(false)),
            r#""x" ILIKE ?"#,
            s("a"),
        );
        one(
            DIALECT_DEFAULT,
            with_cs(cond("x", Op::NotLike, s("a")), Some(false)),
            r#""x" NOT ILIKE ?"#,
            s("a"),
        );
        one(
            DIALECT_MYSQL,
            with_cs(cond("x", Op::Contains, s("a")), Some(false)),
            "LOWER(`x`) LIKE LOWER(?) ESCAPE '!'",
            s("%a%"),
        );
        one(
            DIALECT_MSSQL,
            with_cs(cond("x", Op::Like, s("a")), Some(false)),
            "LOWER([x]) LIKE LOWER(?)",
            s("a"),
        );
        one(
            DIALECT_ORACLE,
            with_cs(cond("x", Op::Like, s("a")), Some(false)),
            r#"LOWER("x") LIKE LOWER(?)"#,
            s("a"),
        );
        // SQLite 的 LIKE 本就不区分 ASCII：不套 LOWER
        one(
            DIALECT_SQLITE,
            with_cs(cond("x", Op::Like, s("a")), Some(false)),
            r#""x" LIKE ?"#,
            s("a"),
        );
    }

    #[test]
    fn case_sensitive_rendering() {
        one(
            DIALECT_DEFAULT,
            with_cs(cond("x", Op::Like, s("a")), Some(true)),
            r#""x" LIKE ?"#,
            s("a"),
        );
        one(
            DIALECT_MYSQL,
            with_cs(cond("x", Op::Contains, s("a")), Some(true)),
            "CAST(`x` AS BINARY) LIKE CAST(? AS BINARY) ESCAPE '!'",
            s("%a%"),
        );
        one(
            DIALECT_MSSQL,
            with_cs(cond("x", Op::NotLike, s("a")), Some(true)),
            "[x] COLLATE Latin1_General_CS_AS NOT LIKE ?",
            s("a"),
        );
        // SQLite 强制区分走 GLOB，模式按 GLOB 规则重转义
        one(
            DIALECT_SQLITE,
            with_cs(cond("x", Op::Contains, s("a*b")), Some(true)),
            r#""x" GLOB ?"#,
            s("*a\\*b*"),
        );
        one(
            DIALECT_SQLITE,
            with_cs(cond("x", Op::Like, s("a*b%")), Some(true)),
            r#""x" GLOB ?"#,
            s("a\\*b*"),
        );
    }

    #[test]
    fn empty_ignores_value() {
        let (sql, args) =
            where_of(DIALECT_DEFAULT, cond("x", Op::Empty, Value::Null)).expect("render ok");
        assert_eq!(sql, r#"("x" IS NULL OR "x" = '')"#);
        assert!(args.is_empty());
    }

    #[test]
    fn regex_per_flavor() {
        one(
            DIALECT_DEFAULT,
            cond("x", Op::Regex, s("^a")),
            r#""x" ~ ?"#,
            s("^a"),
        );
        one(
            DIALECT_DEFAULT,
            with_cs(cond("x", Op::Regex, s("^a")), Some(false)),
            r#""x" ~* ?"#,
            s("^a"),
        );
        one(
            DIALECT_MYSQL,
            cond("x", Op::Regex, s("^a")),
            "`x` REGEXP ?",
            s("^a"),
        );
        one(
            DIALECT_MYSQL,
            with_cs(cond("x", Op::Regex, s("^a")), Some(true)),
            "CAST(`x` AS BINARY) REGEXP CAST(? AS BINARY)",
            s("^a"),
        );
        one(
            DIALECT_ORACLE,
            with_cs(cond("x", Op::Regex, s("^a")), Some(false)),
            r#"REGEXP_LIKE("x", ?, 'i')"#,
            s("^a"),
        );
        one(
            DIALECT_SQLITE,
            cond("x", Op::Regex, s("^a")),
            r#""x" REGEXP ?"#,
            s("^a"),
        );
        one(
            DIALECT_SQLITE,
            with_cs(cond("x", Op::Regex, s("^a")), Some(false)),
            r#""x" REGEXP '(?i)' || ?"#,
            s("^a"),
        );
    }

    #[test]
    fn regex_on_mssql_is_not_supported() {
        let err = where_of(DIALECT_MSSQL, cond("x", Op::Regex, s("a"))).unwrap_err();
        assert!(
            err.to_string().contains("POLYDB_ERR_NOT_SUPPORTED"),
            "unexpected error: {err}"
        );
        assert_eq!(DIALECT_MSSQL.regex_support(), RegexSupport::Unsupported);
        assert_eq!(DIALECT_SQLITE.regex_support(), RegexSupport::Supported);
    }

    #[test]
    fn value_required_for_text_and_wrapped_ops() {
        for op in [
            Op::Contains,
            Op::StartsWith,
            Op::EndsWith,
            Op::Regex,
            Op::Like,
        ] {
            let err = where_of(DIALECT_DEFAULT, cond("x", op, Value::Null)).unwrap_err();
            assert!(
                err.to_string().contains("POLYDB_ERR_INVALID_PARAM"),
                "op {:?} unexpected error: {err}",
                op
            );
        }
    }

    #[test]
    fn non_scalar_value_rejected() {
        let err = where_of(
            DIALECT_DEFAULT,
            cond("x", Op::Contains, Value::Array(vec![Value::Integer(1)])),
        )
        .unwrap_err();
        assert!(
            err.to_string().contains("POLYDB_ERR_INVALID_PARAM"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn case_sensitive_ignored_by_non_text_ops() {
        one(
            DIALECT_MYSQL,
            with_cs(cond("n", Op::Eq, Value::Integer(7)), Some(true)),
            "`n` = ?",
            Value::Integer(7),
        );
        let mut inc = cond("n", Op::In, Value::Null);
        inc.values = vec![Value::Integer(1), Value::Integer(2)];
        let (sql, args) = where_of(DIALECT_MYSQL, with_cs(inc, Some(false))).expect("render ok");
        assert_eq!(sql, "`n` IN (?, ?)");
        assert_eq!(args, vec![Value::Integer(1), Value::Integer(2)]);
    }
}
