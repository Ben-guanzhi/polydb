package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	detailMsg struct {
		detail *tableDetail
		err    error
	}
	queryMsg struct {
		res     *protocol.QueryResult
		err     error
		elapsed time.Duration
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
)

// tableDetail 聚合一张表的列、索引、外键与 DDL。
type tableDetail struct {
	columns []protocol.ColumnInfo
	indexes []protocol.IndexInfo
	fks     []protocol.ForeignKeyInfo
	ddl     string
}

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
