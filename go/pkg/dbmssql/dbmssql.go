// Package dbmssql 实现基于 microsoft/go-mssqldb 的 SQL 驱动。
package dbmssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

type Conn struct {
	db  *sql.DB
	dsn string
	mu  sync.Mutex
}

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
	return &Conn{db: db, dsn: dsn}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindMSSQL }
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

// Begin 开启一个新的事务。go-mssqldb 对 BeginTxOptions 的支持不完整，
// TxOptionsFromMode 会把不支持的隔离级别退化为 LevelDefault；MSSQL 的隔离级别
// 若确需指定，可让驱动后续用 SET TRANSACTION ISOLATION LEVEL 补充设置。
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

func (c *Conn) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT name FROM sys.schemas
		  WHERE name NOT IN ('sys','INFORMATION_SCHEMA','guest')
		    AND name NOT LIKE 'db_%' AND name NOT LIKE '##%'
		  ORDER BY name`)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	out := make([]protocol.SchemaInfo, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, protocol.SchemaInfo{Name: name})
	}
	return out, rows.Err()
}

func (c *Conn) ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT name, 'table' FROM sys.tables WHERE schema_id = SCHEMA_ID(?)
		  UNION ALL
		  SELECT name, 'view' FROM sys.views WHERE schema_id = SCHEMA_ID(?)
		  ORDER BY name`, schema, schema)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	out := make([]protocol.TableInfo, 0)
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out = append(out, protocol.TableInfo{Name: name, Schema: schema, Type: protocol.TableType(typ)})
	}
	return out, rows.Err()
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT c.name,
		        ty.name + CASE
		          WHEN ty.name IN ('varchar','nvarchar','char','nchar','binary','varbinary')
		            THEN '(' + CASE WHEN c.max_length = -1 THEN 'max'
		                            ELSE CAST(c.max_length AS varchar(10)) END + ')'
		          WHEN ty.name IN ('decimal','numeric')
		            THEN '(' + CAST(c.precision AS varchar(10)) + ',' + CAST(c.scale AS varchar(10)) + ')'
		          ELSE '' END,
		        c.is_nullable, dc.definition, c.is_identity, c.column_id
		   FROM sys.columns c
		   JOIN sys.types ty ON ty.user_type_id = c.user_type_id
		   JOIN sys.tables t ON t.object_id = c.object_id
		   JOIN sys.schemas s ON s.schema_id = t.schema_id
		   LEFT JOIN sys.default_constraints dc
		          ON dc.parent_object_id = c.object_id AND dc.parent_column_id = c.column_id
		  WHERE s.name = ? AND t.name = ?
		  ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	var cols []protocol.ColumnInfo
	for rows.Next() {
		var name, dataType string
		var nullable, auto bool
		var def sql.NullString
		var pos int
		if err := rows.Scan(&name, &dataType, &nullable, &def, &auto, &pos); err != nil {
			return nil, err
		}
		var defPtr *string
		if def.Valid {
			defPtr = &def.String
		}
		cols = append(cols, protocol.ColumnInfo{
			Name:            name,
			DataType:        dataType,
			Nullable:        nullable,
			DefaultValue:    defPtr,
			IsAutoIncrement: auto,
			OrdinalPosition: int32(pos),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pkRows, err := c.db.QueryContext(ctx,
		`SELECT col.name
		   FROM sys.indexes i
		   JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		   JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		   JOIN sys.tables t ON t.object_id = i.object_id
		   JOIN sys.schemas s ON s.schema_id = t.schema_id
		  WHERE s.name = ? AND t.name = ? AND i.is_primary_key = 1
		  ORDER BY ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer pkRows.Close()
	pkSet := map[string]bool{}
	for pkRows.Next() {
		var name string
		if err := pkRows.Scan(&name); err != nil {
			return nil, err
		}
		pkSet[name] = true
	}
	for i := range cols {
		if pkSet[cols[i].Name] {
			cols[i].IsPrimaryKey = true
		}
	}
	return cols, nil
}

func (c *Conn) ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT i.name, i.is_unique, i.is_primary_key, i.type_desc, col.name, ic.key_ordinal
		   FROM sys.indexes i
		   JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		   JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		   JOIN sys.tables t ON t.object_id = i.object_id
		   JOIN sys.schemas s ON s.schema_id = t.schema_id
		  WHERE s.name = ? AND t.name = ? AND i.index_id > 0
		  ORDER BY i.name, ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	type idx struct {
		unique, primary bool
		typ             protocol.IndexType
		cols            []protocol.IndexColumn
	}
	order := []string{}
	groups := map[string]*idx{}
	for rows.Next() {
		var name string
		var unique, primary bool
		var typeDesc string
		var col string
		var pos int
		if err := rows.Scan(&name, &unique, &primary, &typeDesc, &col, &pos); err != nil {
			return nil, err
		}
		if _, ok := groups[name]; !ok {
			order = append(order, name)
			groups[name] = &idx{unique: unique, primary: primary, typ: indexType(typeDesc)}
		}
		groups[name].cols = append(groups[name].cols, protocol.IndexColumn{Name: col, Position: int32(pos)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]protocol.IndexInfo, 0)
	for _, name := range order {
		g := groups[name]
		out = append(out, protocol.IndexInfo{Name: name, Unique: g.unique, Primary: g.primary, Type: g.typ, Columns: g.cols})
	}
	return out, nil
}

func indexType(desc string) protocol.IndexType {
	switch {
	case strings.Contains(desc, "CLUSTERED"):
		return protocol.IndexTypeBTree
	case strings.Contains(desc, "NONCLUSTERED"):
		return protocol.IndexTypeBTree
	case strings.Contains(desc, "SPATIAL"):
		return protocol.IndexTypeSpatial
	default:
		return protocol.IndexTypeOther
	}
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT fk.name, fkcol.name, rs.name, rt.name, rcol.name, fkc.constraint_column_id
		   FROM sys.foreign_keys fk
		   JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		   JOIN sys.columns fkcol ON fkcol.object_id = fkc.parent_object_id AND fkcol.column_id = fkc.parent_column_id
		   JOIN sys.tables t ON t.object_id = fk.parent_object_id
		   JOIN sys.schemas s ON s.schema_id = t.schema_id
		   JOIN sys.columns rcol ON rcol.object_id = fkc.referenced_object_id AND rcol.column_id = fkc.referenced_column_id
		   JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id
		   JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
		  WHERE s.name = ? AND t.name = ?
		  ORDER BY fk.name, fkc.constraint_column_id`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	type agg struct {
		refSchema, refTable string
		from, to            []string
	}
	order := []string{}
	seen := map[string]bool{}
	groups := map[string]*agg{}
	for rows.Next() {
		var name, from, refSchema, refTable, to string
		var seq int
		if err := rows.Scan(&name, &from, &refSchema, &refTable, &to, &seq); err != nil {
			return nil, err
		}
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
			groups[name] = &agg{refSchema: refSchema, refTable: refTable}
		}
		groups[name].from = append(groups[name].from, from)
		groups[name].to = append(groups[name].to, to)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]protocol.ForeignKeyInfo, 0)
	for _, name := range order {
		a := groups[name]
		out = append(out, protocol.ForeignKeyInfo{
			Name:              name,
			Columns:           a.from,
			ReferencedSchema:  a.refSchema,
			ReferencedTable:   a.refTable,
			ReferencedColumns: a.to,
		})
	}
	return out, nil
}

func (c *Conn) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	cols, err := c.ListColumns(ctx, schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", &protocol.PolyDBError{Code: protocol.ErrTableNotFound, Message: "table not found: " + schema + "." + table}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s.%s (\n", quoteIdent(schema), quoteIdent(table))
	for i, col := range cols {
		suffix := ""
		switch {
		case col.IsPrimaryKey:
			suffix = " PRIMARY KEY"
		case col.IsAutoIncrement:
			suffix = " IDENTITY(1,1)"
		case !col.Nullable:
			suffix = " NOT NULL"
		}
		if i < len(cols)-1 {
			fmt.Fprintf(&b, "  %s %s%s,\n", quoteIdent(col.Name), col.DataType, suffix)
		} else {
			fmt.Fprintf(&b, "  %s %s%s\n", quoteIdent(col.Name), col.DataType, suffix)
		}
	}
	b.WriteString(");")
	return b.String(), nil
}

func quoteIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

func timeNowMs() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}

// ─── 表数据浏览（behavior.md §13）────────────────────────────

// BrowseRows 按表浏览行：共享构造器产参数化 SQL 后走自身执行管线。
func (c *Conn) BrowseRows(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	sqlText, args, err := dbcore.BuildRowsQuery(dbcore.MSSQLSQLDialect(), schema, table, req)
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
	sqlText, args, err := dbcore.BuildRowsCountQuery(dbcore.MSSQLSQLDialect(), schema, table, req)
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
