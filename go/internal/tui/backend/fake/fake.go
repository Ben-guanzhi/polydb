// Package fake 提供 transport.Client 的内存假实现，供 UI 测试使用。
// 每个方法都是可覆写的 func 字段；未设置时返回零值与 nil。
// 仅被 _test.go 引用，不进入生产二进制。
package fake

import (
	"context"

	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

// Client 是 transport.Client 的假实现。
type Client struct {
	ListConnectionsFunc     func() ([]protocol.ConnectionInfo, error)
	GetConnectionFunc       func(id string) (protocol.ConnectionInfo, error)
	CreateConnectionFunc    func(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error)
	UpdateConnectionFunc    func(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error)
	DeleteConnectionFunc    func(id string) (bool, error)
	PingFunc                func(ctx context.Context, id string) error
	ConnectFunc             func(ctx context.Context, id string) error
	DisconnectFunc          func(id string)
	IsConnectedFunc         func(id string) bool
	ListSchemasFunc         func(ctx context.Context, id string) ([]protocol.SchemaInfo, error)
	ListTablesFunc          func(ctx context.Context, id, schema string) ([]protocol.TableInfo, error)
	ListColumnsFunc         func(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error)
	ListIndexesFunc         func(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error)
	ListForeignKeysFunc     func(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error)
	CreateTableSQLFunc      func(ctx context.Context, id, schema, table string) (string, error)
	ExecuteFunc             func(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
	ExecuteQueryFunc        func(ctx context.Context, queryID, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
	CancelQueryFunc         func(ctx context.Context, queryID string) (bool, error)
	BrowseRowsFunc          func(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error)
	BrowseRowsCountFunc     func(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (uint64, error)
	SchemaDiffFunc          func(ctx context.Context, req *protocol.SchemaDiffRequest) (*protocol.SchemaDiffPlan, error)
	SchemaDDLFunc           func(ctx context.Context, req *protocol.SchemaDdlRequest) (*protocol.DdlScript, error)
	BeginTransactionFunc    func(ctx context.Context, id string) (*protocol.TransactionInfo, error)
	ExecuteInTxFunc         func(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
	CommitTransactionFunc   func(ctx context.Context, txID string) (*protocol.TransactionInfo, error)
	RollbackTransactionFunc func(ctx context.Context, txID string) (*protocol.TransactionInfo, error)
	SelectDBFunc            func(ctx context.Context, id string, index int) error
	ScanKeysFunc            func(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error)
	GetValueFunc            func(ctx context.Context, id, key string) (protocol.RedisValue, error)
	SetValueFunc            func(ctx context.Context, id, key string, value protocol.RedisValue) error
	ExecCommandFunc         func(ctx context.Context, id string, args []string) (protocol.RedisReply, error)
	GetBlobFunc             func(ctx context.Context, connID, blobID string) ([]byte, error)
}

// 编译期证明 Client 满足 transport.Client。
var _ transport.Client = (*Client)(nil)

func (c *Client) ListConnections() ([]protocol.ConnectionInfo, error) {
	if c.ListConnectionsFunc != nil {
		return c.ListConnectionsFunc()
	}
	return nil, nil
}

func (c *Client) GetConnection(id string) (protocol.ConnectionInfo, error) {
	if c.GetConnectionFunc != nil {
		return c.GetConnectionFunc(id)
	}
	return protocol.ConnectionInfo{}, nil
}

func (c *Client) CreateConnection(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
	if c.CreateConnectionFunc != nil {
		return c.CreateConnectionFunc(req)
	}
	return protocol.ConnectionInfo{}, nil
}

func (c *Client) UpdateConnection(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error) {
	if c.UpdateConnectionFunc != nil {
		return c.UpdateConnectionFunc(id, req)
	}
	return protocol.ConnectionInfo{}, nil
}

func (c *Client) DeleteConnection(id string) (bool, error) {
	if c.DeleteConnectionFunc != nil {
		return c.DeleteConnectionFunc(id)
	}
	return false, nil
}

func (c *Client) Ping(ctx context.Context, id string) error {
	if c.PingFunc != nil {
		return c.PingFunc(ctx, id)
	}
	return nil
}

func (c *Client) Connect(ctx context.Context, id string) error {
	if c.ConnectFunc != nil {
		return c.ConnectFunc(ctx, id)
	}
	return nil
}

func (c *Client) Disconnect(id string) {
	if c.DisconnectFunc != nil {
		c.DisconnectFunc(id)
	}
}

func (c *Client) IsConnected(id string) bool {
	if c.IsConnectedFunc != nil {
		return c.IsConnectedFunc(id)
	}
	return false
}

func (c *Client) ListSchemas(ctx context.Context, id string) ([]protocol.SchemaInfo, error) {
	if c.ListSchemasFunc != nil {
		return c.ListSchemasFunc(ctx, id)
	}
	return nil, nil
}

func (c *Client) ListTables(ctx context.Context, id, schema string) ([]protocol.TableInfo, error) {
	if c.ListTablesFunc != nil {
		return c.ListTablesFunc(ctx, id, schema)
	}
	return nil, nil
}

func (c *Client) ListColumns(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error) {
	if c.ListColumnsFunc != nil {
		return c.ListColumnsFunc(ctx, id, schema, table)
	}
	return nil, nil
}

func (c *Client) ListIndexes(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error) {
	if c.ListIndexesFunc != nil {
		return c.ListIndexesFunc(ctx, id, schema, table)
	}
	return nil, nil
}

func (c *Client) ListForeignKeys(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	if c.ListForeignKeysFunc != nil {
		return c.ListForeignKeysFunc(ctx, id, schema, table)
	}
	return nil, nil
}

func (c *Client) CreateTableSQL(ctx context.Context, id, schema, table string) (string, error) {
	if c.CreateTableSQLFunc != nil {
		return c.CreateTableSQLFunc(ctx, id, schema, table)
	}
	return "", nil
}

func (c *Client) Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	if c.ExecuteFunc != nil {
		return c.ExecuteFunc(ctx, id, sql, args...)
	}
	return &protocol.QueryResult{}, nil
}

func (c *Client) ExecuteQuery(ctx context.Context, queryID, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	if c.ExecuteQueryFunc != nil {
		return c.ExecuteQueryFunc(ctx, queryID, id, sql, args...)
	}
	return &protocol.QueryResult{}, nil
}

func (c *Client) CancelQuery(ctx context.Context, queryID string) (bool, error) {
	if c.CancelQueryFunc != nil {
		return c.CancelQueryFunc(ctx, queryID)
	}
	return false, nil
}

func (c *Client) BrowseRows(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	if c.BrowseRowsFunc != nil {
		return c.BrowseRowsFunc(ctx, id, schema, table, req)
	}
	return &protocol.TableRowsResult{}, nil
}

func (c *Client) BrowseRowsCount(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	if c.BrowseRowsCountFunc != nil {
		return c.BrowseRowsCountFunc(ctx, id, schema, table, req)
	}
	return 0, nil
}

func (c *Client) SchemaDiff(ctx context.Context, req *protocol.SchemaDiffRequest) (*protocol.SchemaDiffPlan, error) {
	if c.SchemaDiffFunc != nil {
		return c.SchemaDiffFunc(ctx, req)
	}
	return &protocol.SchemaDiffPlan{}, nil
}

func (c *Client) SchemaDDL(ctx context.Context, req *protocol.SchemaDdlRequest) (*protocol.DdlScript, error) {
	if c.SchemaDDLFunc != nil {
		return c.SchemaDDLFunc(ctx, req)
	}
	return &protocol.DdlScript{}, nil
}

func (c *Client) BeginTransaction(ctx context.Context, id string) (*protocol.TransactionInfo, error) {
	if c.BeginTransactionFunc != nil {
		return c.BeginTransactionFunc(ctx, id)
	}
	return &protocol.TransactionInfo{}, nil
}

func (c *Client) ExecuteInTx(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	if c.ExecuteInTxFunc != nil {
		return c.ExecuteInTxFunc(ctx, txID, sql, args...)
	}
	return &protocol.QueryResult{}, nil
}

func (c *Client) CommitTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	if c.CommitTransactionFunc != nil {
		return c.CommitTransactionFunc(ctx, txID)
	}
	return &protocol.TransactionInfo{}, nil
}

func (c *Client) RollbackTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	if c.RollbackTransactionFunc != nil {
		return c.RollbackTransactionFunc(ctx, txID)
	}
	return &protocol.TransactionInfo{}, nil
}

func (c *Client) SelectDB(ctx context.Context, id string, index int) error {
	if c.SelectDBFunc != nil {
		return c.SelectDBFunc(ctx, id, index)
	}
	return nil
}

func (c *Client) ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	if c.ScanKeysFunc != nil {
		return c.ScanKeysFunc(ctx, id, cursor, pattern, count)
	}
	return &protocol.RedisScanPage{}, nil
}

func (c *Client) GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error) {
	if c.GetValueFunc != nil {
		return c.GetValueFunc(ctx, id, key)
	}
	return protocol.RedisValue{}, nil
}

func (c *Client) SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error {
	if c.SetValueFunc != nil {
		return c.SetValueFunc(ctx, id, key, value)
	}
	return nil
}

func (c *Client) ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error) {
	if c.ExecCommandFunc != nil {
		return c.ExecCommandFunc(ctx, id, args)
	}
	return protocol.RedisReply{}, nil
}

func (c *Client) GetBlob(ctx context.Context, connID, blobID string) ([]byte, error) {
	if c.GetBlobFunc != nil {
		return c.GetBlobFunc(ctx, connID, blobID)
	}
	return nil, nil
}
