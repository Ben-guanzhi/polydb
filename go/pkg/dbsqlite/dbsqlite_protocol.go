package dbsqlite

import (
	"context"
	"database/sql"

	dbc "github.com/dbcore/dbcore"
	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/dbcore/bridge"
	"github.com/polydb/polydb/pkg/protocol"
)

// ProtocolDriver is the polydb-legacy protocol-facing view of *Conn. polydb's appcore
// and polydb-cli (and the existing dbsqlite tests) speak protocol.* types, so they wrap
// the dbcore-native *Conn with this shim. It forwards directly to *Conn's protocol-native
// helper methods — no value conversion, zero overhead.
type ProtocolDriver struct{ c *Conn }

// NewProtocolDriver wraps a dbcore-native *Conn as polydb's protocol-typed dbcore.Driver.
func NewProtocolDriver(c *Conn) *ProtocolDriver { return &ProtocolDriver{c: c} }

// SQLTx exposes the dbcore-native transaction driver underneath (Option B):
// appcore hands it to dbcore.TxManager via bridge.SQLTxProvider.
func (p *ProtocolDriver) SQLTx() dbc.SQLTxDriver { return p.c }

func (p *ProtocolDriver) Kind() protocol.DatabaseKind    { return protocol.DatabaseKindSQLite }
func (p *ProtocolDriver) Ping(ctx context.Context) error { return p.c.Ping(ctx) }
func (p *ProtocolDriver) Close() error                   { return p.c.Close() }
func (p *ProtocolDriver) AsSQL() (pdb.SQLDriver, bool)   { return p, true }
func (p *ProtocolDriver) AsKV() (pdb.KVDriver, bool)     { return nil, false }

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

// ─── shared translation helpers (dbcore request/value → polydb) ───

func pageOffset(page, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	p := page
	if p < 1 {
		p = 1
	}
	return (p - 1) * pageSize
}

func mapFilterOp(op dbc.FilterOp) protocol.FilterOperator {
	switch op {
	case dbc.FilterEq:
		return protocol.FilterOpEq
	case dbc.FilterNeq:
		return protocol.FilterOpNe
	case dbc.FilterGt:
		return protocol.FilterOpGt
	case dbc.FilterLt:
		return protocol.FilterOpLt
	case dbc.FilterLike:
		return protocol.FilterOpLike
	}
	return protocol.FilterOpEq
}

func browseResultToDBCore(r *protocol.TableRowsResult, total uint64) *dbc.TableRowsResult {
	cols := make([]dbc.ColumnInfo, len(r.Columns))
	for i, c := range r.Columns {
		var nullable bool
		if c.Nullable != nil {
			nullable = *c.Nullable
		}
		cols[i] = dbc.ColumnInfo{Name: c.Name, Type: c.DataType, Nullable: nullable}
	}
	rows := make([][]dbc.Value, len(r.Rows))
	for i, row := range r.Rows {
		rows[i] = make([]dbc.Value, len(row))
		for j, cell := range row {
			rows[i][j] = bridge.ValueToDB(cell)
		}
	}
	return &dbc.TableRowsResult{Columns: cols, Rows: rows, Total: total}
}

// sqlTxShim adapts a dbcore.SQLTx to polydb's pdb.SQLTx (identical method set), so the
// wrapped *Conn's ExecuteIn can run on the transaction produced by dbcore.TxManager.
type sqlTxShim struct{ dbc.SQLTx }
