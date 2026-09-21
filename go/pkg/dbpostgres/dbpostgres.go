// Package dbpostgres 实现基于 jackc/pgx/v5（stdlib 适配）的 SQL 驱动。
package dbpostgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

type Conn struct {
	db  *sql.DB
	dsn string
	mu  sync.Mutex
}

// Open 通过 DSN（postgres://user:pass@host:port/dbname）建立连接池。
func Open(ctx context.Context, dsn string) (*Conn, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Conn{db: db, dsn: dsn}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindPostgres }
func (c *Conn) AsSQL() (dbcore.SQLDriver, bool) { return c, true }
func (c *Conn) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (c *Conn) Ping(ctx context.Context) error {
	return c.db.PingContext(ctx)
}

func (c *Conn) Close() error {
	return c.db.Close()
}

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

func (c *Conn) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT nspname FROM pg_catalog.pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname")
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
		`SELECT c.relname,
		        CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' ELSE 'table' END
		   FROM pg_catalog.pg_class c
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = $1 AND c.relkind IN ('r','v','m','p') ORDER BY c.relname`, schema)
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
		out = append(out, protocol.TableInfo{
			Name:   name,
			Schema: schema,
			Type:   protocol.TableType(typ),
		})
	}
	return out, rows.Err()
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT a.attname,
		        format_type(a.atttypid, a.atttypmod),
		        NOT a.attnotnull,
		        pg_get_expr(d.adbin, d.adrelid),
		        COALESCE(a.attidentity <> '', a.attgenerated <> ''),
		        a.attnum
		   FROM pg_catalog.pg_attribute a
		   JOIN pg_catalog.pg_class cl ON cl.oid = a.attrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
		   LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE n.nspname = $1 AND cl.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		  ORDER BY a.attnum`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()

	var cols []protocol.ColumnInfo
	for rows.Next() {
		var name, dataType string
		var nullable bool
		var def sql.NullString
		var auto bool
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

	// 主键列标记（复合主键全部标记）。
	pkRows, err := c.db.QueryContext(ctx,
		`SELECT a.attname
		   FROM pg_catalog.pg_index i
		   JOIN pg_catalog.pg_class cl ON cl.oid = i.indrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
		   JOIN pg_catalog.pg_attribute a ON a.attrelid = cl.oid AND a.attnum = ANY(i.indkey)
		  WHERE n.nspname = $1 AND cl.relname = $2 AND i.indisprimary`, schema, table)
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
		`SELECT c.relname,
		        i.indisunique,
		        i.indisprimary
		   FROM pg_catalog.pg_index i
		   JOIN pg_catalog.pg_class cl ON cl.oid = i.indrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
		   JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		  WHERE n.nspname = $1 AND cl.relname = $2 ORDER BY c.relname`, schema, table)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()

	out := make([]protocol.IndexInfo, 0)
	for rows.Next() {
		var name string
		var unique, primary bool
		if err := rows.Scan(&name, &unique, &primary); err != nil {
			return nil, err
		}
		cols, err := c.indexColumns(ctx, schema, name)
		if err != nil {
			return nil, err
		}
		out = append(out, protocol.IndexInfo{Name: name, Unique: unique, Primary: primary, Columns: cols})
	}
	return out, rows.Err()
}

func (c *Conn) indexColumns(ctx context.Context, schema, index string) ([]protocol.IndexColumn, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT a.attname, i.N
		   FROM pg_catalog.pg_index i
		   JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		   CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS ik(attnum, N)
		   JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = ik.attnum
		  WHERE n.nspname = $1 AND c.relname = $2 ORDER BY ik.N`, schema, index)
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	out := make([]protocol.IndexColumn, 0)
	for rows.Next() {
		var name string
		var pos int64
		if err := rows.Scan(&name, &pos); err != nil {
			return nil, err
		}
		out = append(out, protocol.IndexColumn{Name: name, Position: int32(pos)})
	}
	return out, rows.Err()
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT con.conname,
		        fk.attname,
		        ns.nspname,
		        rel.relname,
		        pk.attname
		   FROM pg_catalog.pg_constraint con
		   JOIN pg_catalog.pg_class cl ON cl.oid = con.conrelid
		   JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
		   CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS fk(attnum, N)
		   JOIN pg_catalog.pg_attribute fka ON fka.attrelid = cl.oid AND fka.attnum = fk.attnum
		   JOIN pg_catalog.pg_class rel ON rel.oid = con.confrelid
		   JOIN pg_catalog.pg_namespace ns ON ns.oid = rel.relnamespace
		   CROSS JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS pk(attnum, N2)
		   JOIN pg_catalog.pg_attribute pka ON pka.attrelid = rel.oid AND pka.attnum = pk.attnum
		  WHERE con.contype = 'f' AND n.nspname = $1 AND cl.relname = $2 AND fk.N = pk.N2
		  ORDER BY con.conname, fk.N`, schema, table)
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
		case col.IsAutoIncrement:
			suffix = " GENERATED ALWAYS AS IDENTITY"
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
	sqlText, args, err := dbcore.BuildRowsQuery(dbcore.DefaultSQLDialect(), schema, table, req)
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
	sqlText, args, err := dbcore.BuildRowsCountQuery(dbcore.DefaultSQLDialect(), schema, table, req)
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
