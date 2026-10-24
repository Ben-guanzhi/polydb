// Package dbmysql 实现基于 go-sql-driver/mysql 的 SQL 驱动。
package dbmysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	dbc "github.com/dbcore/dbcore"
	dbcoremysql "github.com/dbcore/dbcore/mysql"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

type Conn struct {
	db  *sql.DB
	dsn string
	mu  sync.Mutex
}

// Open 通过 DSN（user:pass@tcp(host:port)/dbname）建立连接池。
func Open(ctx context.Context, dsn string) (*Conn, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &Conn{db: db, dsn: dsn}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindMySQL }
func (c *Conn) AsSQL() (dbcore.SQLDriver, bool) { return c, true }
func (c *Conn) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (c *Conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }
func (c *Conn) Close() error                   { return c.db.Close() }

func (c *Conn) Execute(ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executeIn(ctx, c.db, sql, args)
}

func (c *Conn) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executeIn(ctx, exec, sql, args)
}

func (c *Conn) executeIn(ctx context.Context, exec dbcore.SQLTx, sql string, args []protocol.Value) (*protocol.QueryResult, error) {
	start := timeNowMs()
	stmtType := dbcore.DetectStatementType(sql)

	if stmtType == protocol.StatementTypeSelect || stmtType == protocol.StatementTypeOther {
		rows, err := exec.QueryContext(ctx, sql, dbcore.ValueArgs(args)...)
		if err != nil {
			return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
		}
		defer rows.Close()
		return queryResult(rows, stmtType, start)
	}

	res, err := exec.ExecContext(ctx, sql, dbcore.ValueArgs(args)...)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	affected, _ := res.RowsAffected()
	return &protocol.QueryResult{
		AffectedRows:    affected,
		ExecutionTimeMs: timeNowMs() - start,
		StatementType:   stmtType,
	}, nil
}

func (c *Conn) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	return tx, nil
}

func queryResult(rows *sql.Rows, stmtType protocol.StatementType, start float64) (*protocol.QueryResult, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "column types: " + err.Error()}
	}
	columns := make([]protocol.ResultColumn, len(colTypes))
	for i, ct := range colTypes {
		columns[i] = protocol.ResultColumn{Name: ct.Name(), DataType: dbcore.DeclTypeOf(ct)}
	}
	var resultRows [][]protocol.Value
	for rows.Next() {
		raw := make([]any, len(colTypes))
		for i := range raw {
			raw[i] = new(any)
		}
		if err := rows.Scan(raw...); err != nil {
			return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "scan: " + err.Error()}
		}
		rowVals := make([]protocol.Value, len(raw))
		for i, r := range raw {
			rowVals[i] = dbcore.DriverToValueTyped(*(r.(*any)), columns[i].DataType)
		}
		resultRows = append(resultRows, rowVals)
	}
	if err := rows.Err(); err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	return &protocol.QueryResult{
		Columns:         columns,
		Rows:            resultRows,
		ExecutionTimeMs: timeNowMs() - start,
		StatementType:   stmtType,
	}, nil
}

// ─── 目录/DDL：经共享内核 dbcore 驱动（INTEGRATION-PLAN P1-5）───────────────
//
// 方言知识（目录 SQL、类型折叠、SHOW CREATE TABLE、schema 过滤清单）只在内核
// 维护一份；本包仅做 protocol 类型映射与错误码包装。执行/事务/表浏览分页仍走
// 自身管线（不含方言知识，保留 execution_time_ms 等协议语义）。

func (c *Conn) kernel() *dbcoremysql.MySQLDriver { return dbcoremysql.NewFromDB(c.db) }

// queryErr 把内核错误统一包成 ErrQueryFailed。
func queryErr(err error) error {
	return &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
}

// fkActionFromKernel 把内核的标准动作拼写（"CASCADE"/"SET NULL"/…）映射为
// protocol 的 wire 拼写（"cascade"/"set_null"/…）；空值原样传递（= 无信息）。
func fkActionFromKernel(s string) protocol.ForeignKeyAction {
	switch s {
	case dbc.FKActionCascade:
		return protocol.FKActionCascade
	case dbc.FKActionSetNull:
		return protocol.FKActionSetNull
	case dbc.FKActionSetDefault:
		return protocol.FKActionSetDefault
	case dbc.FKActionRestrict:
		return protocol.FKActionRestrict
	case dbc.FKActionNoAction:
		return protocol.FKActionNoAction
	}
	return protocol.ForeignKeyAction(s)
}

func (c *Conn) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	infos, err := c.kernel().ListSchemas(ctx)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.SchemaInfo, len(infos))
	for i, s := range infos {
		out[i] = protocol.SchemaInfo{Name: s.Name}
	}
	return out, nil
}

func (c *Conn) ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	infos, err := c.kernel().ListTables(ctx, schema)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.TableInfo, len(infos))
	for i, t := range infos {
		typ := protocol.TableTypeTable
		if t.Type == "view" {
			typ = protocol.TableTypeView
		}
		ti := protocol.TableInfo{Name: t.Name, Schema: t.Schema, Type: typ, Comment: t.Comment}
		if t.Rows >= 0 { // 内核 -1 = 未知（视图/未统计），保持 nil 而非 -1
			rows := t.Rows
			ti.RowCount = &rows
		}
		out[i] = ti
	}
	return out, nil
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	infos, err := c.kernel().ListColumns(ctx, schema, table)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.ColumnInfo, len(infos))
	for i, col := range infos {
		out[i] = protocol.ColumnInfo{
			Name:            col.Name,
			DataType:        col.Type,
			Nullable:        col.Nullable,
			DefaultValue:    col.Default,
			IsPrimaryKey:    col.IsPrimaryKey,
			IsAutoIncrement: col.IsAutoIncrement,
			Comment:         col.Comment,
			OrdinalPosition: col.OrdinalPosition,
		}
	}
	return out, nil
}

func (c *Conn) ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	infos, err := c.kernel().ListIndexes(ctx, schema, table)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.IndexInfo, len(infos))
	for i, idx := range infos {
		cols := make([]protocol.IndexColumn, len(idx.Columns))
		for j, name := range idx.Columns {
			cols[j] = protocol.IndexColumn{Name: name, Position: int32(j + 1)}
		}
		out[i] = protocol.IndexInfo{Name: idx.Name, Unique: idx.Unique, Primary: idx.Primary, Columns: cols}
	}
	return out, nil
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	infos, err := c.kernel().ListForeignKeys(ctx, schema, table)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.ForeignKeyInfo, len(infos))
	for i, fk := range infos {
		out[i] = protocol.ForeignKeyInfo{
			Name:              fk.Name,
			Columns:           fk.Columns,
			ReferencedSchema:  fk.RefSchema,
			ReferencedTable:   fk.RefTable,
			ReferencedColumns: fk.RefColumns,
			OnUpdate:          fkActionFromKernel(fk.OnUpdate),
			OnDelete:          fkActionFromKernel(fk.OnDelete),
		}
	}
	return out, nil
}

func (c *Conn) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	ddl, err := c.kernel().CreateTableSQL(ctx, schema, table)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", &protocol.PolyDBError{Code: protocol.ErrTableNotFound, Message: "table not found: " + schema + "." + table}
		}
		return "", queryErr(err)
	}
	return ddl, nil
}

func timeNowMs() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}

// ─── 表数据浏览（behavior.md §13）────────────────────────────

// BrowseRows 按表浏览行：共享构造器产参数化 SQL 后走自身执行管线。
func (c *Conn) BrowseRows(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	sqlText, args, err := dbcore.BuildRowsQuery(dbcore.MySQLSQLDialect(), schema, table, req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.executeIn(ctx, c.db, sqlText, args)
	if err != nil {
		return nil, err
	}
	return dbcore.RowsResultToBrowsePage(res, req.Offset, dbcore.BrowseRowsLimits(req.Limit)), nil
}

// BrowseRowsCount 对同条件执行 COUNT(*)。
func (c *Conn) BrowseRowsCount(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	sqlText, args, err := dbcore.BuildRowsCountQuery(dbcore.MySQLSQLDialect(), schema, table, req)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.executeIn(ctx, c.db, sqlText, args)
	if err != nil {
		return 0, err
	}
	return dbcore.CountResultToUint64(res)
}
