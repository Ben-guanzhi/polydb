// Package dboracle 实现基于 sijms/go-ora（纯 Go，免 cgo）的 SQL 驱动。
package dboracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
	_ "github.com/sijms/go-ora/v2"
)

type Conn struct {
	db  *sql.DB
	dsn string
	mu  sync.Mutex
}

// Open 通过 DSN（oracle://user:pass@host:port/service）建立连接池。
func Open(ctx context.Context, dsn string) (*Conn, error) {
	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, fmt.Errorf("open oracle: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping oracle: %w", err)
	}
	return &Conn{db: db, dsn: dsn}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindOracle }
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

// Begin 开启一个新的事务。go-ora 与 godror 都支持 BeginTxOptions；
// TxOptionsFromMode 会把不支持的隔离级别退化为 LevelDefault。
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

// maintainedSchemas 是 Oracle 自带维护 schema，默认不展示。
var maintainedSchemas = map[string]bool{
	"SYS": true, "SYSTEM": true, "OUTLN": true, "DBSNMP": true, "APPQOSSYS": true,
	"CTXSYS": true, "MDSYS": true, "ORDSYS": true, "ORDDATA": true, "ORDPLUGINS": true,
	"SI_INFORMTN_SCHEMA": true, "WMSYS": true, "XDB": true, "XS$NULL": true, "DVSYS": true,
	"AUDSYS": true, "GSMADMIN_INTERNAL": true, "OJVMSYS": true, "LBACSYS": true,
}

func (c *Conn) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT username FROM all_users WHERE oracle_maintained = 'N' ORDER BY username`)
	if err != nil {
		// 低版本 Oracle 无 oracle_maintained 列，退化为排除清单。
		var where []string
		for s := range maintainedSchemas {
			where = append(where, fmt.Sprintf("'%s'", s))
		}
		rows, err = c.db.QueryContext(ctx,
			"SELECT username FROM all_users WHERE username NOT IN ("+strings.Join(where, ",")+") ORDER BY username")
		if err != nil {
			return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
		}
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
		`SELECT table_name, 'table' FROM all_tables WHERE owner = ?
		  UNION ALL
		  SELECT view_name, 'view' FROM all_views WHERE owner = ?
		  ORDER BY 1`, schema, schema)
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
		`SELECT c.column_name,
		        c.data_type || CASE
		          WHEN c.data_type IN ('VARCHAR2','VARCHAR','CHAR','NVARCHAR2','NCHAR')
		            THEN '(' || c.char_length || ')'
		          WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL
		            THEN '(' || c.data_precision || ',' || NVL(c.data_scale, 0) || ')'
		          ELSE '' END,
		        c.nullable = 'Y', c.column_id,
		        CASE WHEN ic.column_name IS NOT NULL THEN 1 ELSE 0 END
		   FROM all_tab_columns c
		   LEFT JOIN all_tab_identity_cols ic
		          ON ic.owner = c.owner AND ic.table_name = c.table_name AND ic.column_name = c.column_name
		  WHERE c.owner = ? AND c.table_name = ?
		  ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	var cols []protocol.ColumnInfo
	for rows.Next() {
		var name, dataType string
		var nullable bool
		var pos int
		var auto int
		if err := rows.Scan(&name, &dataType, &nullable, &pos, &auto); err != nil {
			return nil, err
		}
		cols = append(cols, protocol.ColumnInfo{
			Name:            name,
			DataType:        dataType,
			Nullable:        nullable,
			IsAutoIncrement: auto == 1,
			OrdinalPosition: int32(pos),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pkRows, err := c.db.QueryContext(ctx,
		`SELECT cc.column_name
		   FROM all_constraints c
		   JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		  WHERE c.owner = ? AND c.table_name = ? AND c.constraint_type = 'P'
		  ORDER BY cc.position`, schema, table)
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
		`SELECT i.index_name, i.uniqueness, i.index_type, ic.column_name, ic.column_position
		   FROM all_indexes i
		   JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
		  WHERE i.table_owner = ? AND i.table_name = ?
		  ORDER BY i.index_name, ic.column_position`, schema, table)
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
	primary := map[string]bool{}
	pc, err := c.db.QueryContext(ctx,
		`SELECT constraint_name FROM all_constraints WHERE owner = ? AND table_name = ? AND constraint_type = 'P'`,
		schema, table)
	if err == nil {
		defer pc.Close()
		for pc.Next() {
			var name string
			if err := pc.Scan(&name); err == nil {
				primary[name] = true
			}
		}
	}
	for rows.Next() {
		var name, uniqueness, typeDesc, col string
		var pos int
		if err := rows.Scan(&name, &uniqueness, &typeDesc, &col, &pos); err != nil {
			return nil, err
		}
		if _, ok := groups[name]; !ok {
			order = append(order, name)
			groups[name] = &idx{
				unique:  strings.EqualFold(uniqueness, "UNIQUE"),
				primary: primary[name],
				typ:     oracleIndexType(typeDesc),
			}
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

func oracleIndexType(desc string) protocol.IndexType {
	switch {
	case strings.Contains(strings.ToUpper(desc), "BITMAP"):
		return protocol.IndexTypeHash
	case strings.Contains(strings.ToUpper(desc), "FUNCTION"):
		return protocol.IndexTypeOther
	default:
		return protocol.IndexTypeBTree
	}
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT c.constraint_name, c1.column_name, rc.owner, rc.table_name, c2.column_name
		   FROM all_constraints c
		   JOIN all_cons_columns c1 ON c1.owner = c.owner AND c1.constraint_name = c.constraint_name
		   JOIN all_constraints rc ON rc.owner = c.r_owner AND rc.constraint_name = c.r_constraint_name
		   JOIN all_cons_columns c2 ON c2.owner = rc.owner AND c2.constraint_name = rc.constraint_name AND c2.position = c1.position
		  WHERE c.owner = ? AND c.table_name = ? AND c.constraint_type = 'R'
		  ORDER BY c.constraint_name, c1.position`, schema, table)
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
		if err := rows.Scan(&name, &from, &refSchema, &refTable, &to); err != nil {
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
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func timeNowMs() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}

// ─── 表数据浏览（behavior.md §13）────────────────────────────

// BrowseRows 按表浏览行：共享构造器产参数化 SQL 后走自身执行管线。
func (c *Conn) BrowseRows(ctx context.Context, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	sqlText, args, err := dbcore.BuildRowsQuery(dbcore.OracleSQLDialect(), schema, table, req)
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
	sqlText, args, err := dbcore.BuildRowsCountQuery(dbcore.OracleSQLDialect(), schema, table, req)
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
