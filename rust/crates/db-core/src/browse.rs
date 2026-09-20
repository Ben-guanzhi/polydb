//! 表数据浏览的共享查询构造器（behavior.md §13）：与 Go 侧 `dbcore/browse.go` 逐条对应。
//! 条件值一律走绑定参数，标识符由各方言的 quote 函数引用（内部引号翻倍）。

use polydb_core::{CoreError, CoreResult, TableRowsRequest, Value};

/// 浏览查询需要的方言差异。
#[derive(Clone, Copy)]
pub struct BrowseDialect {
    pub quote: fn(&str) -> String,
    pub limit_clause: fn(u64, u64) -> String,
    /// 分页子句要求 ORDER BY 且请求未带排序时使用（MSSQL）；None 表示无需兜底。
    pub order_by_fallback: Option<&'static str>,
}

/// ANSI 双引号标识符 + LIMIT/OFFSET（sqlite / postgres）。
pub const DIALECT_DEFAULT: BrowseDialect = BrowseDialect {
    quote: quote_double,
    limit_clause: limit_offset_clause,
    order_by_fallback: None,
};

/// 反引号标识符 + LIMIT/OFFSET（mysql）。
pub const DIALECT_MYSQL: BrowseDialect = BrowseDialect {
    quote: quote_backtick,
    limit_clause: limit_offset_clause,
    order_by_fallback: None,
};

/// 方括号标识符 + OFFSET/FETCH + ORDER BY 兜底（mssql）。
pub const DIALECT_MSSQL: BrowseDialect = BrowseDialect {
    quote: quote_bracket,
    limit_clause: offset_fetch_clause,
    order_by_fallback: Some("(SELECT NULL)"),
};

/// 双引号标识符 + OFFSET/FETCH（oracle 12c+）。
pub const DIALECT_ORACLE: BrowseDialect = BrowseDialect {
    quote: quote_double,
    limit_clause: offset_fetch_clause,
    order_by_fallback: None,
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

/// 分页钳制（§13.2）：limit 缺省 200，上限 10000。
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
            Op::Eq | Op::Ne | Op::Lt | Op::Le | Op::Gt | Op::Ge | Op::Like | Op::NotLike => {
                if value_is_null(&cond.value) {
                    return Err(invalid_param(format!(
                        "condition {i}: op {} requires value",
                        cond.op.as_str()
                    )));
                }
            }
            Op::Null | Op::NotNull => {}
        }
    }
    Ok(())
}

fn value_is_null(v: &Value) -> bool {
    matches!(v, Value::Null)
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
            Op::Eq | Op::Ne | Op::Lt | Op::Le | Op::Gt | Op::Ge | Op::Like | Op::NotLike => {
                let op = match cond.op {
                    Op::Eq => "=",
                    Op::Ne => "<>",
                    Op::Lt => "<",
                    Op::Le => "<=",
                    Op::Gt => ">",
                    Op::Ge => ">=",
                    Op::Like => "LIKE",
                    _ => "NOT LIKE",
                };
                sql.push_str(&format!("{col} {op} ?"));
                args.push(cond.value.clone());
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
