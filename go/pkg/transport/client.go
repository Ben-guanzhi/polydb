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

	// ExecuteQuery 同 Execute，但带上客户端预分配的 queryID（须是 UUID），
	// 使执行期间的 CancelQuery 能在服务端 in-flight registry 命中。
	// queryID 为空表示不注册（不可取消）。Local 忽略 queryID，取消只靠 ctx。
	ExecuteQuery(ctx context.Context, queryID, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error)

	// CancelQuery 请求取消在途查询，返回是否命中（best-effort，behavior.md §2.2：
	// 取消不保证一定能中断底层驱动）。Local 无服务端 registry，恒返回 (false, nil)。
	CancelQuery(ctx context.Context, queryID string) (bool, error)

	// BrowseRows 按表浏览行数据（服务端分页；M11 数据面，只读操作）。
	BrowseRows(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error)

	// BrowseRowsCount 对同过滤条件执行精确 COUNT(*)（behavior.md §13.1，只读操作）。
	BrowseRowsCount(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (uint64, error)

	// 结构比对与同步（Compare & Sync E5，behavior.md §14/§15）。
	// SchemaDiff 只读采集两侧快照并产出差异计划；SchemaDDL 由服务端**重算**计划后
	// 生成脚本（勾选校验、陈旧 id 与重建组未整组勾选都会报错）。
	// 两端（Local/Remote）行为一致：Local 直调 app-core，Remote 打同一组 REST 端点。
	SchemaDiff(ctx context.Context, req *protocol.SchemaDiffRequest) (*protocol.SchemaDiffPlan, error)
	SchemaDDL(ctx context.Context, req *protocol.SchemaDdlRequest) (*protocol.DdlScript, error)

	// 事务（T3 变更跟踪编辑；单次 finalize 语义见 spec/behavior.md §10）。
	// Begin 在连接上开启事务；ExecuteInTx 在事务内跑一条参数化 SQL；
	// Commit/Rollback 二选一 finalize（重复 finalize 返回 TRANSACTION_NOT_FOUND）。
	BeginTransaction(ctx context.Context, id string) (*protocol.TransactionInfo, error)
	ExecuteInTx(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
	CommitTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error)
	RollbackTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error)

	// Redis KV（M6 前端 Redis 模式；读写受连接 read_only 约束，见 app-core）。
	SelectDB(ctx context.Context, id string, index int) error
	ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error)
	GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error)
	SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error
	ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error)

	// 大字段旁路取回（spec/encoding.md §5.2）：blobID 为 $polydb_blob:<uuid> 的
	// UUID 部分。Local 进程内直读存储（零序列化）；Remote 打 REST 二进制通道
	//（connID 仅用于拼路径，服务端只校验格式）。未知/过期返回
	// *protocol.PolyDBError(BLOB_NOT_FOUND)。
	GetBlob(ctx context.Context, connID, blobID string) ([]byte, error)
}
