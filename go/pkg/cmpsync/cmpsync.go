// Package cmpsync 是结构比对与同步在**终端渲染侧**的纯函数层（Compare & Sync E5）。
//
// 定位要说清楚：差异规则与语句生成的真相源在 pkg/schemadiff（与 Rust 的
// polydb-schema-diff 对拍），本包**不重复判定**，只做三件事——
//  1. 呈现：相位/动作的中文名、按相位分组、脚本摘要；
//  2. 勾选集运算：默认勾选、切换、整组补漏（§15.5 的第二条守卫会 400，提前提示缺口）；
//  3. 执行编排：按响应里的两个能力位把脚本切成「事务外 PRAGMA + 主体」。
//
// 它与 Web 侧 web/src/lib/compareSync.ts 是同一层的双实现（呈现层两套，规则层一套）：
// 本包不持久化状态——TUI 进程一退内存态即清，这正是 §15.5 想要的行为
// （存下来的勾选集下次打开必然可能陈旧）。
package cmpsync

import (
	"fmt"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// PhaseLabel 相位 → 中文段名；数值在 E1 定死，与服务端排序一致。
func PhaseLabel(phase int) string {
	switch phase {
	case 0:
		return "事务外前置（PRAGMA）"
	case 10:
		return "删除外键"
	case 20:
		return "删除索引"
	case 30:
		return "删除列"
	case 40:
		return "删除表"
	case 50:
		return "新建表"
	case 60:
		return "新增列"
	case 70:
		return "变更列"
	case 80:
		return "新增索引"
	case 90:
		return "新增外键"
	case 255:
		return "事务外收尾（PRAGMA）"
	default:
		return fmt.Sprintf("相位 %d", phase)
	}
}

// OpLabel 是原子动作的中文名，与 Web 侧 OP_LABEL 逐字一致。
func OpLabel(op protocol.SchemaDiffOp) string {
	switch op {
	case protocol.OpAddColumn:
		return "加列"
	case protocol.OpAddForeignKey:
		return "加外键"
	case protocol.OpAddIndex:
		return "加索引"
	case protocol.OpAlterColumn:
		return "改列"
	case protocol.OpCreateTable:
		return "建表"
	case protocol.OpDropColumn:
		return "删列"
	case protocol.OpDropForeignKey:
		return "删外键"
	case protocol.OpDropIndex:
		return "删索引"
	case protocol.OpDropTable:
		return "删表"
	default:
		return string(op)
	}
}

// alwaysRebuildOps 是必然走整表重建的动作（E3 规则 2）。
// drop_column / 隐性重建在呈现层判不出来，交服务端兜（这里只是提示，不是判定）。
var alwaysRebuildOps = map[protocol.SchemaDiffOp]bool{
	protocol.OpAlterColumn:    true,
	protocol.OpAddForeignKey:  true,
	protocol.OpDropForeignKey: true,
}

// PhaseGroup 是同一相位的一组条目。
type PhaseGroup struct {
	Phase   int
	Label   string
	Entries []protocol.SchemaDiffEntry
}

// GroupByPhase 按相位分组，保持服务端给的顺序（计划已按 phase 升序）。
func GroupByPhase(entries []protocol.SchemaDiffEntry) []PhaseGroup {
	out := []PhaseGroup{}
	for _, e := range entries {
		if n := len(out); n > 0 && out[n-1].Phase == e.Phase {
			out[n-1].Entries = append(out[n-1].Entries, e)
			continue
		}
		out = append(out, PhaseGroup{Phase: e.Phase, Label: PhaseLabel(e.Phase), Entries: []protocol.SchemaDiffEntry{e}})
	}
	return out
}

// DefaultSelection 是服务端给的默认勾选集（破坏性条目默认不勾，除非 select_destructive）。
func DefaultSelection(entries []protocol.SchemaDiffEntry) []string {
	out := []string{}
	for _, e := range entries {
		if e.Selected {
			out = append(out, e.ID)
		}
	}
	return out
}

// AllIDs 是计划里的全部条目 id（全选）。
func AllIDs(entries []protocol.SchemaDiffEntry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// NonDestructiveIDs 是全部非破坏性条目 id（"只勾非破坏"）。
func NonDestructiveIDs(entries []protocol.SchemaDiffEntry) []string {
	out := []string{}
	for _, e := range entries {
		if !e.Destructive {
			out = append(out, e.ID)
		}
	}
	return out
}

// ToggleEntry 勾选/取消一条；返回新集合，不改原切片。
func ToggleEntry(sel []string, id string) []string {
	out := make([]string, 0, len(sel)+1)
	found := false
	for _, s := range sel {
		if s == id {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		out = append(out, id)
	}
	return out
}

// SelectedIDs 把勾选集还原成计划顺序的 id 数组：服务端按重算出的计划校验，
// 顺序无关，但保序去重能让两端文案一致。
func SelectedIDs(entries []protocol.SchemaDiffEntry, sel []string) []string {
	set := toSet(sel)
	out := []string{}
	for _, e := range entries {
		if set[e.ID] {
			out = append(out, e.ID)
		}
	}
	return out
}

// EntriesOfTable 取出某表的全部条目（表名按大小写不敏感比较，与服务端的分组口径一致）。
func EntriesOfTable(entries []protocol.SchemaDiffEntry, table string) []protocol.SchemaDiffEntry {
	out := []protocol.SchemaDiffEntry{}
	for _, e := range entries {
		if strings.EqualFold(e.Table, table) {
			out = append(out, e)
		}
	}
	return out
}

// RebuildLikelyTables 是本次计划里"大概率进重建路径"的表，用于提示整组勾选。
func RebuildLikelyTables(entries []protocol.SchemaDiffEntry) []string {
	out := []string{}
	for _, e := range entries {
		if !alwaysRebuildOps[e.Op] {
			continue
		}
		seen := false
		for _, t := range out {
			if strings.EqualFold(t, e.Table) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, e.Table)
		}
	}
	return out
}

// GroupGap 是"勾了重建动作却漏勾同表其他条目"的缺口。
type GroupGap struct {
	Table   string
	Missing []string
}

// GroupGaps 提前算出缺口：这种勾选集提交给服务端一定 400（§15.5 第二条守卫），
// 呈现层有责任在生成脚本前把它显示出来。
func GroupGaps(entries []protocol.SchemaDiffEntry, sel []string) []GroupGap {
	chosen := toSet(sel)
	gaps := []GroupGap{}
	for _, table := range RebuildLikelyTables(entries) {
		group := EntriesOfTable(entries, table)
		hit := false
		for _, e := range group {
			if chosen[e.ID] && alwaysRebuildOps[e.Op] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		var missing []string
		for _, e := range group {
			if !chosen[e.ID] {
				missing = append(missing, e.ID)
			}
		}
		if len(missing) > 0 {
			gaps = append(gaps, GroupGap{Table: table, Missing: missing})
		}
	}
	return gaps
}

// FillGroupGaps 把缺口一次性补齐（"整表一起勾"），结果仍按计划顺序。
func FillGroupGaps(entries []protocol.SchemaDiffEntry, sel []string) []string {
	seen := toSet(sel)
	for _, gap := range GroupGaps(entries, sel) {
		for _, id := range gap.Missing {
			seen[id] = true
		}
	}
	out := []string{}
	for _, e := range entries {
		if seen[e.ID] {
			out = append(out, e.ID)
		}
	}
	return out
}

// ScriptSummary 是一份脚本的摘要，用于执行前的风险提示。
type ScriptSummary struct {
	Total         int
	NonReversible int
	Warned        int
	Tables        []string
}

// SummarizeScript 统计语句数、不可逆条数、带告警条数与涉及的表。
func SummarizeScript(stmts []protocol.DdlStatement) ScriptSummary {
	out := ScriptSummary{Total: len(stmts), Tables: []string{}}
	for _, s := range stmts {
		if s.NonReversible {
			out.NonReversible++
		}
		if len(s.Warnings) > 0 {
			out.Warned++
		}
		if s.Table == "" {
			continue
		}
		seen := false
		for _, t := range out.Tables {
			if strings.EqualFold(t, s.Table) {
				seen = true
				break
			}
		}
		if !seen {
			out.Tables = append(out.Tables, s.Table)
		}
	}
	return out
}

// ExecutionPlan 把脚本切成三段（behavior.md §15.4）。
type ExecutionPlan struct {
	// PragmaHead 必须在事务外先跑（事务内 PRAGMA foreign_keys 是 no-op）；可能为 nil。
	PragmaHead *protocol.DdlStatement
	// PragmaTail 必须在事务外收尾；可能为 nil。
	PragmaTail *protocol.DdlStatement
	// Body 是主体语句。
	Body []protocol.DdlStatement
	// WrapInTx 为 false 时该方言的 DDL 会隐式提交，主体只能逐条跑（失败留中间态）。
	WrapInTx bool
}

// PlanExecution 按响应里的两个能力位分派执行方式：
// pragmas_outside_tx ⇒ 首尾两条 PRAGMA（相位 0 / 255 且 entry_ids 为空）留在事务外；
// ddl_in_tx ⇒ 主体能否整体包进一个事务。
func PlanExecution(script *protocol.DdlScript) ExecutionPlan {
	plan := ExecutionPlan{Body: []protocol.DdlStatement{}, WrapInTx: script.DdlInTx}
	headIdx, tailIdx := -1, -1
	for i, s := range script.Statements {
		if len(s.EntryIDs) != 0 {
			continue
		}
		if s.Phase == 0 && headIdx < 0 {
			headIdx = i
		}
		if s.Phase == 255 && tailIdx < 0 {
			tailIdx = i
		}
	}
	for i, s := range script.Statements {
		switch i {
		case headIdx:
			plan.PragmaHead = &script.Statements[i]
		case tailIdx:
			plan.PragmaTail = &script.Statements[i]
		default:
			plan.Body = append(plan.Body, s)
		}
	}
	return plan
}

// FailedStatement 是一条执行失败的语句及其上下文。
type FailedStatement struct {
	// Index 是它在整份脚本（含 PRAGMA）里的下标。
	Index    int
	Phase    int
	Table    string
	SQL      string
	Reason   string
	EntryIDs []string
}

// ApplyOutcome 是一次应用的结果。
type ApplyOutcome struct {
	Ran int
	// Failed 只记录**首个**失败及此后被跳过的语句；主体走事务时失败即整体回滚。
	Failed     []FailedStatement
	RolledBack bool
	Skipped    int
}

// CandidateConnections 是期望侧候选：Redis 无模式概念（服务端拒），
// 跨种类服务端同样拒绝 ⇒ 只允许与基准侧同 kind。
func CandidateConnections(all []protocol.ConnectionInfo, source *protocol.ConnectionInfo) []protocol.ConnectionInfo {
	out := []protocol.ConnectionInfo{}
	if source == nil {
		return out
	}
	for _, c := range all {
		if c.Kind == source.Kind && IsComparableKind(c.Kind) {
			out = append(out, c)
		}
	}
	return out
}

// IsComparableKind 报告该种类能否参与结构比对。
func IsComparableKind(kind protocol.DatabaseKind) bool {
	return kind != protocol.DatabaseKindRedis
}

// ConfirmMatches 是代跑回滚的强确认：必须**逐字**输入基准侧连接名
// （与 Web 侧 confirmText.trim() === source.name 同口径，只剥首尾空白）。
func ConfirmMatches(typed, baseName string) bool {
	return strings.TrimSpace(typed) == baseName
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
