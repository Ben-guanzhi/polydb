//! SQLite 的 DDL 语句生成（Compare & Sync E3）。
//!
//! 输入 = 两侧快照 + **已勾选**的变更条目（E1 的 `Entry`），输出 = 有序语句脚本 + 回滚脚本。
//! 零 IO、零 UI：语句只是文本，执行由调用方走既有的 `/query` 或 `/api/transactions`。
//!
//! 规则全部来自 `docs/plan-compare-sync.md` 附录 A.11 / A.15 的**实测取证**（不是文档推测）：
//!
//! 1. 直改：`create_table` / `drop_table` / `add_column` / `drop_column` / `add_index` / `drop_index`。
//! 2. 恒走重建：`alter_column` / `add_foreign_key` / `drop_foreign_key`
//!    （SQLite 无 `ALTER COLUMN`、无 `ADD/DROP CONSTRAINT`）。
//! 3. `drop_column` 若该列是主键、或出现在任一索引里 ⇒ 转重建
//!    （实测 `cannot drop PRIMARY KEY column`、`error in index … after drop column`）。
//! 4. 名字以 `sqlite_` 开头的索引是 PRIMARY KEY / UNIQUE 约束的自动索引，
//!    既不能 `CREATE INDEX` 也不能 `DROP INDEX`（实测 `object name reserved for internal use`、
//!    `index associated with UNIQUE or PRIMARY KEY constraint cannot be dropped`）
//!    ⇒ 涉及它的 `add_index` / `drop_index` 转重建，重建时以表级 `UNIQUE (…)` 还原。
//! 5. 脚本含重建时首尾加 `PRAGMA foreign_keys=OFF/ON`，并置 `pragmas_outside_tx = true`
//!    （实测：FK 开着时重建被引用的父表会级联清空子表数据；而 `PRAGMA foreign_keys`
//!    在事务内是 no-op）。脚本自身**不含** `BEGIN`/`COMMIT`。
//! 6. 不可逆判定：`drop_table` / `drop_column` 恒不可逆；`alter_column` 当 `flags` 含 `type`
//!    或 `nullability` 时不可逆（仅 `default` 可逆）；其余可逆。
//! 7. 回滚承接正向的重建：正向重建过的表在回滚方向**强制**再重建。直改语句补不回重建时
//!    丢弃的对象（例：删一个被索引覆盖的列 ⇒ 整表重建 ⇒ 回滚只 ADD COLUMN 会永久留下没有索引的表）。
//! 8. 勾选校验（`select_entries`）：id 必须属于当前计划；进入重建的表必须**整组**勾选，
//!    否则该表上未勾选的变更会被重建语句一并落地。
//! 9. 视图不参与比对，但**名字**要进快照（E6.5，取证 A.15 G13/G16/G17）：临时表名避让它们，
//!    删表与重建挂粗粒度视图警告——SQLite 没有依赖目录，判定不了「哪个视图引用了哪张表」，
//!    所以只报数不猜。
//!
//! 其余方言显式 `NOT_SUPPORTED`（定案见 §8-1 与 §8-7：四方言的 `on_update`/`on_delete`、
//! `generic_type` 尚未落地，diff 比不出差异，凭文档盲写语法等于「数字上已实现、实际未验证」）。

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyAction, IndexInfo, PolyDBError,
};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};

use crate::normalize::{fold_spaces, lower_ascii, normalize_type};
use crate::{
    column_map, index_object, index_sig, table_map, view_names, Entry, Op, Options, Snapshot,
    TableSnapshot,
};

/// 固定警告文案：逐字进 `spec/behavior.md` §15，并由共享向量语料锁定。
pub const W_TRIGGERS: &str = "重建表会丢失该表上的触发器（快照不含触发器）";
pub const W_GENERATED_COLUMNS: &str = "生成列会退化为普通列（快照不标注生成列）";
pub const W_CHECK_CONSTRAINTS: &str = "CHECK 约束会丢失（快照不含 CHECK 约束）";
pub const W_AUTOINCREMENT: &str = "主键整数列的 AUTOINCREMENT 关键字无法从快照还原";
pub const W_CREATE_TABLE: &str = "新建表只按快照里的列定义生成，不含 CHECK 约束、生成列与触发器";
pub const W_DROP_COLUMN_CHECK: &str = "该列若被 CHECK 约束引用，删除后约束会一并消失（快照不可见）";
pub const W_ADD_COLUMN_LIMITS: &str =
    "SQLite 的 ADD COLUMN 只接受常量默认值，非空表上加 NOT NULL 且无默认值的列也会报错";
pub const W_REBUILD_NEW_NOT_NULL: &str =
    "重建只复制两侧同名列，新增的 NOT NULL 列若无默认值会让数据搬运 INSERT 报错";

/// 视图警告里的名单上限：再多只报数不报名（警告是给人扫一眼的）。
const VIEW_WARN_MAX: usize = 5;

/// 「N 个视图（a, b 等）」——快照里的视图名升序，超过 5 个以「等」收尾。
fn view_scope(names: &[String]) -> String {
    let head = names
        .iter()
        .take(VIEW_WARN_MAX)
        .cloned()
        .collect::<Vec<_>>()
        .join(", ");
    if names.len() > VIEW_WARN_MAX {
        format!("{} 个视图（{head} 等）", names.len())
    } else {
        format!("{} 个视图（{head}）", names.len())
    }
}

/// 删表的视图警告（取证 A.15 G16：`DROP` 没有依赖检查，语句会成功、视图就此失效，
/// 且原先**一条警告都没有**）。文案逐字进 behavior.md §15，Go 侧必须一致。
fn w_view_drop(views: &[String]) -> String {
    format!(
        "本库现有 {}：DROP TABLE 不检查依赖，语句会成功，但引用它的视图将失效（快照不含视图定义，判定不了是哪一个）",
        view_scope(views)
    )
}

/// 重建的视图警告（取证 A.15 G13：失败点不在 `DROP` 而在 `ALTER TABLE … RENAME`，
/// 它会重解析依赖视图 ⇒ 整条重建直接报错）。
fn w_view_rebuild(views: &[String]) -> String {
    format!(
        "本库现有 {}：重建经 RENAME，而 SQLite 改名时会重解析依赖该表的视图 ⇒ 有视图引用它就整条重建报错",
        view_scope(views)
    )
}

const PRAGMA_FK_OFF: &str = "PRAGMA foreign_keys=OFF;";
const PRAGMA_FK_ON: &str = "PRAGMA foreign_keys=ON;";
/// PRAGMA 包裹条的相位：首条在最前、末条在最后。它们不属于任何变更条目，`entry_ids` 为空。
const PHASE_PRAGMA_FIRST: u8 = 0;
const PHASE_PRAGMA_LAST: u8 = 255;

/// 一条可执行语句。
///
/// `entry_ids` 是这条语句实现的变更条目 id：直改动作恰好 1 个；重建表的一组语句承载该表
/// 被吸收的**全部**条目（重建以目标快照为基准，同表其余变更自然一并落进新定义里）；
/// PRAGMA 包裹条为空。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Statement {
    #[serde(default)]
    pub entry_ids: Vec<String>,
    pub sql: String,
    pub phase: u8,
    /// 语句作用的表名（原样大小写）；PRAGMA 包裹条为空串。
    #[serde(default)]
    pub table: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    /// 该语句承载的条目里只要有一条不可逆，整条语句就标为不可逆。
    #[serde(default)]
    pub non_reversible: bool,
}

/// 语句脚本 + 其反向脚本。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Script {
    pub statements: Vec<Statement>,
    pub rollback: Vec<Statement>,
    /// 任一脚本含首尾 PRAGMA 包裹条时为 true：那两条必须留在事务外（取证 G6）。
    pub pragmas_outside_tx: bool,
    /// SQLite 的 DDL 可以在事务里（取证 G7，实测值）。
    pub ddl_in_tx: bool,
}

fn invalid(msg: impl Into<String>) -> CoreError {
    CoreError::from(PolyDBError::new("POLYDB_ERR_INVALID_PARAM", msg.into()))
}

fn quote_ident(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

fn join_idents(cols: &[String]) -> String {
    cols.iter()
        .map(|c| quote_ident(c))
        .collect::<Vec<_>>()
        .join(",")
}

fn table_or_err<'a>(t: Option<&'a TableSnapshot>, name: &str) -> CoreResult<&'a TableSnapshot> {
    t.ok_or_else(|| invalid(format!("快照中缺少表 {name}，无法生成 DDL")))
}

/// 列的顺序：按 `ordinal_position` 稳定排序（缺省 0 时保持快照原序）。
fn ordered_columns(t: &TableSnapshot) -> Vec<&ColumnInfo> {
    let mut v: Vec<&ColumnInfo> = t.columns.iter().collect();
    v.sort_by_key(|c| c.ordinal_position);
    v
}

fn sorted_by_position(cols: &[(i32, String)]) -> Vec<String> {
    let mut v = cols.to_vec();
    v.sort_by_key(|(p, _)| *p);
    v.into_iter().map(|(_, n)| n).collect()
}

/// 主键列：优先取快照里标记为主键的索引（带列序），否则退回列上的 `is_primary_key`。
fn pk_columns(t: &TableSnapshot) -> Vec<String> {
    if let Some(p) = t.indexes.iter().find(|i| i.primary) {
        let cols: Vec<(i32, String)> = p
            .columns
            .iter()
            .map(|c| (c.position, c.name.clone()))
            .collect();
        if !cols.is_empty() {
            return sorted_by_position(&cols);
        }
    }
    ordered_columns(t)
        .into_iter()
        .filter(|c| c.is_primary_key)
        .map(|c| c.name.clone())
        .collect()
}

fn sql_action(a: Option<ForeignKeyAction>) -> Option<&'static str> {
    match a {
        Some(ForeignKeyAction::Cascade) => Some("CASCADE"),
        Some(ForeignKeyAction::SetNull) => Some("SET NULL"),
        Some(ForeignKeyAction::SetDefault) => Some("SET DEFAULT"),
        Some(ForeignKeyAction::Restrict) => Some("RESTRICT"),
        Some(ForeignKeyAction::NoAction) => Some("NO ACTION"),
        None => None,
    }
}

/// 默认值字面量：保持快照原样，只折叠空白。
///
/// 不走 `normalize_default`——`(current_timestamp)` 这类表达式默认**必须**带括号，
/// 归一化会把括号剥掉而生成非法 SQL。空串与 `NULL` 视为无默认值。
fn literal_default(raw: Option<&str>) -> Option<String> {
    let d = fold_spaces(raw.unwrap_or("").trim());
    if d.is_empty() || lower_ascii(&d) == "null" {
        return None;
    }
    Some(d)
}

/// 默认值是否为 SQLite 认可的常量字面量（数字 / 单引号串 / blob / NULL）。
///
/// 用于给 `add_column` 挂警告：非常量默认值在 `ADD COLUMN` 上直接报错（取证 G11）。
fn is_constant_default(raw: Option<&str>) -> bool {
    let Some(d) = literal_default(raw) else {
        return true; // 无默认值即 NULL，本身是常量
    };
    let b = d.as_bytes();
    if b.first() == Some(&b'\'') {
        // 单引号串：内部只允许 '' 转义，且必须收尾在最后一个字符
        let mut i = 1;
        while i < b.len() {
            if b[i] != b'\'' {
                i += 1;
                continue;
            }
            if i + 1 < b.len() && b[i + 1] == b'\'' {
                i += 2;
                continue;
            }
            return i + 1 == b.len();
        }
        return false;
    }
    if (b.first() == Some(&b'x') || b.first() == Some(&b'X')) && b.get(1) == Some(&b'\'') {
        return b.last() == Some(&b'\'')
            && d[2..d.len() - 1].bytes().all(|c| c.is_ascii_hexdigit());
    }
    is_number(&d)
}

fn is_number(s: &str) -> bool {
    let body = match s.strip_prefix(['-', '+']) {
        Some(t) => t,
        None => s,
    };
    let mut digits = false;
    let mut dot = false;
    let mut exp = false;
    let b = body.as_bytes();
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            c if c.is_ascii_digit() => digits = true,
            b'.' if !dot && !exp => dot = true,
            b'e' | b'E' if digits && !exp => {
                exp = true;
                if i + 1 < b.len() && matches!(b[i + 1], b'+' | b'-') {
                    i += 1;
                }
            }
            _ => return false,
        }
        i += 1;
    }
    digits
}

/// 快照里的列定义文本：`"a" integer NOT NULL DEFAULT 'x'`。
///
/// 类型走 `normalize_type`，与 diff 条目的 `from`/`to` 用同一字面量。
/// 不读 `is_auto_increment`：SQLite 驱动的快照里该字段恒为 false（取证 G3 同源），
/// `AUTOINCREMENT` 只存在于 `sqlite_master.sql` 文本中，由警告覆盖。
fn column_def(kind: DatabaseKind, c: &ColumnInfo) -> String {
    let mut s = quote_ident(&c.name);
    let ty = normalize_type(kind, &c.data_type);
    if !ty.is_empty() {
        s.push(' ');
        s.push_str(&ty);
    }
    if !c.nullable {
        s.push_str(" NOT NULL");
    }
    if let Some(d) = literal_default(c.default_value.as_deref()) {
        s.push_str(&format!(" DEFAULT {d}"));
    }
    s
}

/// 自动索引：名字以 `sqlite_` 开头（SQLite 为 PRIMARY KEY / UNIQUE 约束自建）。
fn is_auto_index(name: &str) -> bool {
    lower_ascii(name.trim()).starts_with("sqlite_")
}

/// 表级约束：主键、由自动索引还原的 UNIQUE、外键。
fn table_constraints(t: &TableSnapshot) -> Vec<String> {
    let mut out = Vec::new();
    let pk = pk_columns(t);
    if !pk.is_empty() {
        out.push(format!("PRIMARY KEY ({})", join_idents(&pk)));
    }
    let mut uniq: Vec<(String, String)> = t
        .indexes
        .iter()
        .filter(|i| !i.primary && is_auto_index(&i.name))
        .map(|i| {
            let cols: Vec<(i32, String)> = i
                .columns
                .iter()
                .map(|c| (c.position, c.name.clone()))
                .collect();
            let cols = sorted_by_position(&cols);
            (
                lower_ascii(&i.name),
                format!("UNIQUE ({})", join_idents(&cols)),
            )
        })
        .collect();
    uniq.sort_by(|a, b| a.0.cmp(&b.0));
    out.extend(uniq.into_iter().map(|(_, s)| s));

    let mut fks: Vec<(String, String)> = t
        .foreign_keys
        .iter()
        .map(|f| {
            let cols = join_idents(&f.columns);
            let refs = join_idents(&f.referenced_columns);
            let mut s = format!(
                "FOREIGN KEY ({cols}) REFERENCES {}({refs})",
                quote_ident(&f.referenced_table)
            );
            if let Some(a) = sql_action(f.on_delete) {
                s.push_str(&format!(" ON DELETE {a}"));
            }
            if let Some(a) = sql_action(f.on_update) {
                s.push_str(&format!(" ON UPDATE {a}"));
            }
            (format!("{cols}>{refs}"), s)
        })
        .collect();
    fks.sort_by(|a, b| a.0.cmp(&b.0));
    out.extend(fks.into_iter().map(|(_, s)| s));
    out
}

/// 建表主体：列定义 + 表级约束，单行以 `,` 连接（向量语料按字面锁定）。
fn create_table_body(kind: DatabaseKind, t: &TableSnapshot) -> String {
    let mut parts: Vec<String> = ordered_columns(t)
        .into_iter()
        .map(|c| column_def(kind, c))
        .collect();
    parts.extend(table_constraints(t));
    parts.join(",")
}

/// 索引的可执行名字：无名索引合成 `polydb_idx_<表>_<列>`（两端同一规则）。
fn index_exec_name(t: &TableSnapshot, idx: &IndexInfo) -> String {
    if !idx.name.trim().is_empty() {
        return idx.name.clone();
    }
    format!("polydb_idx_{}_{}", t.name, index_sig(idx).replace(',', "_"))
}

fn create_index_sql(t: &TableSnapshot, idx: &IndexInfo) -> String {
    let uniq = if idx.unique { "UNIQUE " } else { "" };
    let cols: Vec<(i32, String)> = idx
        .columns
        .iter()
        .map(|c| (c.position, c.name.clone()))
        .collect();
    format!(
        "CREATE {uniq}INDEX {} ON {}({});",
        quote_ident(&index_exec_name(t, idx)),
        quote_ident(&t.name),
        join_idents(&sorted_by_position(&cols))
    )
}

fn mk_stmt(
    entry_ids: &[String],
    sql: String,
    phase: u8,
    table: &str,
    warnings: Vec<String>,
    non_reversible: bool,
) -> Statement {
    Statement {
        entry_ids: entry_ids.to_vec(),
        sql,
        phase,
        table: table.to_string(),
        warnings,
        non_reversible,
    }
}

fn pragma_stmt(sql: &str, phase: u8) -> Statement {
    mk_stmt(&[], sql.to_string(), phase, "", Vec::new(), false)
}

/// 条目是否不可逆（规则 6）。
fn entry_non_reversible(e: &Entry) -> bool {
    match e.op {
        Op::DropTable | Op::DropColumn => true,
        Op::AlterColumn => e
            .flags
            .iter()
            .any(|f| matches!(f, crate::ColumnFlag::Type | crate::ColumnFlag::Nullability)),
        _ => false,
    }
}

/// 条目 `object` → 快照里的索引（E1 对无名索引给的是合成名 `idx(a,b)`）。
fn find_index<'a>(t: &'a TableSnapshot, object: &str) -> Option<&'a IndexInfo> {
    let want = lower_ascii(object.trim());
    t.indexes
        .iter()
        .find(|i| lower_ascii(&index_object(i)) == want)
}

fn column_of<'a>(t: &'a TableSnapshot, object: &str) -> Option<&'a ColumnInfo> {
    column_map(t).get(&lower_ascii(object.trim())).copied()
}

/// 该条目是否必须走「重建表」路径（规则 2/3/4）。
fn needs_rebuild(e: &Entry, src: Option<&TableSnapshot>, tgt: Option<&TableSnapshot>) -> bool {
    match e.op {
        Op::AlterColumn | Op::AddForeignKey | Op::DropForeignKey => true,
        Op::DropColumn => {
            let Some(t) = src else { return false };
            let col = lower_ascii(e.object.trim());
            if column_of(t, &col).is_some_and(|c| c.is_primary_key) {
                return true;
            }
            t.indexes
                .iter()
                .any(|i| i.columns.iter().any(|c| lower_ascii(&c.name) == col))
        }
        Op::AddIndex => index_needs_rebuild(tgt, &e.object),
        Op::DropIndex => index_needs_rebuild(src, &e.object),
        Op::CreateTable | Op::DropTable | Op::AddColumn => false,
    }
}

fn index_needs_rebuild(t: Option<&TableSnapshot>, object: &str) -> bool {
    match t {
        Some(t) => match find_index(t, object) {
            Some(i) => i.primary || is_auto_index(&i.name),
            None => is_auto_index(object),
        },
        None => is_auto_index(object),
    }
}

/// 单列 INTEGER 主键：SQLite 里它是 rowid 别名，而 `AUTOINCREMENT` 不在快照里。
fn has_integer_pk(kind: DatabaseKind, t: &TableSnapshot) -> bool {
    let pk = pk_columns(t);
    if pk.len() != 1 {
        return false;
    }
    match column_of(t, &pk[0]) {
        Some(c) => normalize_type(kind, &c.data_type) == "integer",
        None => false,
    }
}

fn create_warnings(kind: DatabaseKind, t: &TableSnapshot) -> Vec<String> {
    let mut w = vec![W_CREATE_TABLE.to_string()];
    if has_integer_pk(kind, t) {
        w.push(W_AUTOINCREMENT.to_string());
    }
    w
}

fn rebuild_warnings(
    kind: DatabaseKind,
    tgt: &TableSnapshot,
    src: &TableSnapshot,
    views: &[String],
) -> Vec<String> {
    let mut w = vec![
        W_TRIGGERS.to_string(),
        W_GENERATED_COLUMNS.to_string(),
        W_CHECK_CONSTRAINTS.to_string(),
    ];
    if has_integer_pk(kind, tgt) {
        w.push(W_AUTOINCREMENT.to_string());
    }
    let sc = column_map(src);
    let has_new_not_null = ordered_columns(tgt).into_iter().any(|c| {
        !c.nullable
            && literal_default(c.default_value.as_deref()).is_none()
            && !sc.contains_key(&lower_ascii(&c.name))
    });
    if has_new_not_null {
        w.push(W_REBUILD_NEW_NOT_NULL.to_string());
    }
    if !views.is_empty() {
        w.push(w_view_rebuild(views));
    }
    w
}

/// 重建用的临时表名：与任一侧快照里的**表或视图**重名时追加序号
/// （撞视图同样会让首条 `CREATE TABLE` 失败，A.15 G17）。
fn tmp_name(base: &str, taken: &BTreeSet<String>) -> String {
    let mut cand = format!("polydb_tmp_{base}");
    let mut n = 2usize;
    while taken.contains(&lower_ascii(&cand)) {
        cand = format!("polydb_tmp_{base}_{n}");
        n += 1;
    }
    cand
}

/// 一次 `emit_script` 在**被执行侧**看得见的名字。
///
/// `taken` 是临时表必须避让的已占用名（表名 + 视图名，G17）；`views` 是被执行侧的视图名单，
/// 只用来出视图告警（G13/G16）。两者同源于同一对快照，故合并传参。
struct Seen {
    taken: BTreeSet<String>,
    views: Vec<String>,
}

/// 重建一张表：建新表 → 搬同名列 → 删旧表 → 改名 → 补用户索引。
///
/// 整组语句的 `phase` 取该表在计划里**第一个**条目的相位（组的输出位置就在那里），
/// `entry_ids` 是该表被吸收的全部条目。
fn rebuild_statements(
    kind: DatabaseKind,
    tgt: &TableSnapshot,
    src: &TableSnapshot,
    seen: &Seen,
    entry_ids: &[String],
    phase: u8,
    non_reversible: bool,
) -> Vec<Statement> {
    let tmp = tmp_name(&tgt.name, &seen.taken);
    let sc = column_map(src);
    let common: Vec<String> = ordered_columns(tgt)
        .into_iter()
        .filter(|c| sc.contains_key(&lower_ascii(&c.name)))
        .map(|c| c.name.clone())
        .collect();
    let list = join_idents(&common);
    let mut out = vec![
        mk_stmt(
            entry_ids,
            format!(
                "CREATE TABLE {}({});",
                quote_ident(&tmp),
                create_table_body(kind, tgt)
            ),
            phase,
            &tgt.name,
            rebuild_warnings(kind, tgt, src, &seen.views),
            non_reversible,
        ),
        mk_stmt(
            entry_ids,
            format!(
                "INSERT INTO {}({list}) SELECT {list} FROM {};",
                quote_ident(&tmp),
                quote_ident(&src.name)
            ),
            phase,
            &tgt.name,
            Vec::new(),
            non_reversible,
        ),
        mk_stmt(
            entry_ids,
            format!("DROP TABLE {};", quote_ident(&src.name)),
            phase,
            &tgt.name,
            Vec::new(),
            non_reversible,
        ),
        mk_stmt(
            entry_ids,
            format!(
                "ALTER TABLE {} RENAME TO {};",
                quote_ident(&tmp),
                quote_ident(&tgt.name)
            ),
            phase,
            &tgt.name,
            Vec::new(),
            non_reversible,
        ),
    ];
    let mut user: Vec<(String, String)> = tgt
        .indexes
        .iter()
        .filter(|i| !i.primary && !is_auto_index(&i.name))
        .map(|i| {
            (
                lower_ascii(&index_exec_name(tgt, i)),
                create_index_sql(tgt, i),
            )
        })
        .collect();
    user.sort_by(|a, b| a.0.cmp(&b.0));
    out.extend(
        user.into_iter()
            .map(|(_, sql)| mk_stmt(entry_ids, sql, phase, &tgt.name, Vec::new(), non_reversible)),
    );
    out
}

/// 直改动作 → 单条语句。
fn direct_statement(
    kind: DatabaseKind,
    e: &Entry,
    src: Option<&TableSnapshot>,
    tgt: Option<&TableSnapshot>,
    views: &[String],
) -> CoreResult<Statement> {
    let one = vec![e.id.clone()];
    let nr = entry_non_reversible(e);
    match e.op {
        Op::CreateTable => {
            let t = table_or_err(tgt, &e.table)?;
            Ok(mk_stmt(
                &one,
                format!(
                    "CREATE TABLE {}({});",
                    quote_ident(&t.name),
                    create_table_body(kind, t)
                ),
                e.phase,
                &e.table,
                create_warnings(kind, t),
                nr,
            ))
        }
        Op::DropTable => {
            // 库里一个视图都没有 ⇒ 没什么可警告的，保持原有的空警告。
            let warnings: Vec<String> = if views.is_empty() {
                Vec::new()
            } else {
                vec![w_view_drop(views)]
            };
            Ok(mk_stmt(
                &one,
                format!("DROP TABLE {};", quote_ident(&e.table)),
                e.phase,
                &e.table,
                warnings,
                nr,
            ))
        }
        Op::AddColumn => {
            let t = table_or_err(tgt, &e.table)?;
            let c = column_of(t, &e.object).ok_or_else(|| {
                invalid(format!(
                    "快照中缺少列 {}.{}，无法生成 DDL",
                    e.table, e.object
                ))
            })?;
            let risky = !is_constant_default(c.default_value.as_deref())
                || (!c.nullable && literal_default(c.default_value.as_deref()).is_none());
            let warnings = if risky {
                vec![W_ADD_COLUMN_LIMITS.to_string()]
            } else {
                Vec::new()
            };
            Ok(mk_stmt(
                &one,
                format!(
                    "ALTER TABLE {} ADD COLUMN {};",
                    quote_ident(&e.table),
                    column_def(kind, c)
                ),
                e.phase,
                &e.table,
                warnings,
                nr,
            ))
        }
        Op::DropColumn => Ok(mk_stmt(
            &one,
            format!(
                "ALTER TABLE {} DROP COLUMN {};",
                quote_ident(&e.table),
                quote_ident(e.object.trim())
            ),
            e.phase,
            &e.table,
            vec![W_DROP_COLUMN_CHECK.to_string()],
            nr,
        )),
        Op::AddIndex => {
            let t = table_or_err(tgt, &e.table)?;
            let idx = find_index(t, &e.object).ok_or_else(|| {
                invalid(format!(
                    "快照中缺少索引 {}.{}，无法生成 DDL",
                    e.table, e.object
                ))
            })?;
            Ok(mk_stmt(
                &one,
                create_index_sql(t, idx),
                e.phase,
                &e.table,
                Vec::new(),
                nr,
            ))
        }
        Op::DropIndex => {
            // 名字以快照为准；快照里无名的索引（手写快照的未命名 UNIQUE）合成可执行名，
            // 与 CREATE INDEX 用同一套规则，回滚才不会 DROP 一个不存在的对象。
            let name = match src {
                Some(t) => match find_index(t, &e.object) {
                    Some(i) => index_exec_name(t, i),
                    None => e.object.trim().to_string(),
                },
                None => e.object.trim().to_string(),
            };
            Ok(mk_stmt(
                &one,
                format!("DROP INDEX {};", quote_ident(&name)),
                e.phase,
                &e.table,
                Vec::new(),
                nr,
            ))
        }
        Op::AlterColumn | Op::AddForeignKey | Op::DropForeignKey => Err(invalid(format!(
            "{} 动作必须走重建路径，不该单独成语句",
            e.op.as_str()
        ))),
    }
}

/// 生成一个方向的脚本。返回 (语句列表, 本方向实际重建的表键)。
///
/// `forced` 是**必须**整表重建的表键集合：回滚方向用它承接正向的重建（见 `generate`）——
/// 正向一旦重建过某表，该表的原始定义就已消失，回滚只能用同一套重建规则还原，
/// 直改语句（如 `ADD COLUMN`）会把重建时被丢弃的索引/约束永久留在库外。
fn emit_script(
    kind: DatabaseKind,
    source: &Snapshot,
    target: &Snapshot,
    entries: &[Entry],
    forced: &BTreeSet<String>,
) -> CoreResult<(Vec<Statement>, BTreeSet<String>)> {
    let src = table_map(source);
    let tgt = table_map(target);
    // 警告只报**被改的那一侧**（source 是执行侧）的视图：回滚方向会互换两侧，
    // 因此回滚脚本的视图警告按回滚真正要动的库来出。
    let views = view_names(source);
    let mut taken: BTreeSet<String> = BTreeSet::new();
    taken.extend(src.keys().cloned());
    taken.extend(tgt.keys().cloned());
    // 视图名也要占位：临时表撞上**视图**同样让首条 CREATE TABLE 失败（A.15 G17，
    // 而 `table_map` 为了「视图不参与比对」把它们跳掉了，所以这里得单独取一次）。
    taken.extend(views.iter().map(|n| lower_ascii(n)));
    taken.extend(view_names(target).into_iter().map(|n| lower_ascii(&n)));
    let seen = Seen { taken, views };

    // 1. 哪些表必须整体重建（条目自身触发，或来自 forced——回滚方向承接正向的重建）
    let mut rebuild: BTreeSet<String> = BTreeSet::new();
    for e in entries {
        let k = lower_ascii(&e.table);
        if forced.contains(&k) || needs_rebuild(e, src.get(&k).copied(), tgt.get(&k).copied()) {
            rebuild.insert(k);
        }
    }

    // 2. 重建表吸收同表全部条目：ids 按条目顺序、phase 取第一个、不可逆取或
    let mut absorbed: BTreeMap<String, (Vec<String>, u8, bool)> = BTreeMap::new();
    for e in entries {
        let k = lower_ascii(&e.table);
        if !rebuild.contains(&k) {
            continue;
        }
        let slot = absorbed
            .entry(k)
            .or_insert_with(|| (Vec::new(), e.phase, false));
        slot.0.push(e.id.clone());
        slot.2 |= entry_non_reversible(e);
    }

    // 3. 按条目顺序输出（E1 的计划顺序已含建表拓扑，这里不再重排）
    let mut out: Vec<Statement> = Vec::new();
    let mut done: BTreeSet<String> = BTreeSet::new();
    for e in entries {
        let k = lower_ascii(&e.table);
        if let Some((ids, phase, nr)) = absorbed.get(&k) {
            if !done.insert(k.clone()) {
                continue;
            }
            let t = table_or_err(tgt.get(&k).copied(), &e.table)?;
            let s = table_or_err(src.get(&k).copied(), &e.table)?;
            out.extend(rebuild_statements(kind, t, s, &seen, ids, *phase, *nr));
            continue;
        }
        out.push(direct_statement(
            kind,
            e,
            src.get(&k).copied(),
            tgt.get(&k).copied(),
            &seen.views,
        )?);
    }

    // 4. 含重建 ⇒ 首尾 PRAGMA 包裹（必须留在事务外，取证 G5 + G6）
    if rebuild.is_empty() {
        return Ok((out, rebuild));
    }
    let mut wrapped = Vec::with_capacity(out.len() + 2);
    wrapped.push(pragma_stmt(PRAGMA_FK_OFF, PHASE_PRAGMA_FIRST));
    wrapped.append(&mut out);
    wrapped.push(pragma_stmt(PRAGMA_FK_ON, PHASE_PRAGMA_LAST));
    Ok((wrapped, rebuild))
}

fn inverse_op(op: Op) -> Op {
    crate::ops_gen::lookup_op_row(op)
        .map(|r| r.inverse)
        .unwrap_or(op)
}

/// 正向条目 id → 反向条目 id：只替换最后一段 op。
///
/// 对象名在镜像比对里保持不变——E1 的 `add_*` 取目标侧对象、`drop_*` 取源侧对象，
/// 互换两侧后取到的正是同一个对象，所以 id 的表段与对象段都能对上。
fn mirror_id(id: &str) -> String {
    match id.rsplit_once('/') {
        Some((head, tail)) => match Op::from_wire(tail) {
            Some(op) => format!("{head}/{}", inverse_op(op).as_str()),
            None => id.to_string(),
        },
        None => id.to_string(),
    }
}

fn check_kind(source: &Snapshot, target: &Snapshot) -> CoreResult<DatabaseKind> {
    for snap in [source, target] {
        if snap.kind == DatabaseKind::Redis {
            return Err(CoreError::NotSupported(
                "Redis 无模式概念，不参与结构比对".into(),
            ));
        }
    }
    if source.kind != target.kind {
        return Err(CoreError::NotSupported(format!(
            "跨种类比对未支持：{} vs {}（v1 只比同一种数据库）",
            source.kind, target.kind
        )));
    }
    if !crate::ops_gen::ddl_supported(&source.kind.to_string()) {
        return Err(CoreError::NotSupported(format!(
            "{} 方言的 DDL 生成在 v1 未实现（当前只生成 {}）",
            source.kind,
            crate::ops_gen::OP_DDL_DIALECTS.join("/")
        )));
    }
    Ok(source.kind)
}

/// 由（两侧快照, 已勾选条目）生成 **SQLite** 的语句脚本与回滚脚本。
///
/// `entries` 是已勾选的变更条目，正常由 [`select_entries`] 从 `diff(source, target, opts)`
/// 里筛出（它同时锁定「重建表必须整组勾选」）；`opts` 只用于反向镜像重算，
/// 使回滚与正向严格对称。
///
/// 回滚 = source/target 互换后按同一套规则再生成（定案 §8-2），
/// 因此不可逆标记也按同一规则打在回滚语句上：例如正向 `create_table` 的回滚是
/// `drop_table`，回滚脚本自身也就成了不可逆动作。
pub fn generate(
    source: &Snapshot,
    target: &Snapshot,
    entries: &[Entry],
    opts: &Options,
) -> CoreResult<Script> {
    let kind = check_kind(source, target)?;
    let (statements, rebuilt) = emit_script(kind, source, target, entries, &BTreeSet::new())?;
    let want: BTreeSet<String> = entries.iter().map(|e| mirror_id(&e.id)).collect();
    let reverse: Vec<Entry> = crate::diff(target, source, opts)?
        .into_iter()
        .filter(|e| want.contains(&e.id))
        .collect();
    // 正向重建过的表，回滚也必须重建：原表定义已不在库里，直改语句补不回索引与约束。
    let (rollback, rebuilt_rev) = emit_script(kind, target, source, &reverse, &rebuilt)?;
    Ok(Script {
        statements,
        rollback,
        pragmas_outside_tx: !rebuilt.is_empty() || !rebuilt_rev.is_empty(),
        ddl_in_tx: true,
    })
}

/// 按勾选 id 从**全量计划**里取出子集，并锁定两条选择规则。
///
/// 1. 每个 id 必须真实出现在 `plan` 里，否则计划已过期（两侧结构在勾选后又变了），
///    调用方必须重新比对——绝不能拿旧 id 去解释新计划。
/// 2. 进入整表重建的表，其**全部**条目必须一起勾选：重建以目标快照为准生成新表定义，
///    同表被漏掉的变更会被悄悄带进新表里（用户以为只勾了两条，实则是三条）。
///
/// `plan` 必须是 `diff(source, target, opts)` 的原始输出（顺序即相位序）。
pub fn select_entries(
    source: &Snapshot,
    target: &Snapshot,
    plan: &[Entry],
    selected: &[String],
) -> CoreResult<Vec<Entry>> {
    let want: BTreeSet<&str> = selected.iter().map(String::as_str).collect();
    // 两个索引一次建好：id ⇒ 是否在计划里，表键 ⇒ 该表全部条目 id（按计划顺序入桶）。
    let mut in_plan: BTreeSet<&str> = BTreeSet::new();
    let mut ids_by_table: BTreeMap<String, Vec<&str>> = BTreeMap::new();
    for e in plan {
        in_plan.insert(e.id.as_str());
        ids_by_table
            .entry(lower_ascii(&e.table))
            .or_default()
            .push(e.id.as_str());
    }
    let mut stale: Vec<&str> = Vec::new();
    let mut seen_stale: BTreeSet<&str> = BTreeSet::new();
    for id in selected {
        if !in_plan.contains(id.as_str()) && seen_stale.insert(id.as_str()) {
            stale.push(id.as_str());
        }
    }
    if !stale.is_empty() {
        return Err(invalid(format!(
            "勾选的条目不在当前比对计划里：{}（请重新比对后再执行）",
            stale.join("、")
        )));
    }
    let src = table_map(source);
    let tgt = table_map(target);
    let picked: Vec<&Entry> = plan
        .iter()
        .filter(|e| want.contains(e.id.as_str()))
        .collect();
    let mut rebuild: BTreeSet<String> = BTreeSet::new();
    for e in &picked {
        let k = lower_ascii(&e.table);
        if needs_rebuild(e, src.get(&k).copied(), tgt.get(&k).copied()) {
            rebuild.insert(k);
        }
    }
    // 按计划的表出现顺序报错，保证双端逐字一致
    let mut seen: BTreeSet<String> = BTreeSet::new();
    for e in &picked {
        let k = lower_ascii(&e.table);
        if !rebuild.contains(&k) || !seen.insert(k.clone()) {
            continue;
        }
        let mut missing: Vec<&str> = Vec::new();
        if let Some(ids) = ids_by_table.get(&k) {
            missing.extend(ids.iter().copied().filter(|id| !want.contains(id)));
        }
        if !missing.is_empty() {
            return Err(invalid(format!(
                "表 {} 需要整表重建，同表变更必须整组勾选：缺少 {}",
                e.table,
                missing.join("、")
            )));
        }
    }
    Ok(picked.into_iter().cloned().collect())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{diff, ColumnFlag, TableType};
    use polydb_core::{ForeignKeyAction, IndexColumn};

    fn col(name: &str, ty: &str, nullable: bool, ord: i32) -> ColumnInfo {
        ColumnInfo {
            name: name.into(),
            data_type: ty.into(),
            generic_type: None,
            nullable,
            default_value: None,
            max_length: None,
            precision: None,
            scale: None,
            is_primary_key: false,
            is_auto_increment: false,
            comment: None,
            ordinal_position: ord,
        }
    }

    fn pk_col(name: &str, ty: &str) -> ColumnInfo {
        let mut c = col(name, ty, false, 1);
        c.is_primary_key = true;
        c
    }

    fn with_default(mut c: ColumnInfo, d: &str) -> ColumnInfo {
        c.default_value = Some(d.into());
        c
    }

    fn idx(name: &str, primary: bool, unique: bool, cols: &[&str]) -> IndexInfo {
        IndexInfo {
            name: name.into(),
            unique,
            primary,
            index_type: None,
            columns: cols
                .iter()
                .enumerate()
                .map(|(i, c)| IndexColumn {
                    name: c.to_string(),
                    position: i as i32,
                    order: None,
                    prefix_length: None,
                })
                .collect(),
            comment: None,
        }
    }

    fn fk(cols: &[&str], refs: &str, ref_cols: &[&str]) -> polydb_core::ForeignKeyInfo {
        polydb_core::ForeignKeyInfo {
            name: String::new(),
            columns: cols.iter().map(|c| c.to_string()).collect(),
            referenced_schema: "main".into(),
            referenced_table: refs.into(),
            referenced_columns: ref_cols.iter().map(|c| c.to_string()).collect(),
            on_update: None,
            on_delete: None,
        }
    }

    fn table(name: &str, columns: Vec<ColumnInfo>) -> TableSnapshot {
        TableSnapshot {
            name: name.into(),
            table_type: Some(TableType::Table),
            columns,
            indexes: Vec::new(),
            foreign_keys: Vec::new(),
        }
    }

    fn with_indexes(mut t: TableSnapshot, indexes: Vec<IndexInfo>) -> TableSnapshot {
        t.indexes = indexes;
        t
    }

    fn with_fks(mut t: TableSnapshot, fks: Vec<polydb_core::ForeignKeyInfo>) -> TableSnapshot {
        t.foreign_keys = fks;
        t
    }

    fn snap(kind: DatabaseKind, tables: Vec<TableSnapshot>) -> Snapshot {
        Snapshot::new(kind, "main", tables)
    }

    fn entry(op: Op, table: &str, object: &str) -> Entry {
        Entry {
            id: format!(
                "{}/{}/{}",
                lower_ascii(table),
                lower_ascii(object),
                op.as_str()
            ),
            op,
            phase: op.phase(),
            table: table.into(),
            object: object.into(),
            from: None,
            to: None,
            flags: Vec::new(),
            destructive: op.destructive(),
            selected: true,
            reason: String::new(),
        }
    }

    fn sqls(v: &[Statement]) -> Vec<String> {
        v.iter().map(|s| s.sql.clone()).collect()
    }

    fn one(stmts: &[Statement]) -> String {
        assert_eq!(stmts.len(), 1, "期望单条语句：{:?}", sqls(stmts));
        stmts[0].sql.clone()
    }

    fn gen(source: &Snapshot, target: &Snapshot, entries: &[Entry]) -> Script {
        generate(source, target, entries, &Options::default()).unwrap()
    }

    /// 用真实 diff 的输出喂生成器（勾选全部条目），断言两端镜像关系。
    fn gen_from_diff(source: &Snapshot, target: &Snapshot) -> Script {
        let opts = Options {
            select_destructive: true,
            ..Default::default()
        };
        let entries = diff(source, target, &opts).unwrap();
        generate(source, target, &entries, &opts).unwrap()
    }

    #[test]
    fn create_table_is_rendered_from_the_target_snapshot() {
        let tgt = table(
            "u",
            vec![
                pk_col("id", "INTEGER"),
                col("name", "varchar(20)", true, 2),
                with_default(col("status", "text", false, 3), "'new'"),
            ],
        );
        let s = snap(
            DatabaseKind::Sqlite,
            vec![with_indexes(
                tgt,
                vec![idx("sqlite_autoindex_u_1", false, true, &["name"])],
            )],
        );
        let src = snap(DatabaseKind::Sqlite, Vec::new());
        let out = gen(&src, &s, &[entry(Op::CreateTable, "u", "")]);
        assert_eq!(
            one(&out.statements),
            "CREATE TABLE \"u\"(\"id\" integer NOT NULL,\"name\" varchar(20),\"status\" text NOT NULL DEFAULT 'new',PRIMARY KEY (\"id\"),UNIQUE (\"name\"));"
        );
        let st = &out.statements[0];
        assert_eq!(st.entry_ids, ["u//create_table"]);
        assert_eq!(st.phase, 50);
        assert_eq!(st.table, "u");
        assert!(!st.non_reversible);
        // 单列 INTEGER 主键 ⇒ 额外提示 AUTOINCREMENT 无法还原
        assert_eq!(
            st.warnings,
            [W_CREATE_TABLE.to_string(), W_AUTOINCREMENT.to_string()]
        );
        // 回滚 = 互换后同规则生成：建表的回滚就是删表，且按规则 6 不可逆
        assert_eq!(sqls(&out.rollback), ["DROP TABLE \"u\";"]);
        assert!(out.rollback[0].non_reversible);
        assert_eq!(out.rollback[0].entry_ids, ["u//drop_table"]);
        assert!(!out.pragmas_outside_tx);
        assert!(out.ddl_in_tx);
    }

    #[test]
    fn column_and_index_ops_apply_directly() {
        let src = with_indexes(
            table(
                "t",
                vec![col("a", "text", true, 1), col("gone", "text", true, 2)],
            ),
            vec![idx("idx_gone", false, false, &["a"])],
        );
        let tgt = with_indexes(
            table(
                "t",
                vec![
                    col("a", "text", true, 1),
                    with_default(col("b", "text", true, 2), "'x'"),
                ],
            ),
            vec![idx("idx_a", false, false, &["a"])],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let entries = [
            entry(Op::DropIndex, "t", "idx_gone"),
            entry(Op::DropColumn, "t", "gone"),
            entry(Op::AddColumn, "t", "b"),
            entry(Op::AddIndex, "t", "idx_a"),
        ];
        let out = gen(&s, &tt, &entries);
        assert_eq!(
            sqls(&out.statements),
            [
                "DROP INDEX \"idx_gone\";",
                "ALTER TABLE \"t\" DROP COLUMN \"gone\";",
                "ALTER TABLE \"t\" ADD COLUMN \"b\" text DEFAULT 'x';",
                "CREATE INDEX \"idx_a\" ON \"t\"(\"a\");"
            ]
        );
        assert_eq!(
            out.statements[1].warnings,
            [W_DROP_COLUMN_CHECK.to_string()]
        );
        assert!(out.statements[1].non_reversible);
        assert!(!out.statements[0].non_reversible);
        assert!(!out.statements[2].non_reversible);
        assert!(out.statements[2].warnings.is_empty());
        assert!(!out.pragmas_outside_tx);
    }

    #[test]
    fn alter_column_rebuilds_and_wraps_pragmas() {
        let src = table(
            "t",
            vec![pk_col("id", "INTEGER"), col("name", "varchar(20)", true, 2)],
        );
        let tgt = table(
            "t",
            vec![pk_col("id", "INTEGER"), col("name", "varchar(30)", true, 2)],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let mut e = entry(Op::AlterColumn, "t", "name");
        e.flags = vec![ColumnFlag::Type];
        let out = gen(&s, &tt, &[e]);
        assert_eq!(
            sqls(&out.statements),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,\"name\" varchar(30),PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\",\"name\") SELECT \"id\",\"name\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "PRAGMA foreign_keys=ON;"
            ]
        );
        assert!(out.pragmas_outside_tx);
        assert_eq!(out.statements[0].phase, 0);
        assert_eq!(out.statements[5].phase, 255);
        assert!(out.statements[0].entry_ids.is_empty());
        let head = &out.statements[1];
        assert_eq!(head.phase, 70);
        assert_eq!(head.entry_ids, ["t/name/alter_column"]);
        assert!(head.non_reversible, "type 变更不可逆（规则 6）");
        assert_eq!(
            head.warnings,
            [
                W_TRIGGERS.to_string(),
                W_GENERATED_COLUMNS.to_string(),
                W_CHECK_CONSTRAINTS.to_string(),
                W_AUTOINCREMENT.to_string(),
            ]
        );
        // 只有默认值变化的 alter_column 可逆
        let mut e2 = entry(Op::AlterColumn, "t", "name");
        e2.flags = vec![ColumnFlag::Default];
        let out2 = gen(&s, &tt, &[e2]);
        assert!(!out2.statements[1].non_reversible);
        // 反向脚本用同一套规则，只是基准换成源侧定义
        assert_eq!(
            sqls(&out.rollback),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,\"name\" varchar(20),PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\",\"name\") SELECT \"id\",\"name\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "PRAGMA foreign_keys=ON;"
            ]
        );
    }

    #[test]
    fn drop_column_on_indexed_or_primary_column_rebuilds() {
        // 被索引引用 → 重建；纯列 → 直改（取证 G2）
        let src = with_indexes(
            table(
                "t",
                vec![pk_col("id", "INTEGER"), col("note", "text", true, 2)],
            ),
            vec![idx("idx_note", false, false, &["note"])],
        );
        let tgt = table("t", vec![pk_col("id", "INTEGER")]);
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let out = gen(&s, &tt, &[entry(Op::DropColumn, "t", "note")]);
        assert!(out.pragmas_outside_tx);
        assert_eq!(out.statements.len(), 6);
        assert_eq!(
            out.statements[1].sql,
            "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,PRIMARY KEY (\"id\"));"
        );
        // 主键列本身也走重建
        let src2 = table("t", vec![pk_col("id", "INTEGER")]);
        let tgt2 = table("t", vec![col("x", "text", true, 2)]);
        let out2 = gen(
            &snap(DatabaseKind::Sqlite, vec![src2]),
            &snap(DatabaseKind::Sqlite, vec![tgt2]),
            &[entry(Op::DropColumn, "t", "id")],
        );
        assert!(out2.pragmas_outside_tx);
    }

    #[test]
    fn autoindex_changes_rebuild_and_restore_as_table_constraint() {
        // sqlite_autoindex_* 既不能 CREATE 也不能 DROP（取证 G12/G13）→ 重建 + UNIQUE 还原
        let src = table(
            "t",
            vec![col("a", "text", true, 1), col("b", "text", true, 2)],
        );
        let tgt = with_indexes(
            table(
                "t",
                vec![col("a", "text", true, 1), col("b", "text", true, 2)],
            ),
            vec![idx("sqlite_autoindex_t_1", false, true, &["a", "b"])],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let out = gen(&s, &tt, &[entry(Op::AddIndex, "t", "sqlite_autoindex_t_1")]);
        assert!(out.pragmas_outside_tx);
        assert_eq!(out.statements.len(), 6);
        assert_eq!(
            out.statements[1].sql,
            "CREATE TABLE \"polydb_tmp_t\"(\"a\" text,\"b\" text,UNIQUE (\"a\",\"b\"));"
        );
        // 删除自动索引同样走重建
        let out2 = gen(
            &tt,
            &s,
            &[entry(Op::DropIndex, "t", "sqlite_autoindex_t_1")],
        );
        assert!(out2.pragmas_outside_tx);
        assert_eq!(
            out2.statements[1].sql,
            "CREATE TABLE \"polydb_tmp_t\"(\"a\" text,\"b\" text);"
        );
    }

    #[test]
    fn foreign_key_ops_rebuild_with_inline_clause() {
        let src = table("orders", vec![col("uid", "integer", true, 1)]);
        let tgt = with_fks(
            table("orders", vec![col("uid", "integer", true, 1)]),
            vec![{
                let mut f = fk(&["uid"], "users", &["id"]);
                f.on_delete = Some(ForeignKeyAction::Cascade);
                f.on_update = Some(ForeignKeyAction::NoAction);
                f
            }],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let out = gen(&s, &tt, &[entry(Op::AddForeignKey, "orders", "fk(uid)")]);
        assert_eq!(out.statements.len(), 6);
        assert_eq!(
            out.statements[1].sql,
            "CREATE TABLE \"polydb_tmp_orders\"(\"uid\" integer,FOREIGN KEY (\"uid\") REFERENCES \"users\"(\"id\") ON DELETE CASCADE ON UPDATE NO ACTION);"
        );
    }

    #[test]
    fn rebuild_group_absorbs_every_entry_of_the_table() {
        // 同一张表上：删列 + 改列 + 建索引（非自动）→ 只重建一次，三条条目都挂在组上
        let src = with_indexes(
            table(
                "t",
                vec![pk_col("id", "INTEGER"), col("gone", "text", true, 2)],
            ),
            Vec::new(),
        );
        let tgt = with_indexes(
            table(
                "t",
                vec![pk_col("id", "INTEGER"), col("name", "text", false, 3)],
            ),
            vec![idx("idx_name", false, false, &["name"])],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let mut alter = entry(Op::AlterColumn, "t", "name");
        alter.flags = vec![ColumnFlag::Nullability];
        let entries = [
            entry(Op::DropColumn, "t", "gone"),
            alter,
            entry(Op::AddIndex, "t", "idx_name"),
        ];
        let out = gen(&s, &tt, &entries);
        assert_eq!(
            out.statements.len(),
            7,
            "PRAGMA×2 + 重建 4 条 + 补索引 1 条"
        );
        assert_eq!(
            sqls(&out.statements)[5],
            "CREATE INDEX \"idx_name\" ON \"t\"(\"name\");"
        );
        let ids = out.statements[1].entry_ids.clone();
        assert_eq!(
            ids,
            [
                "t/gone/drop_column".to_string(),
                "t/name/alter_column".to_string(),
                "t/idx_name/add_index".to_string()
            ]
        );
        assert!(out.statements[1..6].iter().all(|s| s.non_reversible));
        assert!(out.pragmas_outside_tx);
    }

    #[test]
    fn tmp_table_name_avoids_existing_tables() {
        let mut extra = table("polydb_tmp_t", vec![col("z", "text", true, 1)]);
        extra.table_type = Some(TableType::Table);
        let src = table("t", vec![col("a", "text", true, 1)]);
        let mut tgt = table("t", vec![col("a", "text", false, 1)]);
        tgt.indexes = vec![idx("sqlite_autoindex_t_1", false, true, &["a"])];
        let s = snap(DatabaseKind::Sqlite, vec![src, extra]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt]);
        let out = gen(&s, &tt, &[entry(Op::AddIndex, "t", "sqlite_autoindex_t_1")]);
        assert!(out.statements[1].sql.contains("\"polydb_tmp_t_2\""));
    }

    fn view(name: &str) -> TableSnapshot {
        let mut t = table(name, Vec::new());
        t.table_type = Some(TableType::View);
        t
    }

    #[test]
    fn view_names_sort_by_lowercase_and_dedup_case_insensitively() {
        let s = snap(
            DatabaseKind::Sqlite,
            vec![
                table("t", vec![col("a", "text", true, 1)]),
                view("v_b"),
                view("V_A"),
                view("v_a"),
                view("v_c"),
            ],
        );
        // 键取小写 ⇒ 顺序按小写字节；同名只留首个，与 table_map 的 or_insert 同规则
        assert_eq!(view_names(&s), ["V_A", "v_b", "v_c"]);
        let single = snap(
            DatabaseKind::Sqlite,
            vec![table("t", Vec::new()), view("v_a")],
        );
        assert_eq!(view_names(&single), ["v_a"]);
        let no_view = snap(DatabaseKind::Sqlite, vec![table("t", Vec::new())]);
        assert!(view_names(&no_view).is_empty());
    }

    #[test]
    fn view_scope_lists_at_most_five_names() {
        let many: Vec<String> = (0..7).map(|i| format!("v_{i}")).collect();
        assert_eq!(view_scope(&many), "7 个视图（v_0, v_1, v_2, v_3, v_4 等）");
        assert_eq!(view_scope(&["v_a".into()]), "1 个视图（v_a）");
        assert_eq!(
            view_scope(&["v_a".into(), "v_b".into()]),
            "2 个视图（v_a, v_b）"
        );
    }

    #[test]
    fn drop_table_and_rebuild_warn_about_views_on_the_executed_side() {
        let src = snap(
            DatabaseKind::Sqlite,
            vec![
                table(
                    "t",
                    vec![pk_col("id", "INTEGER"), col("a", "text", true, 2)],
                ),
                view("z_report"),
                view("a_report"),
                view("polydb_tmp_t"),
            ],
        );
        let out = gen(
            &src,
            &snap(DatabaseKind::Sqlite, Vec::new()),
            &[entry(Op::DropTable, "t", "")],
        );
        assert_eq!(out.statements[0].warnings, [w_view_drop(&view_names(&src))]);
        // 目标侧没有视图 ⇒ 回滚（在目标侧建表）不该带视图警告
        assert!(out.rollback[0]
            .warnings
            .contains(&W_CREATE_TABLE.to_string()));
        assert!(!out.rollback[0].warnings.iter().any(|w| w.contains("视图")));

        let mut tgt = table(
            "t",
            vec![pk_col("id", "INTEGER"), col("a", "text", false, 2)],
        );
        tgt.indexes = vec![idx("sqlite_autoindex_t_1", false, true, &["a"])];
        let out2 = gen(
            &src,
            &snap(DatabaseKind::Sqlite, vec![tgt]),
            &[entry(Op::AddIndex, "t", "sqlite_autoindex_t_1")],
        );
        let head = &out2.statements[1];
        assert_eq!(
            head.warnings.last().unwrap(),
            &w_view_rebuild(&view_names(&src))
        );
        // 视图名占掉了 polydb_tmp_t ⇒ 临时表改名，且警告里点的是视图名
        assert!(head.sql.contains("\"polydb_tmp_t_2\""));
        assert!(head.warnings.iter().any(|w| {
            w.contains("a_report") && w.contains("z_report") && w.contains("重建经 RENAME")
        }));
    }

    #[test]
    fn no_views_means_no_view_warning_anywhere() {
        let src = snap(
            DatabaseKind::Sqlite,
            vec![table("t", vec![col("a", "text", true, 1)])],
        );
        let tgt = snap(
            DatabaseKind::Sqlite,
            vec![table("t", vec![col("a", "text", false, 1)])],
        );
        let out = gen(&src, &tgt, &[entry(Op::DropTable, "t", "")]);
        assert!(out.statements[0].warnings.is_empty());
        let mut e = entry(Op::AlterColumn, "t", "a");
        e.flags = vec![ColumnFlag::Nullability];
        let out2 = gen(&src, &tgt, &[e]);
        for st in out2.statements.iter().chain(out2.rollback.iter()) {
            assert!(
                !st.warnings.iter().any(|w| w.contains("视图")),
                "无视图时不该出现视图警告：{:?}",
                st.warnings
            );
        }
        assert!(view_names(&src).is_empty());
    }

    #[test]
    fn add_column_with_risky_definition_warns() {
        let src = table("t", vec![col("a", "text", true, 1)]);
        let tgt_nn = table(
            "t",
            vec![col("a", "text", true, 1), col("b", "text", false, 2)],
        );
        let tgt_expr = table(
            "t",
            vec![
                col("a", "text", true, 1),
                with_default(col("b", "text", true, 2), "current_timestamp"),
            ],
        );
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let out = gen(
            &s,
            &snap(DatabaseKind::Sqlite, vec![tgt_nn]),
            &[entry(Op::AddColumn, "t", "b")],
        );
        assert_eq!(
            one(&out.statements),
            "ALTER TABLE \"t\" ADD COLUMN \"b\" text NOT NULL;"
        );
        assert_eq!(
            out.statements[0].warnings,
            [W_ADD_COLUMN_LIMITS.to_string()]
        );
        let out2 = gen(
            &s,
            &snap(DatabaseKind::Sqlite, vec![tgt_expr]),
            &[entry(Op::AddColumn, "t", "b")],
        );
        assert_eq!(
            out2.statements[0].warnings,
            [W_ADD_COLUMN_LIMITS.to_string()]
        );
    }

    #[test]
    fn constant_default_recognition() {
        assert!(is_constant_default(None));
        assert!(is_constant_default(Some("NULL")));
        assert!(is_constant_default(Some("0")));
        assert!(is_constant_default(Some("-1")));
        assert!(is_constant_default(Some("+1.5")));
        assert!(is_constant_default(Some("1e3")));
        assert!(is_constant_default(Some("'it''s'")));
        assert!(is_constant_default(Some("x'1f'")));
        assert!(!is_constant_default(Some("current_timestamp")));
        assert!(!is_constant_default(Some("(1+2)")));
        assert!(!is_constant_default(Some("'unterminated")));
        assert!(!is_constant_default(Some("abc")));
    }

    #[test]
    fn empty_selection_produces_empty_script() {
        let s = snap(DatabaseKind::Sqlite, Vec::new());
        let t = snap(
            DatabaseKind::Sqlite,
            vec![table("u", vec![col("a", "text", true, 1)])],
        );
        let out = gen(&s, &t, &[]);
        assert!(out.statements.is_empty());
        assert!(out.rollback.is_empty());
        assert!(!out.pragmas_outside_tx);
        assert!(out.ddl_in_tx);
    }

    #[test]
    fn mirror_id_inverts_only_the_op_segment() {
        assert_eq!(mirror_id("u//create_table"), "u//drop_table");
        assert_eq!(mirror_id("t/name/alter_column"), "t/name/alter_column");
        assert_eq!(mirror_id("t/idx_a/add_index"), "t/idx_a/drop_index");
        assert_eq!(
            mirror_id("t/fk(a,b)/add_foreign_key"),
            "t/fk(a,b)/drop_foreign_key"
        );
        assert_eq!(mirror_id("nonsense"), "nonsense");
        assert_eq!(mirror_id("t/x/unknown_op"), "t/x/unknown_op");
    }

    #[test]
    fn non_sqlite_dialects_and_redis_are_rejected() {
        let mk = |kind: DatabaseKind| Snapshot::new(kind, "main", Vec::new());
        for kind in [
            DatabaseKind::Postgres,
            DatabaseKind::MySql,
            DatabaseKind::Mssql,
            DatabaseKind::Oracle,
        ] {
            let s = mk(kind);
            let err = generate(&s, &s, &[], &Options::default())
                .unwrap_err()
                .to_string();
            assert_eq!(
                err,
                format!(
                    "not supported: {} 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）",
                    s.kind
                )
            );
        }
        let sqlite = mk(DatabaseKind::Sqlite);
        let pg = mk(DatabaseKind::Postgres);
        assert_eq!(
            generate(&sqlite, &pg, &[], &Options::default())
                .unwrap_err()
                .to_string(),
            "not supported: 跨种类比对未支持：sqlite vs postgres（v1 只比同一种数据库）"
        );
        let redis = mk(DatabaseKind::Redis);
        assert_eq!(
            generate(&redis, &redis, &[], &Options::default())
                .unwrap_err()
                .to_string(),
            "not supported: Redis 无模式概念，不参与结构比对"
        );
    }

    #[test]
    fn missing_snapshot_object_is_an_invalid_param() {
        let s = snap(DatabaseKind::Sqlite, Vec::new());
        let t = snap(DatabaseKind::Sqlite, vec![table("u", vec![])]);
        let err = generate(
            &s,
            &t,
            &[entry(Op::AddColumn, "u", "zz")],
            &Options::default(),
        )
        .unwrap_err()
        .to_protocol();
        assert_eq!(err.code, "POLYDB_ERR_INVALID_PARAM");
        assert_eq!(err.message, "快照中缺少列 u.zz，无法生成 DDL");
        let err2 = generate(
            &s,
            &t,
            &[entry(Op::CreateTable, "gone", "")],
            &Options::default(),
        )
        .unwrap_err()
        .to_protocol();
        assert_eq!(err2.message, "快照中缺少表 gone，无法生成 DDL");
    }

    #[test]
    fn generated_from_real_diff_is_symmetric() {
        // 用 E1 的真实计划（而非手写条目）验证镜像关系：正向与回滚的语句条数一致
        let src = with_indexes(
            table(
                "t",
                vec![pk_col("id", "INTEGER"), col("name", "varchar(20)", true, 2)],
            ),
            Vec::new(),
        );
        let tgt = table(
            "t",
            vec![
                pk_col("id", "INTEGER"),
                col("name", "varchar(30)", true, 2),
                col("extra", "text", true, 3),
            ],
        );
        let extra_tgt = table("u", vec![pk_col("id", "INTEGER")]);
        let s = snap(DatabaseKind::Sqlite, vec![src]);
        let tt = snap(DatabaseKind::Sqlite, vec![tgt, extra_tgt]);
        let out = gen_from_diff(&s, &tt);
        assert_eq!(
            sqls(&out.statements),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"u\"(\"id\" integer NOT NULL,PRIMARY KEY (\"id\"));",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,\"name\" varchar(30),\"extra\" text,PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\",\"name\") SELECT \"id\",\"name\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "PRAGMA foreign_keys=ON;"
            ]
        );
        assert_eq!(sqls(&out.rollback).len(), out.statements.len());
        assert_eq!(
            sqls(&out.rollback),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,\"name\" varchar(20),PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\",\"name\") SELECT \"id\",\"name\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "DROP TABLE \"u\";",
                "PRAGMA foreign_keys=ON;"
            ]
        );
    }

    fn absorb_pair_snapshots() -> (Snapshot, Snapshot) {
        let src = snap(
            DatabaseKind::Sqlite,
            vec![with_indexes(
                table(
                    "t",
                    vec![pk_col("id", "INTEGER"), col("qty", "INTEGER", true, 2)],
                ),
                vec![idx("idx_qty", false, false, &["qty"])],
            )],
        );
        let tgt = snap(
            DatabaseKind::Sqlite,
            vec![with_indexes(
                table(
                    "t",
                    vec![
                        pk_col("id", "INTEGER"),
                        col("qty", "TEXT", true, 2),
                        col("name", "TEXT", false, 3),
                    ],
                ),
                vec![idx("idx_name", false, false, &["name"])],
            )],
        );
        (src, tgt)
    }

    #[test]
    fn rollback_rebuilds_every_table_the_forward_rebuilt() {
        // 删一个被索引覆盖的列：正向整表重建（旧表连同它的索引一起消失）。
        // 回滚若只走 ADD COLUMN + CREATE INDEX 的直改路径，一旦某个镜像条目没能对上
        // 就永久留下与源侧不同的定义——所以正向重建过的表，回滚一律重建回源快照。
        let src = snap(
            DatabaseKind::Sqlite,
            vec![with_indexes(
                table(
                    "t",
                    vec![pk_col("id", "INTEGER"), col("note", "text", true, 2)],
                ),
                vec![idx("idx_note", false, false, &["note"])],
            )],
        );
        let tgt = snap(
            DatabaseKind::Sqlite,
            vec![table("t", vec![pk_col("id", "INTEGER")])],
        );
        let opts = Options {
            select_destructive: true,
            ..Default::default()
        };
        let plan = diff(&src, &tgt, &opts).unwrap();
        // 该表要重建 ⇒ 必须整组勾选（两条都在计划里）
        let picked = select_entries(
            &src,
            &tgt,
            &plan,
            &["t/idx_note/drop_index".into(), "t/note/drop_column".into()],
        )
        .unwrap();
        let out = generate(&src, &tgt, &picked, &opts).unwrap();
        assert_eq!(
            sqls(&out.statements),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\") SELECT \"id\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "PRAGMA foreign_keys=ON;"
            ]
        );
        assert_eq!(
            sqls(&out.rollback),
            [
                "PRAGMA foreign_keys=OFF;",
                "CREATE TABLE \"polydb_tmp_t\"(\"id\" integer NOT NULL,\"note\" text,PRIMARY KEY (\"id\"));",
                "INSERT INTO \"polydb_tmp_t\"(\"id\") SELECT \"id\" FROM \"t\";",
                "DROP TABLE \"t\";",
                "ALTER TABLE \"polydb_tmp_t\" RENAME TO \"t\";",
                "CREATE INDEX \"idx_note\" ON \"t\"(\"note\");",
                "PRAGMA foreign_keys=ON;"
            ]
        );
        // 回滚组承载的是镜像 id，按反向计划顺序
        assert_eq!(
            out.rollback[1].entry_ids,
            ["t/note/add_column", "t/idx_note/add_index"]
        );
    }

    #[test]
    fn unnamed_index_drop_uses_the_executable_name() {
        // 快照里无名的 UNIQUE 索引：object 是合成的 idx(a,b)，DROP 必须打真名
        let src = snap(
            DatabaseKind::Sqlite,
            vec![with_indexes(
                table(
                    "t",
                    vec![col("a", "text", true, 1), col("b", "text", true, 2)],
                ),
                vec![idx("", false, true, &["a", "b"])],
            )],
        );
        let tgt = snap(
            DatabaseKind::Sqlite,
            vec![table(
                "t",
                vec![col("a", "text", true, 1), col("b", "text", true, 2)],
            )],
        );
        let opts = Options {
            select_destructive: true,
            ..Default::default()
        };
        let plan = diff(&src, &tgt, &opts).unwrap();
        assert_eq!(plan[0].object, "idx(a,b)");
        let picked = select_entries(&src, &tgt, &plan, &["t/idx(a,b)/drop_index".into()]).unwrap();
        let out = generate(&src, &tgt, &picked, &opts).unwrap();
        assert_eq!(one(&out.statements), "DROP INDEX \"polydb_idx_t_a_b\";");
        // 回滚把同一个索引按可执行名建回来
        assert_eq!(
            one(&out.rollback),
            "CREATE UNIQUE INDEX \"polydb_idx_t_a_b\" ON \"t\"(\"a\",\"b\");"
        );
    }

    #[test]
    fn select_entries_keeps_plan_order_and_subset() {
        let (src, tgt) = absorb_pair_snapshots();
        let plan = diff(&src, &tgt, &Options::default()).unwrap();
        let ids: Vec<String> = plan.iter().map(|e| e.id.clone()).collect();
        assert_eq!(
            ids,
            [
                "t/idx_qty/drop_index",
                "t/name/add_column",
                "t/qty/alter_column",
                "t/idx_name/add_index"
            ]
        );
        // 只勾非重建表条目：合法，且顺序仍按计划走
        let picked = select_entries(
            &src,
            &tgt,
            &plan,
            &["t/name/add_column".into(), "t/idx_qty/drop_index".into()],
        )
        .unwrap();
        assert_eq!(
            picked.iter().map(|e| e.id.as_str()).collect::<Vec<_>>(),
            ["t/idx_qty/drop_index", "t/name/add_column"]
        );
        // 空勾选 → 空计划
        assert!(select_entries(&src, &tgt, &plan, &[]).unwrap().is_empty());
        // 全勾 = 重建整组
        let all: Vec<String> = ids.clone();
        assert_eq!(select_entries(&src, &tgt, &plan, &all).unwrap().len(), 4);
    }

    #[test]
    fn select_entries_rejects_stale_ids() {
        let (src, tgt) = absorb_pair_snapshots();
        let plan = diff(&src, &tgt, &Options::default()).unwrap();
        let err = select_entries(
            &src,
            &tgt,
            &plan,
            &["t/nope/drop_column".into(), "t/ghost/add_index".into()],
        )
        .unwrap_err()
        .to_protocol();
        assert_eq!(err.code, "POLYDB_ERR_INVALID_PARAM");
        assert_eq!(
            err.message,
            "勾选的条目不在当前比对计划里：t/nope/drop_column、t/ghost/add_index（请重新比对后再执行）"
        );
        // 同一失效 id 重复勾选：按首次出现去重，只点一次名
        let dup = select_entries(
            &src,
            &tgt,
            &plan,
            &["t/ghost/add_index".into(), "t/ghost/add_index".into()],
        )
        .unwrap_err()
        .to_protocol();
        assert_eq!(
            dup.message,
            "勾选的条目不在当前比对计划里：t/ghost/add_index（请重新比对后再执行）"
        );
    }

    #[test]
    fn select_entries_requires_whole_group_for_rebuilt_tables() {
        let (src, tgt) = absorb_pair_snapshots();
        let plan = diff(&src, &tgt, &Options::default()).unwrap();
        // alter_column 会让 t 整表重建：漏掉同表其它条目必须报错，
        // 否则未勾选的 name 列会被重建语句悄悄建出来。
        let err = select_entries(&src, &tgt, &plan, &["t/qty/alter_column".into()])
            .unwrap_err()
            .to_protocol();
        assert_eq!(err.code, "POLYDB_ERR_INVALID_PARAM");
        assert_eq!(
            err.message,
            "表 t 需要整表重建，同表变更必须整组勾选：缺少 t/idx_qty/drop_index、t/name/add_column、t/idx_name/add_index"
        );
        // 不含重建触发者的子集不受此约束
        assert!(select_entries(&src, &tgt, &plan, &["t/idx_qty/drop_index".into()]).is_ok());
    }
}
