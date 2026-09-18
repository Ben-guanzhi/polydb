package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

type view int

const (
	viewConns view = iota
	viewForm
	viewTables
	viewTable
	viewQuery
)

var viewTitles = map[view]string{
	viewConns:  "连接",
	viewForm:   "新建连接",
	viewTables: "库表结构",
	viewTable:  "表详情",
	viewQuery:  "查询",
}

type model struct {
	app *appcore.AppCore

	view   view
	width  int
	height int
	quit   bool
	busy   string
	status string

	form       formModel
	conns      []protocol.ConnectionInfo
	connCursor int
	connStatus map[string]*protocol.ConnectionStatus
	pendingDel string // 待确认删除的连接 id

	selectedID string
	schemas    []protocol.SchemaInfo
	schemaIdx  int
	tables     []protocol.TableInfo
	tableCur   int

	tableDetail    *tableDetail
	tableDetailErr error

	queryInput  textarea.Model
	queryResult *protocol.QueryResult
	queryErr    error
	querySQL    string

	// autocomplete 状态：在查询视图中 Ctrl+Space 触发，↑/↓ 选择，Enter/Tab 接受，Esc 关闭。
	acWord       string
	acCandidates []string
	acIdx        int
	acSchema     string // 候选所属 schema（用于列补全上下文）
	acTable      string // 当前候选对应的表（列补全结果）
	acColumns    []protocol.ColumnInfo
}

func New(app *appcore.AppCore) *model {
	ta := textarea.New()
	ta.Placeholder = "输入 SQL（F5 执行）…"
	ta.ShowLineNumbers = true
	ta.SetHeight(6)
	return &model{
		app:        app,
		view:       viewConns,
		connStatus: make(map[string]*protocol.ConnectionStatus),
		queryInput: ta,
	}
}

func (m *model) Init() tea.Cmd {
	return loadConnsCmd(m.app)
}

// ─── Update ─────────────────────────────────────────────────

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.queryInput.SetWidth(msg.Width - 6)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case connsLoadedMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "加载连接失败: " + msg.err.Error()
		} else {
			m.conns = msg.conns
			if m.connCursor >= len(m.conns) {
				m.connCursor = 0
			}
		}
		return m, nil
	case statusMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "测试失败: " + msg.err.Error()
		} else {
			m.connStatus[msg.id] = msg.st
			m.status = statusLine(msg.st)
		}
		return m, nil
	case createdMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "创建失败: " + msg.err.Error()
			return m, nil
		}
		m.conns = append(m.conns, msg.conn)
		m.status = "已创建「" + msg.conn.Name + "」"
		m.view = viewConns
		return m, nil
	case deletedMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "删除失败: " + msg.err.Error()
		} else {
			m.pendingDel = ""
			m.status = "已删除"
			if m.selectedID != "" {
				m.selectedID = ""
			}
			return m, loadConnsCmd(m.app)
		}
		return m, nil
	case schemasMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "打开连接失败: " + msg.err.Error()
			return m, nil
		}
		m.selectedID = msg.id
		m.schemas = msg.schemas
		m.schemaIdx = 0
		m.view = viewTables
		return m, tablesCmd(m.app, msg.id, m.schemas[0].Name)
	case tablesMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "加载表失败: " + msg.err.Error()
			return m, nil
		}
		m.tables = msg.tables
		if m.tableCur >= len(m.tables) {
			m.tableCur = 0
		}
		return m, nil
	case detailMsg:
		m.busy = ""
		if msg.err != nil {
			m.tableDetailErr = msg.err
			m.status = "加载详情失败: " + msg.err.Error()
		} else {
			m.tableDetail = msg.detail
			m.tableDetailErr = nil
		}
		m.view = viewTable
		return m, nil
	case queryMsg:
		m.busy = ""
		if msg.err != nil {
			m.queryErr = msg.err
			m.queryResult = nil
			m.status = "查询失败: " + msg.err.Error()
		} else {
			m.queryResult = msg.res
			m.queryErr = nil
			m.querySQL = ""
		}
		m.view = viewQuery
		return m, nil
	case columnsLoadedMsg:
		if msg.err != nil {
			m.status = "加载列失败: " + msg.err.Error()
			return m, nil
		}
		if msg.table != m.acTable || m.acSchema != m.schemas[m.schemaIdx].Name {
			return m, nil
		}
		m.acColumns = msg.columns
		cands := make([]string, 0, len(msg.columns))
		for _, c := range msg.columns {
			cands = append(cands, c.Name)
		}
		if len(cands) == 0 {
			m.acCandidates = nil
			m.acWord = ""
			return m, nil
		}
		m.acCandidates = cands
		m.acIdx = 0
		return m, nil
	}
	return m, nil
}

func statusLine(st *protocol.ConnectionStatus) string {
	if st.Connected {
		return fmt.Sprintf("已连接 · %.1f ms", st.LatencyMs)
	}
	return "连接失败: " + st.Error
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy != "" {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m, nil
	}
	switch m.view {
	case viewConns:
		return m.connsKey(msg)
	case viewForm:
		return m.formKey(msg)
	case viewTables:
		return m.tablesKey(msg)
	case viewTable:
		return m.tableKey(msg)
	case viewQuery:
		return m.queryKey(msg)
	}
	return m, nil
}

// ─── 连接列表 ────────────────────────────────────────────────

func (m *model) connsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyUp:
		if m.connCursor > 0 {
			m.connCursor--
		}
	case tea.KeyDown:
		if m.connCursor < len(m.conns)-1 {
			m.connCursor++
		}
		m.pendingDel = ""
	case tea.KeyEnter:
		if len(m.conns) > 0 {
			c := m.conns[m.connCursor]
			m.busy = "连接中 " + c.Name + " …"
			return m, openConnCmd(m.app, c.ID)
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if m.connCursor > 0 {
				m.connCursor--
			}
		case "j":
			if m.connCursor < len(m.conns)-1 {
				m.connCursor++
			}
			m.pendingDel = ""
		case "n":
			m.form = newForm()
			m.view = viewForm
		case "d":
			if len(m.conns) == 0 {
				return m, nil
			}
			c := m.conns[m.connCursor]
			if m.pendingDel == c.ID {
				m.busy = "删除中 …"
				return m, deleteCmd(m.app, c.ID)
			}
			m.pendingDel = c.ID
			m.status = "再按 d 确认删除「" + c.Name + "」"
		case "t":
			if len(m.conns) > 0 {
				c := m.conns[m.connCursor]
				m.busy = "测试中 …"
				return m, testCmd(m.app, c.ID)
			}
		case "r":
			m.busy = "刷新中 …"
			return m, loadConnsCmd(m.app)
		case "q":
			m.quit = true
			return m, tea.Quit
		}
	case tea.KeyEsc:
		m.pendingDel = ""
		m.status = ""
	}
	return m, nil
}

// ─── 新建表单 ────────────────────────────────────────────────

func (m *model) formKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.view = viewConns
		return m, nil
	case tea.KeyEnter:
		if m.form.focus == len(m.form.fields)-1 {
			req := m.form.request()
			if strings.TrimSpace(req.Name) == "" {
				m.status = "名称不能为空"
				return m, nil
			}
			m.busy = "创建中 …"
			return m, createCmd(m.app, &req)
		}
	}
	f, submitted := m.form.updateKey(msg)
	m.form = f
	if submitted {
		req := m.form.request()
		if strings.TrimSpace(req.Name) == "" {
			m.status = "名称不能为空"
			return m, nil
		}
		m.busy = "创建中 …"
		return m, createCmd(m.app, &req)
	}
	return m, nil
}

// ─── 库表浏览 ────────────────────────────────────────────────

func (m *model) tablesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyBackspace:
		m.view = viewConns
		return m, nil
	case tea.KeyUp:
		if m.tableCur > 0 {
			m.tableCur--
		}
	case tea.KeyDown:
		if m.tableCur < len(m.tables)-1 {
			m.tableCur++
		}
	case tea.KeyEnter:
		if len(m.tables) > 0 {
			t := m.tables[m.tableCur]
			m.busy = "加载详情 …"
			return m, detailCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name, t.Name)
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if m.tableCur > 0 {
				m.tableCur--
			}
		case "j":
			if m.tableCur < len(m.tables)-1 {
				m.tableCur++
			}
		case "s":
			if len(m.schemas) > 1 {
				m.schemaIdx = (m.schemaIdx + 1) % len(m.schemas)
				m.busy = "加载表 …"
				return m, tablesCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name)
			}
		case "q":
			return m.gotoQuery("")
		}
	}
	return m, nil
}

func (m *model) tableKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyBackspace:
		m.view = viewTables
		return m, nil
	case tea.KeyUp:
		if len(m.tables) > 0 && m.tableCur > 0 {
			m.tableCur--
			m.busy = "加载详情 …"
			return m, detailCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name, m.tables[m.tableCur].Name)
		}
	case tea.KeyDown:
		if len(m.tables) > 1 && m.tableCur < len(m.tables)-1 {
			m.tableCur++
			m.busy = "加载详情 …"
			return m, detailCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name, m.tables[m.tableCur].Name)
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if len(m.tables) > 0 && m.tableCur > 0 {
				m.tableCur--
				m.busy = "加载详情 …"
				return m, detailCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name, m.tables[m.tableCur].Name)
			}
		case "j":
			if len(m.tables) > 1 && m.tableCur < len(m.tables)-1 {
				m.tableCur++
				m.busy = "加载详情 …"
				return m, detailCmd(m.app, m.selectedID, m.schemas[m.schemaIdx].Name, m.tables[m.tableCur].Name)
			}
		case "q":
			if len(m.tables) > 0 {
				t := m.tables[m.tableCur]
				schema := m.schemas[m.schemaIdx].Name
				sql := fmt.Sprintf("SELECT * FROM %q.%q LIMIT 100;", schema, t.Name)
				return m.gotoQuery(sql)
			}
		}
	}
	return m, nil
}

// ─── 查询视图 ────────────────────────────────────────────────

func (m *model) gotoQuery(prefill string) (tea.Model, tea.Cmd) {
	m.queryInput.SetValue(prefill)
	m.querySQL = prefill
	m.queryResult = nil
	m.queryErr = nil
	m.acWord = ""
	m.acCandidates = nil
	m.acIdx = 0
	m.acSchema = ""
	m.acTable = ""
	m.acColumns = nil
	m.queryInput.Focus()
	m.view = viewQuery
	return m, nil
}

func (m *model) queryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		if len(m.acCandidates) > 0 {
			m.acWord = ""
			m.acCandidates = nil
			m.acIdx = 0
			return m, nil
		}
		m.queryInput.Blur()
		if m.tableDetail != nil {
			m.view = viewTable
		} else {
			m.view = viewTables
		}
		return m, nil
	case tea.KeyF5, tea.KeyCtrlE:
		return m.runQuery()
	}
	if len(m.acCandidates) > 0 {
		switch msg.Type {
		case tea.KeyUp:
			if m.acIdx > 0 {
				m.acIdx--
			}
			return m, nil
		case tea.KeyDown:
			if m.acIdx < len(m.acCandidates)-1 {
				m.acIdx++
			}
			return m, nil
		case tea.KeyCtrlN:
			if m.acIdx < len(m.acCandidates)-1 {
				m.acIdx++
			}
			return m, nil
		case tea.KeyCtrlP:
			if m.acIdx > 0 {
				m.acIdx--
			}
			return m, nil
		case tea.KeyEnter, tea.KeyTab:
			m.acceptCompletion()
			return m, nil
		}
	}
	if msg.Type == tea.KeyTab {
		return m.triggerAutocomplete()
	}
	var cmd tea.Cmd
	m.queryInput, cmd = m.queryInput.Update(msg)
	return m, cmd
}

// triggerAutocomplete 生成表名候选（无 . 前缀）或列名候选（含 . 前缀）。
func (m *model) triggerAutocomplete() (tea.Model, tea.Cmd) {
	if m.selectedID == "" || len(m.schemas) == 0 || len(m.tables) == 0 {
		return m, nil
	}
	text := m.queryInput.Value()
	cur, _ := cursorPos(m.queryInput)
	if cur > len(text) {
		cur = len(text)
	}
	start := cur
	for start > 0 {
		r := text[start-1]
		if isIdentPartRune(r) {
			start--
		} else {
			break
		}
	}
	word := text[start:cur]
	prefix := ""
	if i := strings.LastIndex(word, "."); i >= 0 {
		prefix = word[:i+1]
		word = word[i+1:]
	}
	var cands []string
	switch {
	case prefix != "":
		schema := m.schemas[m.schemaIdx].Name
		for _, t := range m.tables {
			if word == "" || strings.EqualFold(t.Name, word) {
				m.acSchema = schema
				m.acTable = t.Name
				if m.acColumns == nil {
					return m, columnsCmd(m.app, m.selectedID, schema, t.Name)
				}
				for _, c := range m.acColumns {
					cands = append(cands, c.Name)
				}
				break
			}
		}
	default:
		m.acSchema = ""
		m.acTable = ""
		m.acColumns = nil
		for _, t := range m.tables {
			cands = append(cands, t.Name)
		}
	}
	if len(cands) == 0 {
		return m, nil
	}
	m.acWord = prefix + word
	m.acCandidates = cands
	m.acIdx = 0
	return m, nil
}

// acceptCompletion 把光标前匹配前缀的 SQL 文本替换为当前选中的候选。
func (m *model) acceptCompletion() {
	if len(m.acCandidates) == 0 {
		return
	}
	text := m.queryInput.Value()
	cur, _ := cursorPos(m.queryInput)
	if cur > len(text) {
		cur = len(text)
	}
	word := m.acWord
	if len(word) > cur {
		word = text[:cur]
	}
	start := cur - len(word)
	if start < 0 {
		start = 0
	}
	newWord := m.acCandidates[m.acIdx]
	if m.acTable != "" {
		newWord = m.acTable + "." + newWord
	}
	newText := text[:start] + newWord + text[cur:]
	m.queryInput.SetValue(newText)
	// SetValue 已将光标放在文本末尾，符合「接受候选后继续输入」的预期。
	m.acWord = ""
	m.acCandidates = nil
	m.acIdx = 0
}

// cursorPos 返回 textarea 光标在完整 SQL 文本中的字节偏移；
// 第二返回值是所在行号（0 起）。
func cursorPos(ta textarea.Model) (int, int) {
	li := ta.LineInfo()
	row := ta.Line() + li.RowOffset
	text := ta.Value()
	lines := strings.Split(text, "\n")
	if row >= len(lines) {
		return len(text), row
	}
	total := 0
	for i := 0; i < row && i < len(lines); i++ {
		total += len(lines[i]) + 1 // +1 for '\n'
	}
	return total + li.StartColumn + li.CharOffset, row
}

func isIdentPartRune(r byte) bool {
	return r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func (m *model) runQuery() (tea.Model, tea.Cmd) {
	sql := strings.TrimSpace(m.queryInput.Value())
	if sql == "" {
		m.status = "SQL 为空"
		return m, nil
	}
	m.querySQL = sql
	m.busy = "执行中 …"
	return m, queryCmd(m.app, m.selectedID, sql)
}

// ─── View ───────────────────────────────────────────────────

func (m *model) View() string {
	if m.quit {
		return ""
	}
	var body string
	switch m.view {
	case viewConns:
		body = m.connsView()
	case viewForm:
		body = m.form.view()
	case viewTables:
		body = m.tablesView()
	case viewTable:
		body = m.tableView()
	case viewQuery:
		body = m.queryView()
	}
	return m.frame(body)
}

func (m *model) frame(body string) string {
	var b strings.Builder
	title := "polydb TUI · " + viewTitles[m.view]
	if m.selectedID != "" {
		title += " · " + m.selectedID[:min(8, len(m.selectedID))]
	}
	b.WriteString(title + "\n")
	b.WriteString(strings.Repeat("─", min(m.width, 60)) + "\n")
	b.WriteString(body)
	b.WriteString("\n")
	if m.busy != "" {
		b.WriteString(m.busy)
	} else if m.status != "" {
		b.WriteString(m.status)
	} else {
		b.WriteString(m.footer())
	}
	return b.String()
}

func (m *model) footer() string {
	switch m.view {
	case viewConns:
		return "↑/↓ 选择 · Enter 打开 · n 新建 · t 测试 · d 删除 · r 刷新 · q 退出"
	case viewForm:
		return "Tab 切换字段 · ←/→ 切换类型 · Enter 提交 · Esc 返回"
	case viewTables:
		return "↑/↓ 选择 · Enter 详情 · s 切换 schema · q 查询 · Esc 返回"
	case viewTable:
		return "↑/↓ 切换表 · q 查询该表 · Esc 返回"
	case viewQuery:
		if len(m.acCandidates) > 0 {
			return "↑/↓ 选择 · Enter 接受 · Esc 关闭 · F5 执行"
		}
		return "Tab 补全表/列 · F5 / Ctrl+E 执行 · Esc 返回"
	}
	return ""
}

func (m *model) connsView() string {
	if len(m.conns) == 0 {
		return "暂无连接，按 n 新建。\n"
	}
	var b strings.Builder
	for i, c := range m.conns {
		marker := "  "
		if i == m.connCursor {
			marker = "▸ "
		}
		line := marker + c.Name + "  " + c.Database + " · " + string(c.Kind)
		if st, ok := m.connStatus[c.ID]; ok {
			if st.Connected {
				line += fmt.Sprintf("  \x1b[32m●\x1b[0m %.1f ms", st.LatencyMs)
			} else {
				line += "  \x1b[31m●\x1b[0m 连接失败"
			}
		}
		if m.pendingDel == c.ID {
			line += "  \x1b[33m待确认删除\x1b[0m"
		}
		if i == m.connCursor {
			line = "\x1b[7m" + line + "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m *model) tablesView() string {
	if m.busy != "" {
		return "加载中 …\n"
	}
	schema := m.schemas[m.schemaIdx].Name
	var b strings.Builder
	b.WriteString("Schema: " + schema)
	if len(m.schemas) > 1 {
		b.WriteString("（s 切换，共 " + fmt.Sprint(len(m.schemas)) + " 个）")
	}
	b.WriteString("\n")
	if len(m.tables) == 0 {
		b.WriteString("（无表）")
		return b.String()
	}
	for i, t := range m.tables {
		marker := "  "
		if i == m.tableCur {
			marker = "▸ "
		}
		line := marker + t.Name
		if t.Type != "" && t.Type != protocol.TableTypeTable {
			line += "  [" + string(t.Type) + "]"
		}
		if i == m.tableCur {
			line = "\x1b[7m" + line + "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m *model) tableView() string {
	if m.tableDetailErr != nil {
		return "加载失败: " + m.tableDetailErr.Error() + "\n"
	}
	if m.tableDetail == nil {
		return "加载中 …\n"
	}
	d := m.tableDetail
	t := m.tables[m.tableCur]
	var b strings.Builder
	b.WriteString("表 " + m.schemas[m.schemaIdx].Name + "." + t.Name + "\n\n")
	b.WriteString("列：\n")
	if len(d.columns) == 0 {
		b.WriteString("（无）\n")
	}
	for _, col := range d.columns {
		flags := ""
		if col.IsPrimaryKey {
			flags += " PK"
		}
		if col.IsAutoIncrement {
			flags += " AUTO"
		}
		if !col.Nullable {
			flags += " NOT NULL"
		}
		if col.DefaultValue != nil {
			flags += " DEFAULT " + *col.DefaultValue
		}
		fmt.Fprintf(&b, "  %-20s %s%s\n", col.Name, col.DataType, flags)
	}
	if len(d.indexes) > 0 {
		b.WriteString("\n索引：\n")
		for _, idx := range d.indexes {
			names := make([]string, len(idx.Columns))
			for i, c := range idx.Columns {
				names[i] = c.Name
			}
			kind := "idx"
			if idx.Primary {
				kind = "PK"
			} else if idx.Unique {
				kind = "unique"
			}
			fmt.Fprintf(&b, "  %s (%s) [%s]\n", idx.Name, strings.Join(names, ", "), kind)
		}
	}
	if len(d.fks) > 0 {
		b.WriteString("\n外键：\n")
		for _, fk := range d.fks {
			fmt.Fprintf(&b, "  %s: %s → %s.%s(%s)\n",
				fk.Name, strings.Join(fk.Columns, ","), fk.ReferencedSchema, fk.ReferencedTable,
				strings.Join(fk.ReferencedColumns, ","))
		}
	}
	if d.ddl != "" {
		b.WriteString("\nDDL：\n  " + strings.ReplaceAll(d.ddl, "\n", "\n  ") + "\n")
	}
	return b.String()
}

func (m *model) queryView() string {
	var b strings.Builder
	b.WriteString("SQL：\n")
	b.WriteString(m.queryInput.View())
	b.WriteString("\n")
	if len(m.acCandidates) > 0 {
		b.WriteString(m.acPanel())
	}
	if m.queryErr != nil {
		b.WriteString("\x1b[31m" + m.queryErr.Error() + "\x1b[0m\n")
		return b.String()
	}
	if m.queryResult != nil {
		r := m.queryResult
		meta := stBadge(r.StatementType) + fmt.Sprintf(" · 执行 %.2f ms", r.ExecutionTimeMs)
		if r.AffectedRows > 0 {
			meta += fmt.Sprintf(" · 影响 %d 行", r.AffectedRows)
		}
		if r.Truncated || (r.HasMore && r.TotalRows != nil) {
			meta += " · 结果已截断"
			if r.TotalRows != nil {
				meta += fmt.Sprintf("（共 %d 行）", *r.TotalRows)
			}
		}
		b.WriteString(meta + "\n")
		if len(r.Columns) > 0 {
			maxRows := max(3, m.height-16)
			table := resultTable(r.Columns, r.Rows, max(m.width-4, 20), maxRows)
			b.WriteString(table + "\n")
		} else {
			b.WriteString("（无结果集）\n")
		}
	} else {
		b.WriteString("执行 SQL 后此处显示结果。\n")
	}
	return b.String()
}

// acPanel 渲染自动补全候选面板（最多 8 行，当前项反显）。
func (m *model) acPanel() string {
	const maxRows = 8
	start := m.acIdx
	if start > maxRows-1 {
		start = maxRows - 1
	}
	end := min(start+maxRows, len(m.acCandidates))
	var b strings.Builder
	if m.acTable != "" {
		b.WriteString("列 " + m.acSchema + "." + m.acTable + "：\n")
	} else {
		b.WriteString("表 " + m.schemas[m.schemaIdx].Name + "：\n")
	}
	for i := start; i < end; i++ {
		marker := "  "
		if i == m.acIdx {
			marker = "▸ "
		}
		line := marker + m.acCandidates[i]
		if i == m.acIdx {
			line = "\x1b[7m" + line + "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
