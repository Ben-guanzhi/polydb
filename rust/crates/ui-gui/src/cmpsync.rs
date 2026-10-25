//! 结构比对与同步向导在 GUI 侧的纯函数层（Compare & Sync E5.4，状态机与分派环路 E6.3 抽层）。
//!
//! 定位与 Go `pkg/cmpsync`、Web `lib/compareSync.ts` 同一层：差异与语句生成的真相源在
//! `polydb-schema-diff`（服务端判定），本层**不重复判定**，只做五件事——
//!   1. 呈现：相位/动作的中文名、按相位分组、脚本摘要；
//!   2. 勾选集运算：默认勾选、切换、整组补漏（§15.5 的第二条守卫会在服务端 400，提前显示缺口）；
//!   3. 执行编排：按响应里的两个能力位把脚本切成「事务外 PRAGMA + 主体」；
//!   4. 向导状态机（E6.3）：步骤/游标/勾选集/回滚确认缓冲的**纯转移** + 按键语义表；
//!   5. 分派环路（E6.3）：`run_script` 把「该不该开事务」变成可断言的调用序列。
//!
//! 4、5 两块此前长在 `app.rs` 的 GPUI 实体与 `self.io` 闭包里，于是 GUI 是三份呈现实现里
//! 唯一一份没有 keystroke 级测试、也没有应用环路测试的。搬进本层后这里**不出现 gpui 类型**，
//! `app.rs` 只剩接线：发 IO、`cx.notify()`、把返回的文案写进 `status`。
//!
//! 刻意不落盘：存下来的勾选集下一轮必然陈旧（§15.5 第一条守卫）。
//! AGPL 红线：只借鉴 TablePro 的交互语义，未移植任何外部代码。

use async_trait::async_trait;
use polydb_core::{
    ConnectionId, ConnectionInfo, CoreResult, DatabaseKind, DdlScript, DdlStatement,
    SchemaDdlRequest, SchemaDiffEntry, SchemaDiffOp, SchemaDiffOptions, SchemaDiffTarget,
};

use crate::util::{char_ceil, char_floor};

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
    filter_entries(entries, |e| e.selected)
}

/// 计划里的全部条目 id（全选）。
pub(crate) fn all_ids(entries: &[SchemaDiffEntry]) -> Vec<String> {
    filter_entries(entries, |_| true)
}

/// 全部非破坏性条目 id（「只勾非破坏」）。
pub(crate) fn non_destructive_ids(entries: &[SchemaDiffEntry]) -> Vec<String> {
    filter_entries(entries, |e| !e.destructive)
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
    let sel_set: std::collections::HashSet<&String> = sel.iter().collect();
    filter_entries(entries, |e| sel_set.contains(&e.id))
}

/// 某表的全部条目。
pub(crate) fn entries_of_table(entries: &[SchemaDiffEntry], table: &str) -> Vec<SchemaDiffEntry> {
    entries
        .iter()
        .filter(|e| same_table(&e.table, table))
        .cloned()
        .collect()
}

/// 通用条目过滤：按谓词筛选并提取 id（SICP ch1 高阶函数：消除手写循环重复）。
fn filter_entries<F>(entries: &[SchemaDiffEntry], pred: F) -> Vec<String>
where
    F: Fn(&SchemaDiffEntry) -> bool,
{
    entries
        .iter()
        .filter(|e| pred(e))
        .map(|e| e.id.clone())
        .collect()
}

/// 本次计划里「大概率进重建路径」的表，用于提示整组勾选（按首次出现顺序去重）。
/// 归一化键 + HashSet 判重（SICP ch2 集合表示选择：linear scan → O(1) 成员判定，
/// 与 same_table 的 eq_ignore_ascii_case 完全等价），保序仍由 Vec 承担。
pub(crate) fn rebuild_likely_tables(entries: &[SchemaDiffEntry]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
    for e in entries {
        if !always_rebuild(e.op) {
            continue;
        }
        if seen.insert(e.table.to_ascii_lowercase()) {
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
    // 预构建勾选集成员表（SICP ch2 集合表示选择）：成员判定 O(1)，不再 Θ(k) 线性扫。
    let sel_set: std::collections::HashSet<&String> = sel.iter().collect();
    let mut gaps = Vec::new();
    for table in rebuild_likely_tables(entries) {
        let group = entries_of_table(entries, &table);
        let hit = group
            .iter()
            .any(|e| always_rebuild(e.op) && sel_set.contains(&e.id));
        if !hit {
            continue;
        }
        let missing: Vec<String> = group
            .iter()
            .filter(|e| !sel_set.contains(&e.id))
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
    let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
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
        if seen.insert(s.table.to_ascii_lowercase()) {
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

// ─── E6.3 向导状态机（纯层，无 gpui）───────────────────────

/// 向导步骤：与 Web `STEP_ORDER` / `STEP_LABEL` 同序同名。
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum CmpStep {
    Source,
    Target,
    Review,
    Apply,
}

impl CmpStep {
    pub(crate) fn label(self) -> &'static str {
        match self {
            CmpStep::Source => "选基准侧",
            CmpStep::Target => "选期望侧",
            CmpStep::Review => "差异与勾选",
            CmpStep::Apply => "脚本与应用",
        }
    }
}

/// 一条执行失败的语句（只记首个失败，其余计入 `SyncOutcome::skipped`）。
#[derive(Clone, Debug)]
pub(crate) struct FailedStmt {
    pub(crate) index: usize,
    pub(crate) phase: u8,
    pub(crate) table: String,
    pub(crate) entries: Vec<String>,
    pub(crate) sql: String,
    pub(crate) reason: String,
}

/// 一次脚本执行的结果，与 Go `cmpsync.ApplyOutcome` 同口径。
#[derive(Clone, Debug)]
pub(crate) struct SyncOutcome {
    pub(crate) ran: usize,
    pub(crate) failed: Vec<FailedStmt>,
    pub(crate) skipped: usize,
    pub(crate) rolled_back: bool,
    /// true ⇒ 这次跑的是回滚脚本。
    pub(crate) rollback: bool,
}

/// 向导态。刻意**不落盘**：存下来的勾选集下一轮必然陈旧（behavior.md §15.5 第一条守卫）。
pub(crate) struct CompareState {
    pub(crate) step: CmpStep,
    /// 已确认的两侧（步骤 ①② 只挪高亮，Enter 才写回这里）。
    pub(crate) src: ConnectionId,
    pub(crate) tgt: Option<ConnectionId>,
    /// ①② 两步各自列表的高亮下标。
    pub(crate) src_cur: usize,
    pub(crate) tgt_cur: usize,
    pub(crate) ignore_defaults: bool,
    pub(crate) select_destructive: bool,
    /// 比对 / 生成 / 执行在途（期间拒绝再次触发）。
    pub(crate) loading: bool,
    pub(crate) entries: Vec<SchemaDiffEntry>,
    pub(crate) sel: Vec<String>,
    pub(crate) entry_cur: usize,
    pub(crate) gaps: Vec<GroupGap>,
    pub(crate) script: Option<DdlScript>,
    pub(crate) stmt_cur: usize,
    pub(crate) outcome: Option<SyncOutcome>,
    /// 正向脚本已成功执行 ⇒ 才允许代跑回滚。
    pub(crate) applied: bool,
    /// 回滚强确认输入态。
    pub(crate) confirming: bool,
    pub(crate) confirm_buf: String,
    pub(crate) confirm_cursor: usize,
    /// A4 锚定表（schema, table）：Review 步只显示该表的差异条目。锚定是
    /// **过滤与预选**，服务端不知晓；全量计划保留在 `all_entries`，解除锚定
    /// 即恢复全量并按服务端默认重算勾选。
    pub(crate) anchor: Option<(String, String)>,
    /// 本次比对的全部条目（锚定过滤前的原样计划）。
    pub(crate) all_entries: Vec<SchemaDiffEntry>,
}

/// 列表高亮移动（`delta` 为 ±1，越界回绕，与 TUI/GUI 其它列表一致）。
fn shift_index(cur: usize, total: usize, delta: isize) -> usize {
    if total == 0 {
        return 0;
    }
    let cur = cur.min(total - 1) as isize;
    ((cur + delta + total as isize) % total as isize) as usize
}

impl CompareState {
    pub(crate) fn new(src: ConnectionId, src_cur: usize) -> Self {
        Self {
            step: CmpStep::Source,
            src,
            tgt: None,
            src_cur,
            tgt_cur: 0,
            ignore_defaults: false,
            select_destructive: false,
            loading: false,
            entries: Vec::new(),
            sel: Vec::new(),
            entry_cur: 0,
            gaps: Vec::new(),
            script: None,
            stmt_cur: 0,
            outcome: None,
            applied: false,
            confirming: false,
            confirm_buf: String::new(),
            confirm_cursor: 0,
            anchor: None,
            all_entries: Vec::new(),
        }
    }

    /// A4 锚定：把 Review 步定界到一张表。比对尚未发生时只记录锚，
    /// `finish_diff` 会带锚过滤。
    pub(crate) fn anchor_to(&mut self, schema: &str, table: &str) {
        self.anchor = Some((schema.to_string(), table.to_string()));
        if !self.all_entries.is_empty() {
            self.apply_anchor();
        }
    }

    /// 解除锚定：恢复全量条目并按服务端默认重算勾选（锚定视图里的手动
    /// 勾选不保留——视图变了，默认重来最可预期；生成前有缺口提示兜底）。
    pub(crate) fn clear_anchor(&mut self) {
        self.anchor = None;
        if !self.all_entries.is_empty() {
            self.apply_anchor();
        }
    }

    /// 按 anchor 把 `all_entries` 过滤进 `entries`，重算默认勾选、缺口与
    /// 高亮。`sel` 用服务端 `e.selected` 计划（非破坏性预选），锚定与否同规。
    fn apply_anchor(&mut self) {
        self.entries = match &self.anchor {
            Some((_, table)) => self
                .all_entries
                .iter()
                .filter(|e| e.table == *table)
                .cloned()
                .collect(),
            None => self.all_entries.clone(),
        };
        self.sel = default_selection(&self.entries);
        self.entry_cur = 0;
        self.recalc_gaps();
    }

    pub(crate) fn recalc_gaps(&mut self) {
        self.gaps = group_gaps(&self.entries, &self.sel);
    }

    /// 四步共用的高亮移动：`total` 由调用侧给（①② 步来自连接列表，③④ 步来自本状态）。
    pub(crate) fn shift(&mut self, delta: isize, total: usize) {
        match self.step {
            CmpStep::Source => self.src_cur = shift_index(self.src_cur, total, delta),
            CmpStep::Target => self.tgt_cur = shift_index(self.tgt_cur, total, delta),
            CmpStep::Review => self.entry_cur = shift_index(self.entry_cur, total, delta),
            CmpStep::Apply => self.stmt_cur = shift_index(self.stmt_cur, total, delta),
        }
    }

    /// 点击行 = 先把高亮挪过去，再走该步的 Enter 语义（键盘与鼠标共用一套状态）。
    pub(crate) fn set_cur(&mut self, ix: usize) {
        match self.step {
            CmpStep::Source => self.src_cur = ix,
            CmpStep::Target => self.tgt_cur = ix,
            CmpStep::Review => self.entry_cur = ix,
            CmpStep::Apply => self.stmt_cur = ix,
        }
    }

    /// ① Enter 之后：基准侧即执行侧，期望侧待选。
    pub(crate) fn goto_target(&mut self, src: ConnectionId, src_name: &str) -> String {
        self.src = src;
        self.tgt = None;
        self.tgt_cur = 0;
        self.step = CmpStep::Target;
        format!("结构同步向导 ② 选期望侧（基准：{src_name}）")
    }

    /// 比对/生成/执行发起：占用 `loading`，清掉上一次的结果。
    pub(crate) fn begin_io(&mut self) {
        self.loading = true;
        self.outcome = None;
    }

    /// 比对回来：勾选集由服务端给的计划重算（客户端不缓存），并把文案算好。
    /// 锚定时只显示锚定表的条目（A4），全量计划留在 `all_entries`。
    pub(crate) fn finish_diff(&mut self, entries: Vec<SchemaDiffEntry>) -> String {
        self.loading = false;
        self.all_entries = entries;
        self.apply_anchor();
        self.script = None;
        self.applied = false;
        self.step = CmpStep::Review;
        if self.entries.is_empty() {
            if self.anchor.is_some() && !self.all_entries.is_empty() {
                let (_, table) = self.anchor.as_ref().unwrap();
                format!("锚定表 {table} 无差异（按 u 解除锚定看全量）")
            } else {
                "两侧结构一致，无需同步".into()
            }
        } else {
            self.review_status()
        }
    }

    pub(crate) fn fail_io(&mut self) {
        self.loading = false;
    }

    /// ③ 步的状态行：已勾计数（含破坏性）+ 缺口提示。
    pub(crate) fn review_status(&self) -> String {
        let destruct = self
            .entries
            .iter()
            .filter(|e| e.destructive && self.sel.iter().any(|s| s == &e.id))
            .count();
        let mut s = format!(
            "共 {} 条变更 · 已勾 {} 条（其中破坏性 {}）",
            self.entries.len(),
            self.sel.len(),
            destruct
        );
        if let Some((schema, table)) = &self.anchor {
            s += &format!(
                " · 锚定 {schema}.{table}（{}/{}，按 u 解除）",
                self.entries.len(),
                self.all_entries.len()
            );
        }
        if let Some(g) = self.gaps.first() {
            s += &format!(" · ⚠ 重建表 {} 未整组勾选（按 f 补齐）", g.table);
        }
        s
    }

    /// Space / 点击复选框。下标越界时什么都不改（返回 None，调用侧不刷 status）。
    pub(crate) fn toggle_at(&mut self, ix: usize) -> Option<String> {
        let id = self.entries.get(ix).map(|e| e.id.clone())?;
        self.sel = toggle_entry(&self.sel, &id);
        self.recalc_gaps();
        Some(self.review_status())
    }

    /// `a` 只勾非破坏 / `A` 全选 / `f` 补齐重建组，都是整份替换勾选集。
    pub(crate) fn select_all(&mut self, only_safe: bool) -> String {
        self.sel = if only_safe {
            non_destructive_ids(&self.entries)
        } else {
            all_ids(&self.entries)
        };
        self.recalc_gaps();
        self.review_status()
    }

    pub(crate) fn fill_gaps(&mut self) -> String {
        if self.gaps.is_empty() {
            return "没有缺口：重建表的条目已整组勾选".into();
        }
        self.sel = fill_group_gaps(&self.entries, &self.sel);
        self.recalc_gaps();
        self.review_status()
    }

    /// `i` 忽略默认值 / `n` 破坏性默认勾选：都是**服务端**选项，返回是否要重跑比对
    /// （只在 ③ 步重跑——①② 步还没有计划，跑了也没地方画）。
    pub(crate) fn toggle_option(&mut self, ignore: bool) -> (String, bool) {
        let field = if ignore {
            &mut self.ignore_defaults
        } else {
            &mut self.select_destructive
        };
        *field = !*field;
        let name = if ignore {
            "忽略默认值"
        } else {
            "破坏性默认勾选"
        };
        let status = format!("{} = {}", name, if *field { "开" } else { "关" });
        (status, self.step == CmpStep::Review)
    }

    /// 生成脚本前的门禁：`Ok(req)` 可提交；`Err(Some(提示))` 要显示；`Err(None)` 静默返回。
    /// 缺口未补齐的勾选集提交给服务端一定 400（§15.5 第二条守卫），呈现层提前拦。
    pub(crate) fn ddl_request(&self) -> Result<SchemaDdlRequest, Option<&'static str>> {
        if self.loading {
            return Err(None);
        }
        if self.entries.is_empty() {
            return Err(Some("两侧结构一致，无需同步"));
        }
        if self.sel.is_empty() {
            return Err(Some("尚未勾选任何变更：按 a/A 勾选后再生成"));
        }
        if !self.gaps.is_empty() {
            return Err(Some("⚠ 重建表的条目必须整组勾选：按 f 补齐后再生成"));
        }
        let tgt = self.tgt.ok_or(None)?;
        Ok(SchemaDdlRequest {
            source: SchemaDiffTarget {
                connection_id: self.src,
                schema: None,
            },
            target: SchemaDiffTarget {
                connection_id: tgt,
                schema: None,
            },
            // 必须与比对时同一份选项：回滚方向按它镜像重算。
            options: Some(SchemaDiffOptions {
                ignore_defaults: Some(self.ignore_defaults),
                select_destructive: Some(self.select_destructive),
            }),
            // 勾选集按计划顺序回给服务端，与 TUI/Web 的文案口径一致。
            selected: selected_ids(&self.entries, &self.sel),
        })
    }

    /// ③ `g` 回来：进 ④ 步，执行态清零。
    pub(crate) fn finish_script(&mut self, script: DdlScript) -> String {
        self.loading = false;
        let s = summarize_script(&script.statements);
        self.script = Some(script);
        self.step = CmpStep::Apply;
        self.stmt_cur = 0;
        self.outcome = None;
        self.applied = false;
        self.confirming = false;
        format!(
            "脚本 {} 条 · 不可逆 {} · 涉及 {} 张表：x 执行 · r 回滚",
            s.total,
            s.non_reversible,
            s.tables.len()
        )
    }

    /// 执行前的门禁：给出要跑的 (连接, 脚本)。`Err(Some(提示))` 需要显示，`Err(None)` 静默。
    pub(crate) fn script_to_run(
        &self,
        rollback: bool,
    ) -> Result<(ConnectionId, DdlScript), Option<&'static str>> {
        if self.loading {
            return Err(None);
        }
        let script = self
            .script
            .clone()
            .ok_or(Some("还没有脚本：回第三步按 g 生成"))?;
        if rollback && !self.applied {
            return Err(Some("还没有成功执行过正向脚本，无需回滚"));
        }
        let will_run = if rollback {
            &script.rollback
        } else {
            &script.statements
        };
        if will_run.is_empty() {
            return Err(Some("脚本为空，无需执行"));
        }
        Ok((self.src, script))
    }

    /// ④ `x` / 回滚确认后的发起。
    pub(crate) fn begin_apply(&mut self, rollback: bool) -> &'static str {
        self.loading = true;
        self.confirming = false;
        self.confirm_buf.clear();
        self.confirm_cursor = 0;
        if rollback {
            "回滚脚本执行中…"
        } else {
            "同步执行中…"
        }
    }

    /// 执行回来的文案（与 TUI `onSyncApply`、Web 侧同口径）。
    pub(crate) fn finish_apply(&mut self, out: SyncOutcome) -> String {
        self.loading = false;
        let ok = out.failed.is_empty();
        let status = if out.rollback {
            if ok {
                self.applied = false;
                format!(
                    "回滚脚本已执行 {} 条；结构退回去了，正向里丢的数据找不回来（§15.6）",
                    out.ran
                )
            } else {
                let f = &out.failed[0];
                format!("回滚中断：第 {} 条失败（{}）", f.index + 1, f.reason)
            }
        } else {
            self.applied = ok;
            if ok {
                format!("同步完成：{} 条语句已执行（按 c 复查差异）", out.ran)
            } else {
                let f = &out.failed[0];
                format!("执行中断：第 {} 条失败（{}）", f.index + 1, f.reason)
            }
        };
        self.outcome = Some(out);
        status
    }

    /// ④ `r`：进入回滚强确认输入态（与 Web/TUI 同口径：逐字输入基准侧连接名）。
    pub(crate) fn start_confirm(&mut self, base_name: &str) -> String {
        if !self.applied {
            return "还没有成功执行过正向脚本，无需回滚".into();
        }
        self.confirming = true;
        self.confirm_buf.clear();
        self.confirm_cursor = 0;
        format!("代跑回滚不可逆：请逐字输入基准侧连接名「{base_name}」")
    }

    pub(crate) fn cancel_confirm(&mut self) -> &'static str {
        self.confirming = false;
        self.confirm_buf.clear();
        self.confirm_cursor = 0;
        "已取消回滚确认"
    }

    pub(crate) fn insert_confirm(&mut self, text: &str) {
        let boundary = char_ceil(
            &self.confirm_buf,
            self.confirm_cursor.min(self.confirm_buf.len()),
        );
        self.confirm_buf.insert_str(boundary, text);
        self.confirm_cursor = boundary + text.len();
    }

    pub(crate) fn backspace_confirm(&mut self) {
        if self.confirm_cursor == 0 {
            return;
        }
        // 退到**光标前那个字符**的起点：char_floor 在边界上原地不动，所以先退一格。
        let start = char_floor(&self.confirm_buf, self.confirm_cursor - 1);
        self.confirm_buf
            .replace_range(start..self.confirm_cursor, "");
        self.confirm_cursor = start;
    }

    /// 返回上一步（Esc / Backspace）；① 步的 Esc 由调用侧直接关窗。
    pub(crate) fn back(&mut self) {
        self.outcome = None;
        self.confirming = false;
        self.step = match self.step {
            CmpStep::Source => CmpStep::Source,
            CmpStep::Target => {
                self.tgt = None;
                CmpStep::Source
            }
            CmpStep::Review => CmpStep::Target,
            CmpStep::Apply => CmpStep::Review,
        };
    }

    /// 弹层底部键位提示（与 TUI 的 `cmpFooter` 同语义，逐字对齐后写进 docs/frontends.md）。
    pub(crate) fn footer(&self) -> &'static str {
        if self.confirming {
            return "逐字输入基准侧连接名 · Enter 确认执行回滚 · Esc 取消";
        }
        match self.step {
            CmpStep::Source => "↑/↓ · k/j 选基准侧 · Enter 下一步 · Esc 退出向导（基准侧=要被改成期望侧的样子，也是执行侧）",
            CmpStep::Target => "↑/↓ · k/j 选期望侧 · i 忽略默认值 · n 破坏性默认勾选 · Enter 开始比对 · Esc 上一步",
            CmpStep::Review => "↑/↓ · k/j 条目 · Space 勾选 · a 只勾非破坏 · A 全选 · i/n 改选项并重比对 · f 补齐重建组 · g 生成脚本 · Esc 上一步",
            CmpStep::Apply => "↑/↓ · k/j 滚动 · x 执行正向 · r 代跑回滚 · c 复查差异 · g 重新生成 · Esc 返回勾选",
        }
    }
}

/// 一次按键在向导里的语义。`app.rs` 只负责把动作接到 IO 与 `cx.notify()` 上，
/// 「哪个键在哪一步该干什么」这张表本身在这里，因此可被逐条断言。
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum CmpAction {
    Nothing,
    /// ↑/↓ 与 k/j
    Move(isize),
    /// i=true 忽略默认值 / n=false 破坏性默认勾选
    ToggleOption(bool),
    PickSource,
    RunDiff,
    ToggleAtCursor,
    SelectAll {
        only_safe: bool,
    },
    FillGaps,
    /// `u`：解除锚定（Review 步，A4）
    ClearAnchor,
    Generate,
    Apply,
    StartConfirm,
    Back,
    /// ① 步的 Esc：关窗而不是后退。
    Close,
}

pub(crate) fn key_action(step: CmpStep, loading: bool, key: &str) -> CmpAction {
    match key {
        "up" | "k" => return CmpAction::Move(-1),
        "down" | "j" => return CmpAction::Move(1),
        "i" => return CmpAction::ToggleOption(true),
        "n" => return CmpAction::ToggleOption(false),
        "escape" => {
            return if step == CmpStep::Source {
                CmpAction::Close
            } else {
                CmpAction::Back
            }
        }
        _ => {}
    }
    match (step, key) {
        (CmpStep::Source, "enter") => CmpAction::PickSource,
        (CmpStep::Target, "enter") => CmpAction::RunDiff,
        (CmpStep::Review, "enter") | (CmpStep::Review, "space") => CmpAction::ToggleAtCursor,
        (CmpStep::Review, "a") => CmpAction::SelectAll { only_safe: true },
        (CmpStep::Review, "A") => CmpAction::SelectAll { only_safe: false },
        (CmpStep::Review, "f") => CmpAction::FillGaps,
        (CmpStep::Review, "u") => CmpAction::ClearAnchor,
        (CmpStep::Review, "r") => CmpAction::RunDiff,
        (CmpStep::Review, "g") => CmpAction::Generate,
        // ④ 步的执行/回滚/复查/重新生成在途时一律吞掉（其余步的同类键自己带 loading 门禁）。
        (CmpStep::Apply, "x") if !loading => CmpAction::Apply,
        (CmpStep::Apply, "r") if !loading => CmpAction::StartConfirm,
        (CmpStep::Apply, "c") if !loading => CmpAction::RunDiff,
        (CmpStep::Apply, "g") if !loading => CmpAction::Generate,
        (_, "backspace") => CmpAction::Back,
        _ => CmpAction::Nothing,
    }
}

// ─── E6.3 分派环路：把「该不该开事务」变成可断言的调用序列 ───

/// 跑一份脚本只需要这五个动作——与 Go `transport.Client` 的同名方法一一对应。
/// 抽出来是为了让**分派**能在没有 GPUI 的情况下被逐条断言（此前它内联在 `app.rs`
/// 的 `self.io` async 闭包里，是同一逻辑的第三份拷贝、也是唯一没测试的那份）。
#[async_trait]
pub(crate) trait SyncExec: Send + Sync {
    async fn begin(&self) -> CoreResult<String>;
    async fn exec_in_tx(&self, tx: &str, sql: &str) -> CoreResult<()>;
    async fn exec(&self, sql: &str) -> CoreResult<()>;
    async fn commit(&self, tx: &str) -> CoreResult<()>;
    async fn rollback(&self, tx: &str) -> CoreResult<()>;
}

/// 按响应里两个能力位分派执行（behavior.md §15.4）。`rollback=true` 跑 `script.rollback`，
/// 两个能力位正向与回滚共用一份（浅拷贝语义：只换 statements）。
pub(crate) async fn run_script(
    script: &DdlScript,
    rollback: bool,
    ex: &dyn SyncExec,
) -> SyncOutcome {
    let mut sc = script.clone();
    if rollback {
        sc.statements = script.rollback.clone();
    }
    let plan = plan_execution(&sc);
    // (语句, 是否进事务)：PRAGMA 只有确认「主体在事务里」时才需要拆出去，
    // 否则整份按 wrap_in_tx 逐条或整体跑（与 Go applyScriptSync 同分支）。
    let mut steps: Vec<(&DdlStatement, bool)> = Vec::new();
    if plan.wrap_in_tx && sc.pragmas_outside_tx {
        if let Some(head) = plan.pragma_head {
            steps.push((head, false));
        }
        steps.extend(plan.body.into_iter().map(|s| (s, true)));
        if let Some(tail) = plan.pragma_tail {
            steps.push((tail, false));
        }
    } else {
        steps.extend(sc.statements.iter().map(|s| (s, plan.wrap_in_tx)));
    }
    let mut out = SyncOutcome {
        ran: 0,
        failed: Vec::new(),
        skipped: 0,
        rolled_back: false,
        rollback,
    };
    let mut txn: Option<String> = None;
    // 失败只记账，**不**清掉 txn：留着给循环末尾回滚（与 Go `applyScriptSync` 同形；
    // 提交失败那条分支里 `txn.take()` 已经取走了句柄，天然不会再回滚）。
    let fail = |out: &mut SyncOutcome, index: usize, s: &DdlStatement, reason: String| {
        out.failed.push(FailedStmt {
            index,
            phase: s.phase,
            table: s.table.clone(),
            entries: s.entry_ids.clone(),
            sql: s.sql.clone(),
            reason,
        });
        out.skipped = steps.len().saturating_sub(index + 1);
    };
    for (i, (s, in_tx)) in steps.iter().enumerate() {
        if *in_tx && txn.is_none() {
            match ex.begin().await {
                Ok(tx) => txn = Some(tx),
                Err(e) => {
                    fail(&mut out, i, s, format!("开启事务失败: {e}"));
                    break;
                }
            }
        }
        // 主体跑完接着是事务外 PRAGMA：先提交，PRAGMA 进事务是 no-op。
        if !in_tx {
            if let Some(tx) = txn.take() {
                if let Err(e) = ex.commit(&tx).await {
                    fail(&mut out, i, s, format!("提交失败: {e}"));
                    break;
                }
            }
        }
        let r = match txn.as_ref() {
            Some(tx) => ex.exec_in_tx(tx, &s.sql).await,
            None => ex.exec(&s.sql).await,
        };
        match r {
            Ok(_) => out.ran += 1,
            Err(e) => {
                fail(&mut out, i, s, e.to_string());
                break;
            }
        }
    }
    if let Some(tx) = txn {
        if !out.failed.is_empty() {
            // 主体在事务里 ⇒ 失败即整体回滚，不留中间态。
            out.rolled_back = ex.rollback(&tx).await.is_ok();
        } else if let Err(e) = ex.commit(&tx).await {
            out.failed.push(FailedStmt {
                index: steps.len().saturating_sub(1),
                phase: 0,
                table: String::new(),
                entries: Vec::new(),
                sql: "(COMMIT)".into(),
                reason: format!("提交失败: {e}"),
            });
        }
    }
    out
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
        assert_eq!(CmpStep::Source.label(), "选基准侧");
        assert_eq!(CmpStep::Target.label(), "选期望侧");
        assert_eq!(CmpStep::Review.label(), "差异与勾选");
        assert_eq!(CmpStep::Apply.label(), "脚本与应用");
    }

    // ─── E6.3 应用环路：真引擎 + 调用录音（与 Go 的 apply_dispatch_test.go 同一批断言）───
    //
    // 分派逻辑此前内联在 `app.rs` 的 `self.io` async 闭包里：那是同一规则的第三份拷贝，
    // 也是唯一看不见调用序列的一份。搬进 `run_script` 后，这里套一层**录音装饰器**
    // （透传给真 LocalTransport + 真 :memory: SQLite），既断言**谁在事务里**，
    // 也断言**库里到底剩了什么**——两组断言互斥，缺一都不能证明分派。

    use polydb_app_core::AppCore;
    use polydb_core::{BeginTransactionRequest, CreateConnectionRequest, Value};
    use polydb_storage::{FileKeyring, Storage};
    use polydb_transport::{LocalTransport, Transport};
    use std::sync::{Arc, Mutex};

    fn sqlite_req(name: &str) -> CreateConnectionRequest {
        CreateConnectionRequest {
            name: name.to_string(),
            kind: DatabaseKind::Sqlite,
            host: None,
            port: None,
            database: Some(":memory:".to_string()),
            username: None,
            password_ref: None,
            password: None,
            options: Default::default(),
            ssh_tunnel: None,
            default_schema: None,
            read_only: None,
            group: None,
            color: None,
        }
    }

    /// 真引擎 + 已建连的连接 id。走的是产品同一条线路（LocalTransport → AppCore → rusqlite）。
    fn engine(name: &str) -> (LocalTransport, ConnectionId) {
        let dir = std::env::temp_dir().join(format!("polydb-e63-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
        let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
        let app = Arc::new(AppCore::with_keyring(storage, Arc::new(kr)));
        let t = LocalTransport::new(app);
        let id = t.create_connection(&sqlite_req(name)).unwrap().id;
        t.connect(id).unwrap();
        (t, id)
    }

    /// 录音装饰器：只记录调用序列，执行一律透传给真客户端——不 mock 数据库。
    struct Recorder {
        t: LocalTransport,
        id: ConnectionId,
        calls: Mutex<Vec<String>>,
    }

    impl Recorder {
        fn new(t: LocalTransport, id: ConnectionId) -> Self {
            Self {
                t,
                id,
                calls: Mutex::new(Vec::new()),
            }
        }
        fn push(&self, s: String) {
            self.calls.lock().unwrap().push(s);
        }
        fn seq(&self) -> String {
            self.calls.lock().unwrap().join(" | ")
        }
        fn begins(&self) -> usize {
            self.calls
                .lock()
                .unwrap()
                .iter()
                .filter(|c| *c == "begin")
                .count()
        }
    }

    #[async_trait]
    impl SyncExec for Recorder {
        async fn begin(&self) -> CoreResult<String> {
            self.push("begin".into());
            let info = self
                .t
                .begin_transaction(&BeginTransactionRequest {
                    connection_id: self.id,
                    isolation_level: None,
                })
                .await?;
            Ok(info.id.to_string())
        }
        async fn exec_in_tx(&self, tx: &str, sql: &str) -> CoreResult<()> {
            self.push(format!("tx:{sql}"));
            self.t.execute_in_transaction(tx, sql, &[]).await?;
            Ok(())
        }
        async fn exec(&self, sql: &str) -> CoreResult<()> {
            self.push(format!("exec:{sql}"));
            self.t.execute(self.id, sql, &[]).await?;
            Ok(())
        }
        async fn commit(&self, tx: &str) -> CoreResult<()> {
            self.push("commit".into());
            self.t.commit_transaction(tx).await?;
            Ok(())
        }
        async fn rollback(&self, tx: &str) -> CoreResult<()> {
            self.push("rollback".into());
            self.t.rollback_transaction(tx).await?;
            Ok(())
        }
    }

    fn run(ex: &Recorder, script: &DdlScript, rollback: bool) -> SyncOutcome {
        polydb_rt::block_on(run_script(script, rollback, ex))
    }

    /// 用真引擎回读：某张表在不在。走未包装的客户端，不污染录音序列。
    fn table_count(t: &LocalTransport, id: ConnectionId, name: &str) -> i64 {
        let sql =
            format!("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='{name}'");
        let res = polydb_rt::block_on(t.execute(id, &sql, &[])).unwrap();
        match &res.rows[0][0] {
            Value::Integer(n) => *n,
            v => panic!("回读 {name} 不是整数：{v:?}"),
        }
    }

    fn stmt(sql: &str, phase: u8, table: &str) -> DdlStatement {
        DdlStatement {
            entry_ids: if table.is_empty() {
                vec![]
            } else {
                vec!["e".into()]
            },
            sql: sql.into(),
            phase,
            table: table.into(),
            warnings: vec![],
            non_reversible: false,
        }
    }

    fn script_of(
        statements: Vec<DdlStatement>,
        pragmas_outside_tx: bool,
        ddl_in_tx: bool,
    ) -> DdlScript {
        DdlScript {
            statements,
            rollback: vec![],
            pragmas_outside_tx,
            ddl_in_tx,
        }
    }

    /// 分派一：`pragmas_outside_tx=false` ⇒ 整份脚本一个事务；中途失败时已落地的语句必须被撤销。
    #[test]
    fn whole_tx_branch_rolls_back_landed_statements() {
        let (t, id) = engine("e63-tx");
        let ex = Recorder::new(t, id);
        let script = script_of(
            vec![
                stmt("CREATE TABLE e6_tx(a INTEGER)", 50, "e6_tx"),
                stmt("CREATE TABLE e6_tx(b INTEGER)", 50, "e6_tx"), // 重名 ⇒ 必炸
                stmt("CREATE TABLE e6_after(b INTEGER)", 50, "e6_after"),
            ],
            false,
            true,
        );
        let out = run(&ex, &script, false);

        assert_eq!(
            ex.seq(),
            "begin | tx:CREATE TABLE e6_tx(a INTEGER) | tx:CREATE TABLE e6_tx(b INTEGER) | rollback",
            "整份包进单事务：不得出现事务外的 exec"
        );
        assert_eq!(
            (out.ran, out.failed.len(), out.rolled_back, out.skipped),
            (1, 1, true, 1)
        );
        // Ran=1 只说明"执行过没报错"；回滚后库里必须什么都没有了。
        assert_eq!(
            table_count(&ex.t, id, "e6_tx"),
            0,
            "事务分支必须撤销已落地的建表"
        );
        assert_eq!(
            table_count(&ex.t, id, "e6_after"),
            0,
            "失败之后的语句不该跑"
        );
    }

    /// 分派二：`ddl_in_tx=false` ⇒ 不开事务、逐条跑，失败必须留下中间态（与上一份互斥）。
    #[test]
    fn non_tx_branch_leaves_intermediate_state() {
        let (t, id) = engine("e63-seq");
        let ex = Recorder::new(t, id);
        let script = script_of(
            vec![
                stmt("CREATE TABLE e6_seq(a INTEGER)", 50, "e6_seq"),
                stmt("CREATE TABLE e6_seq(b INTEGER)", 50, "e6_seq"),
                stmt("CREATE TABLE e6_seq2(c INTEGER)", 50, "e6_seq2"),
            ],
            false,
            false,
        );
        let out = run(&ex, &script, false);

        assert_eq!(
            ex.seq(),
            "exec:CREATE TABLE e6_seq(a INTEGER) | exec:CREATE TABLE e6_seq(b INTEGER)",
            "逐条分支不得开事务"
        );
        assert_eq!(
            (out.ran, out.failed.len(), out.rolled_back, out.skipped),
            (1, 1, false, 1)
        );
        assert_eq!(
            table_count(&ex.t, id, "e6_seq"),
            1,
            "无事务分支下已落地语句必须留在库里"
        );
    }

    /// 分派一的另一种形状：`pragmas_outside_tx=true` ⇒ 首尾 PRAGMA 在事务外，主体共享一个事务。
    #[test]
    fn keeps_head_and_tail_pragmas_outside_the_tx() {
        let (t, id) = engine("e63-pragma");
        let ex = Recorder::new(t, id);
        let script = script_of(
            vec![
                stmt("PRAGMA foreign_keys=OFF", 0, ""),
                stmt("CREATE TABLE e6_w(a INTEGER)", 50, "e6_w"),
                stmt("CREATE INDEX ix_e6_w ON e6_w(a)", 80, "e6_w"),
                stmt("PRAGMA foreign_keys=ON", 255, ""),
            ],
            true,
            true,
        );
        let out = run(&ex, &script, false);

        assert_eq!(
            ex.seq(),
            "exec:PRAGMA foreign_keys=OFF | begin | tx:CREATE TABLE e6_w(a INTEGER) | \
             tx:CREATE INDEX ix_e6_w ON e6_w(a) | commit | exec:PRAGMA foreign_keys=ON"
        );
        assert_eq!(ex.begins(), 1, "主体两条必须共享一个事务");
        assert_eq!((out.ran, out.failed.len(), out.rolled_back), (4, 0, false));
        assert_eq!(
            table_count(&ex.t, id, "e6_w"),
            1,
            "成功分支必须真把表建出来"
        );
    }

    /// 回滚方向复用同一分派：跑 `rollback` 那一份语句，能力位与正向共享，首尾 PRAGMA 照样在事务外。
    #[test]
    fn rollback_direction_reuses_the_same_dispatch() {
        let (t, id) = engine("e63-rb");
        polydb_rt::block_on(t.execute(id, "CREATE TABLE e6_rb(a INTEGER)", &[])).unwrap();
        let ex = Recorder::new(t, id);
        let mut script = script_of(
            vec![
                stmt("PRAGMA foreign_keys=OFF", 0, ""),
                stmt("DROP TABLE e6_missing", 40, "e6_missing"), // 正向那份故意给一条必炸的
                stmt("PRAGMA foreign_keys=ON", 255, ""),
            ],
            true,
            true,
        );
        script.rollback = vec![
            stmt("PRAGMA foreign_keys=OFF", 0, ""),
            stmt("DROP TABLE e6_rb", 40, "e6_rb"),
            stmt("PRAGMA foreign_keys=ON", 255, ""),
        ];
        let out = run(&ex, &script, true);

        assert_eq!(
            ex.seq(),
            "exec:PRAGMA foreign_keys=OFF | begin | tx:DROP TABLE e6_rb | \
             commit | exec:PRAGMA foreign_keys=ON",
            "回滚方向必须跑 rollback 那份且保持同一分派"
        );
        assert_eq!((out.ran, out.failed.len(), out.rollback), (3, 0, true));
        assert_eq!(table_count(&ex.t, id, "e6_rb"), 0, "回滚分支必须真把表删掉");
    }

    /// 键位表：三端同一套「哪个键在哪一步干什么」，这里把它逐条钉住。
    #[test]
    fn key_action_table_matches_the_other_two_frontends() {
        use CmpAction::*;
        // 四步共用的移动与选项键
        for step in [
            CmpStep::Source,
            CmpStep::Target,
            CmpStep::Review,
            CmpStep::Apply,
        ] {
            assert_eq!(key_action(step, false, "up"), Move(-1), "{step:?}");
            assert_eq!(key_action(step, false, "k"), Move(-1), "{step:?}");
            assert_eq!(key_action(step, false, "down"), Move(1), "{step:?}");
            assert_eq!(key_action(step, false, "j"), Move(1), "{step:?}");
            assert_eq!(key_action(step, false, "i"), ToggleOption(true), "{step:?}");
            assert_eq!(
                key_action(step, false, "n"),
                ToggleOption(false),
                "{step:?}"
            );
            assert_eq!(key_action(step, false, "backspace"), Back, "{step:?}");
            // ① 步的 Esc 是关窗，其余步是后退
            assert_eq!(
                key_action(step, false, "escape"),
                if step == CmpStep::Source { Close } else { Back },
                "{step:?}"
            );
        }
        assert_eq!(key_action(CmpStep::Source, false, "enter"), PickSource);
        assert_eq!(key_action(CmpStep::Target, false, "enter"), RunDiff);
        assert_eq!(key_action(CmpStep::Review, false, "enter"), ToggleAtCursor);
        assert_eq!(key_action(CmpStep::Review, false, "space"), ToggleAtCursor);
        assert_eq!(
            key_action(CmpStep::Review, false, "a"),
            SelectAll { only_safe: true }
        );
        assert_eq!(
            key_action(CmpStep::Review, false, "A"),
            SelectAll { only_safe: false }
        );
        assert_eq!(key_action(CmpStep::Review, false, "f"), FillGaps);
        assert_eq!(key_action(CmpStep::Review, false, "u"), ClearAnchor);
        assert_eq!(key_action(CmpStep::Review, false, "r"), RunDiff);
        assert_eq!(key_action(CmpStep::Review, false, "g"), Generate);
        assert_eq!(key_action(CmpStep::Apply, false, "x"), Apply);
        assert_eq!(key_action(CmpStep::Apply, false, "r"), StartConfirm);
        assert_eq!(key_action(CmpStep::Apply, false, "c"), RunDiff);
        assert_eq!(key_action(CmpStep::Apply, false, "g"), Generate);

        // ④ 步在途：四个动作键一律吞掉（重复触发会并发改同一份脚本）。
        for key in ["x", "r", "c", "g"] {
            assert_eq!(key_action(CmpStep::Apply, true, key), Nothing, "{key}");
        }
        // 其余步的同类键不靠 loading，靠各自的门禁函数。
        assert_eq!(key_action(CmpStep::Review, true, "g"), Generate);
        // 没定义的键不产生动作。
        assert_eq!(key_action(CmpStep::Review, false, "z"), Nothing);
        assert_eq!(key_action(CmpStep::Apply, false, "a"), Nothing);
    }

    /// A4 锚定：Review 只见锚定表、默认勾选按服务端计划重算、解除即回全量。
    #[test]
    fn anchor_filters_reviews_and_clear_restores_full_plan() {
        let (src, tgt) = (ConnectionId::new_v4(), ConnectionId::new_v4());
        let mut s = CompareState::new(src, 0);
        s.goto_target(src, "a");
        s.tgt = Some(tgt);

        let mut t_add = entry("t/a/add_column", AddColumn, 60, "t");
        t_add.selected = true;
        let t_drop = entry("t/x/drop_column", DropColumn, 60, "t"); // 破坏性，默认不勾
        let mut u_add = entry("u/b/add_column", AddColumn, 60, "u");
        u_add.selected = true;

        // 比对前锚定：finish_diff 带锚过滤。
        s.anchor_to("main", "t");
        let status = s.finish_diff(vec![t_add, t_drop, u_add]);
        assert_eq!(s.entries.len(), 2, "只显示锚定表 t 的条目");
        assert_eq!(s.all_entries.len(), 3, "全量计划保留");
        assert_eq!(
            s.sel,
            vec!["t/a/add_column".to_string()],
            "服务端 selected 重放"
        );
        assert!(status.contains("锚定 main.t"), "状态行报锚定：{status}");

        // 解除锚定：恢复全量 + 默认勾选重算。
        s.clear_anchor();
        assert_eq!(s.entries.len(), 3);
        assert_eq!(s.sel.len(), 2, "t/a 与 u/b 两条 selected 默认勾上");

        // 锚定但该表无差异：给"解除锚定看全量"的提示，而不是误报两侧一致。
        s.anchor_to("main", "missing");
        let msg = s.finish_diff(s.all_entries.clone());
        assert_eq!(s.entries.len(), 0);
        assert!(
            msg.contains("无差异") && msg.contains("u"),
            "锚定空表提示：{msg}"
        );
        assert!(!msg.contains("两侧结构一致"), "不得误报全量一致");
    }

    /// 整条向导用按键走一遍：状态机不吃 gpui，所以三端文案与门禁能一次验完。
    #[test]
    fn wizard_walkthrough_by_keystrokes() {
        use CmpStep::*;
        let (src, tgt) = (ConnectionId::new_v4(), ConnectionId::new_v4());
        let mut s = CompareState::new(src, 0);
        assert_eq!(s.step, Source);

        // ① Enter → ②
        s.goto_target(src, "基准库");
        assert_eq!(s.step, Target);
        // ②：选项改了不重跑（还没有计划，跑了没地方画）
        assert_eq!(s.toggle_option(true), ("忽略默认值 = 开".into(), false));
        s.tgt = Some(tgt);
        // Enter → 比对回来的计划（服务端把 `selected` 算好，客户端只是重放）
        let mut a = entry("t/name/alter_column", AlterColumn, 70, "t");
        a.selected = true;
        let mut d = entry("u/ix/drop_index", DropIndex, 30, "u");
        d.selected = false;
        s.finish_diff(vec![entry("t/a/add_column", AddColumn, 60, "t"), a, d]);
        assert_eq!(s.step, Review);
        assert_eq!(s.sel, ids(&["t/name/alter_column"]));
        assert_eq!(
            s.review_status(),
            "共 3 条变更 · 已勾 1 条（其中破坏性 0） · ⚠ 重建表 t 未整组勾选（按 f 补齐）"
        );
        // 缺口未补齐 ⇒ 生成脚本要提前拦住（提交给服务端一定 400）
        assert_eq!(
            s.ddl_request().err().flatten(),
            Some("⚠ 重建表的条目必须整组勾选：按 f 补齐后再生成")
        );
        // f 补齐 → A 全选（破坏性也进来）→ a 只留非破坏
        s.fill_gaps();
        assert!(s.gaps.is_empty());
        assert_eq!(
            s.select_all(false),
            "共 3 条变更 · 已勾 3 条（其中破坏性 1）"
        );
        assert_eq!(
            s.select_all(true),
            "共 3 条变更 · 已勾 2 条（其中破坏性 0）"
        );
        let req = s.ddl_request().expect("补齐后应可生成");
        assert_eq!(
            req.selected,
            ids(&["t/a/add_column", "t/name/alter_column"])
        );
        assert_eq!(req.options.unwrap().ignore_defaults, Some(true));

        // g → ④
        let script = script_of(
            vec![
                stmt("PRAGMA foreign_keys=OFF", 0, ""),
                stmt("CREATE TABLE z(a INTEGER)", 50, "z"),
            ],
            true,
            true,
        );
        s.finish_script(script);
        assert_eq!(s.step, Apply);
        assert!(s.outcome.is_none(), "换步前把上一次执行结果清掉");

        // r 但没跑过正向 ⇒ 门禁挡住；x 跑成功后才允许
        assert_eq!(
            s.script_to_run(true).err().flatten(),
            Some("还没有成功执行过正向脚本，无需回滚")
        );
        s.begin_apply(false);
        let out = SyncOutcome {
            ran: 2,
            failed: vec![],
            skipped: 0,
            rolled_back: false,
            rollback: false,
        };
        assert_eq!(
            s.finish_apply(out),
            "同步完成：2 条语句已执行（按 c 复查差异）"
        );
        assert!(s.applied);
        assert_eq!(
            s.start_confirm("基准库"),
            "代跑回滚不可逆：请逐字输入基准侧连接名「基准库」"
        );
        assert!(s.confirming);
        s.insert_confirm("基准");
        s.insert_confirm("库");
        assert_eq!(s.confirm_buf, "基准库");
        s.backspace_confirm();
        assert_eq!((s.confirm_buf.as_str(), s.confirm_cursor), ("基准", 6));
        assert_eq!(s.cancel_confirm(), "已取消回滚确认");
        assert!(!s.confirming);

        // Esc 逐级返回；只有 ② → ① 才丢掉期望侧（① 步的 Esc 由调用侧关窗）
        s.back();
        assert_eq!(s.step, Review);
        assert_eq!(s.tgt, Some(tgt));
        s.back();
        assert_eq!(s.step, Target);
        assert_eq!(s.tgt, Some(tgt));
        s.back();
        assert_eq!((s.step, s.tgt), (Source, None));
        assert!(s.footer().contains("Enter 下一步"));
    }
}
