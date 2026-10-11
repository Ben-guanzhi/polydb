//! 执行计划：按方言包 EXPLAIN 语句 + 把结果解析成计划树（纯函数，无 IO）。
//!
//! SQLite 用 id/parent 行树，PostgreSQL / MySQL 解析 JSON 计划，Oracle 解析
//! DBMS_XPLAN 的 ASCII 表格；其余（含解析失败）退化为「列 | 行」表格文本。
//! Oracle 与其他方言的差异：需要两条语句（EXPLAIN PLAN 写入 session + DISPLAY
//! 取回），而 Go `pkg/explain`、Web `lib/explain.ts` 仍是单条 EXPLAIN PLAN，
//! 只回一行 "Statement explained."——见 explain_statements 的注释。

use serde_json::{Map, Value as Json};

use polydb_core::{DatabaseKind, QueryResult, Value};

/// 计划树递归上限：防御畸形/自引用输出把栈打爆。
const MAX_DEPTH: usize = 64;

#[derive(Debug, Clone, Default, PartialEq)]
pub(crate) struct Node {
    pub name: String,
    pub detail: String,
    pub cost: String,
    pub rows: String,
    pub children: Vec<Node>,
}

#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Render {
    Tree(Vec<Node>),
    Text(Vec<String>),
}

impl Render {
    /// 渲染文本：树模式展平为带缩进连接线的行，文本模式原样返回。
    pub(crate) fn lines(&self) -> Vec<String> {
        match self {
            Render::Text(t) if t.is_empty() => vec!["(空结果)".into()],
            Render::Text(t) => t.clone(),
            Render::Tree(nodes) if nodes.is_empty() => vec!["(空结果)".into()],
            Render::Tree(nodes) => {
                let mut out = Vec::new();
                for (i, n) in nodes.iter().enumerate() {
                    append_node(&mut out, n, "", i + 1 == nodes.len());
                }
                out
            }
        }
    }
}

fn append_node(dst: &mut Vec<String>, n: &Node, prefix: &str, last: bool) {
    let (connector, child_prefix) = if last {
        ("└─ ", format!("{prefix}   "))
    } else {
        ("├─ ", format!("{prefix}│  "))
    };
    dst.push(format!("{prefix}{connector}{}", node_label(n)));
    for (i, c) in n.children.iter().enumerate() {
        append_node(dst, c, &child_prefix, i + 1 == n.children.len());
    }
}

fn node_label(n: &Node) -> String {
    let mut meta: Vec<&str> = Vec::new();
    if !n.cost.is_empty() {
        meta.push(&n.cost);
    }
    if !n.rows.is_empty() {
        meta.push(&n.rows);
    }
    let mut s = n.name.clone();
    if !meta.is_empty() {
        s.push_str(&format!(" [{}]", meta.join(" · ")));
    }
    if !n.detail.is_empty() {
        s.push_str(&format!("  {}", n.detail));
    }
    s
}

/// 按方言给出需要依次执行的 EXPLAIN 语句（注释与尾分号先剥）。
/// 多数方言一条；Oracle 没有 EXPLAIN 语句，要先 EXPLAIN PLAN 把计划写进 session，
/// 再用 DBMS_XPLAN.DISPLAY 取回——DISPLAY 不带 plan_id/statement_id 时显示本
/// session 最近一次 EXPLAIN PLAN 的计划，所以顺序不能反。
/// 空输入返回空 vec；已经以 EXPLAIN 开头的原样返回。
pub(crate) fn explain_statements(raw: &str, kind: DatabaseKind) -> Vec<String> {
    let clean = strip_comments(raw)
        .trim_matches(|c| " \t\r\n;".contains(c))
        .to_string();
    if clean.is_empty() {
        return Vec::new();
    }
    if starts_with_explain(&clean) {
        return vec![clean];
    }
    match kind {
        DatabaseKind::Sqlite => vec![format!("EXPLAIN QUERY PLAN {clean}")],
        DatabaseKind::Postgres => vec![format!("EXPLAIN (FORMAT JSON) {clean}")],
        DatabaseKind::MySql => vec![format!("EXPLAIN FORMAT=JSON {clean}")],
        DatabaseKind::Oracle => vec![
            format!("EXPLAIN PLAN FOR {clean}"),
            "SELECT * FROM TABLE(DBMS_XPLAN.DISPLAY(NULL, NULL, 'TYPICAL'))".into(),
        ],
        _ => vec![format!("EXPLAIN {clean}")],
    }
}

fn starts_with_explain(s: &str) -> bool {
    let low = s.to_lowercase();
    match low.strip_prefix("explain") {
        Some(rest) => rest.is_empty() || rest.starts_with([' ', '\t', '\n', '\r']),
        None => false,
    }
}

/// 去掉 `--` 行注释与 `/* */` 块注释，字符串字面量内的内容保持原样。
fn strip_comments(s: &str) -> String {
    let b: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0usize;
    while i < b.len() {
        let c = b[i];
        if c == '\'' || c == '"' {
            let j = end_of_quoted(&b, i);
            out.extend(&b[i..j]);
            i = j;
        } else if c == '-' && b.get(i + 1) == Some(&'-') {
            match b[i..].iter().position(|&x| x == '\n') {
                // 换行本身保留，避免把两行语句粘成一行
                Some(nl) => i += nl,
                None => break,
            }
        } else if c == '/' && b.get(i + 1) == Some(&'*') {
            match find_pair(&b[i + 2..], '*', '/') {
                Some(end) => i += 2 + end + 2,
                None => break,
            }
        } else {
            out.push(c);
            i += 1;
        }
    }
    out
}

/// 在 `chars` 中找 `left` 紧跟 `right` 的位置，返回 `left` 的下标。
fn find_pair(chars: &[char], left: char, right: char) -> Option<usize> {
    let mut i = 0usize;
    while i + 1 < chars.len() {
        if chars[i] == left && chars[i + 1] == right {
            return Some(i);
        }
        i += 1;
    }
    None
}

/// 返回闭合引号之后的下标（未闭合则到串尾）。
fn end_of_quoted(b: &[char], start: usize) -> usize {
    let q = b[start];
    let mut i = start + 1;
    while i < b.len() {
        if b[i] == '\\' && q == '\'' {
            i += 2; // MySQL 反斜杠转义
            continue;
        }
        if b[i] == q {
            if b.get(i + 1) == Some(&q) {
                i += 2; // SQL 双写引号转义
                continue;
            }
            return i + 1;
        }
        i += 1;
    }
    b.len()
}

/// 把一次 EXPLAIN 查询的结果解析为计划树或表格文本。
pub(crate) fn render(res: &QueryResult, kind: DatabaseKind) -> Render {
    if res.rows.is_empty() {
        return Render::Text(vec!["(空结果)".into()]);
    }
    let tree = match kind {
        DatabaseKind::Sqlite => sqlite_tree(res),
        DatabaseKind::Postgres => pg_tree(&first_cell(res)),
        DatabaseKind::MySql => mysql_tree(&first_cell(res)),
        DatabaseKind::Oracle => oracle_tree(res),
        _ => None,
    };
    match tree {
        Some(nodes) if !nodes.is_empty() => Render::Tree(nodes),
        _ => Render::Text(table_text(res)),
    }
}

// ─── Oracle：EXPLAIN PLAN + DBMS_XPLAN.DISPLAY 文本 ─────────
//
// 回传的是单列 plan_table_output，整段计划是带换行的 ASCII 表格：列名行在
// 下划线分隔线之间，Operation 列的缩进（每层 2 空格）即树的深度，Id 列为空
// 的续行是 Predicate Information 之类的条件说明。

/// Oracle 算子说明：逐行转录自 PL/SQL Developer 17 随工具发行的 PlanDesc.cfg
/// （47 条，含原文拼写）。键归一化为大写并去掉空格与连字符，所以 DBMS_XPLAN
/// 的 `UNION ALL`（两个空格）能命中 `UNION-ALL`。
const PLAN_DESC: [(&str, &str); 47] = [
    ("ANDEQUAL", "Merge sorted lists returned by indexes"),
    ("BITMAPCONVERSIONCOUNT", "Return the number of ROWIDS"),
    (
        "BITMAPCONVERSIONFROMROWIDS",
        "Convert ROWIDS to bitmap represantations",
    ),
    (
        "BITMAPCONVERSIONTOROWIDS",
        "Convert bitmap represantations to ROWIDS",
    ),
    ("BITMAPINDEXFULLSCAN", "Scan the complete bitmap"),
    (
        "BITMAPINDEXRANGESCAN",
        "Scan the bitmap for a range of values",
    ),
    (
        "BITMAPINDEXSINGLEVALUE",
        "Scan the bitmap for a single value",
    ),
    ("BITMAPMERGE", "Merge multiple bitmaps into one bitmap"),
    (
        "BITMAPMINUS",
        "Subtract the bits from one bitmap from another",
    ),
    ("BITMAPOR", "Perform a bitwise OR operation on two bitmaps"),
    (
        "CONCATENATION",
        "Concatenate result sets without elimination of duplicates",
    ),
    ("CONNECTBY", "Recursively join a table to itself"),
    ("COUNT", "Implicit use of the ROWNUM pseudo column"),
    ("COUNTSTOPKEY", "Explicit use of the ROWNUM pseudo column"),
    (
        "FILTER",
        "Test WHERE clause condition without using an index",
    ),
    ("FIRSTROW", "Return only the first row of a query"),
    ("FORUPDATE", "Place a row-level lock"),
    (
        "HASHJOIN",
        "Join one in-memory table to another table using a hash key",
    ),
    (
        "HASHOUTERJOIN",
        "Join one in-memory table to another table using a hash key",
    ),
    ("INDEXFULLSCAN", "Select all values from an index"),
    (
        "INDEXRANGESCAN",
        "Select a range of values from an index in ascending order",
    ),
    (
        "INDEXRANGESCANDESCENDING",
        "Select a range of values from an index in descending order",
    ),
    (
        "INDEXUNIQUESCAN",
        "Select a unique value from a unique index",
    ),
    (
        "INLISTITERATORCONCATENATED",
        "Iterate over the operation below",
    ),
    (
        "INTERSECTION",
        "Merge result sets returned by queries for an INTERSECT clause",
    ),
    (
        "MERGEJOIN",
        "Join tables by merging sorted lists of records from each table",
    ),
    (
        "MERGEJOINCARTESIAN",
        "Join tables by merging sorted lists of records from each table",
    ),
    (
        "MERGEJOINOUTER",
        "Join tables by merging sorted lists of records from each table",
    ),
    (
        "MINUS",
        "Return rows from one query that are not present in a second query",
    ),
    (
        "NESTEDLOOPS",
        "Join two tables where at least one index is used",
    ),
    (
        "NESTEDLOOPOUTER",
        "Join two tables where at least one index is used",
    ),
    (
        "PROJECTION",
        "Return a single set of records from multiple queries",
    ),
    (
        "REMOTE",
        "Send an SQL statement to a remote database via a database link",
    ),
    ("SEQUENCE", "Access a NEXTVAL or CURRVAL of a sequence"),
    (
        "SORTAGGREGATE",
        "Sort a result set for an aggregation function (MIN, MAX, ...)",
    ),
    ("SORTGROUPBY", "Sort a result set for a GROUP BY clause"),
    (
        "SORTJOIN",
        "Sort a set of records for a MERGE JOIN operation",
    ),
    ("SORTORDERBY", "Sort a result set for an ORDER BY clause"),
    ("SORTUNIQUE", "Sort a result set and eliminate duplicates"),
    (
        "TABLEACCESSBYINDEXROWID",
        "Return a single row based on its ROWID",
    ),
    (
        "TABLEACCESSBYROWID",
        "Return a single row based on its ROWID",
    ),
    ("TABLEACCESSCLUSTER", "Return rows based on a cluster key"),
    ("TABLEACCESSFULL", "Return all rows from a table"),
    (
        "TABLEACCESSHASH",
        "Return a single row based on its hash key",
    ),
    ("UNION", "Concatenate result sets and elimnate duplicates"),
    (
        "UNIONALL",
        "Concatenate result sets without eliminating duplicates",
    ),
    ("VIEW", "Resolve view and return the results"),
];

/// 算子说明查表：大小写不敏感，忽略空格与连字符。
fn plan_desc(op: &str) -> Option<&'static str> {
    let key: String = op
        .chars()
        .filter(|c| !c.is_whitespace())
        .map(|c| c.to_ascii_uppercase())
        .collect();
    PLAN_DESC.iter().find(|(k, _)| *k == key).map(|(_, v)| *v)
}

fn oracle_tree(res: &QueryResult) -> Option<Vec<Node>> {
    // DBMS_XPLAN 输出是单列 plan_table_output，整段计划带换行；驱动可能整段
    // 塞在一格里，也可能已按行拆开，两种形状都按换行拼回来。
    let mut raw = String::new();
    for r in &res.rows {
        for v in r {
            raw.push_str(&cell_of(v));
            raw.push('\n');
        }
    }
    parse_oracle_plan(&raw)
}

/// 解析 DBMS_XPLAN 的 ASCII 表格为计划树。
fn parse_oracle_plan(raw: &str) -> Option<Vec<Node>> {
    let mut cols: Option<Vec<(usize, String)>> = None;
    let mut flat: Vec<(usize, Node)> = Vec::new();
    // 首行 Operation 列的前导空格作为基准：DBMS_XPLAN 的 `| ` 分隔符会占掉一格，
    // 而有的驱动已经剥掉了这一格，两种形状都要能算出同样的相对深度。
    let mut base: Option<usize> = None;

    for line in raw.lines() {
        let t = line.trim();
        if !t.starts_with('|') {
            continue;
        }
        let mut cells: Vec<&str> = t.split('|').skip(1).collect();
        if cells.last().map(|c| c.trim()) == Some("") {
            cells.pop();
        }
        let ops: Vec<&str> = cells.iter().map(|c| c.trim()).collect();

        // 列名行：记下各列下标，后面按名取值。
        if cols.is_none() && ops.iter().any(|c| c.eq_ignore_ascii_case("operation")) {
            cols = Some(
                ops.iter()
                    .enumerate()
                    .map(|(i, c)| (i, c.to_string()))
                    .collect(),
            );
            continue;
        }
        let Some(cols) = &cols else { continue };
        let col = |name: &str| -> Option<usize> {
            cols.iter()
                .find(|(_, n)| n.eq_ignore_ascii_case(name))
                .map(|(i, _)| *i)
        };
        let Some(i_op) = col("operation") else {
            continue;
        };
        if i_op >= ops.len() {
            continue;
        }

        // Id 列为空的续行：Predicate Information 的条件说明，挂到上一个节点。
        if ops.first().is_some_and(|s| s.is_empty()) {
            if let Some(last) = flat.last_mut() {
                if let Some(msg) = ops.get(1).copied().filter(|s| predicate_detail(s)) {
                    last.1.detail = join(&last.1.detail, msg);
                }
            }
            continue;
        }

        let lead = cells[i_op].len() - cells[i_op].trim_start().len();
        let base = *base.get_or_insert(lead);
        let depth = lead.saturating_sub(base) / 2;
        if depth >= MAX_DEPTH {
            continue;
        }
        let op = ops[i_op];
        let name = match col("options")
            .and_then(|i| ops.get(i))
            .filter(|s| !s.is_empty())
        {
            Some(o) => format!("{op} {o}"),
            None => op.to_string(),
        };
        let mut n = Node {
            name,
            ..Default::default()
        };
        if let Some(i) = col("name") {
            if let Some(s) = ops.get(i).filter(|s| !s.is_empty()) {
                n.detail = format!("on {s}");
            }
        }
        if let Some(i) = col("rows") {
            if let Some(s) = ops.get(i).filter(|s| !s.is_empty()) {
                n.rows = format!("rows≈{s}");
            }
        }
        if let Some(i) = col("cost") {
            if let Some(s) = ops.get(i).filter(|s| !s.is_empty()) {
                n.cost = format!("cost={s}");
            }
        }
        if let Some(d) = plan_desc(op) {
            n.detail = join(&n.detail, d);
        }

        flat.push((depth, n));
    }

    let roots = build_oracle_tree(&mut flat);
    if roots.is_empty() {
        None
    } else {
        Some(roots)
    }
}

/// 把「深度 + 节点」的扁平序列还原成树。
fn build_oracle_tree(flat: &mut [(usize, Node)]) -> Vec<Node> {
    let mut roots = Vec::new();
    let mut cur = 0;
    while cur < flat.len() {
        let d = flat[cur].0;
        roots.push(take_subtree(flat, &mut cur, d));
    }
    roots
}

/// 从游标处取走 depth == d 的那棵子树（含其后代）。
fn take_subtree(flat: &mut [(usize, Node)], cur: &mut usize, d: usize) -> Node {
    let pos = *cur;
    let mut node = std::mem::take(&mut flat[pos].1);
    *cur += 1;
    let mut kids = Vec::new();
    while *cur < flat.len() && flat[*cur].0 > d {
        let child_depth = flat[*cur].0;
        kids.push(take_subtree(flat, cur, child_depth));
    }
    node.children = kids;
    node
}

/// 判断是否为 `5 - filter(...)` 形式的条件说明（区别于标题行与分隔线）。
fn predicate_detail(s: &str) -> bool {
    let Some(dash) = s.find('-') else {
        return false;
    };
    let (num, rest) = (&s[..dash], s[dash + 1..].trim_start());
    !num.trim().is_empty()
        && num.trim().chars().all(|c| c.is_ascii_digit())
        && !rest.is_empty()
        && !rest.starts_with('-')
}

// ─── SQLite：id / parent / detail 行树 ───────────────────────
//
// 第 4 列 notused 实测在多条计划间取值漂移（3.53：216 / 56），是内部标志位而非
// 计划信息，故与 Go 端一致地有意省略。

fn sqlite_tree(res: &QueryResult) -> Option<Vec<Node>> {
    let idx = column_indexes(res);
    let i_id = *idx.get("id")?;
    let i_par = *idx.get("parent")?;
    let i_det = *idx.get("detail")?;

    let mut ids = Vec::with_capacity(res.rows.len());
    let mut parents = Vec::with_capacity(res.rows.len());
    let mut nodes = Vec::with_capacity(res.rows.len());
    for r in &res.rows {
        ids.push(cell_int(r, i_id));
        parents.push(cell_int(r, i_par));
        nodes.push(Node {
            name: format!("Step {}", ids[ids.len() - 1]),
            detail: cell_text(r, i_det),
            ..Default::default()
        });
    }
    Some(assemble_tree(&ids, &parents, nodes))
}

/// 按 id/parent 组树：parent 等于自身或指向不存在的 id 即视为根。
fn assemble_tree(ids: &[i64], parents: &[i64], nodes: Vec<Node>) -> Vec<Node> {
    let pos_of: std::collections::HashMap<i64, usize> = ids
        .iter()
        .copied()
        .enumerate()
        .map(|(p, id)| (id, p))
        .collect();
    let mut kids: Vec<Vec<usize>> = vec![Vec::new(); nodes.len()];
    let mut roots: Vec<usize> = Vec::new();
    for pos in 0..nodes.len() {
        let parent = parents[pos];
        if parent == ids[pos] {
            roots.push(pos);
            continue;
        }
        match pos_of.get(&parent) {
            Some(&p) => kids[p].push(pos),
            None => roots.push(pos),
        }
    }
    let mut out = Vec::with_capacity(roots.len());
    let mut emitted: Vec<usize> = Vec::with_capacity(nodes.len());
    for r in &roots {
        out.push(build_node(
            *r,
            &nodes,
            &kids,
            0,
            &mut Vec::new(),
            &mut emitted,
        ));
    }
    // 环（parent 互指）中的节点没有任何根可达：补为顶层，避免计划行凭空消失。
    for pos in 0..nodes.len() {
        if !emitted.contains(&pos) {
            out.push(build_node(
                pos,
                &nodes,
                &kids,
                0,
                &mut Vec::new(),
                &mut emitted,
            ));
        }
    }
    out
}

fn build_node(
    pos: usize,
    nodes: &[Node],
    kids: &[Vec<usize>],
    depth: usize,
    on_path: &mut Vec<usize>,
    emitted: &mut Vec<usize>,
) -> Node {
    let mut n = nodes[pos].clone();
    emitted.push(pos);
    if depth >= MAX_DEPTH || on_path.contains(&pos) {
        n.detail.push_str(" …");
        return n;
    }
    on_path.push(pos);
    for c in &kids[pos] {
        n.children
            .push(build_node(*c, nodes, kids, depth + 1, on_path, emitted));
    }
    on_path.retain(|&x| x != pos);
    n
}

// ─── PostgreSQL：EXPLAIN (FORMAT JSON) ───────────────────────

fn pg_tree(raw: &str) -> Option<Vec<Node>> {
    let parsed: Json = serde_json::from_str(raw).ok()?;
    let items: Vec<&Map<String, Json>> = match &parsed {
        Json::Array(arr) => arr.iter().filter_map(Json::as_object).collect(),
        Json::Object(o) => vec![o],
        _ => return None,
    };
    let mut nodes = Vec::with_capacity(items.len());
    for item in items {
        let plan = match item.get("Plan").and_then(Json::as_object) {
            Some(p) => p,
            None => item,
        };
        nodes.push(pg_node(plan, 0)?);
    }
    Some(nodes)
}

fn pg_node(plan: &Map<String, Json>, depth: usize) -> Option<Node> {
    if depth >= MAX_DEPTH || plan.is_empty() {
        return None;
    }
    let mut name = sget(plan, &["Node Type", "NodeType", "Operation"]);
    if name.is_empty() {
        name = sget(
            plan,
            &[
                "Relation Name",
                "RelationName",
                "Index Name",
                "Function Name",
            ],
        );
    }
    if name.is_empty() {
        return None;
    }
    let mut n = Node {
        name,
        ..Default::default()
    };
    let rel = sget(plan, &["Relation Name", "RelationName"]);
    if !rel.is_empty() && rel != n.name {
        n.detail = format!("on {rel}");
    }
    let idx = sget(plan, &["Index Name", "IndexName"]);
    if !idx.is_empty() {
        n.detail = join(&n.detail, &format!("using {idx}"));
    }
    let filter = sget(plan, &["Filter", "Join Cond"]);
    if !filter.is_empty() {
        n.detail = join(&n.detail, &filter);
    }
    if let Some(c) = fget(plan, &["Total Cost", "TotalCost"]) {
        n.cost = format!("cost={c:.2}");
    }
    if let Some(r) = fget(plan, &["Plan Rows", "PlanRows"]) {
        n.rows = format!("rows≈{}", r as i64);
    }
    for sub in mlist(plan, "Plans") {
        if let Some(c) = pg_node(&sub, depth + 1) {
            n.children.push(c);
        }
    }
    Some(n)
}

// ─── MySQL：EXPLAIN FORMAT=JSON ──────────────────────────────

fn mysql_tree(raw: &str) -> Option<Vec<Node>> {
    let parsed: Json = serde_json::from_str(raw).ok()?;
    let doc = parsed.as_object()?;
    let qb = doc.get("query_block").and_then(Json::as_object)?;
    let n = mysql_block(qb, 0)?;
    Some(vec![n])
}

fn mysql_block(b: &Map<String, Json>, depth: usize) -> Option<Node> {
    if depth >= MAX_DEPTH || b.is_empty() {
        return None;
    }
    if let Some(t) = b.get("table").and_then(Json::as_object) {
        let mut n = mysql_table(t, depth)?;
        attach_cost(&mut n, b);
        return Some(n);
    }
    let steps = mlist(b, "nested_loop");
    if !steps.is_empty() {
        let mut n = Node {
            name: "Nested loop".into(),
            ..Default::default()
        };
        for s in &steps {
            if let Some(c) = mysql_block(s, depth + 1) {
                n.children.push(c);
            }
        }
        if n.children.is_empty() {
            return None;
        }
        attach_cost(&mut n, b);
        return Some(n);
    }
    for (key, wrapper) in [
        ("ordering_operation", "Sort"),
        ("grouping_operation", "Group"),
        ("windowing", "Window"),
        ("dml_table", "DML"),
    ] {
        let Some(inner) = b.get(key).and_then(Json::as_object) else {
            continue;
        };
        let n = mysql_block(inner, depth + 1)?;
        if n.name == wrapper {
            return Some(n);
        }
        let filesort = matches!(inner.get("using_filesort"), Some(Json::Bool(true)));
        let mut out = Node {
            name: wrapper.into(),
            detail: if filesort {
                "filesort".into()
            } else {
                String::new()
            },
            children: vec![n],
            ..Default::default()
        };
        attach_cost(&mut out, b);
        return Some(out);
    }
    mysql_subquery(b, depth)
}

fn mysql_table(t: &Map<String, Json>, depth: usize) -> Option<Node> {
    let mut name = sget(t, &["table_name", "subquery_name", "reference"]);
    if name.is_empty() {
        name = sget(t, &["message"]);
    }
    if name.is_empty() {
        return None;
    }
    let mut n = Node {
        name,
        ..Default::default()
    };
    let acc = sget(t, &["access_type", "type"]);
    if !acc.is_empty() {
        n.detail = acc;
    }
    let key = sget(t, &["key"]);
    if !key.is_empty() {
        n.detail = join(&n.detail, &format!("key={key}"));
    }
    let cond = sget(t, &["attached_condition"]);
    if !cond.is_empty() {
        n.detail = join(&n.detail, &cond);
    }
    if let Some(r) = fget(t, &["rows_examined_per_scan", "rows"]) {
        n.rows = format!("rows≈{}", r as i64);
    }
    if let Some(subs) = t.get("subqueries") {
        for s in any_list(subs) {
            let Some(m) = s.as_object() else {
                continue;
            };
            if let Some(c) = mysql_block(m, depth + 1) {
                n.children.push(c);
            }
        }
    }
    attach_cost(&mut n, t);
    Some(n)
}

fn mysql_subquery(b: &Map<String, Json>, depth: usize) -> Option<Node> {
    for key in ["select", "subquery", "materialized_subquery"] {
        let Some(inner) = b.get(key).and_then(Json::as_object) else {
            continue;
        };
        if let Some(n) = mysql_block(inner, depth + 1) {
            return Some(n);
        }
    }
    None
}

fn attach_cost(n: &mut Node, b: &Map<String, Json>) {
    if !n.cost.is_empty() {
        return;
    }
    let Some(ci) = b.get("cost_info").and_then(Json::as_object) else {
        return;
    };
    let c = sget(ci, &["query_cost", "total_cost"]);
    if !c.is_empty() {
        n.cost = format!("cost={c}");
    }
}

// ─── 通用兜底：列 | 行 表格文本 ──────────────────────────────

fn table_text(res: &QueryResult) -> Vec<String> {
    let names: Vec<&str> = res.columns.iter().map(|c| c.name.as_str()).collect();
    let mut lines = vec![names.join(" | ")];
    for r in &res.rows {
        let cells: Vec<String> = (0..r.len()).map(|i| cell_text(r, i)).collect();
        lines.push(cells.join(" | "));
    }
    lines
}

// ─── 协议值 / JSON 取值辅助 ─────────────────────────────────

fn column_indexes(res: &QueryResult) -> std::collections::HashMap<String, usize> {
    let mut m = std::collections::HashMap::new();
    for (i, c) in res.columns.iter().enumerate() {
        m.entry(c.name.to_lowercase()).or_insert(i);
    }
    m
}

fn first_cell(res: &QueryResult) -> String {
    res.rows
        .first()
        .and_then(|r| r.first())
        .map(cell_of)
        .unwrap_or_default()
}

fn cell_text(row: &[Value], i: usize) -> String {
    row.get(i).map(cell_of).unwrap_or_default()
}

/// 协议值 → 文本（字符串去引号，NULL → 空串）。
fn cell_of(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::String(s) => s.clone(),
        other => crate::util::value_str(other),
    }
}

fn cell_int(row: &[Value], i: usize) -> i64 {
    match row.get(i) {
        Some(Value::Integer(n)) => *n,
        Some(Value::Float(f)) => *f as i64,
        _ => cell_text(row, i).trim().parse().unwrap_or(0),
    }
}

fn join(a: &str, b: &str) -> String {
    format!("{a} {b}").trim().to_string()
}

/// 返回首个非空字符串字段。
fn sget(m: &Map<String, Json>, keys: &[&str]) -> String {
    for k in keys {
        if let Some(s) = m.get(*k).and_then(Json::as_str) {
            let t = s.trim();
            if !t.is_empty() {
                return t.to_string();
            }
        }
    }
    String::new()
}

/// 返回首个数值字段（JSON 数字或数字字符串）。
fn fget(m: &Map<String, Json>, keys: &[&str]) -> Option<f64> {
    for k in keys {
        match m.get(*k) {
            Some(Json::Number(n)) => {
                if let Some(f) = n.as_f64() {
                    return Some(f);
                }
            }
            Some(Json::String(s)) => {
                if let Ok(f) = s.trim().parse::<f64>() {
                    return Some(f);
                }
            }
            _ => {}
        }
    }
    None
}

/// 取对象数组字段（单个对象也算一项）。
fn mlist(m: &Map<String, Json>, key: &str) -> Vec<Map<String, Json>> {
    match m.get(key) {
        Some(Json::Array(arr)) => arr.iter().filter_map(Json::as_object).cloned().collect(),
        Some(Json::Object(o)) => vec![o.clone()],
        _ => Vec::new(),
    }
}

fn any_list(v: &Json) -> Vec<&Json> {
    match v {
        Json::Array(arr) => arr.iter().collect(),
        Json::Object(_) => vec![v],
        _ => Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use polydb_core::{DatabaseKind::*, ResultColumn, Value};

    fn res(cols: &[&str], rows: Vec<Vec<Value>>) -> QueryResult {
        QueryResult {
            columns: cols
                .iter()
                .map(|n| ResultColumn {
                    name: n.to_string(),
                    table: None,
                    data_type: "TEXT".into(),
                    generic_type: None,
                    nullable: None,
                })
                .collect(),
            rows,
            affected_rows: 0,
            execution_time_ms: 0.0,
            truncated: false,
            total_rows: None,
            has_more: false,
            statement_type: None,
        }
    }

    #[test]
    fn explain_statements_wraps_per_dialect() {
        assert!(explain_statements("  ", Sqlite).is_empty());
        assert_eq!(
            explain_statements("select 1;", Sqlite),
            vec!["EXPLAIN QUERY PLAN select 1".to_string()]
        );
        assert_eq!(
            explain_statements("SELECT 1", Postgres),
            vec!["EXPLAIN (FORMAT JSON) SELECT 1".to_string()]
        );
        assert_eq!(
            explain_statements("SELECT 1", MySql),
            vec!["EXPLAIN FORMAT=JSON SELECT 1".to_string()]
        );
        assert_eq!(
            explain_statements("SELECT 1", Mssql),
            vec!["EXPLAIN SELECT 1".to_string()]
        );
        // 已经带 EXPLAIN 的不重复包裹；大小写与前缀词都要认。
        assert_eq!(
            explain_statements("explain select 1", Sqlite),
            vec!["explain select 1".to_string()]
        );
        assert_eq!(
            explain_statements("EXPLAINX", Sqlite),
            vec!["EXPLAIN QUERY PLAN EXPLAINX".to_string()]
        );
        // Oracle 两条：先写进 session，再用 DISPLAY 取回，顺序不能反。
        let o = explain_statements("SELECT * FROM t", Oracle);
        assert_eq!(o.len(), 2);
        assert_eq!(o[0], "EXPLAIN PLAN FOR SELECT * FROM t");
        assert_eq!(
            o[1],
            "SELECT * FROM TABLE(DBMS_XPLAN.DISPLAY(NULL, NULL, 'TYPICAL'))"
        );
    }

    #[test]
    fn explain_statements_strips_comments_keeping_literals() {
        let sql = "-- 注释\nSELECT '--不是注释' /* 块 */ FROM t;\n";
        assert_eq!(
            explain_statements(sql, Sqlite),
            vec!["EXPLAIN QUERY PLAN SELECT '--不是注释'  FROM t".to_string()]
        );
    }

    #[test]
    fn sqlite_rows_become_tree() {
        let r = res(
            &["id", "parent", "notused", "detail"],
            vec![
                vec![
                    Value::Integer(1),
                    Value::Integer(0),
                    Value::Integer(216),
                    Value::String("SCAN t".into()),
                ],
                vec![
                    Value::Integer(2),
                    Value::Integer(1),
                    Value::Integer(56),
                    Value::String("USE INTEGER INDEX i".into()),
                ],
            ],
        );
        let lines = render(&r, Sqlite).lines();
        assert_eq!(lines[0], "└─ Step 1  SCAN t");
        assert_eq!(lines[1], "   └─ Step 2  USE INTEGER INDEX i");
    }

    #[test]
    fn sqlite_cycle_and_self_parent_survive() {
        // parent 互指：无根可达，补为顶层；自指：视为根。
        let r = res(
            &["id", "parent", "detail"],
            vec![
                vec![
                    Value::Integer(1),
                    Value::Integer(2),
                    Value::String("A".into()),
                ],
                vec![
                    Value::Integer(2),
                    Value::Integer(1),
                    Value::String("B".into()),
                ],
                vec![
                    Value::Integer(3),
                    Value::Integer(3),
                    Value::String("C".into()),
                ],
            ],
        );
        let lines = render(&r, Sqlite).lines();
        assert_eq!(
            lines,
            vec![
                "├─ Step 3  C",
                "└─ Step 1  A",
                "   └─ Step 2  B",
                "      └─ Step 1  A …",
            ]
        );
    }

    #[test]
    fn pg_json_tree() {
        let raw = r#"[{"Plan":{"Node Type":"Aggregate","Total Cost":12.5,"Plan Rows":3,
            "Plans":[{"Node Type":"Seq Scan","Relation Name":"t","Filter":"(a > 1)","Total Cost":2.0,"Plan Rows":3}]}}]"#;
        let r = res(&["QUERY PLAN"], vec![vec![Value::String(raw.into())]]);
        let lines = render(&r, Postgres).lines();
        assert_eq!(lines[0], "└─ Aggregate [cost=12.50 · rows≈3]");
        assert!(lines[1].contains("Seq Scan [cost=2.00 · rows≈3]  on t (a > 1)"));
    }

    #[test]
    fn mysql_json_tree_wraps_sort() {
        let raw = r#"{"query_block":{"cost_info":{"query_cost":"4.60"},"ordering_operation":{
            "using_filesort":true,"table":{"table_name":"t","access_type":"ALL",
            "rows_examined_per_scan":4,"cost_info":{"query_cost":"4.60"}}}}}"#;
        let r = res(&["EXPLAIN"], vec![vec![Value::String(raw.into())]]);
        let lines = render(&r, MySql).lines();
        assert_eq!(lines[0], "└─ Sort [cost=4.60]  filesort");
        assert!(lines[1].contains("t [cost=4.60 · rows≈4]  ALL"));
    }

    #[test]
    fn unparseable_falls_back_to_table_text() {
        // PG 但首格不是 JSON、Oracle 但没有列名行 → 表格文本；MSSQL 一律表格文本。
        let r = res(
            &["id", "parent", "detail"],
            vec![vec![
                Value::Integer(1),
                Value::Integer(0),
                Value::String("SCAN".into()),
            ]],
        );
        assert_eq!(
            render(&r, Postgres).lines(),
            vec!["id | parent | detail", "1 | 0 | SCAN"]
        );
        assert_eq!(render(&r, Mssql).lines()[1], "1 | 0 | SCAN");
        let empty = res(&["a"], vec![]);
        assert_eq!(render(&empty, Sqlite).lines(), vec!["(空结果)"]);
    }

    #[test]
    fn plan_desc_normalizes_options_and_case() {
        assert!(plan_desc("TABLE ACCESS FULL").is_some());
        assert!(plan_desc("table access full").is_some());
        assert!(plan_desc("Index Range Scan Descending").is_some());
        // cfg 里写的是 UNION-ALL，DBMS_XPLAN 输出是 UNION ALL（两个空格）。
        assert!(plan_desc("UNION ALL").is_some());
        assert!(plan_desc("SELECT STATEMENT").is_none());
    }

    #[test]
    fn oracle_plan_becomes_a_tree() {
        let raw = "Plan hash value: 1857779742\n\
                   -----------------------------------------------------------\n\
                   | Id  | Operation                     | Name   | Rows | Cost |\n\
                   -----------------------------------------------------------\n\
                    |   0 | SELECT STATEMENT              |        |    1 |    2 |\n\
                    |   1 |   NESTED LOOPS                      |        |    1 |    2 |\n\
                    |   2 |     TABLE ACCESS FULL               | T1     |    1 |    2 |\n\
                    |   3 |     TABLE ACCESS BY INDEX ROWID     | T2     |    1 |    1 |\n\
                    |   4 |       INDEX RANGE SCAN               | IDX_T2 |    1 |    1 |\n\
                   -----------------------------------------------------------\n\
                   Plan hash value: 1857779742\n";
        let r = res(
            &["plan_table_output"],
            vec![vec![Value::String(raw.into())]],
        );
        let lines = render(&r, Oracle).lines();
        assert_eq!(lines[0], "└─ SELECT STATEMENT [cost=2 · rows≈1]");
        assert!(
            lines[1].contains("NESTED LOOPS [cost=2 · rows≈1]"),
            "{}",
            lines[1]
        );
        assert!(
            lines[1].contains("Join two tables where at least one index is used"),
            "{}",
            lines[1]
        );
        assert!(lines[2].contains("on T1"), "{}", lines[2]);
        assert!(
            lines[2].contains("Return all rows from a table"),
            "{}",
            lines[2]
        );
        // 索引扫描是回表的子节点而不是兄弟：正好多缩进一级（3 空格）。
        let lead = |l: &str| l.len() - l.trim_start().len();
        let at = |s: &str| lines.iter().position(|l| l.contains(s)).unwrap();
        let (row_tab, row_idx) = (at("TABLE ACCESS BY INDEX ROWID"), at("INDEX RANGE SCAN"));
        assert_eq!(
            lead(&lines[row_idx]),
            lead(&lines[row_tab]) + 3,
            "{}",
            lines[row_idx]
        );
        assert!(lines[row_idx].contains("IDX_T2"), "{}", lines[row_idx]);
    }

    #[test]
    fn oracle_plan_predicates_attach_to_the_node() {
        let raw = "-----------------------------------------------------------\n\
                   | Id  | Operation           | Name | Rows | Cost |\n\
                   -----------------------------------------------------------\n\
                    |   0 | SELECT STATEMENT    |      |    1 |    2 |\n\
                    |   1 |   TABLE ACCESS FULL  | T1   |    1 |    2 |\n\
                    |     |   Predicate Information (identified by access path):\n\
                    |     |   --------------------------------------------------\n\
                    |     |   1 - filter(\"X\">10)\n\
                   -----------------------------------------------------------\n";
        let nodes = parse_oracle_plan(raw).expect("plan tree");
        let leaf = &nodes[0].children[0];
        assert_eq!(leaf.name, "TABLE ACCESS FULL");
        assert!(leaf.detail.contains("filter(\"X\">10)"), "{}", leaf.detail);
        // 标题行与分隔线不算条件说明。
        assert!(
            !leaf.detail.contains("Predicate Information"),
            "{}",
            leaf.detail
        );
        assert!(!leaf.detail.contains("----"), "{}", leaf.detail);
    }

    #[test]
    fn oracle_plan_without_a_header_falls_back_to_table_text() {
        // EXPLAIN PLAN 本身只回一行 "Statement explained."，没有列名行。
        let r = res(
            &["plan_table_output"],
            vec![vec![Value::String("Statement explained.".into())]],
        );
        assert_eq!(render(&r, Oracle).lines()[1], "Statement explained.");
        assert!(parse_oracle_plan("Statement explained.").is_none());
    }
}
