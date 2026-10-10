package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	bkey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/cmpsync"
	"github.com/polydb/polydb/pkg/explain"
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
	viewQuickOpen
	viewExplain
	viewCompare
)

// 心跳：每 30s ping 当前连接；测试经 TestMain 置 heartbeatDisabled，
// 避免 drive() 跟随自续的 tea.Tick 命令链而阻塞。
const heartbeatEvery = 30 * time.Second

// quickOpenMax 限制 Quick-open 过滤结果的最大渲染条数。
const quickOpenMax = 200

var heartbeatDisabled bool

// armHB 安排下一跳心跳（测试态返回 nil）。
func (m *model) armHB() tea.Cmd {
	if heartbeatDisabled {
		return nil
	}
	return heartbeatCmd()
}

var viewTitles = map[view]string{
	viewConns:     "连接",
	viewForm:      "新建连接",
	viewTables:    "库表结构",
	viewTable:     "表详情",
	viewQuery:     "查询",
	viewKV:        "Redis 键",
	viewResults:   "结果导航",
	viewBrowse:    "数据浏览",
	viewQuickOpen: "搜索表",
	viewExplain:   "执行计划",
	viewCompare:   "结构比对与同步",
}

// title 返回当前视图标题；表单视图按新建/编辑区分，比对向导按段区分。
func (m *model) title() string {
	if m.view == viewForm && m.form.editing {
		return "编辑连接"
	}
	if m.view == viewCompare {
		return viewTitles[m.view] + " · " + m.cmpStageLabel()
	}
	return viewTitles[m.view]
}

type model struct {
	app    transport.Client
	view   view
	width  int
	height int
	quit   bool
	busy   string
	status string

	helpOpen bool // F1 快捷键帮助面板覆盖开关

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

	// 结果表格的列宽测量缓存（按 queryResult 身份复用；resultsView 与 queryView 共用一份，
	// 因为缓存的是「未按 maxWidth 缩放」的列宽，两处 maxWidth/maxRows 不同也能命中）。
	resWidths widthCache

	// 在途查询取消（T4.1）：cancel 非空表示有查询在跑，queryID 供远程 cancel 端点关联，
	// queryCancelled 记录「本轮结果是被 F8 取消的」——驱动取消错误会被包成
	// POLYDB_ERR_QUERY_FAILED 且协议结构不带 error 链，只能靠自身状态判定。
	queryCancel    context.CancelFunc
	queryID        string
	queryCancelled bool

	// 执行计划（T4.4）：F9 用同一条在途通道跑 EXPLAIN，结果在 viewExplain 渲染。
	explainPending bool
	explainRes     *explain.Render
	explainScroll  int
	explainSQL     string

	// 查询历史抽屉（T2.6）：F4 开合，消费本地 JSONL。
	histOpen bool
	hist     []histEntry
	histCur  int

	// 结果单元格导航（viewResults）：光标位置 + 完整值面板开关。
	resRow   int
	resCol   int
	cellFull bool

	// 完整值面板的 §5.2 旁路取回：blobKey 是当前取回目标（引用 UUID），仅当面板
	// 单元格引用与之相等时展示结果/错误；loading/data/err 单槽缓存，换格回落
	//「按 g 取回」，同一目标可 g 重试。
	blobKey     string
	blobLoading bool
	blobData    []byte
	blobErr     string

	// 数据浏览（viewBrowse）：F7 服务端分页浏览表数据。
	brSchema string
	brTable  string
	brOffset uint64
	brLimit  uint32
	brRes    *protocol.TableRowsResult
	brWidths widthCache // 浏览页列宽测量缓存（键＝brRes 指针）
	brErr    error
	brRow    int
	brCol    int
	brFull   bool

	// 浏览过滤/排序（T2）：已提交条件、AND/OR 逻辑、排序；草稿与焦点环。
	brConds   []protocol.FilterCondition
	brLogic   protocol.FilterLogic
	brSort    []protocol.OrderClause
	brDraft   filterDraft
	brVal     textinput.Model // 过滤值输入框（草稿的当前值字段）
	brField   int             // 过滤编辑聚焦字段：0 列 / 1 op / 2 值 / 3 第二值 / 4 大小写
	brFocus   int             // 0=网格，1=过滤栏编辑
	brEditing bool            // 正在过滤栏编辑草稿（/ 进入）

	// 精确计数（T2.4）：异步 COUNT(*) 结果与在途标记。
	brCount   *uint64
	brCountOK bool

	// 变更跟踪编辑（T3）：本地队列 + 主键/方言元数据 + 单元格编辑/Review 态。
	brQueue     *changes.Queue
	brColumns   []protocol.ColumnInfo // 列元数据（含主键标识）
	brPK        []string              // 主键列名（有序）；空则不可编辑
	brKind      protocol.DatabaseKind
	brReadOnly  bool
	brCellEdit  bool                // 正在编辑当前单元格（brVal 承载新值输入）
	brEditCol   string              // 被编辑列名
	brEditRow   int                 // 被编辑行（brRes 内绝对行号）
	brEditOrig  string              // 被编辑单元格原值（仅展示用）
	brReview    bool                // Review 面板：列出待提交语句
	brReviewCur int                 // Review 面板光标
	brStmts     []changes.Statement // Review 面板预览的语句

	// autocomplete 状态：在查询视图中 Ctrl+Space 触发，↑/↓ 选择，Enter/Tab 接受，Esc 关闭。
	acWord       string
	acCandidates []string
	acIdx        int
	acSchema     string // 候选所属 schema（用于列补全上下文）
	acTable      string // 当前候选对应的表（列补全结果）
	acColumns    []protocol.ColumnInfo

	// Quick-open（Ctrl+P 于库表视图）：跨 schema 汇总全部表并即时模糊过滤。
	qoInput          textinput.Model
	qoAll            []quickItem
	qoFiltered       []quickItem
	qoCur            int
	pendingTablePick string // 选定后待 tablesMsg 落地时定位并加载详情的表名

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
	// kvEdit 是 string 键的就地写回编辑子模式（T4.3；其余类型走 kvCmd 命令行）。
	kvEdit    bool
	kvEditBox textinput.Model

	// 结构比对与同步向导（E5.2）：三段自走的会话态，纯逻辑在 pkg/cmpsync。
	// 刻意不落盘：存下来的勾选集下一轮必然陈旧（behavior.md §15.5 第一条守卫）。
	cmpStage       cmpStage
	cmpPrev        view                       // Esc 退出后回到的视图
	cmpBase        protocol.ConnectionInfo    // 基准侧（要被改成期望侧的样子，也是执行侧）
	cmpCandidates  []protocol.ConnectionInfo  // 期望侧候选
	cmpPickCur     int                        // ① 段候选光标
	cmpDestructOpt bool                       // select_destructive：重跑比对让服务端默认勾选破坏性条目
	cmpIgnoreOpt   bool                       // ignore_defaults：忽略两侧默认值差异（跨环境的序列/时间戳噪声）
	cmpEntries     []protocol.SchemaDiffEntry // ② 段计划（服务端原序）
	cmpSel         []string                   // 勾选集
	cmpCur         int                        // ② 段条目光标
	cmpGaps        []cmpsync.GroupGap         // 重建组漏勾提示
	cmpScript      *protocol.DdlScript        // ③ 段脚本
	cmpScroll      int                        // ③ 段脚本滚动
	cmpRun         string                     // "" / forward / rollback：本轮结果来自哪份脚本
	cmpOutcome     *cmpsync.ApplyOutcome      // 最近一次执行结果
	cmpApplied     bool                       // 正向脚本已完整跑成功 ⇒ 才允许代跑回滚
	cmpConfirmOn   bool                       // 回滚强确认输入态
	cmpConfirm     textinput.Model            // 手输基准侧连接名的输入框
}

func New(app transport.Client) *model {
	ta := textarea.New()
	ta.Placeholder = "输入 SQL（F5 执行）…"
	ta.ShowLineNumbers = true
	ta.SetHeight(6)
	// 与 GUI/Web 对齐：补绑 Ctrl+←/→ 词级移动（bubbles 默认仅 alt+←/→）。
	tkm := ta.KeyMap
	tkm.WordBackward = bkey.NewBinding(bkey.WithKeys("alt+left", "alt+b", "ctrl+left"))
	tkm.WordForward = bkey.NewBinding(bkey.WithKeys("alt+right", "alt+f", "ctrl+right"))
	ta.KeyMap = tkm
	filter := textinput.New()
	filter.Placeholder = "键模式（* 或 user:*）"
	km := textinput.New()
	km.Placeholder = "Redis 命令（GET user:1 / EXPIRE user:1 60 …）"
	kve := textinput.New()
	kve.Prompt = "" // 渲染行自带「> 写回 key」前缀，避免双提示符
	kve.Placeholder = "新的 string 值（Enter 写回，Esc 放弃）"
	qo := textinput.New()
	qo.Placeholder = "输入表名过滤（schema.table 模糊匹配）…"
	bv := textinput.New()
	bv.Placeholder = "值（数字/字符串/true-false；in 用逗号分隔）"
	cf := textinput.New()
	cf.Prompt = "> "
	cf.Placeholder = "逐字输入基准侧连接名以确认代跑回滚"
	m := &model{
		app:        app,
		view:       viewConns,
		connStatus: make(map[string]*protocol.ConnectionStatus),
		queryInput: ta,
		kvFilter:   filter,
		kvCmd:      km,
		kvEditBox:  kve,
		qoInput:    qo,
		brVal:      bv,
		cmpConfirm: cf,
		brLogic:    protocol.FilterLogicAnd,
		brQueue:    changes.New(),
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
			// 失败留在表单里，便于就地改；状态栏已给出原因。
			m.status = "保存失败: " + msg.err.Error()
			return m, nil
		}
		m.view = viewConns
		replaced := false
		for i, c := range m.conns {
			if c.ID == msg.conn.ID {
				m.conns[i] = msg.conn
				m.connCursor = i
				replaced = true
				break
			}
		}
		if !replaced {
			m.conns = append(m.conns, msg.conn)
			m.connCursor = len(m.conns) - 1
		}
		if msg.create {
			m.status = "已创建「" + msg.conn.Name + "」"
		} else {
			m.status = "已保存「" + msg.conn.Name + "」"
		}
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
		// Quick-open 落地：把光标定位到刚切换 schema 中的目标表并加载详情。
		if m.pendingTablePick != "" {
			pick := m.pendingTablePick
			m.pendingTablePick = ""
			for i, t := range m.tables {
				if t.Name == pick {
					m.tableCur = i
					m.busy = "加载详情 …"
					return m, detailCmd(m.app, m.selectedID, msg.schema, pick)
				}
			}
		}
		return m, nil
	case allTablesMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "加载全部表失败: " + msg.err.Error()
			m.view = viewTables
			return m, nil
		}
		m.qoAll = msg.items
		m.qoCur = 0
		m.refilterQuickOpen()
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
		cancelled := m.queryCancelled
		if c := m.queryCancel; c != nil {
			c() // 结束即释放 ctx，避免泄漏
		}
		m.queryCancel, m.queryID, m.queryCancelled = nil, "", false
		// EXPLAIN 那一轮不入历史：querySQL 可能还留着上一条失败的语句，
		// 无守卫会让每次 F9 都重复记一条。
		if sql := m.querySQL; sql != "" && !m.explainPending {
			_ = appendHistory(histEntry{Time: time.Now().Unix(), Conn: m.selectedID, SQL: sql})
		}
		if m.explainPending {
			m.explainPending = false
			if msg.err != nil {
				if cancelled {
					m.status = "EXPLAIN 已取消"
				} else {
					m.status = "EXPLAIN 失败: " + msg.err.Error()
				}
				return m, nil
			}
			kind, _ := m.connKindReadOnly()
			r := explain.RenderResult(msg.res, kind)
			m.explainRes, m.explainScroll = &r, 0
			m.status = "执行计划 · " + string(kind)
			m.view = viewExplain
			return m, nil
		}
		if msg.err != nil {
			m.queryErr = msg.err
			m.queryResult = nil
			if cancelled {
				m.status = "查询已取消"
			} else {
				m.status = "查询失败: " + msg.err.Error()
			}
		} else {
			m.queryResult = msg.res
			m.queryErr = nil
			m.querySQL = ""
			m.resRow, m.resCol, m.cellFull = 0, 0, false
		}
		m.view = viewQuery
		return m, nil
	case blobFetchedMsg:
		// 单槽缓存：只接收当前取回目标的结果，过期回包直接丢弃。
		if m.blobKey == msg.id {
			m.blobLoading = false
			m.blobData, m.blobErr = msg.data, ""
			if msg.err != nil {
				m.blobData = nil
				m.blobErr = blobFetchErrText(msg.err)
			}
		}
	case cancelAckMsg:
		if msg.err != nil {
			m.status = "取消请求失败: " + msg.err.Error()
		}
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
	case browseCountMsg:
		if msg.err != nil {
			m.brCountOK = false
			m.status = "计数失败: " + msg.err.Error()
			if m.busy == "计数中 …" {
				m.busy = ""
			}
			return m, nil
		}
		n := msg.count
		m.brCount = &n
		m.brCountOK = true
		m.busy = ""
		return m, nil
	case browseColsMsg:
		if msg.err != nil {
			m.status = "加载列信息失败: " + msg.err.Error()
			return m, nil
		}
		if msg.table != m.brTable || msg.schema != m.brSchema {
			return m, nil
		}
		m.applyBrowseColumns(msg.columns)
		return m, nil
	case applyResultMsg:
		m.busy = ""
		if msg.err != nil {
			m.status = "提交失败: " + msg.err.Error()
			return m, nil
		}
		m.brQueue = changes.New()
		m.brStmts = nil
		m.status = fmt.Sprintf("提交成功：%d 项变更已应用", msg.applied)
		m.brRow, m.brCol, m.brFull = 0, 0, false
		m.busy = "加载数据 …"
		return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(m.brOffset))
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
		// 服务端回显优先于本地记忆值（spec/behavior.md §1.3）。
		if msg.page.DB != nil {
			m.kvDB = int(*msg.page.DB)
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
		m.kvEdit = false // 重新扫描后旧编辑态无意义
		m.kvEditBox.Blur()
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
	case kvSetMsg:
		m.busy = ""
		if msg.err != nil {
			// 失败留在编辑态并保留已输入内容，便于改正重试。
			m.kvEdit = true
			m.kvEditBox.Focus()
			m.status = "写入失败: " + msg.err.Error()
			return m, nil
		}
		m.status = "已写入「" + truncate(msg.key, 30) + "」"
		// 写后回读：驱动静默失败不能让界面显示旧值却报成功。
		m.busy = "回读值 …"
		return m, kvValueCmd(m.app, m.selectedID, msg.key)
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
	case schemaDiffMsg:
		return m.onSchemaDiff(msg)
	case schemaDdlMsg:
		return m.onSchemaDdl(msg)
	case syncApplyMsg:
		return m.onSyncApply(msg)
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
	// F8 取消在途查询：必须在 busy 门禁之前处理，否则执行期间键全部被吞掉。
	if msg.Type == tea.KeyF8 {
		return m.cancelInflight()
	}
	if m.busy != "" {
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		return m, nil
	}
	// F1 快捷键帮助面板：任意键开合，打开时拦截所有键。
	if msg.Type == tea.KeyF1 {
		m.helpOpen = !m.helpOpen
		return m, nil
	}
	if m.helpOpen {
		m.helpOpen = false
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
	case viewQuickOpen:
		return m.quickOpenKey(msg)
	case viewExplain:
		return m.explainKey(msg)
	case viewCompare:
		return m.compareKey(msg)
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
		case "e":
			if len(m.conns) == 0 {
				return m, nil
			}
			m.form = newEditForm(m.conns[m.connCursor])
			m.view = viewForm
			m.pendingDel = ""
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
			return m.submitForm()
		}
	}
	f, submitted := m.form.updateKey(msg)
	m.form = f
	if submitted {
		return m.submitForm()
	}
	return m, nil
}

// submitForm 按表单态分派：新建走 CreateConnection，编辑走 UpdateConnection。
func (m *model) submitForm() (tea.Model, tea.Cmd) {
	if m.form.editing {
		req := m.form.updateRequest()
		if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
			m.status = "名称不能为空"
			return m, nil
		}
		m.busy = "保存中 …"
		return m, updateCmd(m.app, m.form.connID, &req)
	}
	req := m.form.request()
	if strings.TrimSpace(req.Name) == "" {
		m.status = "名称不能为空"
		return m, nil
	}
	m.busy = "创建中 …"
	return m, createCmd(m.app, &req)
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
	case tea.KeyCtrlP:
		return m.openQuickSwitcher()
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
		case "c":
			return m.openCompare()
		}
	}
	return m, nil
}

// ─── Quick-open（Ctrl+P）────────────────────────────────────

// quickFilter 纯函数：按大小写不敏感子串过滤 schema.table 候选（空 query 全返回）。
func quickFilter(items []quickItem, query string) []quickItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var out []quickItem
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.label()), q) {
			out = append(out, it)
		}
	}
	return out
}

// openQuickSwitcher 进入 Quick-open：清空输入、汇总当前连接各 schema 的全部表。
func (m *model) openQuickSwitcher() (tea.Model, tea.Cmd) {
	m.view = viewQuickOpen
	m.qoInput.SetValue("")
	m.qoInput.CursorEnd()
	m.qoInput.Focus()
	m.qoCur = 0
	m.qoFiltered = nil
	m.busy = "加载全部表 …"
	return m, allTablesCmd(m.app, m.selectedID, m.schemas)
}

// refilterQuickOpen 依据当前输入重算过滤列表并夹紧光标下标。
func (m *model) refilterQuickOpen() {
	m.qoFiltered = quickFilter(m.qoAll, m.qoInput.Value())
	if m.qoCur >= len(m.qoFiltered) {
		m.qoCur = 0
	}
	if len(m.qoFiltered) > quickOpenMax {
		m.qoFiltered = m.qoFiltered[:quickOpenMax]
	}
}

// quickOpenKey 处理 Quick-open 视图按键：Esc 返回、↑/↓ 选择、Enter 跳转、其余编辑过滤。
func (m *model) quickOpenKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.qoInput.Blur()
		m.view = viewTables
		return m, nil
	case tea.KeyUp:
		if m.qoCur > 0 {
			m.qoCur--
		}
		return m, nil
	case tea.KeyDown:
		if m.qoCur < len(m.qoFiltered)-1 {
			m.qoCur++
		}
		return m, nil
	case tea.KeyEnter:
		if m.qoCur < len(m.qoFiltered) {
			return m.pickQuickItem(m.qoFiltered[m.qoCur])
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.qoInput, cmd = m.qoInput.Update(msg)
	m.refilterQuickOpen()
	return m, cmd
}

// pickQuickItem 选定候选：定位 schema、拉取该 schema 的表并在落地后打开目标表详情。
func (m *model) pickQuickItem(it quickItem) (tea.Model, tea.Cmd) {
	si := -1
	for i, s := range m.schemas {
		if s.Name == it.schema {
			si = i
			break
		}
	}
	if si < 0 {
		m.qoInput.Blur()
		m.view = viewTables
		return m, nil
	}
	m.schemaIdx = si
	m.pendingTablePick = it.table
	m.qoInput.Blur()
	m.view = viewTables
	m.busy = "加载表 …"
	return m, tablesCmd(m.app, m.selectedID, it.schema)
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
	if m.histOpen {
		return m.historyKey(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyF4:
		return m.openHistory()
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
	case tea.KeyF9:
		return m.runExplain()
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
	case tea.KeyRunes:
		// 完整值面板 · 旁路引用取回（encoding.md §5.2）：g 取回当前单元格大字段。
		if m.cellFull && !m.blobLoading && string(msg.Runes) == "g" &&
			m.resRow < len(r.Rows) && m.resCol < len(r.Rows[m.resRow]) {
			if _, id, ok := blobRefOf(r.Rows[m.resRow][m.resCol]); ok {
				m.blobKey, m.blobLoading, m.blobData, m.blobErr = id, true, nil, ""
				return m, getBlobCmd(m.app, m.selectedID, id)
			}
		}
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
	fmt.Fprintf(&b, "行 %d/%d · 列 %s (%s)\n", m.resRow+1, len(r.Rows), col.Name, col.DataType)
	b.WriteString(renderTableDecoratedCached(&m.resWidths, r, r.Columns, r.Rows, max(m.width-4, 20), maxRows, start, m.resRow, m.resCol, nil))
	b.WriteString("\n")
	if m.cellFull {
		var v protocol.Value
		if m.resRow < len(r.Rows) && m.resCol < len(r.Rows[m.resRow]) {
			v = r.Rows[m.resRow][m.resCol]
		}
		text := cellFullText(v) + m.blobFetchBlock(v)
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

// browseRequest 依据当前过滤/排序/页大小构造分页请求（offset 由翻页决定）。
func (m *model) browseRequest(offset uint64) *protocol.TableRowsRequest {
	return &protocol.TableRowsRequest{
		Conditions: m.brConds,
		Logic:      m.brLogic,
		OrderBy:    m.brSort,
		Offset:     offset,
		Limit:      m.brLimit,
	}
}

// browseCountRequest 构造计数请求：与列表同过滤条件，但不带分页/排序。
func (m *model) browseCountRequest() *protocol.TableRowsRequest {
	return &protocol.TableRowsRequest{
		Conditions: m.brConds,
		Logic:      m.brLogic,
	}
}

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
	m.brConds = nil
	m.brSort = nil
	m.brLogic = protocol.FilterLogicAnd
	m.brFocus, m.brEditing = 0, false
	m.brCount, m.brCountOK = nil, false
	// T3：重置变更跟踪态并按连接补种方言/只读/主键元数据。
	m.brQueue = changes.New()
	m.brColumns = nil
	m.brPK = nil
	m.brKind, m.brReadOnly = m.connKindReadOnly()
	m.brCellEdit, m.brReview = false, false
	m.brStmts = nil
	m.view = viewBrowse
	m.busy = "加载数据 …"
	return m, tea.Batch(
		browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0)),
		browseColsCmd(m.app, m.selectedID, m.brSchema, m.brTable),
	)
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
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(off))
}

// browseKey 数据浏览键位。焦点态分两层：网格态（brEditing=false）与过滤栏编辑态。
// 网格态：↑/↓ 行 · ←/→ 翻页 · Shift←/→ 列 · Enter 完整值 · r 刷新 · c 计数 · s 排序 ·
//
//	/ 或 Tab 进入过滤栏 · d 删除末条过滤 · o 切换 AND/OR · Esc 返回。
func (m *model) browseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.brReview {
		return m.reviewKey(msg)
	}
	if m.brCellEdit {
		return m.browseCellKey(msg)
	}
	if m.brEditing {
		return m.browseFilterKey(msg)
	}
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
	case tea.KeyTab:
		return m.enterFilterEdit()
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
		case "/":
			return m.enterFilterEdit()
		case "r":
			m.busy = "加载数据 …"
			return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(m.brOffset))
		case "c":
			m.brCount, m.brCountOK = nil, false
			m.busy = "计数中 …"
			return m, browseCountCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseCountRequest())
		case "d":
			return m.dropLastFilter()
		case "o":
			return m.toggleFilterLogic()
		case "s":
			return m.toggleSortCurrentColumn()
		case "p":
			return m.cyclePageSize()
		case "g":
			// 完整值面板 · 旁路引用取回（encoding.md §5.2），与结果导航视图同键。
			if m.brFull && !m.blobLoading && r != nil && len(r.Columns) > 0 && m.brRow < len(r.Rows) {
				col := min(m.brCol, len(r.Columns)-1)
				if col >= 0 && col < len(r.Rows[m.brRow]) {
					if _, id, ok := blobRefOf(r.Rows[m.brRow][col]); ok {
						m.blobKey, m.blobLoading, m.blobData, m.blobErr = id, true, nil, ""
						return m, getBlobCmd(m.app, m.selectedID, id)
					}
				}
			}
		case "e":
			return m.enterCellEdit()
		case "u":
			return m.undoBrowseChange()
		case "w":
			return m.openReview()
		}
	}
	return m, nil
}

// currentBrowseColumn 返回光标所在列名（无数据时返回空串）。
func (m *model) currentBrowseColumn() string {
	if m.brRes == nil || m.brCol >= len(m.brRes.Columns) {
		return ""
	}
	return m.brRes.Columns[m.brCol].Name
}

// enterFilterEdit 进入过滤栏编辑态：草稿默认落在当前列、eq、值聚焦。
func (m *model) enterFilterEdit() (tea.Model, tea.Cmd) {
	col := m.currentBrowseColumn()
	if col == "" {
		m.status = "无可过滤列"
		return m, nil
	}
	m.brDraft = filterDraft{Column: col, Op: protocol.FilterOpEq}
	m.brVal.SetValue("")
	m.brVal.CursorEnd()
	m.brVal.Focus()
	m.brField = 2 // 直接进值输入
	m.brEditing = true
	m.brFocus = 1
	return m, nil
}

// browseFilterFields 当前草稿可 Tab 巡览的字段下标：
// 0 列 / 1 op / 2 值 / 3 第二值（仅 between）/ 4 大小写（仅文本匹配类 op）。
func (m *model) browseFilterFields() []int {
	fs := []int{0, 1, 2}
	if m.brDraft.Op == protocol.FilterOpBetween {
		fs = append(fs, 3)
	}
	if opIsTextMatch(m.brDraft.Op) {
		fs = append(fs, 4)
	}
	return fs
}

// browseFieldFocus 在可用字段列表里前后巡览，并在值字段间同步 textinput 内容。
func (m *model) browseFieldFocus(delta int) {
	fs := m.browseFilterFields()
	pos := 0
	for i, f := range fs {
		if f == m.brField {
			pos = i
			break
		}
	}
	next := fs[((pos+delta)%len(fs)+len(fs))%len(fs)]
	switch m.brField {
	case 2:
		m.brDraft.Raw = m.brVal.Value()
	case 3:
		m.brDraft.Raw2 = m.brVal.Value()
	}
	m.brField = next
	switch next {
	case 2:
		m.brVal.SetValue(m.brDraft.Raw)
		m.brVal.CursorEnd()
		m.brVal.Focus()
	case 3:
		m.brVal.SetValue(m.brDraft.Raw2)
		m.brVal.CursorEnd()
		m.brVal.Focus()
	default:
		m.brVal.Blur()
	}
}

// browseFilterKey 过滤栏编辑态键位：Tab/Shift+Tab 切字段 · ←/→ 调整列或操作符 ·
// Enter 应用 · Esc 取消；值字段路由给 textinput。
func (m *model) browseFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.brEditing = false
		m.brFocus = 0
		m.brVal.Blur()
		return m, nil
	case tea.KeyTab:
		m.browseFieldFocus(1)
		return m, nil
	case tea.KeyShiftTab:
		m.browseFieldFocus(-1)
		return m, nil
	case tea.KeyEnter:
		m.brDraft.Raw = m.brVal.Value()
		return m.applyFilterDraft()
	case tea.KeyUp:
		m.cycleBrowseColumn(-1)
		return m, nil
	case tea.KeyDown:
		m.cycleBrowseColumn(1)
		return m, nil
	case tea.KeyLeft:
		switch m.brField {
		case 0:
			m.cycleBrowseColumn(-1)
			return m, nil
		case 1:
			m.cycleOp(-1)
			return m, nil
		case 4:
			m.cycleFilterCase(-1)
			return m, nil
		}
	case tea.KeyRight:
		switch m.brField {
		case 0:
			m.cycleBrowseColumn(1)
			return m, nil
		case 1:
			m.cycleOp(1)
			return m, nil
		case 4:
			m.cycleFilterCase(1)
			return m, nil
		}
	}
	// 值字段（2/3）把其余键交给 textinput。
	if m.brField == 2 || m.brField == 3 {
		dst := &m.brVal
		if m.brField == 3 {
			// 第二值暂存于 Raw2，输入框内容映射到 Raw2。
			m.brDraft.Raw2 = m.brVal.Value()
		}
		var cmd tea.Cmd
		*dst, cmd = dst.Update(msg)
		return m, cmd
	}
	return m, nil
}

// cycleBrowseColumn 在结果列间循环切换草稿列名。
func (m *model) cycleBrowseColumn(dir int) {
	if m.brRes == nil || len(m.brRes.Columns) == 0 {
		return
	}
	cols := m.brRes.Columns
	cur := 0
	for i, c := range cols {
		if c.Name == m.brDraft.Column {
			cur = i
			break
		}
	}
	cur = (cur + dir + len(cols)) % len(cols)
	m.brDraft.Column = cols[cur].Name
}

// cycleOp 在该方言可用的操作符间循环（跳过不支持的组合，如 MSSQL 的 regex）。
func (m *model) cycleOp(dir int) {
	cur := 0
	for i, op := range browseOps {
		if op == m.brDraft.Op {
			cur = i
			break
		}
	}
	n := len(browseOps)
	for step := 1; step <= n; step++ {
		cand := browseOps[((cur+dir*step)%n+n)%n]
		if opSupported(m.brKind, cand) {
			m.brDraft.Op = cand
			return
		}
	}
}

// cycleFilterCase 在大小写三态间循环（仅文本匹配类 op 有此字段）。
func (m *model) cycleFilterCase(dir int) {
	if !opIsTextMatch(m.brDraft.Op) {
		return
	}
	if dir < 0 {
		dir = 2 // 三态里 -1 ≡ +2
	}
	m.brDraft.Case = filterCase(((int(m.brDraft.Case)+dir)%3 + 3) % 3)
}

// applyFilterDraft 把草稿落地为条件、回到首页重查；缺值则留在编辑态提示。
func (m *model) applyFilterDraft() (tea.Model, tea.Cmd) {
	m.brDraft.Raw = m.brVal.Value()
	if !opSupported(m.brKind, m.brDraft.Op) {
		m.status = opUnsupportedHint(m.brKind, m.brDraft.Op)
		return m, nil
	}
	c, ok := draftToCondition(m.brDraft)
	if !ok {
		m.status = "过滤条件缺少值"
		return m, nil
	}
	m.brConds = append(m.brConds, c)
	m.brEditing = false
	m.brFocus = 0
	m.brVal.Blur()
	m.busy = "加载数据 …"
	m.brOffset = 0
	m.brRow, m.brCol, m.brFull = 0, 0, false
	m.brCount, m.brCountOK = nil, false
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0))
}

// dropLastFilter 删除最后一条过滤条件并回首页重查。
func (m *model) dropLastFilter() (tea.Model, tea.Cmd) {
	if len(m.brConds) == 0 {
		m.status = "无过滤条件"
		return m, nil
	}
	m.brConds = m.brConds[:len(m.brConds)-1]
	m.busy = "加载数据 …"
	m.brOffset = 0
	m.brRow, m.brCol, m.brFull = 0, 0, false
	m.brCount, m.brCountOK = nil, false
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0))
}

// toggleFilterLogic 切换 AND/OR 并在有条件时重查。
func (m *model) toggleFilterLogic() (tea.Model, tea.Cmd) {
	if m.brLogic == protocol.FilterLogicOr {
		m.brLogic = protocol.FilterLogicAnd
	} else {
		m.brLogic = protocol.FilterLogicOr
	}
	if len(m.brConds) < 2 {
		return m, nil
	}
	m.busy = "加载数据 …"
	m.brOffset = 0
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0))
}

// toggleSortCurrentColumn 对当前列设/翻排序并回首页重查（T2.3）。
func (m *model) toggleSortCurrentColumn() (tea.Model, tea.Cmd) {
	col := m.currentBrowseColumn()
	if col == "" {
		m.status = "无可排序列"
		return m, nil
	}
	dir := protocol.SortAsc
	if len(m.brSort) == 1 && m.brSort[0].Column == col && m.brSort[0].Dir == protocol.SortAsc {
		dir = protocol.SortDesc
	}
	m.brSort = []protocol.OrderClause{{Column: col, Dir: dir}}
	m.busy = "加载数据 …"
	m.brOffset = 0
	m.brRow, m.brCol, m.brFull = 0, 0, false
	m.brCount, m.brCountOK = nil, false
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0))
}

// browsePageSizes 可选页大小（T2.3），按 p 循环。
var browsePageSizes = []uint32{50, 200, 1000}

// cyclePageSize 在 50/200/1000 间循环页大小并回首页重查。
func (m *model) cyclePageSize() (tea.Model, tea.Cmd) {
	idx := 0
	for i, s := range browsePageSizes {
		if s == m.brLimit {
			idx = i
			break
		}
	}
	m.brLimit = browsePageSizes[(idx+1)%len(browsePageSizes)]
	m.busy = "加载数据 …"
	m.brOffset = 0
	m.brRow, m.brCol, m.brFull = 0, 0, false
	m.brCount, m.brCountOK = nil, false
	return m, browseCmd(m.app, m.selectedID, m.brSchema, m.brTable, m.browseRequest(0))
}

// browseView 渲染当前页数据表格（带行/列光标）+ 可选完整值面板。
func (m *model) browseView() string {
	if m.brReview {
		return m.reviewView()
	}
	var b strings.Builder
	head := fmt.Sprintf("浏览 %s.%s", m.brSchema, m.brTable)
	if m.brErr != nil {
		return head + "\n浏览失败: " + m.brErr.Error()
	}
	r := m.brRes
	if r == nil {
		return head + "\n（无数据）"
	}
	maxRows := max(3, m.height-22)
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
	b.WriteString(m.browseFilterBar() + "\n")
	b.WriteString(m.browseEditBar() + "\n")
	b.WriteString(renderTableDecoratedCached(&m.brWidths, r, r.Columns, r.Rows, max(m.width-4, 20), maxRows, start, m.brRow, m.brCol, m.dirtyDecorator()))
	if m.brFull && len(r.Columns) > 0 && m.brRow < len(r.Rows) {
		col := min(m.brCol, len(r.Columns)-1)
		var v protocol.Value
		if col < len(r.Rows[m.brRow]) {
			v = r.Rows[m.brRow][col]
		}
		text := cellFullText(v) + m.blobFetchBlock(v)
		lines := strings.Split(text, "\n")
		capN := max(3, m.height-28)
		if len(lines) > capN {
			lines = append(lines[:capN], "…（内容过长，已截断显示）")
		}
		fmt.Fprintf(&b, "\n── 完整值 · 列 %s (%s) ─────────\n", r.Columns[col].Name, r.Columns[col].DataType)
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
	return m.runSQL(sql, true)
}

// runSQL 走统一的在途通道执行 sql（预生成 query_id + 可取消 ctx）。
// toHistory=false 用于 EXPLAIN：不把分析语句灌进查询历史。
func (m *model) runSQL(sql string, toHistory bool) (tea.Model, tea.Cmd) {
	qid := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	if toHistory {
		m.querySQL = sql
	}
	m.queryID = qid
	m.queryCancel = cancel
	m.queryCancelled = false
	m.busy = "执行中 …（F8 取消）"
	return m, queryCmd(ctx, m.app, qid, m.selectedID, sql)
}

// runExplain 按当前连接方言把编辑器 SQL 包成 EXPLAIN 语句并执行（F9）。
func (m *model) runExplain() (tea.Model, tea.Cmd) {
	kind, _ := m.connKindReadOnly()
	sql := explain.BuildSQL(m.queryInput.Value(), kind)
	if sql == "" {
		m.status = "无可分析的 SQL"
		return m, nil
	}
	m.explainPending = true
	m.explainSQL = sql
	return m.runSQL(sql, false)
}

// cancelInflight 中断在途查询：本地取消 ctx，远程再打 cancel 端点让服务端中断执行。
// 两端都是 best-effort（behavior.md §2.2：驱动未必能中途打断已开跑的 SQL）。
func (m *model) cancelInflight() (tea.Model, tea.Cmd) {
	if m.queryCancel == nil {
		m.status = "没有在途查询"
		return m, nil
	}
	qid, cancel := m.queryID, m.queryCancel
	m.queryCancel = nil
	m.queryID = ""
	m.queryCancelled = true
	cancel()
	m.status = "已请求取消查询 …"
	return m, cancelQueryCmd(m.app, qid)
}

// ─── Redis KV 视图 ──────────────────────────────────────────

func (m *model) kvKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 输入子模式：e 写回 string 值、/ 键模式过滤、c 命令输入，其余键路由给焦点输入框。
	if m.kvEdit {
		return m.kvEditKey(msg)
	}
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
		case "e":
			return m.kvStartEdit()
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

// kvStartEdit 进入 string 键的就地写回子模式。
// 只有 string 能整值替换：list/hash/set 等需按元素操作，走 c 命令行（RPUSH/HSET …）。
func (m *model) kvStartEdit() (tea.Model, tea.Cmd) {
	if m.kvValueKey == "" || m.kvValueErr != nil {
		m.status = "先用 Enter 加载键值"
		return m, nil
	}
	if _, ro := m.connKindReadOnly(); ro {
		m.status = "连接标记为只读，不能写入"
		return m, nil
	}
	if m.kvValue.Type != protocol.RedisKeyTypeString {
		m.status = fmt.Sprintf("仅 string 支持就地写回，当前类型 %s；请用 c 命令行（RPUSH/HSET …）", m.kvValue.Type)
		return m, nil
	}
	m.kvEditBox.SetValue(fmt.Sprint(m.kvValue.Value))
	m.kvEditBox.CursorEnd()
	m.kvEditBox.Focus()
	m.kvEdit = true
	return m, nil
}

// kvEditKey 处理写回子模式按键：Enter 提交并回读、Esc 放弃，其余给输入框。
func (m *model) kvEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.kvEditBox, cmd = m.kvEditBox.Update(msg)
	switch msg.Type {
	case tea.KeyEnter:
		m.kvEdit = false
		m.kvEditBox.Blur()
		m.busy = "写入 …"
		return m, kvSetCmd(m.app, m.selectedID, m.kvValueKey, m.kvEditBox.Value())
	case tea.KeyEsc:
		m.kvEdit = false
		m.kvEditBox.Blur()
		m.status = "已放弃写入"
		return m, nil
	}
	return m, cmd
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
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if len(m.kvKeys) > maxRows {
		fmt.Fprintf(&b, "… 显示 %d-%d / %d 键\n", start+1, end, len(m.kvKeys))
	}
	if m.kvValueKey != "" {
		b.WriteString("\n")
		if m.kvValueErr != nil {
			b.WriteString("值加载失败: " + failText(m.kvValueErr.Error()) + "\n")
		} else {
			b.WriteString("键 " + truncate(m.kvValueKey, 40) + " (" + string(m.kvValue.Type) + "):\n")
			b.WriteString(kvValueText(m.kvValue, max(3, m.height-24)) + "\n")
		}
	}
	b.WriteString("\n")
	if m.kvReply != nil {
		b.WriteString("上次回复: " + redisReplyText(m.kvReply) + "\n")
	}
	if m.kvEdit {
		b.WriteString("> 写回 " + truncate(m.kvValueKey, 30) + "  " + m.kvEditBox.View() + "  (Enter 提交 / Esc 放弃)\n")
	} else if m.kvCmdOn {
		b.WriteString("> " + m.kvCmd.View() + "\n")
	} else {
		b.WriteString("> 按 c 输入 Redis 命令 · e 写回 string 值\n")
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
	if m.helpOpen {
		return m.helpView()
	}
	if m.histOpen {
		return m.frame(m.historyView())
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
	case viewQuickOpen:
		body = m.quickOpenView()
	case viewExplain:
		body = m.explainView()
	case viewCompare:
		body = m.compareView()
	}
	return m.frame(body)
}

func (m *model) frame(body string) string {
	var b strings.Builder
	title := "polydb TUI · " + m.title()
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
		b.WriteString(m.footer() + " · F1 帮助")
	}
	return b.String()
}

// colorStatus 状态行着色：含「失败」标红（与 Rust GUI status_color 语义一致）。
func colorStatus(s string) string {
	if strings.Contains(s, "失败") {
		return failText(s)
	}
	return s
}

func (m *model) footer() string {
	switch m.view {
	case viewConns:
		return "↑/↓ 选择 · Enter 打开 · n 新建 · e 编辑 · t 测试 · d 删除 · r 刷新 · q 退出"
	case viewForm:
		return "Tab 切换字段 · ←/→ 切换类型 · Enter 提交 · Esc 返回"
	case viewTables:
		return "↑/↓ 选择 · Enter 详情 · s 切换 schema · q 查询 · F7 浏览数据 · Esc 返回"
	case viewTable:
		return "↑/↓ 切换表 · q 查询该表 · F7 浏览数据 · Esc 返回"
	case viewQuery:
		if m.histOpen {
			return "↑/↓ · j/k 选择 · Enter 回填 · Esc / F4 关闭"
		}
		if len(m.acCandidates) > 0 {
			return "↑/↓ 选择 · Enter 接受 · Esc 关闭 · F5 执行"
		}
		return "Tab 补全表/列 · F5 / Ctrl+E 执行 · F9 执行计划 · F8 取消 · F4 历史 · F6 导航结果 · Esc 返回"
	case viewKV:
		if m.kvEdit {
			return "编辑值 · Enter 写回并回读 · Esc 放弃"
		}
		if m.kvFilterOn {
			return "输入模式 · Enter 应用 · Esc 取消"
		}
		if m.kvCmdOn {
			return "输入命令 · Enter 执行 · Esc 返回列表"
		}
		return "↑/↓ 键 · Enter 值 · / 过滤 · g 更多 · b 换db · c 命令 · e 写回 string · r 刷新 · Esc 返回"
	case viewResults:
		if m.cellFull {
			return "Enter 刷新全值 · Esc 关闭全值（再按返回查询）"
		}
		return "↑/↓/←/→ 移动 · Enter 完整值（JSON 自动美化）· Esc 返回查询"
	case viewBrowse:
		if m.brReview {
			return "Enter/y 提交 · Esc/n 返回 · u 撤销末项 · ↑/↓ 查看"
		}
		if m.brCellEdit {
			return "输入新值（NULL 清空）· Enter 暂存 · Esc 取消"
		}
		if m.brEditing {
			return "Tab 切字段 · ←/→ 调整 · Enter 应用 · Esc 取消"
		}
		if m.brFull {
			return "Enter 刷新全值 · Esc 关闭全值（再按返回列表）"
		}
		return "↑/↓ 行 · ←/→ 翻页 · e 编辑 · u 撤销 · w 审阅 · / 过滤 · c 计数 · s 排序 · p 页 · o 且/或 · d 删过滤 · Esc 返回"
	case viewQuickOpen:
		return "↑/↓ 选择 · Enter 打开 · Esc 返回"
	case viewExplain:
		return "↑/↓ · j/k 滚动 · Esc / q 返回查询"
	case viewCompare:
		return m.cmpFooter()
	}
	return ""
}

// listWindow 计算列表视口：给定总行数、光标、可用行数，返回 [start,end)，
// 尽量让光标居中；avail<=0（终端高度未探测）时返回全量，退化为不裁剪。
func listWindow(total, cursor, avail int) (start, end int) {
	if total <= 0 {
		return 0, 0
	}
	if avail <= 0 || total <= avail {
		return 0, total
	}
	start = cursor - avail/2
	if start < 0 {
		start = 0
	}
	if start+avail > total {
		start = total - avail
	}
	return start, start + avail
}

// contentLines 返回可安全用于列表正文的行数（终端高度未探测时返回 0，令裁剪退化为不生效）。
// 预留：标题 1 + 分隔线 1 + 状态/页脚 1 + 视口提示 1 ≈ 4 行。
func (m *model) contentLines() int {
	if m.height <= 4 {
		return 0
	}
	return m.height - 4
}

func (m *model) connsView() string {
	if len(m.conns) == 0 {
		return "暂无连接，按 n 新建。\n"
	}
	start, end := listWindow(len(m.conns), m.connCursor, m.contentLines())
	var b strings.Builder
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 项\n", start)
	}
	for i := start; i < end; i++ {
		c := m.conns[i]
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
				line += "  " + okText("●") + fmt.Sprintf(" %.1f ms", st.LatencyMs)
			} else {
				line += "  " + failText("●") + " 连接失败"
			}
		}
		if m.pendingDel == c.ID {
			line += "  " + warnText("待确认删除")
		}
		if i == m.connCursor {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if end < len(m.conns) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 项\n", len(m.conns)-end)
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
	// 表清单视口：预留 schema 头 1 行 + 提示 2 行。
	start, end := listWindow(len(m.tables), m.tableCur, m.contentLines()-1)
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 张\n", start)
	}
	for i := start; i < end; i++ {
		t := m.tables[i]
		marker := "  "
		if i == m.tableCur {
			marker = "▸ "
		}
		line := marker + t.Name
		if t.Type != "" && t.Type != protocol.TableTypeTable {
			line += "  [" + string(t.Type) + "]"
		}
		if i == m.tableCur {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if end < len(m.tables) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 张\n", len(m.tables)-end)
	}
	return b.String()
}

func (m *model) quickOpenView() string {
	var b strings.Builder
	b.WriteString("🔍 " + m.qoInput.View() + "\n")
	if len(m.qoFiltered) == 0 {
		b.WriteString("（无匹配）\n")
		return b.String()
	}
	total := len(m.qoAll)
	fmt.Fprintf(&b, "匹配 %d / 共 %d 张表\n", len(m.qoFiltered), total)
	// 候选视口：预留输入 1 + 统计 1 + 提示 2 行。
	start, end := listWindow(len(m.qoFiltered), m.qoCur, m.contentLines()-2)
	if start > 0 {
		fmt.Fprintf(&b, "↑ 上方还有 %d 项\n", start)
	}
	for i := start; i < end; i++ {
		it := m.qoFiltered[i]
		marker := "  "
		if i == m.qoCur {
			marker = "▸ "
		}
		line := marker + it.label()
		if i == m.qoCur {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if end < len(m.qoFiltered) {
		fmt.Fprintf(&b, "↓ 下方还有 %d 项\n", len(m.qoFiltered)-end)
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
		b.WriteString(failText(m.queryErr.Error()) + "\n")
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
			table := renderTableDecoratedCached(&m.resWidths, r, r.Columns, r.Rows, max(m.width-4, 20), maxRows, 0, -1, -1, nil)
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
			line = selected(line)
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
