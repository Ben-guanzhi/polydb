// Package dbcore 定义数据库驱动抽象，与 AGENTS.md §4.2 一致：
// Driver 基接口 + SQLDriver / KVDriver 能力分层。
package dbcore

import (
	"context"
	"database/sql"

	"github.com/polydb/polydb/pkg/protocol"
)

// Driver 是所有数据库驱动的基接口。
type Driver interface {
	Kind() protocol.DatabaseKind
	Ping(ctx context.Context) error
	Close() error
	AsSQL() (SQLDriver, bool)
	AsKV() (KVDriver, bool)
}

// SQLDriver 提供关系型数据库能力。
type SQLDriver interface {
	Driver
	Execute(ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
	ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error)
	ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error)
	ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error)
	ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error)
	ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error)
	CreateTableSQL(ctx context.Context, schema, table string) (string, error)
}

// SQLTxDriver 是 SQLDriver 的可选事务扩展。支持事务的驱动应同时实现 SQLTxDriver，
// 使调用方可用 Begin + ExecuteIn + Commit/Rollback 组合出原子操作。
// 未实现 SQLTxDriver 的驱动（如部分嵌入式或只读驱动）仍可作为 SQLDriver 使用。
type SQLTxDriver interface {
	SQLDriver
	// Begin 开启一个新的事务。返回的 *sql.Tx 由调用方负责 Commit / Rollback。
	// 驱动应把 TxMode.IsolationLevel 翻译成数据库方言；不支持隔离级别的驱动可忽略该字段。
	Begin(ctx context.Context, mode TxMode) (*sql.Tx, error)
	// ExecuteIn 在给定执行器上跑一条 SQL（通常是 tx，也可为 *sql.DB 走普通路径）。
	// 实现应与 Execute 逻辑一致，只是把 sql.DB 换成执行器。
	ExecuteIn(exec SQLTx, ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error)
}

// KVDriver 提供键值数据库（Redis）能力。
type KVDriver interface {
	Driver
	SelectDB(ctx context.Context, idx int) error
	ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error)
	KeyType(ctx context.Context, key string) (protocol.RedisKeyType, error)
	GetValue(ctx context.Context, key string) (protocol.RedisValue, error)
	SetValue(ctx context.Context, key string, value protocol.RedisValue) error
	ExecCommand(ctx context.Context, args []string) (protocol.RedisReply, error)
}

// Connection 包装具体驱动，提供类型安全的能力下发。
type Connection struct {
	driver Driver
}

func NewConnection(d Driver) Connection {
	return Connection{driver: d}
}

func (c Connection) Kind() protocol.DatabaseKind {
	return c.driver.Kind()
}

func (c Connection) Driver() Driver {
	return c.driver
}

func (c Connection) AsSQL() (SQLDriver, error) {
	if d, ok := c.driver.AsSQL(); ok {
		return d, nil
	}
	return nil, &protocol.PolyDBError{
		Code:    protocol.ErrNotSupported,
		Message: string(c.driver.Kind()) + " is not a SQL database",
	}
}

func (c Connection) AsKV() (KVDriver, error) {
	if d, ok := c.driver.AsKV(); ok {
		return d, nil
	}
	return nil, &protocol.PolyDBError{
		Code:    protocol.ErrNotSupported,
		Message: string(c.driver.Kind()) + " is not a KV database",
	}
}
