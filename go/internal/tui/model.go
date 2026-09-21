package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

type view int

const (
	viewConns view = iota
	viewForm
	viewTables
	viewTable
	viewQuery
	viewKV
	viewResults
	viewBrowse
)

// 心跳：每 30s ping 当前连接；测试经 TestMain 置 heartbeatDisabled，
// 避免 drive() 跟随自续的 tea.Tick 命令链而阻塞。
const heartbeatEvery = 30 * time.Second

var heartbeatDisabled bool

// armHB 安排下一跳心跳（测试态返回 nil）。
func (m *model) armHB() tea.Cmd {
	if heartbeatDisabled {
		return nil
	}
	return heartbeatCmd()
}

var viewTitles = map[view]string{
	viewConns:   "连接",
	viewForm:    "新建连接",
	viewTables:  "库表结构",
	viewTable:   "表详情",
	viewQuery:   "查询",
	viewKV:      "Redis 键",
	viewResults: "结果导航",
	viewBrowse:  "数据浏览",
}

type model struct {
	app transport.Client

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

	// 结果单元格导航（viewResults）：光标位置 + 完整值面板开关。
	resRow   int
	resCol   int
	cellFull bool

	// 数据浏览（viewBrowse）：F7 服务端分页浏览表数据。
	brSchema string
	brTable  string
	brOffset uint64
	brLimit  uint32
	brRes    *protocol.TableRowsResult
	brErr    error
	brRow    int
	brCol    int
	brFull   bool

	// autocomplete 状态：在查询视图中 Ctrl+Space 触发，↑/↓ 选择，Enter/Tab 接受，Esc 关闭。
	acWord       string
	acCandidates []string
	acIdx        int
	acSchema     string // 候选所属 schema（用于列补全上下文）
	acTable      string // 当前候选对应的表（列补全结果）
	acColumns    []protocol.ColumnInfo

	// Redis KV（M6 前端 Redis 模式；redis 连接走 viewKV，不进库表/查询流）。
	kvDB       int
	kvPattern  string
	kvKeys     []protocol.RedisKeyInfo
	kvCur      int
	kvPageCur  uint64
	kvValue    protocol.RedisValue
	kvValueKey string
	kvValueErr error
	kvReply    *protocol.RedisReply
	kvFilter   textinput.Model
	kvFilterOn bool
	kvCmd      textinput.Model
	kvCmdOn    bool
}

func New(app transport.Client) *model {
	ta := textarea.New()
	ta.Placeholder = "输入 SQL（F5 执行）…"
	ta.ShowLineNumbers = true
	ta.SetHeight(6)
	filter := textinput.New()
	filter.Placeholder = "键模式（* 或 user:*）"
	km := textinput.New()
	km.Placeholder = "Redis 命令（GET user:1 / EXPIRE user:1 60 …）"
	m := &model{
		app:        app,
		view:       viewConns,
		connStatus: make(map[string]*protocol.ConnectionStatus),
		queryInput: ta,
		kvFilter:   filter,
		kvCmd:      km,
	}
	return m
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
	case tea.BatchMsg:
		// 测试/直连场景兜底：bubbletea 运行时会自行展开 BatchMsg，
		// 直接调用 Update 时在此顺序展开，避免消息被丢弃。
		var pending []tea.Cmd
		for _, c := range msg {
			if c == nil {
				continue
			}
			mm, cmd := m.Update(c())
			self, ok := mm.(*model)
			if !ok {
				return mm, cmd
			}
			m = self
			if cmd != nil {
				pending = append(pending, cmd)
			}
		}
		return m, tea.Batch(pending...)
	case hbTickMsg:
		if m.selectedID == "" || m.busy != "" {
			return m, m.armHB()
		}
		return m, tea.Batch(testCmd(m.app, m.selectedID), m.armHB())
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
		return m, tea.Batch(tablesCmd(m.app, msg.id, m.schemas[0].Name), m.armHB())
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
			m.resRow, m.resCol, m.cellFull = 0, 0, false
		}
		m.view = viewQuery
		return m, nil
	case browseMsg:
		m.busy = ""
		if msg.err != nil {
			m.brRes = nil
			m.brErr = msg.err
			m.status = "浏览失败: " + msg.err.Error()
		} else {
			m.brRes = msg.res
			m.brErr = nil
			m.brOffset = msg.offset
			m.brRow, m.brCol, m.brFull = 0, 0, false
		}
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
	case kvScanMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "打开连接或扫描失败: " + msg.err.Error()
			return m, nil
		}
		if msg.reset {
			m.kvKeys = msg.page.Keys
		} else {
			m.kvKeys = append(m.kvKeys, msg.page.Keys...)
		}
		m.kvPageCur = msg.page.Cursor
		m.kvPattern = msg.pattern
		if len(m.kvKeys) == 0 {
			m.kvCur = 0
		}
		if m.kvCur >= len(m.kvKeys) {
			m.kvCur = len(m.kvKeys) - 1
		}
		m.kvValueKey = ""
		m.kvValueErr = nil
		m.kvFilter.SetValue(msg.pattern)
		m.kvFilterOn = false
		m.kvFilter.Blur()
		m.view = viewKV
		return m, nil
	case kvValueMsg:
		m.busy = ""
		if msg.err != nil {
			m.kvValueErr = msg.err
			m.kvValue = protocol.RedisValue{}
		} else {
			m.kvValueErr = nil
			m.kvValue = msg.val
			m.kvValueKey = msg.key
		}
		return m, nil
	case kvExecMsg:
		m.busy = ""
		m.kvCmdOn = false
		m.kvCmd.Blur()
		if msg.err != nil {
			m.kvReply = nil
			m.status = "命令失败: " + msg.err.Error()
		} else {
			m.kvReply = msg.reply
		}
		return m, nil
	case kvDBMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "切换 db 失败: " + msg.err.Error()
			return m, nil
		}
		m.kvDB = msg.index
		m.kvValueKey = ""
		m.kvValueErr = nil
		m.kvReply = nil
		m.busy = "扫描键 …"
		return m, scanCmd(m.app, m.selectedID, m.kvPattern, 0, true)
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
	case viewKV:
		return m.kvKey(msg)
	case viewResults:
		return m.resultsKey(msg)
	case viewBrowse:
		return m.browseKey(msg)
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
			if c.Kind == protocol.DatabaseKindRedis {
				return m, kvOpenCmd(m.app, c.ID)
			}
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
	case tea.KeyF7:
		return m.startBrowse()
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
	case tea.KeyF7:
		return m.startBrowse()
	}
	return m, nil
}

// ─── 查询视图 ────────────────────────────────────────────────

func (m *model) gotoQuery(prefill string) (tea.Model, tea.Cmd) {
	m.queryInput.SetValue(prefill)
	m.querySQL = prefill
	m.queryResult = nil
	m.queryErr = nil
	m.resRow, m.resCol, m.cellFull = 0, 0, false
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
	case tea.KeyF6:
		if m.queryResult != nil && len(m.queryResult.Columns) > 0 {
			m.resRow, m.resCol, m.cellFull = 0, 0, false
			m.queryInput.Blur()
			m.view = viewResults
		} else {
			m.status = "无结果集可导航（先 F5 执行查询）"
		}
		return m, nil
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

// ─── 结果单元格导航（viewResults）────────────────────────────

func (m *model) resultsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.queryResult
	if r == nil || len(r.Columns) == 0 {
		m.view = viewQuery
		m.queryInput.Focus()
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.cellFull {
			m.cellFull = false
			return m, nil
		}
		m.view = viewQuery
		m.queryInput.Focus()
		return m, nil
	case tea.KeyEnter:
		m.cellFull = true
	case tea.KeyUp:
		if m.resRow > 0 {
			m.resRow--
		}
	case tea.KeyDown:
		if m.resRow < len(r.Rows)-1 {
			m.resRow++
		}
	case tea.KeyLeft:
		if m.resCol > 0 {
			m.resCol--
		}
	case tea.KeyRight:
		if m.resCol < len(r.Columns)-1 {
			m.resCol++
		}
	}
	return m, nil
}

// resultsView 渲染带光标高亮的结果表格 + 可选的完整值面板（JSON 自动美化）。
func (m *model) resultsView() string {
	r := m.queryResult
	var b strings.Builder
	maxRows := max(3, m.height-20)
	start := 0
	if m.resRow >= maxRows {
		start = m.resRow - maxRows/2
	}
	if start+maxRows > len(r.Rows) {
		start = max(0, len(r.Rows)-maxRows)
	}
	col := r.Columns[m.resCol]
	b.WriteString(fmt.Sprintf("行 %d/%d · 列 %s (%s)\n", m.resRow+1, len(r.Rows), col.Name, col.DataType))
	b.WriteString(renderTable(r.Columns, r.Rows, max(m.width-4, 20), maxRows, start, m.resRow, m.resCol))
	b.WriteString("\n")
	if m.cellFull {
		var v protocol.Value
		if m.resRow < len(r.Rows) && m.resCol < len(r.Rows[m.resRow]) {
			v = r.Rows[m.resRow][m.resCol]
		}
		text := cellFullText(v)
		lines := strings.Split(text, "\n")
		capN := max(3, m.height-28)
		if len(lines) > capN {
			lines = append(lines[:capN], "…（内容过长，已截断显示）")
		}
		b.WriteString("\n── 完整值 ─────────────────────────\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	return b.String()
}

// ─── 数据浏览（viewBrowse，F7）────────────────────────────────

// startBrowse 对当前表发起服务端分页浏览（第一页）。
func (m *model) startBrowse() (tea.Model, tea.Cmd) {
	if m.selectedID == "" || len(m.tables) == 0 {
		m.status = "无表可浏览"
		return m, nil
	}
	m.brSchema = m.schemas[m.schemaIdx].Name
	m.brTable = m.tables[m.tableCur].Name
	m.brOffset = 0
	m.brLimit = 200
	m.brRes = nil
	m.brErr = nil
	m.brRow, m.brCol, m.brFull = 0, 0, false
	m.view = viewBrowse
	m.busy = "加载数据 …"
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.brOffset, m.brLimit)
}

// browsePage 拉取上一页/下一页（dir<0 回退，>0 前进，首页不回退）。
func (m *model) browsePage(dir int) (tea.Model, tea.Cmd) {
	var off uint64
	if dir > 0 {
		off = m.brOffset + uint64(m.brLimit)
	} else {
		if m.brOffset == 0 {
			return m, nil
		}
		if m.brOffset < uint64(m.brLimit) {
			off = 0
		} else {
			off = m.brOffset - uint64(m.brLimit)
		}
	}
	m.busy = "加载数据 …"
	m.brRow, m.brCol, m.brFull = 0, 0, false
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, off, m.brLimit)
}

// browseKey 数据浏览键位：↑/↓ 行 · ←/→ 翻页 · Shift←/→ 列 · Enter 完整值 · r 刷新 · Esc 返回。
func (m *model) browseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.brRes
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.brFull {
			m.brFull = false
			return m, nil
		}
		if m.tableDetail != nil {
			m.view = viewTable
		} else {
			m.view = viewTables
		}
		return m, nil
	case tea.KeyEnter:
		m.brFull = true
		return m, nil
	case tea.KeyUp:
		if m.brRow > 0 {
			m.brRow--
		}
	case tea.KeyDown:
		if r != nil && m.brRow < len(r.Rows)-1 {
			m.brRow++
		}
	case tea.KeyLeft:
		return m.browsePage(-1)
	case tea.KeyRight:
		if r != nil && r.HasMore {
			return m.browsePage(1)
		}
	case tea.KeyShiftLeft:
		if m.brCol > 0 {
			m.brCol--
		}
	case tea.KeyShiftRight:
		if r != nil && m.brCol < len(r.Columns)-1 {
			m.brCol++
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if m.brRow > 0 {
				m.brRow--
			}
		case "j":
			if r != nil && m.brRow < len(r.Rows)-1 {
				m.brRow++
			}
		case "h":
			return m.browsePage(-1)
		case "l":
			if r != nil && r.HasMore {
				return m.browsePage(1)
			}
		case "r":
			m.busy = "加载数据 …"
			return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.brOffset, m.brLimit)
		}
	}
	return m, nil
}

// browseView 渲染当前页数据表格（带行/列光标）+ 可选完整值面板。
func (m *model) browseView() string {
	var b strings.Builder
	head := fmt.Sprintf("浏览 %s.%s", m.brSchema, m.brTable)
	if m.brErr != nil {
		return head + "\n浏览失败: " + m.brErr.Error()
	}
	r := m.brRes
	if r == nil {
		return head + "\n（无数据）"
	}
	maxRows := max(3, m.height-20)
	start := 0
	if m.brRow >= maxRows {
		start = m.brRow - maxRows/2
	}
	if start+maxRows > len(r.Rows) {
		start = max(0, len(r.Rows)-maxRows)
	}
	hdr := fmt.Sprintf("%s · 行 %d–%d", head, m.brOffset+1, m.brOffset+uint64(len(r.Rows)))
	if r.TotalEstimate != nil {
		hdr += fmt.Sprintf(" / 共约 %d", *r.TotalEstimate)
	}
	if r.HasMore {
		hdr += " · 有下一页"
	}
	b.WriteString(hdr + "\n")
	b.WriteString(renderTable(r.Columns, r.Rows, max(m.width-4, 20), maxRows, start, m.brRow, m.brCol))
	if m.brFull && len(r.Columns) > 0 && m.brRow < len(r.Rows) {
		col := min(m.brCol, len(r.Columns)-1)
		var v protocol.Value
		if col < len(r.Rows[m.brRow]) {
			v = r.Rows[m.brRow][col]
		}
		text := cellFullText(v)
		lines := strings.Split(text, "\n")
		capN := max(3, m.height-28)
		if len(lines) > capN {
			lines = append(lines[:capN], "…（内容过长，已截断显示）")
		}
		b.WriteString(fmt.Sprintf("\n── 完整值 · 列 %s (%s) ─────────\n", r.Columns[col].Name, r.Columns[col].DataType))
		b.WriteString(strings.Join(lines, "\n"))
	}
	return b.String()
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

// ─── Redis KV 视图 ──────────────────────────────────────────

func (m *model) kvKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 输入子模式：/ 键模式过滤、c 命令输入，其余键路由给焦点输入框。
	if m.kvFilterOn {
		var cmd tea.Cmd
		m.kvFilter, cmd = m.kvFilter.Update(msg)
		switch msg.Type {
		case tea.KeyEnter:
			pat := strings.TrimSpace(m.kvFilter.Value())
			if pat == "" {
				pat = "*"
			}
			m.kvFilterOn = false
			m.kvFilter.Blur()
			m.kvValueKey = ""
			m.kvValueErr = nil
			m.busy = "扫描键 …"
			return m, scanCmd(m.app, m.selectedID, pat, 0, true)
		case tea.KeyEsc:
			m.kvFilterOn = false
			m.kvFilter.Blur()
			return m, nil
		}
		return m, cmd
	}
	if m.kvCmdOn {
		var cmd tea.Cmd
		m.kvCmd, cmd = m.kvCmd.Update(msg)
		switch msg.Type {
		case tea.KeyEnter:
			return m.kvRunCommand()
		case tea.KeyEsc:
			m.kvCmdOn = false
			m.kvCmd.Blur()
			return m, nil
		}
		return m, cmd
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyBackspace:
		m.view = viewConns
		m.status = ""
		return m, nil
	case tea.KeyUp:
		if m.kvCur > 0 {
			m.kvCur--
		}
	case tea.KeyDown:
		if m.kvCur < len(m.kvKeys)-1 {
			m.kvCur++
		}
	case tea.KeyEnter:
		if len(m.kvKeys) > 0 {
			ki := m.kvKeys[m.kvCur]
			m.kvValueKey = ki.Key
			m.kvValue = protocol.RedisValue{}
			m.kvValueErr = nil
			m.busy = "加载值 …"
			return m, kvValueCmd(m.app, m.selectedID, ki.Key)
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if m.kvCur > 0 {
				m.kvCur--
			}
		case "j":
			if m.kvCur < len(m.kvKeys)-1 {
				m.kvCur++
			}
		case "/":
			m.kvFilter.SetValue(m.kvPattern)
			m.kvFilter.CursorEnd()
			m.kvFilter.Focus()
			m.kvFilterOn = true
			return m, nil
		case "r":
			m.kvValueKey = ""
			m.kvValueErr = nil
			m.kvReply = nil
			m.busy = "刷新 …"
			return m, scanCmd(m.app, m.selectedID, m.kvPattern, 0, true)
		case "g":
			if m.kvPageCur != 0 {
				m.busy = "加载下一页 …"
				return m, scanCmd(m.app, m.selectedID, m.kvPattern, m.kvPageCur, false)
			}
		case "b":
			next := (m.kvDB + 1) % 16
			m.busy = "切换 db " + fmt.Sprint(next) + " …"
			return m, kvSelectDbCmd(m.app, m.selectedID, next)
		case "c":
			m.kvCmd.Focus()
			m.kvCmdOn = true
			return m, nil
		case "q":
			m.view = viewConns
			m.status = ""
			return m, nil
		}
	}
	return m, nil
}

// kvRunCommand 解析命令并下发 ExecCommand（引号拆分与 Web 端 splitArgs 一致）。
func (m *model) kvRunCommand() (tea.Model, tea.Cmd) {
	raw := strings.TrimSpace(m.kvCmd.Value())
	if raw == "" {
		m.status = "命令为空"
		return m, nil
	}
	args := splitArgs(raw)
	m.kvCmd.SetValue("")
	m.kvReply = nil
	m.busy = "执行中 …"
	return m, kvExecCmd(m.app, m.selectedID, args)
}

func (m *model) kvView() string {
	var b strings.Builder
	header := fmt.Sprintf("db %d · 模式 %s", m.kvDB, m.kvPattern)
	if m.kvPageCur != 0 {
		header += " · 有剩余键（g）"
	}
	b.WriteString(header + "\n")
	if len(m.kvKeys) == 0 {
		if m.busy != "" {
			b.WriteString("加载中 …\n")
		} else {
			b.WriteString("（无键，按 / 输入模式或 c 执行命令）\n")
		}
	}
	maxRows := max(5, m.height-16)
	start := min(max(m.kvCur-maxRows/2, 0), max(len(m.kvKeys)-maxRows, 0))
	end := min(start+maxRows, len(m.kvKeys))
	for i := start; i < end; i++ {
		ki := m.kvKeys[i]
		marker := "  "
		if i == m.kvCur {
			marker = "▸ "
		}
		line := marker + truncate(ki.Key, 36) + "  [" + string(ki.Type) + "]"
		if ki.TTL != nil && *ki.TTL > 0 {
			line += fmt.Sprintf("  ttl:%d", *ki.TTL)
		}
		if i == m.kvCur {
			line = "\x1b[7m" + line + "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	if len(m.kvKeys) > maxRows {
		fmt.Fprintf(&b, "… 显示 %d-%d / %d 键\n", start+1, end, len(m.kvKeys))
	}
	if m.kvValueKey != "" {
		b.WriteString("\n")
		if m.kvValueErr != nil {
			b.WriteString("值加载失败: \x1b[31m" + m.kvValueErr.Error() + "\x1b[0m\n")
		} else {
			b.WriteString("键 " + truncate(m.kvValueKey, 40) + " (" + string(m.kvValue.Type) + "):\n")
			b.WriteString(kvValueText(m.kvValue, max(3, m.height-24)) + "\n")
		}
	}
	b.WriteString("\n")
	if m.kvReply != nil {
		b.WriteString("上次回复: " + redisReplyText(m.kvReply) + "\n")
	}
	if m.kvCmdOn {
		b.WriteString("> " + m.kvCmd.View() + "\n")
	} else {
		b.WriteString("> 按 c 输入 Redis 命令\n")
	}
	return b.String()
}

// kvValueText 按类型渲染键值（与 Web 端 ValueView 对齐；超行数截断）。
func kvValueText(v protocol.RedisValue, maxLines int) string {
	var lines []string
	switch v.Type {
	case protocol.RedisKeyTypeString, protocol.RedisKeyTypeStream:
		lines = append(lines, strings.Split(fmt.Sprint(v.Value), "\n")...)
	case protocol.RedisKeyTypeList, protocol.RedisKeyTypeSet:
		items, _ := v.Value.([]string)
		if len(items) == 0 {
			lines = []string{"（空）"}
		}
		for i, it := range items {
			lines = append(lines, fmt.Sprintf("%d) %s", i+1, it))
		}
	case protocol.RedisKeyTypeZSet:
		items, _ := v.Value.([]protocol.RedisZSetMember)
		for _, mem := range items {
			lines = append(lines, fmt.Sprintf("%s  (%.6g)", mem.Member, mem.Score))
		}
	case protocol.RedisKeyTypeHash:
		mm, _ := v.Value.(map[string]string)
		for k, val := range mm {
			lines = append(lines, k+" = "+val)
		}
	default:
		lines = []string{"(nil)"}
	}
	for i, l := range lines {
		if i >= maxLines {
			total := len(lines)
			lines = lines[:maxLines]
			lines = append(lines, fmt.Sprintf("… 仅显示前 %d / %d 项", maxLines, total))
			break
		}
		lines[i] = "  " + truncate(l, 60)
	}
	return strings.Join(lines, "\n")
}

// redisReplyText 渲染命令回复（与 Web 端 ReplyView 对齐）。
func redisReplyText(r *protocol.RedisReply) string {
	switch r.Type {
	case protocol.RedisReplyNull:
		return "(nil)"
	case protocol.RedisReplyInteger:
		return fmt.Sprint(r.Value)
	case protocol.RedisReplyError:
		return "ERR " + fmt.Sprint(r.Value)
	case protocol.RedisReplySimpleString:
		return fmt.Sprint(r.Value)
	case protocol.RedisReplyBulkString:
		return fmt.Sprintf("%q", fmt.Sprint(r.Value))
	case protocol.RedisReplyArray:
		sub, _ := r.Value.([]protocol.RedisReply)
		return redisReplyArrayText(sub, 0)
	}
	return "?"
}

func redisReplyArrayText(items []protocol.RedisReply, depth int) string {
	if len(items) == 0 {
		return "(空数组)"
	}
	indent := strings.Repeat("  ", depth+1)
	var b strings.Builder
	for i, it := range items {
		if i >= 32 {
			fmt.Fprintf(&b, "%s…\n", indent)
			break
		}
		switch it.Type {
		case protocol.RedisReplyArray:
			sub, _ := it.Value.([]protocol.RedisReply)
			fmt.Fprintf(&b, "%s%d) [array %d]\n", indent, i+1, len(sub))
			b.WriteString(redisReplyArrayText(sub, depth+1))
		case protocol.RedisReplyNull:
			fmt.Fprintf(&b, "%s%d) (nil)\n", indent, i+1)
		case protocol.RedisReplyError:
			fmt.Fprintf(&b, "%s%d) ERR %v\n", indent, i+1, it.Value)
		default:
			fmt.Fprintf(&b, "%s%d) %v\n", indent, i+1, it.Value)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitArgs 把命令行拆为参数（支持单双引号与反斜杠转义），与 Web 端 splitArgs 行为一致。
func splitArgs(input string) []string {
	var args []string
	cur := ""
	quote := byte(0)
	for i := 0; i < len(input); i++ {
		ch := input[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else if ch == '\\' && i+1 < len(input) && (input[i+1] == quote || input[i+1] == '\\') {
				cur += string(input[i+1])
				i++
			} else {
				cur += string(ch)
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == ' ' || ch == '\t':
			if cur != "" {
				args = append(args, cur)
				cur = ""
			}
		default:
			cur += string(ch)
		}
	}
	if cur != "" {
		args = append(args, cur)
	}
	return args
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
	case viewKV:
		body = m.kvView()
	case viewResults:
		body = m.resultsView()
	case viewBrowse:
		body = m.browseView()
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
		b.WriteString(colorStatus(m.status))
	} else {
		b.WriteString(m.footer())
	}
	return b.String()
}

// colorStatus 状态行着色：含「失败」标红（与 Rust GUI status_color 语义一致）。
func colorStatus(s string) string {
	if strings.Contains(s, "失败") {
		return "\x1b[31m" + s + "\x1b[0m"
	}
	return s
}

func (m *model) footer() string {
	switch m.view {
	case viewConns:
		return "↑/↓ 选择 · Enter 打开 · n 新建 · t 测试 · d 删除 · r 刷新 · q 退出"
	case viewForm:
		return "Tab 切换字段 · ←/→ 切换类型 · Enter 提交 · Esc 返回"
	case viewTables:
		return "↑/↓ 选择 · Enter 详情 · s 切换 schema · q 查询 · F7 浏览数据 · Esc 返回"
	case viewTable:
		return "↑/↓ 切换表 · q 查询该表 · F7 浏览数据 · Esc 返回"
	case viewQuery:
		if len(m.acCandidates) > 0 {
			return "↑/↓ 选择 · Enter 接受 · Esc 关闭 · F5 执行"
		}
		return "Tab 补全表/列 · F5 / Ctrl+E 执行 · F6 导航结果 · Esc 返回"
	case viewKV:
		if m.kvFilterOn {
			return "输入模式 · Enter 应用 · Esc 取消"
		}
		if m.kvCmdOn {
			return "输入命令 · Enter 执行 · Esc 返回列表"
		}
		return "↑/↓ 键 · Enter 值 · / 过滤 · g 更多 · b 换db · c 命令 · r 刷新 · Esc 返回"
	case viewResults:
		if m.cellFull {
			return "Enter 刷新全值 · Esc 关闭全值（再按返回查询）"
		}
		return "↑/↓/←/→ 移动 · Enter 完整值（JSON 自动美化）· Esc 返回查询"
	case viewBrowse:
		if m.brFull {
			return "Enter 刷新全值 · Esc 关闭全值（再按返回列表）"
		}
		return "↑/↓ 行 · ←/→ 翻页 · Shift←/→ 列 · Enter 完整值 · r 刷新 · Esc 返回"
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
		if c.ReadOnly != nil && *c.ReadOnly {
			line += " · 只读"
		}
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
