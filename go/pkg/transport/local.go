package transport

import (
	"context"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// Local 是进程内传输：直接委托 app-core，零序列化（AGENTS.md §5 编码约定）。
type Local struct {
	app *appcore.AppCore
}

// NewLocal 构造进程内客户端（TUI 默认模式、GUI 未来模式）。
func NewLocal(app *appcore.AppCore) *Local {
	return &Local{app: app}
}

func (l *Local) ListConnections() ([]protocol.ConnectionInfo, error) {
	return l.app.ListConnections()
}

func (l *Local) GetConnection(id string) (protocol.ConnectionInfo, error) {
	return l.app.GetConnection(id)
}

func (l *Local) CreateConnection(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
	return l.app.CreateConnection(req)
}

func (l *Local) UpdateConnection(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error) {
	return l.app.UpdateConnection(id, req)
}

func (l *Local) DeleteConnection(id string) (bool, error) {
	return l.app.DeleteConnection(id)
}

func (l *Local) Ping(ctx context.Context, id string) error {
	return l.app.Ping(ctx, id)
}

func (l *Local) Connect(ctx context.Context, id string) error {
	return l.app.Connect(ctx, id)
}

func (l *Local) Disconnect(id string) {
	l.app.Disconnect(id)
}

func (l *Local) IsConnected(id string) bool {
	return l.app.IsConnected(id)
}

func (l *Local) ListSchemas(ctx context.Context, id string) ([]protocol.SchemaInfo, error) {
	return l.app.ListSchemas(ctx, id)
}

func (l *Local) ListTables(ctx context.Context, id, schema string) ([]protocol.TableInfo, error) {
	return l.app.ListTables(ctx, id, schema)
}

func (l *Local) ListColumns(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error) {
	return l.app.ListColumns(ctx, id, schema, table)
}

func (l *Local) ListIndexes(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error) {
	return l.app.ListIndexes(ctx, id, schema, table)
}

func (l *Local) ListForeignKeys(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	return l.app.ListForeignKeys(ctx, id, schema, table)
}

func (l *Local) CreateTableSQL(ctx context.Context, id, schema, table string) (string, error) {
	return l.app.CreateTableSQL(ctx, id, schema, table)
}

func (l *Local) Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	return l.app.Execute(ctx, id, sql, args...)
}

// ─── Redis KV ───────────────────────────────────────────────

func (l *Local) SelectDB(ctx context.Context, id string, index int) error {
	return l.app.SelectDB(ctx, id, index)
}

func (l *Local) ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	return l.app.ScanKeys(ctx, id, cursor, pattern, count)
}

func (l *Local) GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error) {
	return l.app.GetValue(ctx, id, key)
}

func (l *Local) SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error {
	return l.app.SetValue(ctx, id, key, value)
}

func (l *Local) ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error) {
	return l.app.ExecCommand(ctx, id, args)
}
