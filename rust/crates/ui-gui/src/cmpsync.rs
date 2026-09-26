//! 结构比对与同步向导在 GUI 侧的纯函数层（Compare & Sync E5.4）。
//!
//! 定位与 Go `pkg/cmpsync`、Web `lib/compareSync.ts` 同一层：差异与语句生成的真相源在
//! `polydb-schema-diff`（服务端判定），本层**不重复判定**，只做三件事——
//!   1. 呈现：相位/动作的中文名、按相位分组、脚本摘要；
//!   2. 勾选集运算：默认勾选、切换、整组补漏（§15.5 的第二条守卫会在服务端 400，提前显示缺口）；
//!   3. 执行编排：按响应里的两个能力位把脚本切成「事务外 PRAGMA + 主体」。
//!
//! 刻意不落盘：存下来的勾选集下一轮必然陈旧（§15.5 第一条守卫）。
//! AGPL 红线：只借鉴 TablePro 的交互语义，未移植任何外部代码。

use polydb_core::{
    ConnectionInfo, DatabaseKind, DdlScript, DdlStatement, SchemaDiffEntry, SchemaDiffOp,
};

/// 相位 → 中文段名；数值在 E1 定死，与服务端排序一致。
pub(crate) fn phase_label(phase: u8) -> String {
    let s = match phase {
        0 => "事务外前置（PRAGMA）",
        10 => "删除外键",
        20 => "删除索引",
        30 => "删除列",
        40 => "删除表",
        50 => "新建表",
        60 => "新增列",
        70 => "变更列",
        80 => "新增索引",
        90 => "新增外键",
        255 => "事务外收尾（PRAGMA）",
        _ => return format!("相位 {phase}"),
    };
    s.to_string()
}

/// 原子动作的中文名，与 Go `OpLabel` / Web `OP_LABEL` 逐字一致。
pub(crate) fn op_label(op: SchemaDiffOp) -> &'static str {
    match op {
        SchemaDiffOp::AddColumn => "加列",
        SchemaDiffOp::AddForeignKey => "加外键",
        SchemaDiffOp::AddIndex => "加索引",
        SchemaDiffOp::AlterColumn => "改列",
        SchemaDiffOp::CreateTable => "建表",
        SchemaDiffOp::DropColumn => "删列",
        SchemaDiffOp::DropForeignKey => "删外键",
        SchemaDiffOp::DropIndex => "删索引",
        SchemaDiffOp::DropTable => "删表",
    }
}

/// 必然走整表重建的动作（E3 规则 2）。删列/隐性重建在呈现层判不出来，交服务端兜。
fn always_rebuild(op: SchemaDiffOp) -> bool {
    matches!(
        op,
        SchemaDiffOp::AlterColumn | SchemaDiffOp::AddForeignKey | SchemaDiffOp::DropForeignKey
    )
}

/// 表名比较按大小写不敏感，与服务端的分组口径一致。
fn same_table(a: &str, b: &str) -> bool {
    a.eq_ignore_ascii_case(b)
}

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct PhaseGroup {
    pub(crate) phase: u8,
    pub(crate) label: String,
    pub(crate) entries: Vec<SchemaDiffEntry>,
}

/// 按相位分组，保持服务端给的顺序（计划已按 phase 升序）。
pub(crate) fn group_by_phase(entries: &[SchemaDiffEntry]) -> Vec<PhaseGroup> {
    let mut out: Vec<PhaseGroup> = Vec::new();
    for e in entries {
        match out.last_mut() {
            Some(g) if g.phase == e.phase => g.entries.push(e.clone()),
            _ => out.push(PhaseGroup {
                phase: e.phase,
                label: phase_label(e.phase),
                entries: vec![e.clone()],
            }),
        }
    }
    out
}

/// 服务端给的默认勾选集（破坏性条目默认不勾，除非 select_destructive）。
pub(crate) fn default_selection(entries: &[SchemaDiffEntry]) -> Vec<String> {
    entries
        .iter()
        .filter(|e| e.selected)
        .map(|e| e.id.clone())
        .collect()
}

/// 计划里的全部条目 id（全选）。
pub(crate) fn all_ids(entries: &[SchemaDiffEntry]) -> Vec<String> {
    entries.iter().map(|e| e.id.clone()).collect()
}

/// 全部非破坏性条目 id（「只勾非破坏」）。
pub(crate) fn non_destructive_ids(entries: &[SchemaDiffEntry]) -> Vec<String> {
    entries
        .iter()
        .filter(|e| !e.destructive)
        .map(|e| e.id.clone())
        .collect()
}

/// 勾选/取消一条，返回新集合。
pub(crate) fn toggle_entry(sel: &[String], id: &str) -> Vec<String> {
    let mut out: Vec<String> = sel.iter().filter(|&s| s != id).cloned().collect();
    if out.len() == sel.len() {
        out.push(id.to_string());
    }
    out
}

/// 把勾选集还原成计划顺序的 id 数组：服务端按重算出的计划校验，顺序无关，
/// 但保序去重能让三端文案一致。
pub(crate) fn selected_ids(entries: &[SchemaDiffEntry], sel: &[String]) -> Vec<String> {
    entries
        .iter()
        .filter(|e| sel.iter().any(|s| s == &e.id))
        .map(|e| e.id.clone())
        .collect()
}

/// 某表的全部条目。
pub(crate) fn entries_of_table(entries: &[SchemaDiffEntry], table: &str) -> Vec<SchemaDiffEntry> {
    entries
        .iter()
        .filter(|e| same_table(&e.table, table))
        .cloned()
        .collect()
}

/// 本次计划里「大概率进重建路径」的表，用于提示整组勾选（按首次出现顺序去重）。
pub(crate) fn rebuild_likely_tables(entries: &[SchemaDiffEntry]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for e in entries {
        if !always_rebuild(e.op) {
            continue;
        }
        if !out.iter().any(|t| same_table(t, &e.table)) {
            out.push(e.table.clone());
        }
    }
    out
}

/// 「勾了重建动作却漏勾同表其他条目」的缺口。
#[derive(Debug, Clone, PartialEq)]
pub(crate) struct GroupGap {
    pub(crate) table: String,
    pub(crate) missing: Vec<String>,
}

/// 提前算出缺口：这种勾选集提交给服务端一定 400（§15.5 第二条守卫），
/// 呈现层有责任在生成脚本前把它显示出来。
pub(crate) fn group_gaps(entries: &[SchemaDiffEntry], sel: &[String]) -> Vec<GroupGap> {
    let mut gaps = Vec::new();
    for table in rebuild_likely_tables(entries) {
        let group = entries_of_table(entries, &table);
        let hit = group
            .iter()
            .any(|e| always_rebuild(e.op) && sel.iter().any(|s| s == &e.id));
        if !hit {
            continue;
        }
        let missing: Vec<String> = group
            .iter()
            .filter(|e| !sel.iter().any(|s| s == &e.id))
            .map(|e| e.id.clone())
            .collect();
        if !missing.is_empty() {
            gaps.push(GroupGap { table, missing });
        }
    }
    gaps
}

/// 把缺口一次性补齐（「整表一起勾」），结果仍按计划顺序。
pub(crate) fn fill_group_gaps(entries: &[SchemaDiffEntry], sel: &[String]) -> Vec<String> {
    let mut ids: Vec<String> = sel.to_vec();
    for gap in group_gaps(entries, sel) {
        ids.extend(gap.missing);
    }
    selected_ids(entries, &ids)
}

/// 一份脚本的摘要，用于执行前的风险提示。
#[derive(Debug, Clone, PartialEq, Default)]
pub(crate) struct ScriptSummary {
    pub(crate) total: usize,
    pub(crate) non_reversible: usize,
    pub(crate) warned: usize,
    pub(crate) tables: Vec<String>,
}

/// 统计语句数、不可逆条数、带告警条数与涉及的表。
pub(crate) fn summarize_script(stmts: &[DdlStatement]) -> ScriptSummary {
    let mut out = ScriptSummary {
        total: stmts.len(),
        ..Default::default()
    };
    for s in stmts {
        if s.non_reversible {
            out.non_reversible += 1;
        }
        if !s.warnings.is_empty() {
            out.warned += 1;
        }
        if s.table.is_empty() {
            continue;
        }
        if !out.tables.iter().any(|t| same_table(t, &s.table)) {
            out.tables.push(s.table.clone());
        }
    }
    out
}

/// 把脚本切成三段（behavior.md §15.4）。
pub(crate) struct ExecutionPlan<'a> {
    /// 必须在事务外先跑（事务内 `PRAGMA foreign_keys` 是 no-op）。
    pub(crate) pragma_head: Option<&'a DdlStatement>,
    /// 必须在事务外收尾。
    pub(crate) pragma_tail: Option<&'a DdlStatement>,
    /// 主体语句。
    pub(crate) body: Vec<&'a DdlStatement>,
    /// false ⇒ 该方言的 DDL 会隐式提交，主体只能逐条跑（失败留中间态）。
    pub(crate) wrap_in_tx: bool,
}

/// 按响应里的两个能力位分派执行方式：`pragmas_outside_tx` ⇒ 首尾两条 PRAGMA
/// （相位 0 / 255 且 `entry_ids` 为空）留在事务外；`ddl_in_tx` ⇒ 主体能否整体包进一个事务。
pub(crate) fn plan_execution(script: &DdlScript) -> ExecutionPlan<'_> {
    let mut head: Option<usize> = None;
    let mut tail: Option<usize> = None;
    for (i, s) in script.statements.iter().enumerate() {
        if !s.entry_ids.is_empty() {
            continue;
        }
        if s.phase == 0 && head.is_none() {
            head = Some(i);
        }
        if s.phase == 255 && tail.is_none() {
            tail = Some(i);
        }
    }
    let body: Vec<&DdlStatement> = script
        .statements
        .iter()
        .enumerate()
        .filter(|(i, _)| Some(*i) != head && Some(*i) != tail)
        .map(|(_, s)| s)
        .collect();
    ExecutionPlan {
        pragma_head: head.map(|i| &script.statements[i]),
        pragma_tail: tail.map(|i| &script.statements[i]),
        body,
        wrap_in_tx: script.ddl_in_tx,
    }
}

/// 期望侧候选：Redis 无模式概念（服务端拒），跨种类服务端同样拒绝 ⇒ 只允许同 kind。
/// 候选含基准侧自己（比对必为空），由呈现层标注，不在这里偷偷过滤——三端口径要一致。
pub(crate) fn candidate_connections(
    all: &[ConnectionInfo],
    source: &ConnectionInfo,
) -> Vec<ConnectionInfo> {
    all.iter()
        .filter(|c| c.kind == source.kind && is_comparable_kind(c.kind))
        .cloned()
        .collect()
}

/// 该数据库种类能否参与结构比对。
pub(crate) fn is_comparable_kind(kind: DatabaseKind) -> bool {
    !matches!(kind, DatabaseKind::Redis)
}

/// 代跑回滚的强确认：必须**逐字**输入基准侧连接名
/// （与 Web 侧 `confirmText.trim() === source.name`、TUI 侧 `ConfirmMatches` 同口径）。
pub(crate) fn confirm_matches(typed: &str, base_name: &str) -> bool {
    typed.trim() == base_name
}

/// 列表窗口：返回 `[start, end)`，让光标大致居中（与 TUI `listWindow` 同语义）。
/// 弹层里行为定高单行，靠窗口裁剪而不是真滚动条，三端「↑ 上方还有 N 行」文案才一致。
pub(crate) fn list_window(total: usize, cursor: usize, avail: usize) -> (usize, usize) {
    if total == 0 || avail == 0 || total <= avail {
        return (0, total);
    }
    let start = cursor.saturating_sub(avail / 2);
    let start = start.min(total - avail);
    (start, start + avail)
}

/// 两个能力位 → 人话，让操作者知道会不会包事务（与 TUI `cmpExecMode` 逐字一致）。
pub(crate) fn exec_mode(script: &DdlScript) -> &'static str {
    if script.ddl_in_tx && script.pragmas_outside_tx {
        "首尾 PRAGMA 事务外 · 主体单事务（失败整体回滚）"
    } else if script.ddl_in_tx {
        "全部语句包进单事务（失败整体回滚）"
    } else {
        "该方言 DDL 会隐式提交 ⇒ 逐条执行（失败留下中间态）"
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use polydb_core::{ConnectionId, SchemaDiffOp::*};

    fn entry(id: &str, op: SchemaDiffOp, phase: u8, table: &str) -> SchemaDiffEntry {
        SchemaDiffEntry {
            id: id.to_string(),
            op,
            phase,
            table: table.to_string(),
            object: id.split('/').nth(1).unwrap_or("").to_string(),
            from: None,
            to: None,
            flags: Vec::new(),
            destructive: matches!(op, DropIndex | DropTable | DropColumn | DropForeignKey),
            selected: false,
            reason: String::new(),
        }
    }

    fn conn(name: &str, kind: DatabaseKind) -> ConnectionInfo {
        ConnectionInfo {
            id: ConnectionId::new_v4(),
            name: name.to_string(),
            kind,
            host: None,
            port: None,
            database: None,
            username: None,
            options: Default::default(),
            ssh_tunnel: None,
            default_schema: None,
            read_only: None,
            group: None,
            color: None,
            created_at: chrono::DateTime::<chrono::Utc>::default(),
            updated_at: chrono::DateTime::<chrono::Utc>::default(),
        }
    }

    fn ids(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn labels_match_the_other_two_frontends() {
        assert_eq!(phase_label(0), "事务外前置（PRAGMA）");
        assert_eq!(phase_label(70), "变更列");
        assert_eq!(phase_label(255), "事务外收尾（PRAGMA）");
        assert_eq!(phase_label(42), "相位 42");
        assert_eq!(op_label(AlterColumn), "改列");
        assert_eq!(op_label(DropTable), "删表");
    }

    #[test]
    fn group_by_phase_keeps_server_order() {
        let entries = vec![
            entry("a//drop_table", DropTable, 40, "a"),
            entry("b/x/add_column", AddColumn, 60, "b"),
            entry("b/name/alter_column", AlterColumn, 70, "b"),
        ];
        let groups = group_by_phase(&entries);
        assert_eq!(groups.len(), 3);
        assert_eq!(groups[1].label, "新增列");
        assert_eq!(groups[1].entries.len(), 1);
    }

    #[test]
    fn selection_sets_are_order_preserving() {
        let mut e1 = entry("t/a/add_column", AddColumn, 60, "t");
        e1.selected = true;
        let mut e2 = entry("t/name/alter_column", AlterColumn, 70, "t");
        e2.selected = true;
        let e3 = entry("u//drop_table", DropTable, 40, "u");
        let entries = vec![e1, e2, e3];
        assert_eq!(
            default_selection(&entries),
            ids(&["t/a/add_column", "t/name/alter_column"])
        );
        assert_eq!(all_ids(&entries).len(), 3);
        assert_eq!(non_destructive_ids(&entries).len(), 2);
        // 勾选集乱序进来，也要按计划顺序回给服务端
        let sel = vec![
            "t/name/alter_column".to_string(),
            "u//drop_table".to_string(),
        ];
        assert_eq!(
            selected_ids(&entries, &sel),
            ids(&["t/name/alter_column", "u//drop_table"])
        );
        let toggled = toggle_entry(&sel, "t/a/add_column");
        assert!(toggled.contains(&"t/a/add_column".to_string()));
        assert_eq!(toggle_entry(&toggled, "t/a/add_column"), sel);
    }

    #[test]
    fn gaps_detect_and_fill_a_split_rebuild_group() {
        let entries = vec![
            entry("t/x/add_column", AddColumn, 60, "t"),
            entry("t/name/alter_column", AlterColumn, 70, "t"),
            entry("v/name/alter_column", AlterColumn, 70, "v"),
        ];
        // 勾了 t 的重建动作、漏了同表的加列 ⇒ 缺口；v 一条没勾 ⇒ 不算缺口
        let sel = vec!["t/name/alter_column".to_string()];
        let gaps = group_gaps(&entries, &sel);
        assert_eq!(gaps.len(), 1);
        assert_eq!(gaps[0].table, "t");
        assert_eq!(gaps[0].missing, ids(&["t/x/add_column"]));
        assert_eq!(
            fill_group_gaps(&entries, &sel),
            ids(&["t/x/add_column", "t/name/alter_column"])
        );
        assert!(group_gaps(&entries, &fill_group_gaps(&entries, &sel)).is_empty());
    }

    #[test]
    fn summarize_counts_statements_not_entries() {
        let stmts = vec![
            DdlStatement {
                entry_ids: vec![],
                sql: "PRAGMA foreign_keys=OFF;".into(),
                phase: 0,
                table: String::new(),
                warnings: vec![],
                non_reversible: false,
            },
            DdlStatement {
                entry_ids: vec!["t/name/alter_column".into()],
                sql: "CREATE TABLE t_new(...)".into(),
                phase: 70,
                table: "t".into(),
                warnings: vec!["重建表".into()],
                non_reversible: true,
            },
        ];
        let s = summarize_script(&stmts);
        assert_eq!((s.total, s.non_reversible, s.warned), (2, 1, 1));
        assert_eq!(s.tables, ids(&["t"]));
    }

    #[test]
    fn plan_execution_splits_pragmas_out_of_the_body() {
        let mk = |sql: &str, phase: u8, entry_ids: Vec<String>| DdlStatement {
            entry_ids,
            sql: sql.into(),
            phase,
            table: "t".into(),
            warnings: vec![],
            non_reversible: false,
        };
        let script = DdlScript {
            statements: vec![
                mk("PRAGMA foreign_keys=OFF;", 0, vec![]),
                mk("CREATE TABLE t_new(...)", 70, vec!["e1".into()]),
                mk("DROP TABLE t;", 70, vec!["e1".into()]),
                mk("PRAGMA foreign_keys=ON;", 255, vec![]),
            ],
            rollback: vec![],
            pragmas_outside_tx: true,
            ddl_in_tx: true,
        };
        let plan = plan_execution(&script);
        assert_eq!(
            plan.pragma_head.map(|s| s.sql.as_str()),
            Some("PRAGMA foreign_keys=OFF;")
        );
        assert_eq!(plan.pragma_tail.map(|s| s.phase), Some(255));
        assert_eq!(plan.body.len(), 2);
        assert!(plan.wrap_in_tx);

        // 无 PRAGMA 包裹：整份都是主体
        let plain = DdlScript {
            statements: script.statements[1..3].to_vec(),
            rollback: vec![],
            pragmas_outside_tx: false,
            ddl_in_tx: false,
        };
        let plan = plan_execution(&plain);
        assert!(plan.pragma_head.is_none() && plan.pragma_tail.is_none());
        assert_eq!(plan.body.len(), 2);
        assert!(!plan.wrap_in_tx);
    }

    #[test]
    fn candidates_are_same_kind_only_and_keep_the_base() {
        let base = conn("base", DatabaseKind::Sqlite);
        let all = vec![
            base.clone(),
            conn("want", DatabaseKind::Sqlite),
            conn("pg", DatabaseKind::Postgres),
            conn("cache", DatabaseKind::Redis),
        ];
        let got: Vec<String> = candidate_connections(&all, &base)
            .iter()
            .map(|c| c.name.clone())
            .collect();
        assert_eq!(got, ids(&["base", "want"]));
        assert!(is_comparable_kind(DatabaseKind::Oracle));
        assert!(!is_comparable_kind(DatabaseKind::Redis));
    }

    #[test]
    fn confirm_only_strips_surrounding_space() {
        assert!(confirm_matches("  基准库  ", "基准库"));
        assert!(!confirm_matches("基准", "基准库"));
        assert!(!confirm_matches("基准库 副本", "基准库"));
    }

    #[test]
    fn list_window_keeps_the_cursor_visible() {
        assert_eq!(list_window(0, 0, 8), (0, 0));
        // 装得下就整份显示，不掐头去尾
        assert_eq!(list_window(5, 4, 8), (0, 5));
        assert_eq!(list_window(30, 0, 10), (0, 10));
        assert_eq!(list_window(30, 29, 10), (20, 30));
        assert_eq!(list_window(30, 15, 10), (10, 20));
        // avail 为 0（弹层还没量到高度）时退化成整份，交由渲染层裁
        assert_eq!(list_window(12, 3, 0), (0, 12));
    }

    #[test]
    fn exec_mode_reads_both_capability_bits() {
        let mk = |pragmas_outside_tx, ddl_in_tx| DdlScript {
            statements: vec![],
            rollback: vec![],
            pragmas_outside_tx,
            ddl_in_tx,
        };
        assert_eq!(
            exec_mode(&mk(true, true)),
            "首尾 PRAGMA 事务外 · 主体单事务（失败整体回滚）"
        );
        assert_eq!(
            exec_mode(&mk(false, true)),
            "全部语句包进单事务（失败整体回滚）"
        );
        assert_eq!(
            exec_mode(&mk(true, false)),
            "该方言 DDL 会隐式提交 ⇒ 逐条执行（失败留下中间态）"
        );
    }

    /// 步骤名与 Web `STEP_LABEL` 逐字一致：三端同一份文案，不各写各的。
    #[test]
    fn wizard_step_labels_match_the_web_wizard() {
        use crate::app::CmpStep;
        assert_eq!(CmpStep::Source.label(), "选基准侧");
        assert_eq!(CmpStep::Target.label(), "选期望侧");
        assert_eq!(CmpStep::Review.label(), "差异与勾选");
        assert_eq!(CmpStep::Apply.label(), "脚本与应用");
    }
}
