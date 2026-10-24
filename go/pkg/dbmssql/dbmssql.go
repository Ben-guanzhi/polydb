// Package dbmssql 实现基于 microsoft/go-mssqldb 的 SQL 驱动。
//
// Option B（INTEGRATION-PLAN P1-5 收口）：Conn 原生实现共享内核
// github.com/dbcore/dbcore 的 SQLTxDriver 契约——执行/事务/目录/DDL/表浏览
// 全部委托内核驱动，方言知识只在内核维护一份。polydb 的 protocol 类型面
// 由同包的 ProtocolDriver（dbmssql_protocol.go）适配提供，appcore 经
// NewProtocolDriver 消费。
package dbmssql

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	dbc "github.com/dbcore/dbcore"
	dbcoremssql "github.com/dbcore/dbcore/mssql"
	_ "github.com/microsoft/go-mssqldb"
)

type Conn struct {
	db   *sql.DB
	dsn  string
	mu   sync.Mutex // 只串行化 protocol 管线（*_P helper）；内核路径走连接池自身并发
	kern *dbcoremssql.MSSQLDriver
}

// Conn 原生满足内核的事务扩展契约（Option B 的核心断言）。
var _ dbc.SQLTxDriver = (*Conn)(nil)

// Open 通过 DSN（sqlserver://user:pass@host:port?database=dbname）建立连接池。
func Open(ctx context.Context, dsn string) (*Conn, error) {
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mssql: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mssql: %w", err)
	}
	return &Conn{db: db, dsn: dsn, kern: dbcoremssql.NewFromDB(db)}, nil
}

// ─── dbcore 原生面（dbc.SQLTxDriver）─────────────────────────

func (c *Conn) Kind() dbc.DatabaseKind         { return dbc.KindMSSQL }
func (c *Conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }
func (c *Conn) Close() error                   { return c.db.Close() }
func (c *Conn) AsSQL() (dbc.SQLDriver, bool)   { return c, true }
func (c *Conn) AsKV() (dbc.KVDriver, bool)     { return nil, false }

func (c *Conn) Execute(ctx context.Context, query string, args ...dbc.Value) (*dbc.QueryResult, error) {
	return c.kern.Execute(ctx, query, args...)
}

func (c *Conn) ExecuteIn(exec dbc.SQLTx, ctx context.Context, query string, args ...dbc.Value) (*dbc.QueryResult, error) {
	return c.kern.ExecuteIn(exec, ctx, query, args...)
}

func (c *Conn) Begin(ctx context.Context, mode dbc.TxMode) (*sql.Tx, error) {
	return c.kern.Begin(ctx, mode)
}

func (c *Conn) ListSchemas(ctx context.Context) ([]dbc.SchemaInfo, error) {
	return c.kern.ListSchemas(ctx)
}

func (c *Conn) ListTables(ctx context.Context, schema string) ([]dbc.TableInfo, error) {
	return c.kern.ListTables(ctx, schema)
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]dbc.ColumnInfo, error) {
	return c.kern.ListColumns(ctx, schema, table)
}

func (c *Conn) ListIndexes(ctx context.Context, schema, table string) ([]dbc.IndexInfo, error) {
	return c.kern.ListIndexes(ctx, schema, table)
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]dbc.ForeignKeyInfo, error) {
	return c.kern.ListForeignKeys(ctx, schema, table)
}

func (c *Conn) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	return c.kern.CreateTableSQL(ctx, schema, table)
}

func (c *Conn) BrowseRows(ctx context.Context, schema, table string, req *dbc.TableRowsRequest) (*dbc.TableRowsResult, error) {
	return c.kern.BrowseRows(ctx, schema, table, req)
}

func (c *Conn) BrowseRowsCount(ctx context.Context, schema, table string, req *dbc.TableRowsRequest) (uint64, error) {
	return c.kern.BrowseRowsCount(ctx, schema, table, req)
}
