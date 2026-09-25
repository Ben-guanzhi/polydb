//! PolyDB 结构比对纯函数层（Compare & Sync E1）。
//!
//! 输入两侧**元数据快照**（由 E2 的服务端从驱动采集），输出**结构化变更计划**：
//! 一条 `Entry` = 一个原子动作，带相位（决定执行顺序）、破坏性标记与勾选默认值。
//!
//! 本 crate **零 IO、零 UI、不生成 DDL**（语句生成属 E3，且必须走绑定参数）。
//! Go 侧 `pkg/schemadiff` 是同一套语义的第二实现，两端由
//! `test/schema-diff-vectors/*.json` 共享语料锁死输出（逐字一致，含中文 reason）。
//!
//! v1 有意**不比较**：字符集/排序规则、列顺序、约束名之外的注释、视图与物化视图、
//! 触发器/存储过程/序列/权限。跨种类比对（如 SQLite ↔ PG）直接报错而非猜测。
//! 视图一侧整体跳过：若源侧是视图而目标侧是同名表，仍按「缺失表」产出
//! `create_table`（不猜语义，执行期由数据库报错兜底）。

mod normalize;

pub mod ddl;

pub use ddl::{
    generate, select_entries, Script, Statement, W_ADD_COLUMN_LIMITS, W_AUTOINCREMENT,
    W_CHECK_CONSTRAINTS, W_CREATE_TABLE, W_DROP_COLUMN_CHECK, W_GENERATED_COLUMNS,
    W_REBUILD_NEW_NOT_NULL, W_TRIGGERS,
};
pub use normalize::{lower_ascii, normalize_default, normalize_type};

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyAction, ForeignKeyInfo, IndexColumn,
    IndexInfo, TableType,
};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};

/// 一个原子动作。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Op {
    CreateTable,
    DropTable,
    AddColumn,
    DropColumn,
    AlterColumn,
    AddIndex,
    DropIndex,
    AddForeignKey,
    DropForeignKey,
}

impl Op {
    /// 线路名（与 Go 侧字符串常量逐字一致）。
    pub fn as_str(self) -> &'static str {
        match self {
            Self::CreateTable => "create_table",
            Self::DropTable => "drop_table",
            Self::AddColumn => "add_column",
            Self::DropColumn => "drop_column",
            Self::AlterColumn => "alter_column",
            Self::AddIndex => "add_index",
            Self::DropIndex => "drop_index",
            Self::AddForeignKey => "add_foreign_key",
            Self::DropForeignKey => "drop_foreign_key",
        }
    }

    /// 执行相位：数值小的先执行，用于保证「先解除依赖再删、先建表再加约束」。
    pub fn phase(self) -> u8 {
        match self {
            Self::DropForeignKey => 10,
            Self::DropIndex => 20,
            Self::DropColumn => 30,
            Self::DropTable => 40,
            Self::CreateTable => 50,
            Self::AddColumn => 60,
            Self::AlterColumn => 70,
            Self::AddIndex => 80,
            Self::AddForeignKey => 90,
        }
    }

    /// 破坏性动作：删表/删列/删索引/删外键（默认不勾选，见 `docs/plan-compare-sync.md` D4）。
    pub fn destructive(self) -> bool {
        matches!(
            self,
            Self::DropTable | Self::DropColumn | Self::DropIndex | Self::DropForeignKey
        )
    }

    /// 线路名 → op（`as_str` 的逆）；未知串返回 `None`，供回滚镜像等场景使用。
    pub(crate) fn from_wire(s: &str) -> Option<Self> {
        Some(match s {
            "create_table" => Self::CreateTable,
            "drop_table" => Self::DropTable,
            "add_column" => Self::AddColumn,
            "drop_column" => Self::DropColumn,
            "alter_column" => Self::AlterColumn,
            "add_index" => Self::AddIndex,
            "drop_index" => Self::DropIndex,
            "add_foreign_key" => Self::AddForeignKey,
            "drop_foreign_key" => Self::DropForeignKey,
            _ => return None,
        })
    }
}

/// `alter_column` 的改变维度（供 UI 分行展示与选择性勾选）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ColumnFlag {
    Type,
    Nullability,
    Default,
    AutoIncrement,
}

/// 计划里的一条变更。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Entry {
    /// 稳定标识：`{表名小写}/{对象名小写}/{op}`，供 UI 记忆勾选状态。
    pub id: String,
    pub op: Op,
    /// 执行相位（`Op::phase`），排序主键。
    pub phase: u8,
    /// 表名（取目标侧原样大小写；删表时取源侧）。
    pub table: String,
    /// 列名 / 索引名 / 外键名；表级动作为空。
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub object: String,
    /// 变更前取值（类型 / 列定义等），语义随 `op` 而定。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub from: Option<String>,
    /// 变更后取值。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub to: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub flags: Vec<ColumnFlag>,
    pub destructive: bool,
    pub selected: bool,
    /// 人读原因（中文，双端逐字一致，由共享向量语料锁定）。
    pub reason: String,
}

/// 比对选项。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Options {
    /// 忽略默认值差异（跨环境序列/时间戳默认常属噪声）。
    #[serde(default)]
    pub ignore_defaults: bool,
    /// 破坏性动作是否默认勾选（默认 false，须用户显式确认）。
    #[serde(default)]
    pub select_destructive: bool,
}

/// 一张表的元数据快照（字段名与 `ColumnInfo` / `IndexInfo` / `ForeignKeyInfo` DTO 对齐）。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TableSnapshot {
    pub name: String,
    #[serde(default, rename = "type")]
    pub table_type: Option<TableType>,
    #[serde(default)]
    pub columns: Vec<ColumnInfo>,
    #[serde(default)]
    pub indexes: Vec<IndexInfo>,
    #[serde(default)]
    pub foreign_keys: Vec<ForeignKeyInfo>,
}

/// 一个（种类, schema）的结构快照。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Snapshot {
    pub kind: DatabaseKind,
    #[serde(default)]
    pub schema: String,
    #[serde(default)]
    pub tables: Vec<TableSnapshot>,
}

impl Snapshot {
    /// 构造快照；E2 由驱动的 `list_tables/columns/indexes/foreign_keys` 组装。
    pub fn new(kind: DatabaseKind, schema: impl Into<String>, tables: Vec<TableSnapshot>) -> Self {
        Self {
            kind,
            schema: schema.into(),
            tables,
        }
    }
}

fn null_word(nullable: bool) -> &'static str {
    if nullable {
        "NULL"
    } else {
        "NOT NULL"
    }
}

fn yes_word(b: bool) -> &'static str {
    if b {
        "是"
    } else {
        "否"
    }
}

fn default_word(d: Option<&str>) -> String {
    d.unwrap_or("无").to_string()
}

fn action_word(a: Option<ForeignKeyAction>) -> &'static str {
    match a {
        None => "无",
        Some(ForeignKeyAction::Cascade) => "cascade",
        Some(ForeignKeyAction::SetNull) => "set_null",
        Some(ForeignKeyAction::SetDefault) => "set_default",
        Some(ForeignKeyAction::Restrict) => "restrict",
        Some(ForeignKeyAction::NoAction) => "no_action",
    }
}

fn join_lower(names: &[String]) -> String {
    names
        .iter()
        .map(|n| lower_ascii(n))
        .collect::<Vec<_>>()
        .join(",")
}

/// 索引列签名：按 `position` 升序取列名小写，逗号连接（忽略 ASC/DESC 与前缀长度）。
pub(crate) fn index_sig(idx: &IndexInfo) -> String {
    let mut cols: Vec<&IndexColumn> = idx.columns.iter().collect();
    cols.sort_by_key(|c| (c.position, lower_ascii(&c.name)));
    cols.iter()
        .map(|c| lower_ascii(&c.name))
        .collect::<Vec<_>>()
        .join(",")
}

fn referenced_table(fk: &ForeignKeyInfo) -> String {
    let t = lower_ascii(&fk.referenced_table);
    if fk.referenced_schema.trim().is_empty() {
        t
    } else {
        format!("{}/{}", lower_ascii(&fk.referenced_schema), t)
    }
}

fn fk_key(fk: &ForeignKeyInfo) -> String {
    if !fk.name.trim().is_empty() {
        return format!("n:{}", lower_ascii(&fk.name));
    }
    format!(
        "c:{}->{}({})",
        join_lower(&fk.columns),
        lower_ascii(&fk.referenced_table),
        join_lower(&fk.referenced_columns)
    )
}

fn fk_object(fk: &ForeignKeyInfo) -> String {
    if fk.name.trim().is_empty() {
        format!("fk({})", join_lower(&fk.columns))
    } else {
        fk.name.clone()
    }
}

pub(crate) fn index_object(idx: &IndexInfo) -> String {
    if idx.name.trim().is_empty() {
        format!("idx({})", index_sig(idx))
    } else {
        idx.name.clone()
    }
}

fn is_view(t: &TableSnapshot) -> bool {
    matches!(
        t.table_type,
        Some(TableType::View) | Some(TableType::MaterializedView)
    )
}

pub(crate) fn table_map(snap: &Snapshot) -> BTreeMap<String, &TableSnapshot> {
    let mut m = BTreeMap::new();
    for t in &snap.tables {
        if is_view(t) {
            continue;
        }
        m.entry(lower_ascii(&t.name)).or_insert(t);
    }
    m
}

pub(crate) fn column_map(t: &TableSnapshot) -> BTreeMap<String, &ColumnInfo> {
    let mut m = BTreeMap::new();
    for c in &t.columns {
        m.entry(lower_ascii(&c.name)).or_insert(c);
    }
    m
}

fn mk(op: Op, table: &str, object: &str, reason: String, opts: &Options) -> Entry {
    Entry {
        id: format!(
            "{}/{}/{}",
            lower_ascii(table),
            lower_ascii(object),
            op.as_str()
        ),
        op,
        phase: op.phase(),
        table: table.to_string(),
        object: object.to_string(),
        from: None,
        to: None,
        flags: Vec::new(),
        destructive: op.destructive(),
        selected: !op.destructive() || opts.select_destructive,
        reason,
    }
}

fn diff_columns(
    kind: DatabaseKind,
    table: &str,
    src: &TableSnapshot,
    tgt: &TableSnapshot,
    opts: &Options,
) -> Vec<Entry> {
    let mut out = Vec::new();
    let sc = column_map(src);
    let tc = column_map(tgt);
    for (k, c) in &tc {
        let Some(s) = sc.get(k) else {
            let mut e = mk(Op::AddColumn, table, &c.name, "列缺失，需新增".into(), opts);
            e.to = Some(normalize_type(kind, &c.data_type));
            out.push(e);
            continue;
        };
        let st = normalize_type(kind, &s.data_type);
        let tt = normalize_type(kind, &c.data_type);
        let mut flags = Vec::new();
        let mut reasons: Vec<String> = Vec::new();
        if st != tt {
            flags.push(ColumnFlag::Type);
            reasons.push(format!("类型 {st} → {tt}"));
        }
        if s.nullable != c.nullable {
            flags.push(ColumnFlag::Nullability);
            reasons.push(format!(
                "可空性 {} → {}",
                null_word(s.nullable),
                null_word(c.nullable)
            ));
        }
        if !opts.ignore_defaults {
            let sd = normalize_default(s.default_value.as_deref());
            let td = normalize_default(c.default_value.as_deref());
            if sd != td {
                flags.push(ColumnFlag::Default);
                reasons.push(format!(
                    "默认值 {} → {}",
                    default_word(sd.as_deref()),
                    default_word(td.as_deref())
                ));
            }
        }
        if s.is_auto_increment != c.is_auto_increment {
            flags.push(ColumnFlag::AutoIncrement);
            reasons.push(format!(
                "自增 {} → {}",
                yes_word(s.is_auto_increment),
                yes_word(c.is_auto_increment)
            ));
        }
        if !flags.is_empty() {
            let mut e = mk(Op::AlterColumn, table, &c.name, reasons.join("；"), opts);
            e.from = Some(st);
            e.to = Some(tt);
            e.flags = flags;
            out.push(e);
        }
    }
    for (k, c) in &sc {
        if tc.contains_key(k) {
            continue;
        }
        let mut e = mk(
            Op::DropColumn,
            table,
            &c.name,
            "列多余，需删除".into(),
            opts,
        );
        e.from = Some(normalize_type(kind, &c.data_type));
        out.push(e);
    }
    out
}

fn diff_indexes(
    table: &str,
    src: &TableSnapshot,
    tgt: &TableSnapshot,
    opts: &Options,
) -> Vec<Entry> {
    let mut out = Vec::new();
    let s_pri = src.indexes.iter().find(|i| i.primary);
    let t_pri = tgt.indexes.iter().find(|i| i.primary);
    match (s_pri, t_pri) {
        (Some(s), Some(t)) => {
            if index_sig(s) != index_sig(t) {
                out.push(mk(
                    Op::DropIndex,
                    table,
                    &index_object(s),
                    "主键列变化，需重建".into(),
                    opts,
                ));
                out.push(mk(
                    Op::AddIndex,
                    table,
                    &index_object(t),
                    "主键列变化，需重建".into(),
                    opts,
                ));
            }
        }
        (Some(s), None) => out.push(mk(
            Op::DropIndex,
            table,
            &index_object(s),
            "索引多余，需删除".into(),
            opts,
        )),
        (None, Some(t)) => out.push(mk(
            Op::AddIndex,
            table,
            &index_object(t),
            "索引缺失，需新增".into(),
            opts,
        )),
        (None, None) => {}
    }
    let mut si: BTreeMap<String, &IndexInfo> = BTreeMap::new();
    for i in src.indexes.iter().filter(|i| !i.primary) {
        si.entry(lower_ascii(&index_object(i))).or_insert(i);
    }
    let mut ti: BTreeMap<String, &IndexInfo> = BTreeMap::new();
    for i in tgt.indexes.iter().filter(|i| !i.primary) {
        ti.entry(lower_ascii(&index_object(i))).or_insert(i);
    }
    for (k, t) in &ti {
        let Some(s) = si.get(k) else {
            out.push(mk(
                Op::AddIndex,
                table,
                &index_object(t),
                "索引缺失，需新增".into(),
                opts,
            ));
            continue;
        };
        let (a, b) = (index_sig(s), index_sig(t));
        let mut reasons = Vec::new();
        if a != b {
            reasons.push(format!("索引定义变化：{a} → {b}"));
        }
        if s.unique != t.unique {
            reasons.push(format!(
                "唯一性 {} → {}",
                yes_word(s.unique),
                yes_word(t.unique)
            ));
        }
        if reasons.is_empty() {
            continue;
        }
        let r = reasons.join("；");
        out.push(mk(Op::DropIndex, table, &index_object(s), r.clone(), opts));
        out.push(mk(Op::AddIndex, table, &index_object(t), r, opts));
    }
    for (k, s) in &si {
        if !ti.contains_key(k) {
            out.push(mk(
                Op::DropIndex,
                table,
                &index_object(s),
                "索引多余，需删除".into(),
                opts,
            ));
        }
    }
    out
}

fn diff_foreign_keys(
    table: &str,
    src: &TableSnapshot,
    tgt: &TableSnapshot,
    opts: &Options,
) -> Vec<Entry> {
    let mut out = Vec::new();
    let mut sf: BTreeMap<String, &ForeignKeyInfo> = BTreeMap::new();
    for f in &src.foreign_keys {
        sf.entry(fk_key(f)).or_insert(f);
    }
    let mut tf: BTreeMap<String, &ForeignKeyInfo> = BTreeMap::new();
    for f in &tgt.foreign_keys {
        tf.entry(fk_key(f)).or_insert(f);
    }
    for (k, t) in &tf {
        let Some(s) = sf.get(k) else {
            out.push(mk(
                Op::AddForeignKey,
                table,
                &fk_object(t),
                "外键缺失，需新增".into(),
                opts,
            ));
            continue;
        };
        let mut reasons = Vec::new();
        let (sc, tc) = (join_lower(&s.columns), join_lower(&t.columns));
        if sc != tc {
            reasons.push(format!("列 {sc} → {tc}"));
        }
        let (sr, tr) = (referenced_table(s), referenced_table(t));
        if sr != tr {
            reasons.push(format!("引用表 {sr} → {tr}"));
        }
        let (src_, trc) = (
            join_lower(&s.referenced_columns),
            join_lower(&t.referenced_columns),
        );
        if src_ != trc {
            reasons.push(format!("引用列 {src_} → {trc}"));
        }
        if s.on_delete != t.on_delete {
            reasons.push(format!(
                "ON DELETE {} → {}",
                action_word(s.on_delete),
                action_word(t.on_delete)
            ));
        }
        if s.on_update != t.on_update {
            reasons.push(format!(
                "ON UPDATE {} → {}",
                action_word(s.on_update),
                action_word(t.on_update)
            ));
        }
        if reasons.is_empty() {
            continue;
        }
        let r = reasons.join("；");
        out.push(mk(
            Op::DropForeignKey,
            table,
            &fk_object(s),
            r.clone(),
            opts,
        ));
        out.push(mk(Op::AddForeignKey, table, &fk_object(t), r, opts));
    }
    for (k, s) in &sf {
        if !tf.contains_key(k) {
            out.push(mk(
                Op::DropForeignKey,
                table,
                &fk_object(s),
                "外键多余，需删除".into(),
                opts,
            ));
        }
    }
    out
}

fn diff_table(
    kind: DatabaseKind,
    src: &TableSnapshot,
    tgt: &TableSnapshot,
    opts: &Options,
) -> Vec<Entry> {
    let name = &tgt.name;
    let mut out = diff_columns(kind, name, src, tgt, opts);
    out.extend(diff_indexes(name, src, tgt, opts));
    out.extend(diff_foreign_keys(name, src, tgt, opts));
    out
}

/// 新建表之间按外键引用排依赖序（父表在前）；同名候选按名字序，环按 DFS 到达序。
fn topo_create_tables(sorted: &[Entry], tgt: &BTreeMap<String, &TableSnapshot>) -> Vec<Entry> {
    let mut by_key: BTreeMap<String, Entry> = BTreeMap::new();
    for e in sorted.iter().filter(|e| e.op == Op::CreateTable) {
        by_key.insert(lower_ascii(&e.table), e.clone());
    }
    if by_key.is_empty() {
        return Vec::new();
    }
    let mut deps: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for k in by_key.keys() {
        let Some(t) = tgt.get(k) else { continue };
        let mut d: Vec<String> = t
            .foreign_keys
            .iter()
            .map(|f| lower_ascii(&f.referenced_table))
            .filter(|r| by_key.contains_key(r) && r != k)
            .collect();
        d.sort();
        d.dedup();
        deps.insert(k.clone(), d);
    }
    let mut visited: BTreeSet<String> = BTreeSet::new();
    let mut order: Vec<Entry> = Vec::with_capacity(by_key.len());
    for k in by_key.keys() {
        visit_create(k, &deps, &mut visited, &mut order, &by_key);
    }
    order
}

fn visit_create(
    k: &str,
    deps: &BTreeMap<String, Vec<String>>,
    visited: &mut BTreeSet<String>,
    order: &mut Vec<Entry>,
    by_key: &BTreeMap<String, Entry>,
) {
    if visited.contains(k) {
        return;
    }
    visited.insert(k.to_string());
    if let Some(d) = deps.get(k) {
        for r in d {
            visit_create(r, deps, visited, order, by_key);
        }
    }
    if let Some(e) = by_key.get(k) {
        order.push(e.clone());
    }
}

fn sort_key(e: &Entry) -> (u8, String, String, String) {
    (
        e.phase,
        lower_ascii(&e.table),
        lower_ascii(&e.object),
        e.id.clone(),
    )
}

/// 比对两个快照，产出按相位排好序的结构化计划。
///
/// 错误：两侧种类不同、或任一侧为 Redis（无模式概念）。
pub fn diff(source: &Snapshot, target: &Snapshot, opts: &Options) -> CoreResult<Vec<Entry>> {
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
    let src = table_map(source);
    let tgt = table_map(target);
    let mut out: Vec<Entry> = Vec::new();
    for (k, t) in &tgt {
        match src.get(k) {
            Some(s) => out.extend(diff_table(source.kind, s, t, opts)),
            None => out.push(mk(
                Op::CreateTable,
                &t.name,
                "",
                "表中缺失，需新建".into(),
                opts,
            )),
        }
    }
    for (k, s) in &src {
        if !tgt.contains_key(k) {
            out.push(mk(
                Op::DropTable,
                &s.name,
                "",
                "表中多余，需删除".into(),
                opts,
            ));
        }
    }
    out.sort_by_key(sort_key);
    let create = topo_create_tables(&out, &tgt);
    let mut ci = 0;
    let mut res = Vec::with_capacity(out.len());
    for e in &out {
        if e.op == Op::CreateTable {
            res.push(create[ci].clone());
            ci += 1;
        } else {
            res.push(e.clone());
        }
    }
    Ok(res)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn col(name: &str, ty: &str, nullable: bool) -> ColumnInfo {
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
            ordinal_position: 0,
        }
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

    fn fk(name: &str, cols: &[&str], refs: &str, ref_cols: &[&str]) -> ForeignKeyInfo {
        ForeignKeyInfo {
            name: name.into(),
            columns: cols.iter().map(|c| c.to_string()).collect(),
            referenced_schema: String::new(),
            referenced_table: refs.into(),
            referenced_columns: ref_cols.iter().map(|c| c.to_string()).collect(),
            on_update: None,
            on_delete: None,
        }
    }

    fn snap(kind: DatabaseKind, tables: Vec<TableSnapshot>) -> Snapshot {
        Snapshot::new(kind, "main", tables)
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

    fn ops(entries: &[Entry]) -> Vec<&'static str> {
        entries.iter().map(|e| e.op.as_str()).collect()
    }

    #[test]
    fn identical_snapshots_produce_no_entries() {
        let t = || vec![table("users", vec![col("id", "integer", false)])];
        let out = diff(
            &snap(DatabaseKind::Postgres, t()),
            &snap(DatabaseKind::Postgres, t()),
            &Options::default(),
        )
        .unwrap();
        assert!(out.is_empty(), "应为空计划：{out:?}");
    }

    #[test]
    fn table_level_ops_and_selection_defaults() {
        let src = snap(DatabaseKind::Postgres, vec![table("legacy", vec![])]);
        let tgt = snap(DatabaseKind::Postgres, vec![table("users", vec![])]);
        let out = diff(&src, &tgt, &Options::default()).unwrap();
        assert_eq!(ops(&out), ["drop_table", "create_table"]);
        assert_eq!(out[0].phase, 40);
        assert_eq!(out[1].phase, 50);
        // 相位排序优先于名字序：先删多余表，再建缺失表
        assert_eq!(out[0].table, "legacy");
        assert_eq!(out[1].table, "users");
        assert!(!out[0].selected);
        assert!(out[0].destructive);
        assert!(out[1].selected);
        assert_eq!(out[0].id, "legacy//drop_table");
    }

    #[test]
    fn select_destructive_option_flips_drops() {
        let src = snap(
            DatabaseKind::Postgres,
            vec![table("legacy", vec![col("a", "int", true)])],
        );
        let tgt = snap(DatabaseKind::Postgres, vec![table("legacy", vec![])]);
        let opts = Options {
            select_destructive: true,
            ..Default::default()
        };
        let out = diff(&src, &tgt, &opts).unwrap();
        assert_eq!(ops(&out), ["drop_column"]);
        assert!(out[0].selected);
        assert_eq!(out[0].id, "legacy/a/drop_column");
    }

    #[test]
    fn alter_column_reports_flags_and_reason() {
        let src = snap(
            DatabaseKind::Postgres,
            vec![table(
                "users",
                vec![with_default(
                    col("name", "character varying(20)", false),
                    "'x'",
                )],
            )],
        );
        let tgt = snap(
            DatabaseKind::Postgres,
            vec![table(
                "users",
                vec![with_default(
                    col("name", "VARCHAR(30)", true),
                    "('x'::text)",
                )],
            )],
        );
        let out = diff(&src, &tgt, &Options::default()).unwrap();
        assert_eq!(ops(&out), ["alter_column"]);
        let e = &out[0];
        assert_eq!(e.flags, vec![ColumnFlag::Type, ColumnFlag::Nullability]);
        assert_eq!(e.from.as_deref(), Some("character varying(20)"));
        assert_eq!(e.to.as_deref(), Some("varchar(30)"));
        assert_eq!(
            e.reason,
            "类型 character varying(20) → varchar(30)；可空性 NOT NULL → NULL"
        );
        assert_eq!(e.id, "users/name/alter_column");
    }

    #[test]
    fn ignore_defaults_suppresses_default_diff() {
        let src = snap(
            DatabaseKind::Postgres,
            vec![table("t", vec![with_default(col("a", "int", true), "1")])],
        );
        let tgt = snap(
            DatabaseKind::Postgres,
            vec![table("t", vec![with_default(col("a", "int", true), "2")])],
        );
        let out = diff(&src, &tgt, &Options::default()).unwrap();
        assert_eq!(ops(&out), ["alter_column"]);
        assert_eq!(out[0].reason, "默认值 1 → 2");
        let out2 = diff(
            &src,
            &tgt,
            &Options {
                ignore_defaults: true,
                ..Default::default()
            },
        )
        .unwrap();
        assert!(out2.is_empty());
    }

    #[test]
    fn identifier_matching_is_case_insensitive() {
        let src = snap(
            DatabaseKind::MySql,
            vec![table("Users", vec![col("ID", "INT(11)", false)])],
        );
        let tgt = snap(
            DatabaseKind::MySql,
            vec![table("users", vec![col("id", "int", false)])],
        );
        let out = diff(&src, &tgt, &Options::default()).unwrap();
        assert!(out.is_empty(), "大小写与显示宽度不得造成删+建误判：{out:?}");
    }

    #[test]
    fn index_and_pk_changes_come_in_pairs() {
        let mut src = table("t", vec![col("a", "int", true), col("b", "int", true)]);
        src.indexes = vec![
            idx("PRIMARY", true, true, &["a"]),
            idx("idx_ab", false, true, &["a", "b"]),
        ];
        let mut tgt = table("t", vec![col("a", "int", true), col("b", "int", true)]);
        tgt.indexes = vec![
            idx("t_pkey", true, true, &["b"]),
            idx("idx_ab", false, false, &["b", "a"]),
            idx("idx_c", false, false, &["b"]),
        ];
        let out = diff(
            &snap(DatabaseKind::Postgres, vec![src]),
            &snap(DatabaseKind::Postgres, vec![tgt]),
            &Options::default(),
        )
        .unwrap();
        assert_eq!(
            ops(&out),
            [
                "drop_index",
                "drop_index",
                "add_index",
                "add_index",
                "add_index"
            ]
        );
        // 排序键 = (相位, 表, 对象)：同相位内按对象名小写字典序
        assert_eq!(out[0].object, "idx_ab");
        assert_eq!(out[0].reason, "索引定义变化：a,b → b,a；唯一性 是 → 否");
        assert_eq!(out[1].object, "PRIMARY");
        assert_eq!(out[1].reason, "主键列变化，需重建");
        assert_eq!(out[2].reason, "索引定义变化：a,b → b,a；唯一性 是 → 否");
        assert_eq!(out[3].object, "idx_c");
        assert_eq!(out[3].reason, "索引缺失，需新增");
        assert_eq!(out[4].object, "t_pkey");
        assert_eq!(out[4].reason, "主键列变化，需重建");
    }

    #[test]
    fn foreign_key_action_change_is_a_pair() {
        let src_t = {
            let mut t = table("orders", vec![col("user_id", "int", true)]);
            let mut f = fk("fk_user", &["user_id"], "users", &["id"]);
            f.on_delete = Some(ForeignKeyAction::NoAction);
            t.foreign_keys = vec![f];
            t
        };
        let tgt_t = {
            let mut t = table("orders", vec![col("user_id", "int", true)]);
            let mut f = fk("fk_user", &["user_id"], "db.users", &["uid"]);
            f.referenced_schema = "db".into();
            f.referenced_table = "users".into();
            f.on_delete = Some(ForeignKeyAction::Cascade);
            t.foreign_keys = vec![f];
            t
        };
        let out = diff(
            &snap(DatabaseKind::MySql, vec![src_t]),
            &snap(DatabaseKind::MySql, vec![tgt_t]),
            &Options::default(),
        )
        .unwrap();
        assert_eq!(ops(&out), ["drop_foreign_key", "add_foreign_key"]);
        assert_eq!(out[0].phase, 10);
        assert_eq!(out[1].phase, 90);
        assert_eq!(
            out[0].reason,
            "引用表 users → db/users；引用列 id → uid；ON DELETE no_action → cascade"
        );
        assert!(!out[1].destructive);
    }

    #[test]
    fn views_are_skipped() {
        let mut v = table("v_users", vec![]);
        v.table_type = Some(TableType::View);
        let mut m = table("m_users", vec![]);
        m.table_type = Some(TableType::MaterializedView);
        let src = snap(DatabaseKind::Postgres, vec![v.clone()]);
        let tgt = snap(DatabaseKind::Postgres, vec![m]);
        assert!(diff(&src, &tgt, &Options::default()).unwrap().is_empty());
        // 视图不参与比对：普通表仍应正常识别为缺失
        let tgt2 = snap(DatabaseKind::Postgres, vec![v, table("t", vec![])]);
        assert_eq!(
            ops(&diff(&src, &tgt2, &Options::default()).unwrap()),
            ["create_table"]
        );
    }

    #[test]
    fn phase_order_is_stable_across_tables() {
        let mut src = table("b", vec![col("gone", "int", true)]);
        src.indexes = vec![idx("idx_gone", false, false, &["gone"])];
        let mut tgt = table("b", vec![col("added", "int", true)]);
        tgt.indexes = vec![idx("idx_new", false, false, &["added"])];
        let out = diff(
            &snap(DatabaseKind::Sqlite, vec![src]),
            &snap(DatabaseKind::Sqlite, vec![tgt]),
            &Options::default(),
        )
        .unwrap();
        assert_eq!(
            out.iter().map(|e| e.phase).collect::<Vec<_>>(),
            [20, 30, 60, 80]
        );
    }

    #[test]
    fn cross_kind_and_redis_are_rejected() {
        let s = snap(DatabaseKind::Sqlite, vec![]);
        let m = snap(DatabaseKind::MySql, vec![]);
        let r = snap(DatabaseKind::Redis, vec![]);
        let err = diff(&s, &m, &Options::default()).unwrap_err().to_string();
        assert_eq!(
            err,
            "not supported: 跨种类比对未支持：sqlite vs mysql（v1 只比同一种数据库）"
        );
        assert_eq!(
            diff(&r, &r, &Options::default()).unwrap_err().to_string(),
            "not supported: Redis 无模式概念，不参与结构比对"
        );
        assert_eq!(
            diff(&s, &r, &Options::default()).unwrap_err().to_string(),
            "not supported: Redis 无模式概念，不参与结构比对"
        );
    }
}
