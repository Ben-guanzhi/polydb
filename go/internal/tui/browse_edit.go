package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
)

// 变更跟踪编辑（T3）：单元格编辑入本地队列 → Review 预览 → 单事务提交（逐条行数校验）
// / 失败回滚 / 撤销。只读连接或无主键表禁止编辑。独立实现，语义对齐 Web/Rust。

// connKindReadOnly 返回当前选中连接的方言与只读标记。
func (m *model) connKindReadOnly() (protocol.DatabaseKind, bool) {
	for _, c := range m.conns {
		if c.ID == m.selectedID {
			ro := c.ReadOnly != nil && *c.ReadOnly
			return c.Kind, ro
		}
	}
	return m.brKind, false
}

// colIndexOf 返回列名在结果集中的下标，找不到返回 -1。
func colIndexOf(cols []protocol.ResultColumn, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// browseRowID 用主键值合成稳定行标识（无主键回落到页内绝对序号，但此时禁用编辑）。
func (m *model) browseRowID(ri int) string {
	r := m.brRes
	if r == nil || ri < 0 || ri >= len(r.Rows) {
		return ""
	}
	if len(m.brPK) == 0 {
		return fmt.Sprintf("#%d:%d", m.brOffset, ri)
	}
	parts := make([]string, 0, len(m.brPK))
	for _, pk := range m.brPK {
		var v = protocol.NewNullValue()
		if ci := colIndexOf(r.Columns, pk); ci >= 0 && ci < len(r.Rows[ri]) {
			v = r.Rows[ri][ci]
		}
		parts = append(parts, cellString(v))
	}
	return strings.Join(parts, "\x1f")
}

// browseBuildRow 把结果集第 ri 行快照为 changes.Row（保留列序）。
func (m *model) browseBuildRow(ri int) changes.Row {
	r := m.brRes
	if r == nil || ri < 0 || ri >= len(r.Rows) {
		return nil
	}
	row := make(changes.Row, 0, len(r.Columns))
	for j, c := range r.Columns {
		var v = protocol.NewNullValue()
		if j < len(r.Rows[ri]) {
			v = r.Rows[ri][j]
		}
		row = append(row, changes.Cell{Column: c.Name, Value: v})
	}
	return row
}

// parseEdit 依据原值类型解析用户输入：NULL 恒为 null；原为字符串则保持字符串；
// 其余走 parseScalarValue（bool/int/float 感知）。
func parseEdit(orig protocol.Value, input string) protocol.Value {
	if strings.EqualFold(strings.TrimSpace(input), "NULL") {
		return protocol.NewNullValue()
	}
	if raw, err := orig.MarshalJSON(); err == nil && len(raw) > 0 && raw[0] == '"' {
		return protocol.NewStringValue(input)
	}
	return parseScalarValue(input)
}

// enterCellEdit 进入当前单元格编辑态。
func (m *model) enterCellEdit() (tea.Model, tea.Cmd) {
	if m.brReadOnly {
		m.status = "只读连接：不支持编辑"
		return m, nil
	}
	if len(m.brPK) == 0 {
		m.status = "无主键：无法定位编辑行"
		return m, nil
	}
	r := m.brRes
	if r == nil || len(r.Columns) == 0 || m.brRow >= len(r.Rows) {
		return m, nil
	}
	col := m.currentBrowseColumn()
	if col == "" {
		return m, nil
	}
	ci := colIndexOf(r.Columns, col)
	if ci < 0 {
		return m, nil
	}
	m.brEditCol = col
	m.brEditRow = m.brRow
	m.brEditOrig = cellString(r.Rows[m.brRow][ci])
	m.brVal.SetValue("")
	m.brVal.CursorEnd()
	m.brVal.Focus()
	m.brCellEdit = true
	m.brFull = false
	return m, nil
}

// browseCellKey 单元格编辑键位：常规输入转 textinput，Enter 入队，Esc 取消。
func (m *model) browseCellKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.brCellEdit = false
		m.status = "已取消编辑"
		return m, nil
	case tea.KeyEnter:
		return m.commitCellEdit()
	default:
		var cmd tea.Cmd
		m.brVal, cmd = m.brVal.Update(msg)
		return m, cmd
	}
}

func (m *model) commitCellEdit() (tea.Model, tea.Cmd) {
	r := m.brRes
	if r == nil || m.brEditRow >= len(r.Rows) {
		m.brCellEdit = false
		return m, nil
	}
	ci := colIndexOf(r.Columns, m.brEditCol)
	if ci < 0 {
		m.brCellEdit = false
		return m, nil
	}
	orig := r.Rows[m.brEditRow][ci]
	newVal := parseEdit(orig, m.brVal.Value())
	rowID := m.browseRowID(m.brEditRow)
	m.brQueue.AddCellChange(rowID, m.brEditCol, orig, newVal, m.browseBuildRow(m.brEditRow))
	m.brCellEdit = false
	m.status = fmt.Sprintf("已暂存 %d 项变更（w 审阅提交）", m.brQueue.Size())
	return m, nil
}

// undoBrowseChange 撤销最近一次暂存操作。
func (m *model) undoBrowseChange() (tea.Model, tea.Cmd) {
	if !m.brQueue.Undo() {
		m.status = "无可撤销"
		return m, nil
	}
	m.status = fmt.Sprintf("已撤销一步，剩 %d 项", m.brQueue.Size())
	return m, nil
}

// openReview 打开审阅面板：据当前队列重算语句。
func (m *model) openReview() (tea.Model, tea.Cmd) {
	if m.brQueue.Size() == 0 {
		m.status = "无待提交变更"
		return m, nil
	}
	m.brStmts = changes.BuildStatements(m.brQueue.All(), m.brSchema, m.brTable, m.brPK, m.brKind)
	m.brReview = true
	m.brReviewCur = 0
	return m, nil
}

// reviewKey 审阅面板键位：Enter/y 提交 · Esc/n 返回 · u 撤销末项 · ↑/↓ 移动。
func (m *model) reviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.brReview = false
		return m, nil
	case tea.KeyEnter:
		return m.applyChanges()
	case tea.KeyUp:
		if m.brReviewCur > 0 {
			m.brReviewCur--
		}
	case tea.KeyDown:
		if m.brReviewCur < len(m.brStmts)-1 {
			m.brReviewCur++
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "n":
			m.brReview = false
		case "y":
			return m.applyChanges()
		case "k":
			if m.brReviewCur > 0 {
				m.brReviewCur--
			}
		case "j":
			if m.brReviewCur < len(m.brStmts)-1 {
				m.brReviewCur++
			}
		case "u":
			if m.brQueue.Undo() {
				if m.brQueue.Size() == 0 {
					m.brReview = false
					m.status = "已撤销全部变更"
				} else {
					m.brStmts = changes.BuildStatements(m.brQueue.All(), m.brSchema, m.brTable, m.brPK, m.brKind)
					m.brReviewCur = min(m.brReviewCur, len(m.brStmts)-1)
				}
			}
		}
	}
	return m, nil
}

// applyChanges 单事务提交队列：逐条 ExecuteInTx + 行数校验，任一行数不符或出错即回滚。
func (m *model) applyChanges() (tea.Model, tea.Cmd) {
	if m.brReadOnly || len(m.brPK) == 0 || m.brQueue.Size() == 0 {
		m.status = "无法提交（只读 / 无主键 / 无变更）"
		return m, nil
	}
	m.busy = "提交中 …"
	m.brReview = false
	return m, applyChangesCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.brPK, m.brKind, m.brQueue.All())
}

// dirtyDecorator 返回渲染装饰器：把已暂存单元格显示为新值并标脏。
func (m *model) dirtyDecorator() cellDecorator {
	if m.brQueue == nil || m.brQueue.Size() == 0 || m.brRes == nil {
		return nil
	}
	return func(ri, ci int) (string, bool) {
		if ri < 0 || ri >= len(m.brRes.Rows) || ci < 0 || ci >= len(m.brRes.Columns) {
			return "", false
		}
		ch, ok := m.brQueue.Get(m.browseRowID(ri))
		if !ok {
			return "", false
		}
		col := m.brRes.Columns[ci].Name
		for _, cc := range ch.CellChanges {
			if cc.Column == col {
				return cellString(cc.NewValue), true
			}
		}
		return "", false
	}
}

// applyBrowseColumns 落地列元数据并推导有序主键列名。
func (m *model) applyBrowseColumns(cols []protocol.ColumnInfo) {
	m.brColumns = cols
	var pks []protocol.ColumnInfo
	for _, c := range cols {
		if c.IsPrimaryKey {
			pks = append(pks, c)
		}
	}
	sort.SliceStable(pks, func(i, j int) bool { return pks[i].OrdinalPosition < pks[j].OrdinalPosition })
	m.brPK = m.brPK[:0]
	for _, c := range pks {
		m.brPK = append(m.brPK, c.Name)
	}
}

// browseEditBar 编辑/审阅态下替换过滤栏的一行提示。
func (m *model) browseEditBar() string {
	var seg []string
	if m.brCellEdit {
		seg = append(seg, "编辑 ["+m.brEditCol+"] 原值 ["+m.brEditOrig+"] → ["+m.brVal.Value()+"]")
		seg = append(seg, "Enter 暂存 · Esc 取消（输入 NULL 置空）")
		return strings.Join(seg, "  ")
	}
	n := 0
	if m.brQueue != nil {
		n = m.brQueue.Size()
	}
	if n > 0 {
		return fmt.Sprintf("待提交 %d 项变更 · e 编辑 · u 撤销 · w 审阅提交", n)
	}
	if m.brReadOnly {
		return "只读连接 · e 编辑被禁用"
	}
	if len(m.brPK) == 0 {
		return "无主键表 · 不支持编辑"
	}
	return "e 编辑单元格 · u 撤销 · w 审阅提交"
}

// reviewView 渲染审阅面板：逐条列出参数化预览 SQL。
func (m *model) reviewView() string {
	var b strings.Builder
	b.WriteString(selected(fmt.Sprintf(" 审阅变更（%d 项）", len(m.brStmts))))
	b.WriteString("  Enter/y 提交 · Esc/n 返回 · u 撤销末项 · ↑/↓ 查看\n")
	if len(m.brStmts) == 0 {
		b.WriteString("（队列为空）")
		return b.String()
	}
	avail := max(3, m.height-16)
	start := 0
	if m.brReviewCur >= avail {
		start = m.brReviewCur - avail/2
	}
	if start+avail > len(m.brStmts) {
		start = max(0, len(m.brStmts)-avail)
	}
	for i := start; i < min(start+avail, len(m.brStmts)); i++ {
		s := m.brStmts[i]
		prefix := "  "
		if i == m.brReviewCur {
			prefix = "▸ "
		}
		line := warnText(fmt.Sprintf("%s%s", prefix, truncate(s.SQL, max(m.width-4, 20))))
		if i == m.brReviewCur {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if rem := len(m.brStmts) - (start + avail); rem > 0 {
		fmt.Fprintf(&b, "↓ 下方还有 %d", rem)
	}
	return b.String()
}
