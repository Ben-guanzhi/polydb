package cmpsync

import (
	"reflect"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 用例与 web/src/lib/compareSync.test.ts 同批（呈现层双实现对齐）。
// Web 的状态持久化用例在这里没有对应物——TUI 不落库，见包注释。

func idsOf(cs []protocol.ConnectionInfo) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestGroupByPhaseKeepsServerOrder(t *testing.T) {
	groups := GroupByPhase([]protocol.SchemaDiffEntry{
		{ID: "a//drop_table", Op: protocol.OpDropTable, Phase: 40, Table: "a", Selected: true},
		{ID: "b//create_table", Op: protocol.OpCreateTable, Phase: 50, Table: "b", Selected: true},
		{ID: "t/note/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "t", Selected: true},
		{ID: "t/qty/alter_column", Op: protocol.OpAlterColumn, Phase: 70, Table: "t", Selected: true},
	})
	var phases []int
	for _, g := range groups {
		phases = append(phases, g.Phase)
	}
	if want := []int{40, 50, 60, 70}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("相位序 = %v, want %v", phases, want)
	}
	if groups[0].Label != "删除表" || groups[3].Label != "变更列" {
		t.Fatalf("段名 = %q / %q", groups[0].Label, groups[3].Label)
	}
	if len(groups[0].Entries) != 1 || groups[0].Entries[0].ID != "a//drop_table" {
		t.Fatalf("段内条目 = %v", groups[0].Entries)
	}
}

func TestGroupByPhaseEmptyIsSlice(t *testing.T) {
	if got := GroupByPhase(nil); got == nil || len(got) != 0 {
		t.Fatalf("空计划必须返回空切片而不是 nil，got %#v", got)
	}
}

func TestGroupByPhaseMergesSamePhase(t *testing.T) {
	groups := GroupByPhase([]protocol.SchemaDiffEntry{
		{ID: "t/x/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "t"},
		{ID: "u/y/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "u"},
	})
	if len(groups) != 1 || len(groups[0].Entries) != 2 {
		t.Fatalf("同相位应合成一段：%#v", groups)
	}
}

func TestOpLabelMatchesWeb(t *testing.T) {
	cases := []struct {
		op   protocol.SchemaDiffOp
		want string
	}{
		{protocol.OpAddColumn, "加列"},
		{protocol.OpAddForeignKey, "加外键"},
		{protocol.OpAddIndex, "加索引"},
		{protocol.OpAlterColumn, "改列"},
		{protocol.OpCreateTable, "建表"},
		{protocol.OpDropColumn, "删列"},
		{protocol.OpDropForeignKey, "删外键"},
		{protocol.OpDropIndex, "删索引"},
		{protocol.OpDropTable, "删表"},
	}
	for _, c := range cases {
		if got := OpLabel(c.op); got != c.want {
			t.Fatalf("OpLabel(%s) = %q, want %q", c.op, got, c.want)
		}
	}
	if got := OpLabel(protocol.SchemaDiffOp("weird")); got != "weird" {
		t.Fatalf("未知动作应原样回显，got %q", got)
	}
	if got := PhaseLabel(120); got != "相位 120" {
		t.Fatalf("未知相位 = %q", got)
	}
}

func TestSelectionHelpers(t *testing.T) {
	entries := []protocol.SchemaDiffEntry{
		{ID: "x//drop_table", Op: protocol.OpDropTable, Phase: 40, Table: "x", Destructive: true},
		{ID: "t/note/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "t", Selected: true},
	}
	if got := DefaultSelection(entries); !reflect.DeepEqual(got, []string{"t/note/add_column"}) {
		t.Fatalf("默认勾选 = %v", got)
	}
	if got := NonDestructiveIDs(entries); !reflect.DeepEqual(got, []string{"t/note/add_column"}) {
		t.Fatalf("只勾非破坏 = %v", got)
	}
	if got := AllIDs(entries); !reflect.DeepEqual(got, []string{"x//drop_table", "t/note/add_column"}) {
		t.Fatalf("全选 = %v", got)
	}
}

func TestSelectedIDsUsesPlanOrder(t *testing.T) {
	entries := []protocol.SchemaDiffEntry{
		{ID: "e1", Op: protocol.OpDropTable, Phase: 40},
		{ID: "e2", Op: protocol.OpAddColumn, Phase: 60},
		{ID: "e3", Op: protocol.OpAddIndex, Phase: 80},
	}
	if got := SelectedIDs(entries, []string{"e3", "e1"}); !reflect.DeepEqual(got, []string{"e1", "e3"}) {
		t.Fatalf("回传顺序 = %v, want [e1 e3]", got)
	}
	// 计划里没有的 id（陈旧勾选）不回落进请求：陈旧判定由服务端报错兜住。
	if got := SelectedIDs(entries, []string{"e2", "ghost"}); !reflect.DeepEqual(got, []string{"e2"}) {
		t.Fatalf("含幽灵 id = %v, want [e2]", got)
	}
	if got := SelectedIDs(entries, nil); len(got) != 0 {
		t.Fatalf("空勾选 = %v", got)
	}
}

func TestToggleEntryRoundTrip(t *testing.T) {
	if got := ToggleEntry([]string{"a"}, "a"); len(got) != 0 {
		t.Fatalf("取消后 = %v, want 空", got)
	}
	if got := ToggleEntry([]string{"a"}, "b"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("新勾 = %v", got)
	}
	// 入参切片不能被改坏（TUI 里勾选集是模型字段，共享底层数组会串味）。
	base := []string{"a", "b"}
	ToggleEntry(base, "a")
	ToggleEntry(base, "c")
	if !reflect.DeepEqual(base, []string{"a", "b"}) {
		t.Fatalf("ToggleEntry 改到了入参：%v", base)
	}
}

func TestRebuildGroupGaps(t *testing.T) {
	// 按服务端真实顺序（相位 60 的 t、u 两段在前，70 的改列在后）。
	entries := []protocol.SchemaDiffEntry{
		{ID: "t/note/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "t", Selected: true},
		{ID: "u/x/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "u", Selected: true},
		{ID: "t/qty/alter_column", Op: protocol.OpAlterColumn, Phase: 70, Table: "t", Selected: true},
	}
	if got := RebuildLikelyTables(entries); !reflect.DeepEqual(got, []string{"t"}) {
		t.Fatalf("重建候选表 = %v, want [t]", got)
	}
	gaps := GroupGaps(entries, []string{"t/qty/alter_column"})
	if want := []GroupGap{{Table: "t", Missing: []string{"t/note/add_column"}}}; !reflect.DeepEqual(gaps, want) {
		t.Fatalf("缺口 = %#v, want %#v", gaps, want)
	}
	if got := GroupGaps(entries, []string{"t/qty/alter_column", "t/note/add_column"}); len(got) != 0 {
		t.Fatalf("整组勾满不该有缺口：%v", got)
	}
	// 只勾非重建动作（加列）不构成缺口——它自己不需要整表齐。
	if got := GroupGaps(entries, []string{"u/x/add_column"}); len(got) != 0 {
		t.Fatalf("非重建动作不该报缺口：%v", got)
	}
	got := FillGroupGaps(entries, []string{"t/qty/alter_column"})
	if want := []string{"t/note/add_column", "t/qty/alter_column"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("补全后 = %v, want %v（须按计划顺序，60 在 70 前）", got, want)
	}
}

func TestRebuildGroupGapsCaseInsensitiveTable(t *testing.T) {
	entries := []protocol.SchemaDiffEntry{
		{ID: "orders/note/add_column", Op: protocol.OpAddColumn, Phase: 60, Table: "Orders"},
		{ID: "orders/qty/alter_column", Op: protocol.OpAlterColumn, Phase: 70, Table: "orders"},
	}
	if got := GroupGaps(entries, []string{"orders/qty/alter_column"}); len(got) != 1 ||
		!reflect.DeepEqual(got[0].Missing, []string{"orders/note/add_column"}) {
		t.Fatalf("同表大小写混写应视为一组：%#v", got)
	}
}

func TestPlanExecutionPullsPragmaWrapOutOfTx(t *testing.T) {
	script := &protocol.DdlScript{
		PragmasOutsideTx: true, DdlInTx: true,
		Statements: []protocol.DdlStatement{
			{SQL: "PRAGMA foreign_keys=OFF;", Phase: 0, EntryIDs: []string{}},
			{SQL: `CREATE TABLE "t"(id INTEGER);`, Phase: 50, Table: "t", EntryIDs: []string{"t//create_table"}},
			{SQL: "PRAGMA foreign_keys=ON;", Phase: 255, EntryIDs: []string{}},
		},
	}
	p := PlanExecution(script)
	if p.PragmaHead == nil || p.PragmaHead.SQL != "PRAGMA foreign_keys=OFF;" {
		t.Fatalf("head = %#v", p.PragmaHead)
	}
	if p.PragmaTail == nil || p.PragmaTail.SQL != "PRAGMA foreign_keys=ON;" {
		t.Fatalf("tail = %#v", p.PragmaTail)
	}
	if len(p.Body) != 1 || p.Body[0].SQL != `CREATE TABLE "t"(id INTEGER);` {
		t.Fatalf("body = %v", p.Body)
	}
	if !p.WrapInTx {
		t.Fatal("ddl_in_tx=true 应允许整段包事务")
	}
}

func TestPlanExecutionKeepsAddColumnWithEntryIDsInside(t *testing.T) {
	// 相位 0 但带条目（假想）不能被当成 PRAGMA 包裹条摘出去。
	p := PlanExecution(&protocol.DdlScript{DdlInTx: true, Statements: []protocol.DdlStatement{
		{SQL: "X;", Phase: 0, EntryIDs: []string{"t/x/add_column"}},
	}})
	if p.PragmaHead != nil {
		t.Fatalf("带 entry_ids 的语句不该被摘成 PRAGMA 包裹条：%#v", p.PragmaHead)
	}
	if len(p.Body) != 1 {
		t.Fatalf("body = %v", p.Body)
	}
}

func TestPlanExecutionWithoutRebuild(t *testing.T) {
	p := PlanExecution(&protocol.DdlScript{DdlInTx: true, Statements: []protocol.DdlStatement{
		{SQL: `ALTER TABLE "t" ADD COLUMN "n" text;`, Phase: 60, Table: "t", EntryIDs: []string{"t/n/add_column"}},
	}})
	if p.PragmaHead != nil || p.PragmaTail != nil {
		t.Fatalf("不含重建时不该有事务外语句：head=%#v tail=%#v", p.PragmaHead, p.PragmaTail)
	}
	if len(p.Body) != 1 {
		t.Fatalf("body = %v", p.Body)
	}
	if !p.WrapInTx {
		t.Fatal("WrapInTx 应跟随 ddl_in_tx")
	}
}

func TestPlanExecutionDdlInTxFalse(t *testing.T) {
	p := PlanExecution(&protocol.DdlScript{Statements: []protocol.DdlStatement{
		{SQL: "X", Phase: 60, Table: "t", EntryIDs: []string{"t/x/add_column"}},
	}})
	if p.WrapInTx {
		t.Fatal("ddl_in_tx=false（缺省）时主体不得包进事务")
	}
	if len(p.Body) != 1 {
		t.Fatalf("body = %v", p.Body)
	}
}

func TestSummarizeScript(t *testing.T) {
	got := SummarizeScript([]protocol.DdlStatement{
		{SQL: `DROP TABLE "t";`, Table: "t", Phase: 40, NonReversible: true},
		{SQL: `DROP TABLE "T2";`, Table: "T2", Phase: 40, Warnings: []string{"重建表会丢失该表上的触发器（快照不含触发器）"}},
		{SQL: "PRAGMA foreign_keys=ON;", Phase: 255, EntryIDs: []string{}},
	})
	want := ScriptSummary{Total: 3, NonReversible: 1, Warned: 1, Tables: []string{"t", "T2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("摘要 = %#v, want %#v", got, want)
	}
	empty := SummarizeScript(nil)
	if want := (ScriptSummary{Tables: []string{}}); !reflect.DeepEqual(empty, want) {
		t.Fatalf("空脚本摘要 = %#v, want %#v", empty, want)
	}
}

func TestCandidateConnections(t *testing.T) {
	conn := func(id string, kind protocol.DatabaseKind) protocol.ConnectionInfo {
		return protocol.ConnectionInfo{ID: id, Name: id, Kind: kind}
	}
	all := []protocol.ConnectionInfo{
		conn("a", protocol.DatabaseKindSQLite),
		conn("b", protocol.DatabaseKindSQLite),
		conn("r", protocol.DatabaseKindRedis),
		conn("p", protocol.DatabaseKindPostgres),
	}
	src := conn("a", protocol.DatabaseKindSQLite)
	if got := idsOf(CandidateConnections(all, &src)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("候选 = %v, want [a b]", got)
	}
	redis := conn("r", protocol.DatabaseKindRedis)
	if out := CandidateConnections(all, &redis); len(out) != 0 {
		t.Fatalf("Redis 基准侧不该有候选：%v", out)
	}
	if out := CandidateConnections(all, nil); out == nil || len(out) != 0 {
		t.Fatalf("无基准侧应返回空切片，got %#v", out)
	}
	if IsComparableKind(protocol.DatabaseKindRedis) {
		t.Fatal("Redis 不可比")
	}
	if !IsComparableKind(protocol.DatabaseKindPostgres) {
		t.Fatal("PG 可比（语句生成才受方言限制）")
	}
}

func TestConfirmMatches(t *testing.T) {
	if !ConfirmMatches(" e4_base ", "e4_base") {
		t.Fatal("首尾空白应被剥掉")
	}
	if ConfirmMatches("E4_BASE", "e4_base") {
		t.Fatal("连接名必须逐字一致，大小写不同不算确认")
	}
	if ConfirmMatches("", "e4_base") {
		t.Fatal("空输入不得通过确认")
	}
	if !ConfirmMatches("e4_base", "e4_base") {
		t.Fatal("逐字输入应通过")
	}
}
