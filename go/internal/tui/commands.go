package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
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
)

// tableDetail 聚合一张表的列、索引、外键与 DDL。
type tableDetail struct {
	columns []protocol.ColumnInfo
	indexes []protocol.IndexInfo
	fks     []protocol.ForeignKeyInfo
	ddl     string
}

func loadConnsCmd(a *appcore.AppCore) tea.Cmd {
	return func() tea.Msg {
		conns, err := a.ListConnections()
		return connsLoadedMsg{conns: conns, err: err}
	}
}

func testCmd(a *appcore.AppCore, id string) tea.Cmd {
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

func createCmd(a *appcore.AppCore, req *protocol.CreateConnectionRequest) tea.Cmd {
	return func() tea.Msg {
		conn, err := a.CreateConnection(req)
		return createdMsg{conn: conn, err: err}
	}
}

func deleteCmd(a *appcore.AppCore, id string) tea.Cmd {
	return func() tea.Msg {
		_, err := a.DeleteConnection(id)
		return deletedMsg{err: err}
	}
}

// openConnCmd 打开连接并读取 schema 列表（进入库表浏览的前置步骤）。
// 已连接时跳过 Connect：重复 Connect 会重开驱动，:memory: 等会话数据会丢失。
func openConnCmd(a *appcore.AppCore, id string) tea.Cmd {
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

func tablesCmd(a *appcore.AppCore, id, schema string) tea.Cmd {
	return func() tea.Msg {
		tables, err := a.ListTables(context.Background(), id, schema)
		return tablesMsg{schema: schema, tables: tables, err: err}
	}
}

func detailCmd(a *appcore.AppCore, id, schema, table string) tea.Cmd {
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

func columnsCmd(a *appcore.AppCore, id, schema, table string) tea.Cmd {
	return func() tea.Msg {
		cols, err := a.ListColumns(context.Background(), id, schema, table)
		return columnsLoadedMsg{table: table, columns: cols, err: err}
	}
}

func queryCmd(a *appcore.AppCore, id, sql string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		res, err := a.Execute(context.Background(), id, sql)
		return queryMsg{res: res, err: err, elapsed: time.Since(start)}
	}
}
