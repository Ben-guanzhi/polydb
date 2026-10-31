package transport

import (
	"context"
	"sync"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// Local 是进程内传输：直接委托 app-core，零序列化（AGENTS.md §5 编码约定）。
type Local struct {
	app *appcore.AppCore

	// 在飞查询注册表：queryID → 取消函数。ExecuteQuery 为每条语句派生可
	// 取消 ctx 并登记，CancelQuery 按 queryID 精确命中并取消——进程内取消
	// 自此真实生效（此前恒返回未命中，TUI 的 F8 在 local 模式是死按钮）。
	// 精确按 ID 命中同时免疫"陈旧取消打中新语句"：查询完成后条目即删，
	// 陈旧 ID 查不到，如实报未命中。
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

// NewLocal 构造进程内客户端（TUI 默认模式、GUI 未来模式）。
func NewLocal(app *appcore.AppCore) *Local {
	return &Local{app: app, inflight: make(map[string]context.CancelFunc)}
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

// ExecuteQuery 进程内没有服务端 in-flight registry，queryID 无意义（取消靠 ctx）。
func (l *Local) ExecuteQuery(ctx context.Context, queryID, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	// 派生可取消 ctx 并登记：ctx 中途被父级取消照常生效，CancelQuery 则按
	// queryID 取消这一条。完成后（含取消）注销条目，陈旧 ID 自然未命中。
	runCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.inflight[queryID] = cancel
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.inflight, queryID)
		l.mu.Unlock()
	}()
	return l.app.Execute(runCtx, id, sql, args...)
}

// CancelQuery 按 queryID 精确取消在飞的进程内查询：命中即取消其 ctx，
// 内核驱动（经 app-core 透传）据此中止语句。查询已完成的陈旧 ID 不命中，
// 如实返回 false。
func (l *Local) CancelQuery(ctx context.Context, queryID string) (bool, error) {
	l.mu.Lock()
	cancel, ok := l.inflight[queryID]
	l.mu.Unlock()
	if !ok {
		return false, nil
	}
	cancel()
	return true, nil
}

// BrowseRows 进程内委托 app-core（零序列化）。
func (l *Local) BrowseRows(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	return l.app.BrowseRows(ctx, id, schema, table, req)
}

// BrowseRowsCount 进程内委托 app-core 精确计数（零序列化）。
func (l *Local) BrowseRowsCount(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	return l.app.BrowseRowsCount(ctx, id, schema, table, req)
}

// ─── 结构比对与同步（E5）───────────────────────────────────

func (l *Local) SchemaDiff(ctx context.Context, req *protocol.SchemaDiffRequest) (*protocol.SchemaDiffPlan, error) {
	return l.app.SchemaDiff(ctx, req)
}

func (l *Local) SchemaDDL(ctx context.Context, req *protocol.SchemaDdlRequest) (*protocol.DdlScript, error) {
	return l.app.SchemaDDL(ctx, req)
}

// ─── 事务（T3）─────────────────────────────────────────────

func (l *Local) BeginTransaction(ctx context.Context, id string) (*protocol.TransactionInfo, error) {
	return l.app.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
}

func (l *Local) ExecuteInTx(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	return l.app.ExecuteInTx(ctx, txID, sql, args...)
}

func (l *Local) CommitTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	return l.app.CommitTransaction(txID)
}

func (l *Local) RollbackTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	return l.app.RollbackTransaction(txID)
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

// ─── 大字段旁路取回（spec/encoding.md §5.2）────────────────

// GetBlob 进程内取回（「进程内取回走 GetBlob」，零序列化，同一存储）。
// connID 是 REST 路径参数（blob 为目录级资源），进程内通道忽略。
func (l *Local) GetBlob(ctx context.Context, connID, blobID string) ([]byte, error) {
	return l.app.GetBlob(blobID)
}
