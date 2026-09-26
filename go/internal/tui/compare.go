package tui

// 结构比对与同步向导（Compare & Sync E5.2）：viewCompare 独立视图内自走三段——
//   ① 选期望侧连接 → ② 按相位分组勾选差异条目 → ③ 预览脚本 / 执行 / 代跑回滚。
//
// 呈现之外的纯逻辑全在 pkg/cmpsync（与 Web 的 lib/compareSync.ts 同层双实现），
// 规则判定（差异、语句生成、勾选校验）一律留在服务端：本文件只发请求、画结果。
// AGPL 红线：只借鉴 TablePro 的交互语义，未移植任何外部代码。

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/cmpsync"
	"github.com/polydb/polydb/pkg/protocol"
)

type cmpStage int

const (
	cmpStagePick   cmpStage = iota // ① 期望侧连接
	cmpStageDiff                   // ② 差异计划勾选
	cmpStageScript                 // ③ 脚本预览与执行
)

// 执行分派标识：与 Web 侧 outcomeRun 同语义（一份脚本两份用途）。
const (
	cmpRunForward  = "forward"
	cmpRunRollback = "rollback"
)

func (s cmpStage) label() string {
	switch s {
	case cmpStagePick:
		return "① 选期望侧"
	case cmpStageDiff:
		return "② 勾选变更"
	case cmpStageScript:
		return "③ 预览与执行"
	}
	return ""
}

// cmpStageLabel 供标题栏显示当前段。
func (m *model) cmpStageLabel() string { return m.cmpStage.label() }

// connByID 在已加载的连接列表里按 id 取连接信息。
func (m *model) connByID(id string) *protocol.ConnectionInfo {
	for i := range m.conns {
		if m.conns[i].ID == id {
			return &m.conns[i]
		}
	}
	return nil
}

// openCompare 从库表视图进入向导：基准侧即当前已连接的那一侧，
// 只改向导自己的副本，不动 m.selectedID（退出后仍在原来的连接上）。
func (m *model) openCompare() (tea.Model, tea.Cmd) {
	base := m.connByID(m.selectedID)
	if base == nil {
		m.status = "基准侧连接信息缺失，请先从连接列表打开"
		return m, nil
	}
	if !cmpsync.IsComparableKind(base.Kind) {
		m.status = "Redis 无模式概念，不参与结构比对"
		return m, nil
	}
	if base.ReadOnly != nil && *base.ReadOnly {
		m.status = "基准侧是只读连接，无法执行同步"
		return m, nil
	}
	m.cmpPrev = m.view
	m.view = viewCompare
	m.cmpStage = cmpStagePick
	m.cmpBase = *base
	m.cmpCandidates = cmpsync.CandidateConnections(m.conns, base)
	m.cmpPickCur = 0
	m.cmpDestructOpt = false
	m.cmpIgnoreOpt = false
	m.cmpEntries = nil
	m.cmpSel = nil
	m.cmpCur = 0
	m.cmpGaps = nil
	m.cmpScript = nil
	m.cmpScroll = 0
	m.cmpRun = ""
	m.cmpOutcome = nil
	m.cmpApplied = false
	m.cmpConfirmOn = false
	m.cmpConfirm.SetValue("")
	m.cmpConfirm.Blur()
	m.status = "基准侧：" + base.Name + " · 选择期望侧连接"
	return m, nil
}

// cmpDiffRequest 组比对请求：schema 留空交服务端按种类取默认（§14.5，SQLite 恒 main）。
// 向导内不做 schema 选择——两端都取默认库/模式，这与 Web 向导的「单 schema 不显示下拉」一致。
func (m *model) cmpDiffRequest(target string) *protocol.SchemaDiffRequest {
	return &protocol.SchemaDiffRequest{
		Source: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(m.cmpBase.ID)},
		Target: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(target)},
		Options: &protocol.SchemaDiffOptions{
			IgnoreDefaults:    m.cmpIgnoreOpt,
			SelectDestructive: m.cmpDestructOpt,
		},
	}
}

func (m *model) cmpTargetID() string {
	if m.cmpPickCur >= 0 && m.cmpPickCur < len(m.cmpCandidates) {
		return m.cmpCandidates[m.cmpPickCur].ID
	}
	return ""
}

func (m *model) runCmpDiff(recheck bool) (tea.Model, tea.Cmd) {
	target := m.cmpTargetID()
	if target == "" {
		m.status = "先选一个期望侧连接"
		return m, nil
	}
	m.busy = "比对中 …"
	return m, schemaDiffCmd(m.app, m.cmpDiffRequest(target), recheck)
}

// generateScript 把勾选集按计划顺序回传服务端（陈旧 id 由服务端拒）。
func (m *model) generateScript() (tea.Model, tea.Cmd) {
	picked := cmpsync.SelectedIDs(m.cmpEntries, m.cmpSel)
	if len(picked) == 0 {
		m.status = "至少要勾选一条变更"
		return m, nil
	}
	if len(m.cmpGaps) > 0 {
		m.status = "重建组未整组勾选，服务端会拒绝：按 f 补齐后重试"
		return m, nil
	}
	m.busy = "生成脚本 …"
	return m, schemaDdlCmd(m.app, &protocol.SchemaDdlRequest{
		Source: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(m.cmpBase.ID)},
		Target: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(m.cmpTargetID())},
		Options: &protocol.SchemaDiffOptions{
			IgnoreDefaults:    m.cmpIgnoreOpt,
			SelectDestructive: m.cmpDestructOpt,
		},
		Selected: picked,
	})
}

// cmpAvail 差异列表可渲染行数：预留基准行 1 + 统计 1 + 缺口 1 + 组头若干，粗略留 4 行。
func (m *model) cmpAvail() int {
	return max(3, m.height-10)
}

// cmpIsSelected 报告条目是否在勾选集里。
func (m *model) cmpIsSelected(id string) bool {
	for _, s := range m.cmpSel {
		if s == id {
			return true
		}
	}
	return false
}

// cmpRecalcGaps 每次勾选变化后重算重建组缺口。
func (m *model) cmpRecalcGaps() {
	m.cmpGaps = cmpsync.GroupGaps(m.cmpEntries, m.cmpSel)
}

// ─── 按键 ───────────────────────────────────────────────────

func (m *model) compareKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 回滚强确认输入态优先：只吃 Enter / Esc / Ctrl+C，其余键进输入框。
	if m.cmpConfirmOn {
		return m.cmpConfirmKey(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		return m.cmpBack()
	}
	switch m.cmpStage {
	case cmpStagePick:
		return m.cmpPickKey(msg)
	case cmpStageDiff:
		return m.cmpDiffKey(msg)
	case cmpStageScript:
		return m.cmpScriptKey(msg)
	}
	return m, nil
}

// cmpBack 段内后退：③→②→①→退出向导（与 Web「后退恒放行」同口径）。
func (m *model) cmpBack() (tea.Model, tea.Cmd) {
	switch m.cmpStage {
	case cmpStageScript:
		m.cmpStage = cmpStageDiff
		m.cmpScript = nil
		m.cmpOutcome = nil
		m.cmpRun = ""
		m.cmpScroll = 0
		m.status = "回到勾选：改完再按 g 重新生成"
		return m, nil
	case cmpStageDiff:
		m.cmpStage = cmpStagePick
		m.cmpEntries = nil
		m.cmpSel = nil
		m.cmpGaps = nil
		m.status = "重新选择期望侧连接"
		return m, nil
	}
	m.view = m.cmpPrev
	m.status = ""
	return m, nil
}

func (m *model) cmpPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.cmpPickCur > 0 {
			m.cmpPickCur--
		}
		return m, nil
	case tea.KeyDown:
		if m.cmpPickCur < len(m.cmpCandidates)-1 {
			m.cmpPickCur++
		}
		return m, nil
	case tea.KeyEnter:
		return m.runCmpDiff(false)
	}
	if msg.Type == tea.KeyRunes {
		switch string(msg.Runes) {
		case "k":
			if m.cmpPickCur > 0 {
				m.cmpPickCur--
			}
		case "j":
			if m.cmpPickCur < len(m.cmpCandidates)-1 {
				m.cmpPickCur++
			}
		}
	}
	return m, nil
}

// cmpToggleOption 切换两个**服务端**口径选项（i 忽略默认值 / n 破坏性默认勾选）。
// 两者都改变比对结果，所以必须重跑比对：勾选集由服务端按选项重算，客户端不缓存。
func (m *model) cmpToggleOption(key string) (tea.Model, tea.Cmd) {
	name := "破坏性默认勾选"
	on := false
	if key == "i" {
		name = "忽略默认值"
		m.cmpIgnoreOpt = !m.cmpIgnoreOpt
		on = m.cmpIgnoreOpt
	} else {
		m.cmpDestructOpt = !m.cmpDestructOpt
		on = m.cmpDestructOpt
	}
	m.status = name + " = " + onOff(on)
	return m.runCmpDiff(false)
}

func (m *model) cmpDiffKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 比对为空时也要能改选项：否则「两侧结构一致」会把 i/n 一起锁死，
	// 用户没法关掉刚误开的忽略默认值再重跑。
	if msg.Type == tea.KeyRunes {
		if k := string(msg.Runes); k == "i" || k == "n" {
			return m.cmpToggleOption(k)
		}
	}
	if len(m.cmpEntries) == 0 {
		return m, nil
	}
	clamp := func() { m.cmpCur = min(max(m.cmpCur, 0), len(m.cmpEntries)-1) }
	switch msg.Type {
	case tea.KeyUp:
		m.cmpCur--
		clamp()
		return m, nil
	case tea.KeyDown:
		m.cmpCur++
		clamp()
		return m, nil
	case tea.KeyEnter, tea.KeySpace:
		e := m.cmpEntries[m.cmpCur]
		m.cmpSel = cmpsync.ToggleEntry(m.cmpSel, e.ID)
		m.cmpRecalcGaps()
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		switch string(msg.Runes) {
		case "k":
			m.cmpCur--
			clamp()
		case "j":
			m.cmpCur++
			clamp()
		case "a":
			m.cmpSel = cmpsync.NonDestructiveIDs(m.cmpEntries)
			m.cmpRecalcGaps()
			m.status = "已只勾非破坏性条目"
		case "A":
			m.cmpSel = cmpsync.AllIDs(m.cmpEntries)
			m.cmpRecalcGaps()
			m.status = "已全选（含破坏性条目）"
		case "f":
			if len(m.cmpGaps) == 0 {
				m.status = "没有需要补齐的重建组"
				return m, nil
			}
			m.cmpSel = cmpsync.FillGroupGaps(m.cmpEntries, m.cmpSel)
			m.cmpRecalcGaps()
			m.status = "已按表补齐重建组勾选"
		case "g":
			return m.generateScript()
		case "r":
			return m.runCmpDiff(false)
		}
	}
	return m, nil
}

func (m *model) cmpScriptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cmpScript == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyUp:
		m.cmpScrollDown(-1)
		return m, nil
	case tea.KeyDown:
		m.cmpScrollDown(1)
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		switch string(msg.Runes) {
		case "k":
			m.cmpScrollDown(-1)
		case "j":
			m.cmpScrollDown(1)
		case "x":
			if len(m.cmpScript.Statements) == 0 {
				m.status = "脚本为空，无可执行语句"
				return m, nil
			}
			m.busy = "执行中 …"
			return m, applyScriptCmd(m.app, m.cmpBase.ID, m.cmpScript, cmpRunForward)
		case "c":
			return m.runCmpDiff(true)
		case "r":
			if !m.cmpApplied {
				m.status = "只有正向脚本完整执行成功后才允许代跑回滚"
				return m, nil
			}
			if len(m.cmpScript.Rollback) == 0 {
				m.status = "回滚脚本为空（含不可逆语句，退不回去）"
				return m, nil
			}
			m.cmpConfirmOn = true
			m.cmpConfirm.SetValue("")
			m.cmpConfirm.Focus()
			m.status = "代跑回滚：逐字输入基准侧连接名「" + m.cmpBase.Name + "」"
			return m, nil
		}
	}
	return m, nil
}

func (m *model) cmpScrollDown(delta int) {
	avail := m.cmpAvail()
	total := len(m.cmpScriptLines())
	m.cmpScroll = min(max(m.cmpScroll+delta, 0), max(total-avail, 0))
}

// cmpConfirmKey 是回滚强确认输入态：手输基准侧连接名，Enter 校验后放行。
func (m *model) cmpConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.cmpConfirmOn = false
		m.cmpConfirm.Blur()
		m.status = "已取消回滚"
		return m, nil
	case tea.KeyEnter:
		typed := m.cmpConfirm.Value()
		m.cmpConfirmOn = false
		m.cmpConfirm.Blur()
		if !cmpsync.ConfirmMatches(typed, m.cmpBase.Name) {
			m.status = "输入的确认词与基准侧连接名不符，回滚未执行"
			return m, nil
		}
		m.busy = "回滚中 …"
		return m, applyScriptCmd(m.app, m.cmpBase.ID, m.cmpScript, cmpRunRollback)
	}
	if msg.Type == tea.KeyTab { // 输入框里 Tab 不切焦点，当空格用
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	}
	box, cmd := m.cmpConfirm.Update(msg)
	m.cmpConfirm = box
	return m, cmd
}

// ─── 消息落地 ───────────────────────────────────────────────

func (m *model) onSchemaDiff(msg schemaDiffMsg) (tea.Model, tea.Cmd) {
	m.busy = ""
	if msg.err != nil {
		m.status = "比对失败: " + msg.err.Error()
		return m, nil
	}
	if msg.recheck {
		if len(msg.entries) == 0 {
			m.status = "复查：基准侧已与期望侧一致"
		} else {
			m.status = fmt.Sprintf("复查：仍有 %d 条差异（未勾选的部分本就不会应用）", len(msg.entries))
		}
		return m, nil
	}
	m.cmpEntries = msg.entries
	m.cmpSel = cmpsync.DefaultSelection(msg.entries)
	m.cmpCur = 0
	m.cmpScript = nil
	m.cmpOutcome = nil
	m.cmpRun = ""
	m.cmpApplied = false
	m.cmpRecalcGaps()
	m.cmpStage = cmpStageDiff
	if len(msg.entries) == 0 {
		m.status = "两侧结构一致，无需同步"
		return m, nil
	}
	m.status = fmt.Sprintf("共 %d 条变更，默认勾选 %d 条", len(msg.entries), len(m.cmpSel))
	return m, nil
}

func (m *model) onSchemaDdl(msg schemaDdlMsg) (tea.Model, tea.Cmd) {
	m.busy = ""
	if msg.err != nil {
		m.status = "生成脚本失败: " + msg.err.Error()
		return m, nil
	}
	if msg.script == nil {
		// 协议约定空脚本也是结构体（statements: []）；nil 只可能是实现漏了。
		m.status = "生成脚本失败: 服务端返回空响应"
		return m, nil
	}
	m.cmpScript = msg.script
	m.cmpStage = cmpStageScript
	m.cmpScroll = 0
	m.cmpOutcome = nil
	m.cmpRun = ""
	s := cmpsync.SummarizeScript(msg.script.Statements)
	m.status = fmt.Sprintf("脚本 %d 条 · 不可逆 %d · 涉及 %d 张表：x 执行 · r 回滚", s.Total, s.NonReversible, len(s.Tables))
	return m, nil
}

func (m *model) onSyncApply(msg syncApplyMsg) (tea.Model, tea.Cmd) {
	m.busy = ""
	out := msg.outcome
	m.cmpOutcome = &out
	m.cmpRun = msg.run
	ok := len(out.Failed) == 0
	switch msg.run {
	case cmpRunRollback:
		if ok {
			m.cmpApplied = false
			m.status = fmt.Sprintf("回滚脚本已执行 %d 条；结构退回去了，正向里丢的数据找不回来（§15.6）", out.Ran)
		} else {
			m.status = "回滚中断：第 " + fmt.Sprint(out.Failed[0].Index+1) + " 条失败"
		}
	default:
		m.cmpApplied = ok
		if ok {
			m.status = fmt.Sprintf("同步完成：%d 条语句已执行（按 c 复查差异）", out.Ran)
		} else {
			m.status = "执行中断：第 " + fmt.Sprint(out.Failed[0].Index+1) + " 条失败"
		}
	}
	return m, nil
}

// ─── 渲染 ───────────────────────────────────────────────────

func (m *model) cmpFooter() string {
	if m.cmpConfirmOn {
		return "逐字输入基准侧连接名 · Enter 确认执行回滚 · Esc 取消"
	}
	switch m.cmpStage {
	case cmpStagePick:
		return "↑/↓ · k/j 选择 · Enter 比对 · Esc 退出向导"
	case cmpStageDiff:
		return "↑/↓ · k/j 条目 · Space 勾选 · a 只勾非破坏 · A 全选 · i 忽略默认值 · n 破坏性默认勾选 · f 补齐重建组 · g 生成脚本 · r 重比对 · Esc 返回"
	}
	return "↑/↓ · k/j 滚动 · x 执行正向 · r 代跑回滚 · c 复查差异 · Esc 返回勾选"
}

func (m *model) compareView() string {
	switch m.cmpStage {
	case cmpStagePick:
		return m.cmpPickView()
	case cmpStageDiff:
		return m.cmpDiffListView()
	}
	return m.cmpScriptView()
}

func (m *model) cmpPickView() string {
	var b strings.Builder
	b.WriteString("基准侧（要被改成期望侧的样子，也是执行侧）：" + m.cmpBase.Name + " · " + string(m.cmpBase.Kind) + "\n")
	if len(m.cmpCandidates) == 0 {
		b.WriteString("没有同种类的候选连接（Redis 与跨种类服务端一律拒绝）。\n")
		b.WriteString("按 n 先新建一个同类连接再进来比对。\n")
		return b.String()
	}
	b.WriteString("选择期望侧连接（共 " + fmt.Sprint(len(m.cmpCandidates)) + " 个候选）：\n")
	start, end := listWindow(len(m.cmpCandidates), m.cmpPickCur, m.cmpAvail())
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 项\n", start)
	}
	for i := start; i < end; i++ {
		c := m.cmpCandidates[i]
		line := "  " + c.Name + "  " + c.Database + " · " + string(c.Kind)
		if c.ID == m.cmpBase.ID {
			line += "  " + warnText("（基准侧自己：比对必为空）")
		}
		if c.ReadOnly != nil && *c.ReadOnly {
			line += "  只读"
		}
		if i == m.cmpPickCur {
			line = selected("▸ " + strings.TrimPrefix(line, "  "))
		}
		b.WriteString(line + "\n")
	}
	if end < len(m.cmpCandidates) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 项\n", len(m.cmpCandidates)-end)
	}
	return b.String()
}

func (m *model) cmpDiffListView() string {
	var b strings.Builder
	fmt.Fprintf(&b, "基准 %s → 期望 %s · 忽略默认值=%s · 破坏性默认勾选=%s\n",
		m.cmpBase.Name, m.connName(m.cmpTargetID()), onOff(m.cmpIgnoreOpt), onOff(m.cmpDestructOpt))
	if len(m.cmpEntries) == 0 {
		b.WriteString("两侧结构一致，无需同步。\n")
		return b.String()
	}
	chosenDestructive := 0
	for _, id := range m.cmpSel {
		for _, e := range m.cmpEntries {
			if e.ID == id && e.Destructive {
				chosenDestructive++
			}
		}
	}
	fmt.Fprintf(&b, "共 %d 条变更 · 已勾 %d 条（其中破坏性 %d）\n",
		len(m.cmpEntries), len(m.cmpSel), chosenDestructive)
	for _, g := range m.cmpGaps {
		fmt.Fprintf(&b, "%s重建表 %s 的条目未整组勾选（还差 %s）：按 f 补齐\n",
			warnText("⚠ "), g.Table, strings.Join(g.Missing, ", "))
	}

	// 渲染行 = 相位组头 + 该组条目；光标只对条目移动，故行号与条目下标分开维护。
	// GroupByPhase 保持服务端原序且覆盖全部条目，所以一个递增下标就能对上计划。
	type row struct {
		text  string
		entry int // -1 表示组头
	}
	rows := []row{}
	idx := 0
	for _, g := range cmpsync.GroupByPhase(m.cmpEntries) {
		rows = append(rows, row{warnText("── " + g.Label + "（相位 " + fmt.Sprint(g.Phase) + "）──"), -1})
		for range g.Entries {
			rows = append(rows, row{m.cmpEntryLine(m.cmpEntries[idx]), idx})
			idx++
		}
	}
	cursorRow := 0
	for i, r := range rows {
		if r.entry == m.cmpCur {
			cursorRow = i
			break
		}
	}
	start, end := listWindow(len(rows), cursorRow, m.cmpAvail())
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 行\n", start)
	}
	for i := start; i < end; i++ {
		line := rows[i].text
		if rows[i].entry == m.cmpCur {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if end < len(rows) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 行\n", len(rows)-end)
	}
	return b.String()
}

// cmpEntryLine 一行条目：勾选框 + 动作 + 表/对象 + 从→到 + 破坏性标记 + 服务端理由。
func (m *model) cmpEntryLine(e protocol.SchemaDiffEntry) string {
	box := "[ ]"
	if m.cmpIsSelected(e.ID) {
		box = "[x]"
	}
	line := "  " + box + " " + cmpsync.OpLabel(e.Op) + " " + e.Table
	if e.Object != "" {
		line += " · " + e.Object
	}
	if e.From != nil || e.To != nil {
		line += " " + derefOr(e.From, "∅") + "→" + derefOr(e.To, "∅")
	}
	if e.Destructive {
		line += " " + failText("破坏性")
	}
	if e.Reason != "" {
		line += " — " + e.Reason
	}
	return line
}

func (m *model) cmpScriptView() string {
	var b strings.Builder
	if m.cmpScript == nil {
		b.WriteString("（还没有脚本：回第二步按 g 生成）\n")
		return b.String()
	}
	fwd := cmpsync.SummarizeScript(m.cmpScript.Statements)
	rb := cmpsync.SummarizeScript(m.cmpScript.Rollback)
	fmt.Fprintf(&b, "基准 %s → 期望 %s\n", m.cmpBase.Name, m.connName(m.cmpTargetID()))
	fmt.Fprintf(&b, "正向 %d 条（不可逆 %d · 告警 %d · 表 %s）· 回滚 %d 条（不可逆 %d）\n",
		fwd.Total, fwd.NonReversible, fwd.Warned, tableList(fwd.Tables), rb.Total, rb.NonReversible)
	fmt.Fprintf(&b, "执行方式：%s\n\n", cmpExecMode(m.cmpScript))
	// 正文按 cmpScroll 开窗口：行内已截断过 SQL，这里再截会把 ANSI 序列剪断，故不再 truncate。
	lines := m.cmpScriptLines()
	avail := m.cmpAvail()
	start := min(m.cmpScroll, len(lines))
	end := min(start+avail, len(lines))
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 行\n", start)
	}
	for _, l := range lines[start:end] {
		b.WriteString(l + "\n")
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 行\n", len(lines)-end)
	}
	if m.cmpOutcome != nil {
		b.WriteString("\n" + m.cmpOutcomeView() + "\n")
	}
	if m.cmpConfirmOn {
		b.WriteString("\n回滚强确认：逐字输入基准侧连接名 " + warnText(m.cmpBase.Name) + "\n")
		b.WriteString(m.cmpConfirm.View() + "\n")
	}
	return b.String()
}

func (m *model) connName(id string) string {
	if c := m.connByID(id); c != nil {
		return c.Name
	}
	return "（未选）"
}

// cmpScriptLines 渲染脚本正文：正向全量 + 回滚全量，并标出失败的那一条。
func (m *model) cmpScriptLines() []string {
	if m.cmpScript == nil {
		return nil
	}
	out := []string{}
	appendStmts := func(title string, stmts []protocol.DdlStatement, shift int) {
		out = append(out, title)
		if len(stmts) == 0 {
			out = append(out, "  （空）")
			return
		}
		for i, s := range stmts {
			abs := i + shift
			marker := " "
			if m.cmpOutcome != nil && len(m.cmpOutcome.Failed) > 0 && m.cmpOutcome.Failed[0].Index == abs {
				marker = "✗"
			}
			line := " " + marker + fmt.Sprintf("%2d [%d] ", i+1, s.Phase) +
				truncate(s.SQL, max(20, m.width-16))
			if s.NonReversible {
				line += "  " + failText("不可逆")
			}
			out = append(out, line)
			for _, w := range s.Warnings {
				out = append(out, "      "+warnText("! "+w))
			}
		}
	}
	appendStmts("正向语句：", m.cmpScript.Statements, 0)
	// 回滚结果的 Index 按回滚脚本自身编号，故 shift=0（两份脚本各自从 0 计）。
	appendStmts("\n回滚语句（建议脚本，r 代跑）：", m.cmpScript.Rollback, 0)
	return out
}

func (m *model) cmpOutcomeView() string {
	o := m.cmpOutcome
	run := "正向"
	if m.cmpRun == cmpRunRollback {
		run = "回滚"
	}
	if len(o.Failed) == 0 {
		return okText(fmt.Sprintf("%s脚本执行成功：%d 条已执行", run, o.Ran))
	}
	var b strings.Builder
	suffix := "，未回滚（无开放事务可回，或该方言 DDL 已隐式提交）"
	if o.RolledBack {
		suffix = "，事务已回滚"
	}
	fmt.Fprintf(&b, "%s\n", failText(fmt.Sprintf("%s脚本中断：已执行 %d 条，第 %d 条失败，跳过 %d 条%s",
		run, o.Ran, o.Failed[0].Index+1, o.Skipped, suffix)))
	for _, f := range o.Failed {
		fmt.Fprintf(&b, "  SQL: %s\n", f.SQL)
		fmt.Fprintf(&b, "  原因: %s\n", f.Reason)
		if f.Table != "" {
			fmt.Fprintf(&b, "  相位 %d · 表 %s · 条目 %s\n", f.Phase, f.Table, strings.Join(f.EntryIDs, ", "))
		}
	}
	return b.String()
}

// cmpExecMode 把两个能力位翻成人话，让操作者知道会不会包事务。
func cmpExecMode(script *protocol.DdlScript) string {
	switch {
	case script.DdlInTx && script.PragmasOutsideTx:
		return "首尾 PRAGMA 事务外 · 主体单事务（失败整体回滚）"
	case script.DdlInTx:
		return "全部语句包进单事务（失败整体回滚）"
	default:
		return "该方言 DDL 会隐式提交 ⇒ 逐条执行（失败留下中间态）"
	}
}

func tableList(tables []string) string {
	if len(tables) == 0 {
		return "无"
	}
	return strings.Join(tables, ",")
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
