// Package transport 是前端访问数据的传输层（AGENTS.md 铁律 2：
// 前端只通过 app-core（进程内）或 transport（网络）访问数据，不直接依赖 driver）。
//
// 两个实现：
//   - Local：进程内委托 app-core（TUI/GUI 默认，零序列化）
//   - Remote：走 polydb-server 的 REST 接口（TUI 可连远程服务端，M4 远程模式）
package transport

import (
	"context"

	"github.com/polydb/polydb/pkg/protocol"
)

// Client 是前端视角的统一数据访问接口。方法与 appcore.AppCore 对齐，
// 但只暴露前端实际需要的一部分（TUI 现状 + 连接 CRUD + 元数据 + 查询）。
// 注意：Remote 实现的 Connect/IsConnected 语义与 Local 不同——服务端是懒连接，
// 见 remote.go 的文档。
type Client interface {
	ListConnections() ([]protocol.ConnectionInfo, error)
	GetConnection(id string) (protocol.ConnectionInfo, error)
	CreateConnection(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error)
	UpdateConnection(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error)
	DeleteConnection(id string) (bool, error)

	// Ping 测试连通性（本地=懒连接后 ping；远程=/test 端点）。
	Ping(ctx context.Context, id string) error
	// Connect 打开连接（本地=打开驱动实例；远程=校验连接存在并标记本地已连接，
	// 真正的数据库连接由服务端在首个查询时惰性建立）。
	Connect(ctx context.Context, id string) error
	// Disconnect 关闭连接（本地=关闭驱动；远程=仅清除本地标记）。
	Disconnect(id string)
	// IsConnected 报告本地视角是否已连接（远程实现为本地标记，非服务端状态）。
	IsConnected(id string) bool

	ListSchemas(ctx context.Context, id string) ([]protocol.SchemaInfo, error)
	ListTables(ctx context.Context, id, schema string) ([]protocol.TableInfo, error)
	ListColumns(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error)
	ListIndexes(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error)
	ListForeignKeys(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error)
	CreateTableSQL(ctx context.Context, id, schema, table string) (string, error)

	// Execute 执行一条 SQL。args 为空时不携带参数。
	Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error)

	// BrowseRows 按表浏览行数据（服务端分页；M11 数据面，只读操作）。
	BrowseRows(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error)

	// Redis KV（M6 前端 Redis 模式；读写受连接 read_only 约束，见 app-core）。
	SelectDB(ctx context.Context, id string, index int) error
	ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error)
	GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error)
	SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error
	ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error)
}
