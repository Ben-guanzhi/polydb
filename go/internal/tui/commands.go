package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

// 各类异步结果消息（bubbletea 消息，避免阻塞渲染循环）。
type (
	connsLoadedMsg struct {
		conns []protocol.ConnectionInfo
		err   error
	}
	statusMsg struct {
		id  string
		st  *protocol.ConnectionStatus
		err error
	}
	createdMsg struct {
		conn protocol.ConnectionInfo
		err  error
	}
	deletedMsg struct {
		err error
	}
	schemasMsg struct {
		id      string
		schemas []protocol.SchemaInfo
		err     error
	}
	tablesMsg struct {
		schema string
		tables []protocol.TableInfo
		err    error
	}
	// allTablesMsg 承载跨 schema 汇总的全部表（Quick-open 模糊搜表用）。
	allTablesMsg struct {
		items []quickItem
		err   error
	}
	detailMsg struct {
		detail *tableDetail
		err    error
	}
	queryMsg struct {
		res     *protocol.QueryResult
		err     error
		elapsed time.Duration
	}
	browseMsg struct {
		schema string
		table  string
		offset uint64
		res    *protocol.TableRowsResult
		err    error
	}
	browseCountMsg struct {
		count uint64
		err   error
	}
	// browseColsMsg 承载浏览表的列元数据（T3：主键/只读判定）。
	browseColsMsg struct {
		schema  string
		table   string
		columns []protocol.ColumnInfo
		err     error
	}
	// applyResultMsg 承载一次事务化提交的结果（T3）。
	applyResultMsg struct {
		applied int
		err     error
	}
	columnsLoadedMsg struct {
		table   string
		columns []protocol.ColumnInfo
		err     error
	}
	kvScanMsg struct {
		pattern string
		page    *protocol.RedisScanPage
		reset   bool
		err     error
	}
	kvValueMsg struct {
		key string
		val protocol.RedisValue
		err error
	}
	kvExecMsg struct {
		reply *protocol.RedisReply
		err   error
	}
	kvDBMsg struct {
		index int
		err   error
	}
	hbTickMsg struct{} // 30s 心跳定时：ping 当前连接
)

// tableDetail 聚合一张表的列、索引、外键与 DDL。
type tableDetail struct {
	columns []protocol.ColumnInfo
	indexes []protocol.IndexInfo
	fks     []protocol.ForeignKeyInfo
	ddl     string
}

// quickItem Quick-open 候选：一条 schema.table（跨 schema 汇总后的最小单元）。
type quickItem struct {
	schema string
	table  string
}

// label 返回候选展示文本 schema.table。
func (q quickItem) label() string { return q.schema + "." + q.table }

func loadConnsCmd(a transport.Client) tea.Cmd {
	return func() tea.Msg {
		conns, err := a.ListConnections()
		return connsLoadedMsg{conns: conns, err: err}
	}
}

func testCmd(a transport.Client, id string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		err := a.Ping(context.Background(), id)
		st := &protocol.ConnectionStatus{ID: id, Connected: err == nil}
		if err != nil {
			st.Error = err.Error()
			return statusMsg{id: id, st: st}
		}
		st.LatencyMs = float64(time.Since(start).Microseconds()) / 1000.0
		return statusMsg{id: id, st: st}
	}
}

// heartbeatCmd 心跳：定期 ping 当前活动连接（状态行显示存活/延迟）。
func heartbeatCmd() tea.Cmd {
	return tea.Tick(heartbeatEvery, func(time.Time) tea.Msg { return hbTickMsg{} })
}

func createCmd(a transport.Client, req *protocol.CreateConnectionRequest) tea.Cmd {
	return func() tea.Msg {
		conn, err := a.CreateConnection(req)
		return createdMsg{conn: conn, err: err}
	}
}

func deleteCmd(a transport.Client, id string) tea.Cmd {
	return func() tea.Msg {
		_, err := a.DeleteConnection(id)
		return deletedMsg{err: err}
	}
}

// openConnCmd 打开连接并读取 schema 列表（进入库表浏览的前置步骤）。
// 已连接时跳过 Connect：重复 Connect 会重开驱动，:memory: 等会话数据会丢失。
func openConnCmd(a transport.Client, id string) tea.Cmd {
	return func() tea.Msg {
		if !a.IsConnected(id) {
			if err := a.Connect(context.Background(), id); err != nil {
				return schemasMsg{id: id, err: err}
			}
		}
		schemas, err := a.ListSchemas(context.Background(), id)
		if err != nil {
			return schemasMsg{id: id, err: err}
		}
		if len(schemas) == 0 {
			schemas = []protocol.SchemaInfo{{Name: "main"}}
		}
		return schemasMsg{id: id, schemas: schemas}
	}
}

func tablesCmd(a transport.Client, id, schema string) tea.Cmd {
	return func() tea.Msg {
		tables, err := a.ListTables(context.Background(), id, schema)
		return tablesMsg{schema: schema, tables: tables, err: err}
	}
}

// allTablesCmd 汇总各 schema 的全部表，供 Quick-open 模糊搜表（跨 schema，含未浏览到的）。
// 逐 schema 累积，任一 schema 失败即中止返回已收集部分前的错误。
func allTablesCmd(a transport.Client, id string, schemas []protocol.SchemaInfo) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var items []quickItem
		for _, s := range schemas {
			tables, err := a.ListTables(ctx, id, s.Name)
			if err != nil {
				return allTablesMsg{err: err}
			}
			for _, t := range tables {
				items = append(items, quickItem{schema: s.Name, table: t.Name})
			}
		}
		return allTablesMsg{items: items}
	}
}

func detailCmd(a transport.Client, id, schema, table string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		d := &tableDetail{}
		var err error
		if d.columns, err = a.ListColumns(ctx, id, schema, table); err != nil {
			return detailMsg{err: err}
		}
		if d.indexes, err = a.ListIndexes(ctx, id, schema, table); err != nil {
			return detailMsg{err: err}
		}
		if d.fks, err = a.ListForeignKeys(ctx, id, schema, table); err != nil {
			return detailMsg{err: err}
		}
		if d.ddl, err = a.CreateTableSQL(ctx, id, schema, table); err != nil {
			return detailMsg{err: err}
		}
		return detailMsg{detail: d}
	}
}

func columnsCmd(a transport.Client, id, schema, table string) tea.Cmd {
	return func() tea.Msg {
		cols, err := a.ListColumns(context.Background(), id, schema, table)
		return columnsLoadedMsg{table: table, columns: cols, err: err}
	}
}

func queryCmd(a transport.Client, id, sql string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		res, err := a.Execute(context.Background(), id, sql)
		return queryMsg{res: res, err: err, elapsed: time.Since(start)}
	}
}

// browseCmd 拉取一页表数据（服务端分页；M11 数据浏览）。req 携带过滤/排序/分页。
func browseCmd(a transport.Client, id, schema, table string, req *protocol.TableRowsRequest) tea.Cmd {
	return func() tea.Msg {
		res, err := a.BrowseRows(context.Background(), id, schema, table, req)
		return browseMsg{schema: schema, table: table, offset: req.Offset, res: res, err: err}
	}
}

// browseCountCmd 对同过滤条件执行精确 COUNT(*)（T2.4）。
func browseCountCmd(a transport.Client, id, schema, table string, req *protocol.TableRowsRequest) tea.Cmd {
	return func() tea.Msg {
		n, err := a.BrowseRowsCount(context.Background(), id, schema, table, req)
		return browseCountMsg{count: n, err: err}
	}
}

// browseColsCmd 拉取浏览表的列元数据（T3：推导主键用于编辑定位）。
func browseColsCmd(a transport.Client, id, schema, table string) tea.Cmd {
	return func() tea.Msg {
		cols, err := a.ListColumns(context.Background(), id, schema, table)
		return browseColsMsg{schema: schema, table: table, columns: cols, err: err}
	}
}

// applyChangesCmd 在一个事务内顺序提交变更，逐条校验影响行数；
// 任一条出错或行数不符即回滚并返回错误（spec/behavior.md §10 单次 finalize）。
func applyChangesCmd(a transport.Client, id, schema, table string, pk []string, kind protocol.DatabaseKind, chs []changes.RowChange) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		stmts := changes.BuildStatements(chs, schema, table, pk, kind)
		tx, err := a.BeginTransaction(ctx, id)
		if err != nil {
			return applyResultMsg{err: err}
		}
		for i, s := range stmts {
			res, err := a.ExecuteInTx(ctx, tx.ID, s.ParamSQL, s.Params...)
			if err != nil {
				_ = rollbackQuiet(a, ctx, tx.ID)
				return applyResultMsg{err: err}
			}
			var actual uint64
			if res != nil {
				actual = uint64(res.AffectedRows)
			}
			if changes.RowsMismatch(s, actual) {
				_ = rollbackQuiet(a, ctx, tx.ID)
				return applyResultMsg{err: fmt.Errorf("第 %d 条影响行数 %d 与期望 %d 不符，已回滚", i+1, actual, s.ExpectAffected)}
			}
		}
		if _, err := a.CommitTransaction(ctx, tx.ID); err != nil {
			return applyResultMsg{err: err}
		}
		return applyResultMsg{applied: len(stmts)}
	}
}

func rollbackQuiet(a transport.Client, ctx context.Context, txID string) error {
	_, err := a.RollbackTransaction(ctx, txID)
	return err
}

// ─── Redis KV（M6 前端 Redis 模式）────────────────────────

// kvOpenCmd 打开 Redis 连接并首扫键（Enter 于 redis 连接时触发）。
func kvOpenCmd(a transport.Client, id string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if !a.IsConnected(id) {
			if err := a.Connect(ctx, id); err != nil {
				return kvScanMsg{err: err}
			}
		}
		page, err := a.ScanKeys(ctx, id, 0, "*", 200)
		return kvScanMsg{pattern: "*", page: page, reset: true, err: err}
	}
}

// scanCmd 按 pattern/cursor 扫描一页键。reset=true 替换列表，false 追加。
func scanCmd(a transport.Client, id, pattern string, cursor uint64, reset bool) tea.Cmd {
	return func() tea.Msg {
		page, err := a.ScanKeys(context.Background(), id, cursor, pattern, 200)
		return kvScanMsg{pattern: pattern, page: page, reset: reset, err: err}
	}
}

func kvValueCmd(a transport.Client, id, key string) tea.Cmd {
	return func() tea.Msg {
		v, err := a.GetValue(context.Background(), id, key)
		return kvValueMsg{key: key, val: v, err: err}
	}
}

func kvExecCmd(a transport.Client, id string, args []string) tea.Cmd {
	return func() tea.Msg {
		reply, err := a.ExecCommand(context.Background(), id, args)
		return kvExecMsg{reply: &reply, err: err}
	}
}

func kvSelectDbCmd(a transport.Client, id string, index int) tea.Cmd {
	return func() tea.Msg {
		err := a.SelectDB(context.Background(), id, index)
		return kvDBMsg{index: index, err: err}
	}
}
