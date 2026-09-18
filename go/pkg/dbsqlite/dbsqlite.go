package dbsqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

type Conn struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

func Open(ctx context.Context, path string) (*Conn, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// 内存库是每连接独享的，必须限制单连接，否则跨连接看不到彼此的表。
	if strings.Contains(path, ":memory:") {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	return &Conn{db: db, path: path}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindSQLite }
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

// ExecuteIn 在给定执行器上跑一条 SQL（*sql.DB 或 *sql.Tx）。
// 调用方应确保不持有 c.mu 时并发调用；本方法自行加锁。
func (c *Conn) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executeIn(ctx, exec, sql, args)
}

func (c *Conn) executeIn(ctx context.Context, exec dbcore.SQLTx, sql string, args []protocol.Value) (*protocol.QueryResult, error) {
	start := timeNowMs()
	stmtType := dbcore.DetectStatementType(sql)
	driverArgs := dbcore.ValueArgs(args)

	// SELECT / 返回行集的语句走 Query；否则走 Exec。
	if shouldQuery(stmtType, sql) {
		rows, err := exec.QueryContext(ctx, sql, driverArgs...)
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
			name := ct.Name()
			columns[i] = protocol.ResultColumn{Name: name, DataType: dbcore.DeclTypeOf(ct)}
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
				rowVals[i] = dbcore.DriverToValue(*(r.(*any)))
			}
			resultRows = append(resultRows, rowVals)
		}
		if err := rows.Err(); err != nil {
			return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
		}
		return &protocol.QueryResult{
			Columns:         columns,
			Rows:            resultRows,
			AffectedRows:    0,
			ExecutionTimeMs: timeNowMs() - start,
			StatementType:   stmtType,
		}, nil
	}

	res, err := exec.ExecContext(ctx, sql, driverArgs...)
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

// Begin 开启 SQLite 事务。SQLite 不支持隔离级别选项，TxMode 被忽略。
func (c *Conn) Begin(ctx context.Context, _ dbcore.TxMode) (*sql.Tx, error) {
	c.mu.Lock()
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	c.mu.Unlock()
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	return tx, nil
}

func (c *Conn) ListSchemas(ctx context.Context) ([]protocol.SchemaInfo, error) {
	return []protocol.SchemaInfo{{Name: "main"}}, nil
}

func (c *Conn) ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
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
		t := protocol.TableTypeTable
		if typ == "view" {
			t = protocol.TableTypeView
		}
		out = append(out, protocol.TableInfo{Name: name, Schema: schema, Type: t})
	}
	return out, rows.Err()
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	rows, err := c.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	out := make([]protocol.ColumnInfo, 0)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull int
		var def sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &def, &pk); err != nil {
			return nil, err
		}
		var defaultPtr *string
		if def.Valid {
			defaultPtr = &def.String
		}
		out = append(out, protocol.ColumnInfo{
			Name:            name,
			DataType:        dataType,
			Nullable:        notNull == 0,
			DefaultValue:    defaultPtr,
			IsPrimaryKey:    pk > 0,
			OrdinalPosition: int32(cid + 1),
		})
	}
	return out, rows.Err()
}

func (c *Conn) ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	rows, err := c.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	var indexes = []protocol.IndexInfo{}
	for rows.Next() {
		var seq int
		var name string
		var unique int
		if err := rows.Scan(&seq, &name, &unique); err != nil {
			rows.Close()
			return nil, err
		}
		cols, err := c.indexColumns(ctx, name)
		if err != nil {
			rows.Close()
			return nil, err
		}
		indexes = append(indexes, protocol.IndexInfo{Name: name, Unique: unique != 0, Columns: cols})
	}
	rows.Close()
	return indexes, rows.Err()
}

func (c *Conn) indexColumns(ctx context.Context, index string) ([]protocol.IndexColumn, error) {
	rows, err := c.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%q)", index))
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()
	out := make([]protocol.IndexColumn, 0)
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return nil, err
		}
		out = append(out, protocol.IndexColumn{Name: name, Position: int32(seqno + 1)})
	}
	return out, rows.Err()
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	rows, err := c.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
	if err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	defer rows.Close()

	type fkAgg struct {
		refTable string
		from, to []string
	}
	agg := map[int]*fkAgg{}
	var order []int
	for rows.Next() {
		var id, seq int
		var refTable, from, to string
		var onUpdate, onDelete string
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		if _, ok := agg[id]; !ok {
			agg[id] = &fkAgg{refTable: refTable}
			order = append(order, id)
		}
		agg[id].from = append(agg[id].from, from)
		agg[id].to = append(agg[id].to, to)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out = []protocol.ForeignKeyInfo{}
	for i, id := range order {
		a := agg[id]
		out = append(out, protocol.ForeignKeyInfo{
			Name:              fmt.Sprintf("fk_%d", i),
			Columns:           a.from,
			ReferencedSchema:  schema,
			ReferencedTable:   a.refTable,
			ReferencedColumns: a.to,
		})
	}
	return out, nil
}

func (c *Conn) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	var ddl string
	err := c.db.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl)
	if err == sql.ErrNoRows {
		return "", &protocol.PolyDBError{Code: protocol.ErrTableNotFound, Message: "table not found: " + table}
	}
	if err != nil {
		return "", &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	}
	return ddl, nil
}

func shouldQuery(stmtType protocol.StatementType, sql string) bool {
	if stmtType == protocol.StatementTypeSelect {
		return true
	}
	return strings.Contains(strings.ToUpper(sql), "RETURNING")
}

func timeNowMs() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}
