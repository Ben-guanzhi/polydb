// dbmysql_protocol.go 是 *Conn 的 protocol 类型面视图：polydb 的 appcore /
// server / cli 说 protocol.* 类型（protocol.Value、PolyDBError、
// execution_time_ms / statement_type 线语义），经 ProtocolDriver 消费。
// 内核原生面在 dbmysql.go；本文件不做方言翻译，只做类型映射与错误码包装。
package dbmysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	dbc "github.com/dbcore/dbcore"
	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// ProtocolDriver 把 dbcore 原生的 *Conn 包装成 polydb protocol 类型面。
type ProtocolDriver struct{ c *Conn }

// NewProtocolDriver wraps a dbcore-native *Conn as polydb's protocol-typed dbcore.Driver.
func NewProtocolDriver(c *Conn) *ProtocolDriver { return &ProtocolDriver{c: c} }

// SQLTx 暴露内核原生事务驱动（Option B 接入点）：appcore 的事务注册表经它
// 拿到 dbc.SQLTxDriver 并交给 dbcore.TxManager。
func (p *ProtocolDriver) SQLTx() dbc.SQLTxDriver { return p.c }

func (p *ProtocolDriver) Kind() protocol.DatabaseKind    { return protocol.DatabaseKindMySQL }
func (p *ProtocolDriver) Ping(ctx context.Context) error { return p.c.Ping(ctx) }
func (p *ProtocolDriver) Close() error                   { return p.c.Close() }
func (p *ProtocolDriver) AsSQL() (pdb.SQLDriver, bool)   { return p, true }
func (p *ProtocolDriver) AsKV() (pdb.KVDriver, bool)     { return nil, false }

// Execute 走 protocol 管线：保留 execution_time_ms / statement_type 线语义。
func (p *ProtocolDriver) Execute(ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	return p.c.execP(ctx, sql, args...)
}

func (p *ProtocolDriver) ExecuteIn(exec pdb.SQLTx, ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	return p.c.execInP(exec, ctx, sql, args...)
}

func (p *ProtocolDriver) Begin(ctx context.Context, mode pdb.TxMode) (*sql.Tx, error) {
	return p.c.beginP(ctx, mode)
}

func (p *ProtocolDriver) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	return p.c.listSchemasP(ctx)
}

func (p *ProtocolDriver) ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	return p.c.listTablesP(ctx, schema)
}

func (p *ProtocolDriver) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	return p.c.listColumnsP(ctx, schema, table)
}

func (p *ProtocolDriver) ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	return p.c.listIndexesP(ctx, schema, table)
}

func (p *ProtocolDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	return p.c.listForeignKeysP(ctx, schema, table)
}

func (p *ProtocolDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	return p.c.createTableSQLP(ctx, schema, table)
}

func (p *ProtocolDriver) BrowseRows(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	return p.c.browseRowsP(ctx, schema, table, req)
}

func (p *ProtocolDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	return p.c.browseRowsCountP(ctx, schema, table, req)
}

// ─── protocol 执行/事务管线（原 Conn 实现，签名与语义不变）───────────────

func (c *Conn) execP(ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executeInP(ctx, c.db, sql, args)
}

func (c *Conn) execInP(exec pdb.SQLTx, ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executeInP(ctx, exec, sql, args)
}

func (c *Conn) executeInP(ctx context.Context, exec pdb.SQLTx, sql string, args []protocol.Value) (*protocol.QueryResult, error) {
	start := timeNowMs()
	stmtType := pdb.DetectStatementType(sql)

	if stmtType == protocol.StatementTypeSelect || stmtType == protocol.StatementTypeOther {
		rows, err := exec.QueryContext(ctx, sql, pdb.ValueArgs(args)...)
		if err != nil {
			return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
		}
		defer rows.Close()
		return queryResult(rows, stmtType, start)
	}

	res, err := exec.ExecContext(ctx, sql, pdb.ValueArgs(args)...)
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

func (c *Conn) beginP(ctx context.Context, mode pdb.TxMode) (*sql.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, pdb.TxOptionsFromMode(mode))
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
		columns[i] = protocol.ResultColumn{Name: ct.Name(), DataType: pdb.DeclTypeOf(ct)}
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
			rowVals[i] = pdb.DriverToValueTyped(*(r.(*any)), columns[i].DataType)
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

func timeNowMs() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}

// ─── protocol 目录/DDL：内核结果的类型映射与错误码包装 ────────────────

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

func (c *Conn) listSchemasP(ctx context.Context) ([]protocol.SchemaInfo, error) {
	infos, err := c.kern.ListSchemas(ctx)
	if err != nil {
		return nil, queryErr(err)
	}
	out := make([]protocol.SchemaInfo, len(infos))
	for i, s := range infos {
		out[i] = protocol.SchemaInfo{Name: s.Name}
	}
	return out, nil
}

func (c *Conn) listTablesP(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	infos, err := c.kern.ListTables(ctx, schema)
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

func (c *Conn) listColumnsP(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	infos, err := c.kern.ListColumns(ctx, schema, table)
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

func (c *Conn) listIndexesP(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	infos, err := c.kern.ListIndexes(ctx, schema, table)
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

func (c *Conn) listForeignKeysP(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	infos, err := c.kern.ListForeignKeys(ctx, schema, table)
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

func (c *Conn) createTableSQLP(ctx context.Context, schema, table string) (string, error) {
	ddl, err := c.kern.CreateTableSQL(ctx, schema, table)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", &protocol.PolyDBError{Code: protocol.ErrTableNotFound, Message: "table not found: " + schema + "." + table}
		}
		return "", queryErr(err)
	}
	return ddl, nil
}

// ─── protocol 表数据浏览（behavior.md §13）────────────────────────────

// browseRowsP 按表浏览行：共享构造器产参数化 SQL 后走 protocol 执行管线。
func (c *Conn) browseRowsP(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	sqlText, args, err := pdb.BuildRowsQuery(pdb.MySQLSQLDialect(), schema, table, req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.executeInP(ctx, c.db, sqlText, args)
	if err != nil {
		return nil, err
	}
	return pdb.RowsResultToBrowsePage(res, req.Offset, pdb.BrowseRowsLimits(req.Limit)), nil
}

// browseRowsCountP 对同条件执行 COUNT(*)。
func (c *Conn) browseRowsCountP(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	sqlText, args, err := pdb.BuildRowsCountQuery(pdb.MySQLSQLDialect(), schema, table, req)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.executeInP(ctx, c.db, sqlText, args)
	if err != nil {
		return 0, err
	}
	return pdb.CountResultToUint64(res)
}
