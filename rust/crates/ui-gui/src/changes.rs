//! G3 变更跟踪编辑：单元格编辑先在本地排队，Review 后在单个事务内提交、逐条校验影响行数。
//!
//! 语义以 Web `web/src/lib/changes.ts` 为基准（独立实现，不 port 代码）。
//! 契约依据：spec/behavior.md §10（事务单次 finalize）。

use polydb_core::{DatabaseKind, Value};

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
pub(crate) fn values_equal(a: &Value, b: &Value) -> bool {
    let cross = matches!(
        (a, b),
        (Value::Integer(_), Value::Float(_)) | (Value::Float(_), Value::Integer(_))
    );
    if cross {
        let to_f = |v: &Value| match v {
            Value::Integer(i) => *i as f64,
            Value::Float(f) => *f,
            _ => f64::NAN,
        };
        return to_f(a) == to_f(b);
    }
    a == b
}

/// 每表 tab 一个队列：同格重复编辑合并（保留最初旧值）、改回原值自动塌缩。
#[derive(Debug, Default)]
pub(crate) struct ChangeQueue {
    changes: Vec<RowChange>,
    undo_stack: Vec<UndoOp>,
    seq: i64,
}

impl ChangeQueue {
    pub(crate) fn new() -> Self {
        Self::default()
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
        self.changes
            .iter()
            .position(|c| key_of(&c.row_id, c.rtype) == k)
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
            self.undo_stack.push(UndoOp::Row {
                key: key_of(row_id, RowChangeType::Insert),
            });
            return;
        }
        let key = key_of(row_id, RowChangeType::Update);
        let idx = match self.find(row_id, RowChangeType::Update) {
            Some(i) => i,
            None => {
                self.changes.push(RowChange {
                    row_id: row_id.to_string(),
                    rtype: RowChangeType::Update,
                    cell_changes: Vec::new(),
                    original_row,
                    new_row: None,
                    sequence: {
                        self.seq += 1;
                        self.seq
                    },
                });
                self.changes.len() - 1
            }
        };
        let change = &mut self.changes[idx];
        let orig = row_get(&change.original_row, column);
        // 新值回到原值 → 移除该格（塌缩）；否则合并/追加。
        if values_equal(&new_value, &orig) {
            change.cell_changes.retain(|c| c.column != column);
            self.undo_stack.push(UndoOp::Cell {
                key: key.clone(),
                column: column.to_string(),
            });
            if change.cell_changes.is_empty() {
                self.remove_by_key(&key);
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
        self.undo_stack.push(UndoOp::Cell {
            key: key.clone(),
            column: column.to_string(),
        });
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
        });
        self.undo_stack.push(UndoOp::Row {
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
        });
        self.undo_stack.push(UndoOp::Row {
            key: key_of(&row_id, RowChangeType::Delete),
        });
    }

    fn upsert(&mut self, change: RowChange) {
        let k = key_of(&change.row_id, change.rtype);
        match self
            .changes
            .iter()
            .position(|c| key_of(&c.row_id, c.rtype) == k)
        {
            Some(i) => self.changes[i] = change,
            None => self.changes.push(change),
        }
    }

    fn remove_by_key(&mut self, key: &str) {
        self.changes.retain(|c| key_of(&c.row_id, c.rtype) != key);
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
                let Some(i) = self
                    .changes
                    .iter()
                    .position(|c| key_of(&c.row_id, c.rtype) == key)
                else {
                    return false;
                };
                if self.changes[i].rtype != RowChangeType::Update {
                    return false;
                }
                self.changes[i].cell_changes.retain(|c| c.column != column);
                if self.changes[i].cell_changes.is_empty() {
                    self.changes.remove(i);
                }
                true
            }
        }
    }

    pub(crate) fn clear(&mut self) {
        self.changes.clear();
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

    #[test]
    fn ignores_noop_and_treats_int_float_as_equal() {
        let mut q = ChangeQueue::new();
        q.add_cell_change("r1", "a", s("x"), s("x"), row(&[("a", s("x"))]));
        assert_eq!(q.size(), 0);
        q.add_cell_change("r2", "n", i(5), Value::Float(5.0), row(&[("n", i(5))]));
        assert_eq!(q.size(), 0);
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
