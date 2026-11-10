//! G3 变更跟踪编辑：单元格编辑先在本地排队，Review 后在单个事务内提交、逐条校验影响行数。
//!
//! 语义以 Web `web/src/lib/changes.ts` 为基准（独立实现，不 port 代码）。
//! 契约依据：spec/behavior.md §10（事务单次 finalize）。

use polydb_core::{DatabaseKind, Value};
use std::collections::HashMap;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RowChangeType {
    Update,
    Insert,
    Delete,
}

impl RowChangeType {
    fn as_str(self) -> &'static str {
        match self {
            RowChangeType::Update => "update",
            RowChangeType::Insert => "insert",
            RowChangeType::Delete => "delete",
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct CellChange {
    pub column: String,
    pub old_value: Value,
    pub new_value: Value,
}

/// 整行快照：保留列顺序（INSERT 的列序 load-bearing）。
pub(crate) type Row = Vec<(String, Value)>;

pub(crate) fn row_get(row: &Row, col: &str) -> Value {
    row.iter()
        .find(|(k, _)| k == col)
        .map(|(_, v)| v.clone())
        .unwrap_or(Value::Null)
}

fn row_set(row: &mut Row, col: &str, v: Value) {
    match row.iter_mut().find(|(k, _)| k == col) {
        Some(slot) => slot.1 = v,
        None => row.push((col.to_string(), v)),
    }
}

#[derive(Debug, Clone)]
pub(crate) struct RowChange {
    pub row_id: String,
    pub rtype: RowChangeType,
    pub cell_changes: Vec<CellChange>,
    pub original_row: Row,
    pub new_row: Option<Row>,
    pub sequence: i64,
    /// 查找键（row_id + rtype）：入队时一次算好，索引按它寻址，
    /// 不再每次查找都重新格式化字符串。
    pub(crate) key: String,
}

#[derive(Debug, Clone)]
enum UndoOp {
    Cell { key: String, column: String },
    Row { key: String },
}

fn key_of(row_id: &str, rtype: RowChangeType) -> String {
    format!("{}\u{0}{}", row_id, rtype.as_str())
}

/// 数值跨整型/浮点比较，避免用户回编辑把 `5`（Integer）判成与 `5.0`（Float）不等。
///
/// Integer↔Float 不能走 `as f64`：64 位整数只有 53 位尾数，两个不同的
/// 大于 2^53 的整数会塌成同一个 double 而被误判相等，编辑被当 no-op 静默丢弃。
/// 整数值经 i128 比较，浮点侧只接受"整数值且落在 i64 范围内"的有限数。
pub(crate) fn values_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Integer(i), Value::Float(f)) | (Value::Float(f), Value::Integer(i)) => {
            int_float_equal(*i, *f)
        }
        _ => a == b,
    }
}

fn int_float_equal(i: i64, f: f64) -> bool {
    if !f.is_finite() || f.fract() != 0.0 {
        return false;
    }
    // 整数值的 float 在 i64 范围内经 i128 转换是精确的；超出范围则整数不可能相等。
    if f < i64::MIN as f64 || f > i64::MAX as f64 {
        return false;
    }
    f as i128 == i as i128
}

/// 每表 tab 一个队列：同格重复编辑合并（保留最初旧值）、改回原值自动塌缩。
#[derive(Debug, Default)]
pub(crate) struct ChangeQueue {
    changes: Vec<RowChange>,
    /// key → changes 下标。查找（find/get/undo）从 O(n) 线性扫变 O(1)；
    /// 追加（高频）走增量登记，删除（undo/塌缩，低频）后整表重建。
    index: HashMap<String, usize>,
    undo_stack: Vec<UndoOp>,
    seq: i64,
}

/// undo 深度上限（vim 侧 MAX_UNDO=100 同款）：每次格编辑都入栈，长会话
/// 无上界会滞留整个会话的编辑史；超出即淘汰最旧一条。
const MAX_UNDO_OPS: usize = 100;

impl ChangeQueue {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    /// undo 压栈（带上限淘汰，最旧先出）。
    fn push_undo(&mut self, op: UndoOp) {
        self.undo_stack.push(op);
        if self.undo_stack.len() > MAX_UNDO_OPS {
            self.undo_stack.remove(0);
        }
    }

    pub(crate) fn size(&self) -> usize {
        self.changes.len()
    }

    /// 按 sequence 稳定排序（提交顺序 = 用户操作顺序）。
    pub(crate) fn all(&self) -> Vec<RowChange> {
        let mut v = self.changes.clone();
        v.sort_by_key(|c| c.sequence);
        v
    }

    fn find(&self, row_id: &str, rtype: RowChangeType) -> Option<usize> {
        let k = key_of(row_id, rtype);
        self.index.get(&k).copied()
    }

    /// 取某行已存在的变更（update 优先，其次 delete）。
    pub(crate) fn get(&self, row_id: &str) -> Option<&RowChange> {
        self.find(row_id, RowChangeType::Update)
            .or_else(|| self.find(row_id, RowChangeType::Delete))
            .map(|i| &self.changes[i])
    }

    /// 行级增删（DELETE/INSERT）能力已实现并覆盖测试，UI 仅接单元格编辑，故暂标 dead_code。
    #[allow(dead_code)]
    pub(crate) fn has(&self, row_id: &str) -> bool {
        [
            RowChangeType::Update,
            RowChangeType::Delete,
            RowChangeType::Insert,
        ]
        .iter()
        .any(|t| self.find(row_id, *t).is_some())
    }

    /// 单元格编辑：改回原值则移除该格改动（全空则移除整行变更）。
    /// 对 insert 行直接写 new_row，不进 cell_changes。
    pub(crate) fn add_cell_change(
        &mut self,
        row_id: &str,
        column: &str,
        old_value: Value,
        new_value: Value,
        original_row: Row,
    ) {
        if values_equal(&old_value, &new_value) {
            return;
        }
        if let Some(i) = self.find(row_id, RowChangeType::Insert) {
            let entry = &mut self.changes[i];
            let mut nr = entry.new_row.clone().unwrap_or_default();
            row_set(&mut nr, column, new_value);
            entry.new_row = Some(nr);
            self.push_undo(UndoOp::Row {
                key: key_of(row_id, RowChangeType::Insert),
            });
            return;
        }
        let key = key_of(row_id, RowChangeType::Update);
        let idx = match self.find(row_id, RowChangeType::Update) {
            Some(i) => i,
            None => {
                let seq = {
                    self.seq += 1;
                    self.seq
                };
                let key = key.clone();
                let idx = self.changes.len();
                self.changes.push(RowChange {
                    row_id: row_id.to_string(),
                    rtype: RowChangeType::Update,
                    cell_changes: Vec::new(),
                    original_row,
                    new_row: None,
                    sequence: seq,
                    key,
                });
                self.index
                    .insert(key_of(row_id, RowChangeType::Update), idx);
                idx
            }
        };
        // undo 数据先收集（列名 + 是否塌缩），下面会按需重借 self：
        // push_undo 要 &mut self，与 change 的借用不交叠。
        let orig = {
            let change = &mut self.changes[idx];
            let orig = row_get(&change.original_row, column);
            // 新值回到原值 → 移除该格（塌缩）；否则合并/追加。
            if values_equal(&new_value, &orig) {
                change.cell_changes.retain(|c| c.column != column);
                if change.cell_changes.is_empty() {
                    // 塌缩即整条改动消失：不 push undo——undo() 按 key 查索引，
                    // 而 remove_by_key 已把条目删掉，push 进来的 op 永远解析
                    // 不到，只会白白吞掉用户的一次 undo。
                    self.remove_by_key(&key);
                } else {
                    self.push_undo(UndoOp::Cell {
                        key: key.clone(),
                        column: column.to_string(),
                    });
                }
                return;
            }
            match change.cell_changes.iter_mut().find(|c| c.column == column) {
                Some(prev) => prev.new_value = new_value,
                None => change.cell_changes.push(CellChange {
                    column: column.to_string(),
                    old_value,
                    new_value,
                }),
            }
            orig
        };
        self.push_undo(UndoOp::Cell {
            key: key.clone(),
            column: column.to_string(),
        });
        let _ = orig;
    }

    #[allow(dead_code)]
    pub(crate) fn add_insert(&mut self, row_id: String, row: Row) {
        self.seq += 1;
        let seq = self.seq;
        self.upsert(RowChange {
            row_id: row_id.clone(),
            rtype: RowChangeType::Insert,
            cell_changes: Vec::new(),
            original_row: Vec::new(),
            new_row: Some(row),
            sequence: seq,
            key: key_of(&row_id, RowChangeType::Insert),
        });
        self.push_undo(UndoOp::Row {
            key: key_of(&row_id, RowChangeType::Insert),
        });
    }

    #[allow(dead_code)]
    pub(crate) fn add_delete(&mut self, row_id: String, original_row: Row) {
        self.seq += 1;
        let seq = self.seq;
        self.upsert(RowChange {
            row_id: row_id.clone(),
            rtype: RowChangeType::Delete,
            cell_changes: Vec::new(),
            original_row,
            new_row: None,
            sequence: seq,
            key: key_of(&row_id, RowChangeType::Delete),
        });
        self.push_undo(UndoOp::Row {
            key: key_of(&row_id, RowChangeType::Delete),
        });
    }

    fn upsert(&mut self, change: RowChange) {
        let k = key_of(&change.row_id, change.rtype);
        match self.index.get(&k) {
            Some(&i) => self.changes[i] = change,
            None => {
                let idx = self.changes.len();
                self.changes.push(change);
                self.index.insert(k, idx);
            }
        }
    }

    fn remove_by_key(&mut self, key: &str) {
        let before = self.changes.len();
        self.changes.retain(|c| c.key != key);
        if self.changes.len() != before {
            self.rebuild_index();
        }
    }

    /// 删除类操作（retain/remove）后整表重建索引——删除是低频路径
    /// （undo/塌缩），重建 O(n) 摊还远低于每次格编辑 O(n) 的线性扫。
    fn rebuild_index(&mut self) {
        self.index.clear();
        for (i, c) in self.changes.iter().enumerate() {
            self.index.insert(c.key.clone(), i);
        }
    }

    /// 撤销最近一次操作。cell：移除该格改动（全空则整行）；row：移除整条插入/删除。
    pub(crate) fn undo(&mut self) -> bool {
        let op = match self.undo_stack.pop() {
            Some(o) => o,
            None => return false,
        };
        match op {
            UndoOp::Row { key } => {
                let before = self.changes.len();
                self.remove_by_key(&key);
                self.changes.len() < before
            }
            UndoOp::Cell { key, column } => {
                let Some(&i) = self.index.get(&key) else {
                    return false;
                };
                if self.changes[i].rtype != RowChangeType::Update {
                    return false;
                }
                self.changes[i].cell_changes.retain(|c| c.column != column);
                if self.changes[i].cell_changes.is_empty() {
                    self.changes.remove(i);
                    self.rebuild_index();
                }
                true
            }
        }
    }

    pub(crate) fn clear(&mut self) {
        self.changes.clear();
        self.index.clear();
        self.undo_stack.clear();
    }
}

// ─── 方言与字面量渲染 ─────────────────────────────────────

pub(crate) fn quote_ident(kind: DatabaseKind, n: &str) -> String {
    match kind {
        DatabaseKind::MySql => format!("`{}`", n.replace('`', "``")),
        DatabaseKind::Mssql => format!("[{}]", n.replace(']', "]]")),
        _ => format!("\"{}\"", n.replace('"', "\"\"")),
    }
}

pub(crate) fn quote_string(kind: DatabaseKind, s: &str) -> String {
    if kind == DatabaseKind::MySql {
        format!("'{}'", s.replace('\\', "\\\\").replace('\'', "''"))
    } else {
        format!("'{}'", s.replace('\'', "''"))
    }
}

pub(crate) fn value_to_literal(kind: DatabaseKind, v: &Value) -> String {
    match v {
        Value::Null => "NULL".to_string(),
        Value::Bool(b) => if *b { "TRUE" } else { "FALSE" }.to_string(),
        Value::Integer(i) => i.to_string(),
        Value::U64(u) => u.to_string(),
        Value::Float(f) => format!("{}", f),
        Value::String(s) => {
            if s.is_empty() {
                "''".to_string()
            } else {
                quote_string(kind, s)
            }
        }
        other => quote_string(
            kind,
            &serde_json::to_string(other).unwrap_or_else(|_| "NULL".to_string()),
        ),
    }
}

fn qualified_table(kind: DatabaseKind, schema: &str, table: &str) -> String {
    let t = quote_ident(kind, table);
    if schema.is_empty() {
        t
    } else {
        format!("{}.{}", quote_ident(kind, schema), t)
    }
}

fn pk_where(kind: DatabaseKind, pks: &[String], row: &Row) -> String {
    pks.iter()
        .map(|p| {
            format!(
                "{} = {}",
                quote_ident(kind, p),
                value_to_literal(kind, &row_get(row, p))
            )
        })
        .collect::<Vec<_>>()
        .join(" AND ")
}

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct GeneratedStatement {
    /// 占位符已内联为字面量（Review 预览用）。
    pub sql: String,
    /// 提交用的参数化 SQL。
    pub param_sql: String,
    pub params: Vec<Value>,
    /// 期望影响行数（delete/insert 期望 1；update 0 亦合法）。
    pub expect_affected: u64,
    /// keyed update：actual > expect 才拦（同值更新报 0 是正常的）。
    pub allow_zero: bool,
    /// 定位谓词与表引用（UPDATE/DELETE 有，INSERT 为空）：0 行守卫的同事务
    /// 探针复用——`SELECT 1 FROM table WHERE where` 与提交语句同源生成，
    /// 区分"同值更新 no-op"（行在，放行）与"行已被删/PK 已改"（回滚批次）。
    pub table: String,
    pub where_clause: String,
}

/// 生成提交语句（顺序 = 用户操作顺序）。
pub(crate) fn build_statements(
    changes: &[RowChange],
    schema: &str,
    table: &str,
    pk_columns: &[String],
    kind: DatabaseKind,
) -> Vec<GeneratedStatement> {
    let qt = qualified_table(kind, schema, table);
    let mut out = Vec::new();
    for change in changes {
        match change.rtype {
            RowChangeType::Delete => {
                let where_ = pk_where(kind, pk_columns, &change.original_row);
                let psql = format!(
                    "DELETE FROM {} WHERE {}",
                    qt,
                    pk_columns
                        .iter()
                        .map(|p| format!("{} = ?", quote_ident(kind, p)))
                        .collect::<Vec<_>>()
                        .join(" AND ")
                );
                out.push(GeneratedStatement {
                    sql: format!("DELETE FROM {} WHERE {}", qt, where_),
                    param_sql: psql,
                    params: pk_columns
                        .iter()
                        .map(|p| row_get(&change.original_row, p))
                        .collect(),
                    expect_affected: 1,
                    allow_zero: false,
                    table: qt.clone(),
                    where_clause: where_,
                });
            }
            RowChangeType::Insert => {
                let row = change.new_row.clone().unwrap_or_default();
                let cols: Vec<String> = row.iter().map(|(c, _)| c.clone()).collect();
                let is_default = |c: &str| {
                    let v = row_get(&row, c);
                    let blank =
                        matches!(&v, Value::Null) || matches!(&v, Value::String(s) if s.is_empty());
                    blank && pk_columns.iter().any(|p| p == c)
                };
                let values = cols
                    .iter()
                    .map(|c| {
                        if is_default(c) {
                            "DEFAULT".to_string()
                        } else {
                            value_to_literal(kind, &row_get(&row, c))
                        }
                    })
                    .collect::<Vec<_>>();
                let psql = format!(
                    "INSERT INTO {} ({}) VALUES ({})",
                    qt,
                    cols.iter()
                        .map(|c| quote_ident(kind, c))
                        .collect::<Vec<_>>()
                        .join(", "),
                    cols.iter()
                        .map(|c| if is_default(c) {
                            "DEFAULT".to_string()
                        } else {
                            "?".to_string()
                        })
                        .collect::<Vec<_>>()
                        .join(", ")
                );
                let params = cols
                    .iter()
                    .filter(|c| !is_default(c))
                    .map(|c| row_get(&row, c))
                    .collect();
                out.push(GeneratedStatement {
                    sql: format!(
                        "INSERT INTO {} ({}) VALUES ({})",
                        qt,
                        cols.iter()
                            .map(|c| quote_ident(kind, c))
                            .collect::<Vec<_>>()
                            .join(", "),
                        values.join(", ")
                    ),
                    param_sql: psql,
                    params,
                    expect_affected: 1,
                    allow_zero: false,
                    table: String::new(),
                    where_clause: String::new(),
                });
            }
            RowChangeType::Update => {
                let sets = change
                    .cell_changes
                    .iter()
                    .map(|c| {
                        format!(
                            "{} = {}",
                            quote_ident(kind, &c.column),
                            value_to_literal(kind, &c.new_value)
                        )
                    })
                    .collect::<Vec<_>>()
                    .join(", ");
                let where_ = pk_where(kind, pk_columns, &change.original_row);
                let psets = change
                    .cell_changes
                    .iter()
                    .map(|c| format!("{} = ?", quote_ident(kind, &c.column)))
                    .collect::<Vec<_>>()
                    .join(", ");
                let pwhere = pk_columns
                    .iter()
                    .map(|p| format!("{} = ?", quote_ident(kind, p)))
                    .collect::<Vec<_>>()
                    .join(" AND ");
                let mut params: Vec<Value> = change
                    .cell_changes
                    .iter()
                    .map(|c| c.new_value.clone())
                    .collect();
                params.extend(pk_columns.iter().map(|p| row_get(&change.original_row, p)));
                out.push(GeneratedStatement {
                    sql: format!("UPDATE {} SET {} WHERE {}", qt, sets, where_),
                    param_sql: format!("UPDATE {} SET {} WHERE {}", qt, psets, pwhere),
                    params,
                    expect_affected: 1,
                    allow_zero: true,
                    table: qt.clone(),
                    where_clause: where_,
                });
            }
        }
    }
    out
}

/// 影响行数是否不符：delete/insert 期望恰 1；keyed update 仅当 actual>expect 才拦。
pub(crate) fn rows_mismatch(stmt: &GeneratedStatement, actual: u64) -> bool {
    if stmt.allow_zero {
        actual > stmt.expect_affected
    } else {
        actual != stmt.expect_affected
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(x: &str) -> Value {
        Value::String(x.to_string())
    }
    fn i(x: i64) -> Value {
        Value::Integer(x)
    }
    fn row(pairs: &[(&str, Value)]) -> Row {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.clone()))
            .collect()
    }

    #[test]
    fn merges_repeated_edits_keeping_original_old_value() {
        let mut q = ChangeQueue::new();
        let orig = row(&[("a", s("x")), ("b", i(1))]);
        q.add_cell_change("r1", "a", s("x"), s("y"), orig.clone());
        q.add_cell_change("r1", "a", s("y"), s("z"), orig.clone());
        let c = q.get("r1").unwrap();
        assert_eq!(c.cell_changes.len(), 1);
        assert_eq!(c.cell_changes[0].column, "a");
        assert_eq!(c.cell_changes[0].old_value, s("x"));
        assert_eq!(c.cell_changes[0].new_value, s("z"));
        assert_eq!(q.size(), 1);
    }

    #[test]
    fn collapses_when_edited_back_to_original() {
        let mut q = ChangeQueue::new();
        let orig = row(&[("a", s("x"))]);
        q.add_cell_change("r1", "a", s("x"), s("y"), orig.clone());
        assert_eq!(q.size(), 1);
        q.add_cell_change("r1", "a", s("y"), s("x"), orig);
        assert_eq!(q.size(), 0);
    }

    /// 索引回归：塌缩删除走 remove_by_key+rebuild，删后再编辑必须能重新
    /// 命中（find/get 走 index，重建错位会静默丢变更）。
    #[test]
    fn index_survives_collapse_and_reedit() {
        let mut q = ChangeQueue::new();
        let orig = row(&[("a", s("x"))]);
        q.add_cell_change("r1", "a", s("x"), s("y"), orig.clone());
        // 改回原值 → 塌缩（remove_by_key → rebuild_index）
        q.add_cell_change("r1", "a", s("y"), s("x"), orig.clone());
        assert_eq!(q.size(), 0);
        // 重建后同行再编辑必须重新入队
        q.add_cell_change("r1", "a", s("x"), s("z"), orig.clone());
        let c = q.get("r1").unwrap();
        assert_eq!(c.cell_changes[0].new_value, s("z"));
        assert_eq!(q.size(), 1);
        // undo 塌缩路径（cell 全空 → remove(i)+rebuild）后查找不受影响
        assert!(q.undo());
        assert_eq!(q.size(), 0);
        assert!(q.get("r1").is_none());
        // undo 后新编辑仍可命中
        q.add_cell_change("r1", "a", s("x"), s("w"), orig);
        assert_eq!(q.get("r1").unwrap().cell_changes[0].new_value, s("w"));
    }

    /// 塌缩到空的编辑不得入 undo 栈：undo() 按 key 查索引，而塌缩已把条目
    /// 删除，push 进来的 op 永远解析不到——旧行为吞掉用户一次 undo（undo
    /// 返回 false），真正的上一个编辑反而救不回来。
    #[test]
    fn collapse_to_empty_does_not_consume_an_undo_step() {
        let mut q = ChangeQueue::new();
        let orig = row(&[("a", s("x")), ("b", s("p"))]);
        // 第一个编辑：a → y（真实改动，入 undo 栈）
        q.add_cell_change("r1", "a", s("x"), s("y"), orig.clone());
        assert_eq!(q.size(), 1);
        // 第二个编辑：b 改回原值且该行此前没有 b 的改动 → 不产生改动。
        // 旧行为会为这个 no-op push 一个 undo op。
        q.add_cell_change("r1", "b", s("p"), s("p"), orig.clone());
        assert_eq!(q.size(), 1);
        // undo 必须直接撤销第一个真实编辑，而不是先吃掉一个死 op。
        assert!(q.undo(), "the first undo must land on the real edit");
        assert_eq!(q.size(), 0);

        // 塌缩到空（cell_changes 清空 → 整行移除）同样不入栈：
        q.add_cell_change("r1", "a", s("x"), s("y"), orig.clone());
        q.add_cell_change("r1", "a", s("y"), s("x"), orig.clone());
        assert_eq!(q.size(), 0);
        // 这里 undo 返回 false（栈已空）是正确的——塌缩不欠用户一个 undo。
    }

    #[test]
    fn ignores_noop_and_treats_int_float_as_equal() {
        let mut q = ChangeQueue::new();
        q.add_cell_change("r1", "a", s("x"), s("x"), row(&[("a", s("x"))]));
        assert_eq!(q.size(), 0);
        q.add_cell_change("r2", "n", i(5), Value::Float(5.0), row(&[("n", i(5))]));
        assert_eq!(q.size(), 0);
    }

    #[test]
    fn int_float_equality_is_exact_above_two_pow_53() {
        // The exactness boundary: f64 represents every even integer up to 2^54,
        // but (2^53+1) is not representable — as a float it rounds down to 2^53.
        // `as f64` used to compare the integer against that rounded float equal,
        // so editing such a cell to a "different" value was silently dropped as
        // a no-op.
        let odd: i64 = (1i64 << 53) + 1;
        let even: i64 = (1i64 << 53) + 2;
        assert!(
            !values_equal(&i(odd), &i(even)),
            "distinct integers stay distinct"
        );

        // The odd integer cannot round-trip through f64, so it must not compare
        // equal to the float it would round to.
        assert!(!values_equal(&i(odd), &Value::Float(odd as f64)));
        // The even integer round-trips exactly, so it keeps comparing equal.
        assert!(values_equal(&i(even), &Value::Float(even as f64)));
        assert!(values_equal(
            &i(1i64 << 53),
            &Value::Float((1i64 << 53) as f64)
        ));
        // Values that a f64 can represent exactly keep comparing equal.
        assert!(values_equal(&i(5), &Value::Float(5.0)));
        assert!(values_equal(&Value::Float(5.0), &i(5)));
        // A fractional float is never equal to any integer.
        assert!(!values_equal(&i(5), &Value::Float(5.5)));
    }

    #[test]
    fn undoes_row_level_ops() {
        let mut q = ChangeQueue::new();
        q.add_delete("r1".into(), row(&[("id", i(1)), ("a", s("x"))]));
        q.add_insert("r2".into(), row(&[("id", i(2)), ("a", s("y"))]));
        assert_eq!(q.size(), 2);
        assert!(q.undo());
        assert!(!q.has("r2"));
        assert!(q.undo());
        assert_eq!(q.size(), 0);
    }

    #[test]
    fn orders_statements_by_user_sequence() {
        let mut q = ChangeQueue::new();
        q.add_delete("r1".into(), row(&[("id", i(5)), ("a", s("x"))]));
        q.add_insert("r1".into(), row(&[("id", i(5)), ("a", s("y"))]));
        let all = q.all();
        assert_eq!(all[0].rtype, RowChangeType::Delete);
        assert_eq!(all[1].rtype, RowChangeType::Insert);
        assert!(all[1].sequence > all[0].sequence);
    }

    #[test]
    fn builds_pk_statements_with_default_pk() {
        let mut q = ChangeQueue::new();
        q.add_cell_change(
            "r1",
            "name",
            s("a"),
            s("b"),
            row(&[("id", i(1)), ("name", s("a"))]),
        );
        q.add_delete("r2".into(), row(&[("id", i(2)), ("name", s("c"))]));
        q.add_insert("r3".into(), row(&[("id", i(3)), ("name", s("d"))]));
        q.add_insert("r4".into(), row(&[("id", s("")), ("name", s("e"))]));
        let pk = ["id".to_string()];
        let stmts = build_statements(&q.all(), "main", "t", &pk, DatabaseKind::Sqlite);
        assert_eq!(stmts.len(), 4);
        assert_eq!(
            stmts[0].sql,
            "UPDATE \"main\".\"t\" SET \"name\" = 'b' WHERE \"id\" = 1"
        );
        assert_eq!(
            stmts[0].param_sql,
            "UPDATE \"main\".\"t\" SET \"name\" = ? WHERE \"id\" = ?"
        );
        assert_eq!(stmts[0].params, vec![s("b"), i(1)]);
        assert!(stmts[1]
            .sql
            .contains("DELETE FROM \"main\".\"t\" WHERE \"id\" = 2"));
        assert_eq!(
            stmts[2].sql,
            "INSERT INTO \"main\".\"t\" (\"id\", \"name\") VALUES (3, 'd')"
        );
        assert_eq!(
            stmts[3].sql,
            "INSERT INTO \"main\".\"t\" (\"id\", \"name\") VALUES (DEFAULT, 'e')"
        );
        assert_eq!(stmts[3].params, vec![s("e")]);
        assert!(stmts[0].allow_zero);
        assert!(!stmts[1].allow_zero);
    }

    #[test]
    fn mysql_and_mssql_dialects() {
        assert_eq!(quote_ident(DatabaseKind::MySql, "my table"), "`my table`");
        assert_eq!(quote_ident(DatabaseKind::Mssql, "x"), "[x]");
        assert_eq!(value_to_literal(DatabaseKind::MySql, &s("a'b")), "'a''b'");
        assert_eq!(value_to_literal(DatabaseKind::Sqlite, &Value::Null), "NULL");
    }

    #[test]
    fn row_count_validation() {
        let del = GeneratedStatement {
            sql: String::new(),
            param_sql: String::new(),
            params: vec![],
            expect_affected: 1,
            allow_zero: false,
            table: String::new(),
            where_clause: String::new(),
        };
        let upd = GeneratedStatement {
            allow_zero: true,
            ..del.clone()
        };
        assert!(rows_mismatch(&del, 0));
        assert!(rows_mismatch(&del, 2));
        assert!(!rows_mismatch(&del, 1));
        assert!(!rows_mismatch(&upd, 0));
        assert!(!rows_mismatch(&upd, 1));
        assert!(rows_mismatch(&upd, 2));
    }
}
