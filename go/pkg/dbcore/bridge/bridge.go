// Package bridge is the thin re-export / interop layer that makes polydb a consumer
// of the shared github.com/dbcore/dbcore module (DB-CORE-CONTRACT.md, consolidation
// step "polydb adoption — Option A: thin re-export").
//
// Design notes / why a separate package (not aliases in pkg/dbcore):
//   - polydb's pkg/dbcore already defines TxMode, TxOptionsFromMode, SQLTx using
//     protocol.*-typed interfaces. dbcore uses its own self-typed values by design
//     (contract §2: dbcore must not depend on any app's protocol package). A literal
//     `type X = dbcore.X` alias of the interfaces is therefore impossible — the
//     method signatures differ in parameter/return types, and a polydb driver does
//     NOT satisfy dbcore.SQLTxDriver.
//   - This package therefore re-exports the protocol-free subset of dbcore (DatabaseKind,
//     TxMode, TransactionStatus, TransactionInfo, TxManager, Error, sentinels) and
//     provides the Value / QueryResult converters that are the real interop primitives.
//     Existing polydb consumers are untouched (smallest blast radius). Migrating the
//     polydb interfaces themselves onto dbcore types is the separate Option B effort.
package bridge

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dbcore/dbcore"
	polydbdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// ─── Re-exported shared types (protocol-free subset of dbcore) ───────────────

type (
	DatabaseKind      = dbcore.DatabaseKind
	TxMode            = dbcore.TxMode
	TransactionStatus = dbcore.TransactionStatus
	TransactionInfo   = dbcore.TransactionInfo
	TxManager         = dbcore.TxManager
	Error             = dbcore.Error
)

const (
	KindSQLite   = dbcore.KindSQLite
	KindPostgres = dbcore.KindPostgres
	KindMySQL    = dbcore.KindMySQL
	KindMSSQL    = dbcore.KindMSSQL
	KindOracle   = dbcore.KindOracle
	KindRedis    = dbcore.KindRedis

	ErrCodeNotSupported        = dbcore.ErrCodeNotSupported
	ErrCodeTransactionNotFound = dbcore.ErrCodeTransactionNotFound
	ErrCodeTransactionFailed   = dbcore.ErrCodeTransactionFailed
)

// Sentinel errors mirror dbcore's so polydb code can errors.Is against one source.
var (
	ErrNotSupported        = dbcore.ErrNotSupported
	ErrTransactionNotFound = dbcore.ErrTransactionNotFound
	ErrTransactionFailed   = dbcore.ErrTransactionFailed
)

// TxOptionsFromMode maps a TxMode to *sql.TxOptions (delegates to dbcore).
func TxOptionsFromMode(mode TxMode) *sql.TxOptions { return dbcore.TxOptionsFromMode(mode) }

// NewTxManager starts a dbcore transaction manager over a dbcore.SQLTxDriver.
// polydb's SQL drivers satisfy the native contract since Option B (their *Conn
// implements dbcore.SQLTxDriver; reach it through SQLTxProvider on the
// ProtocolDriver).
func NewTxManager(d dbcore.SQLTxDriver) *TxManager { return dbcore.NewTxManager(d) }

// SQLTxProvider is how a protocol-typed driver exposes the dbcore-native
// transaction driver underneath it (Option B). Every polydb SQL driver package
// (dbsqlite / dbmysql / dbpostgres / dbmssql / dboracle) implements it on its
// ProtocolDriver; appcore uses it to hand dbcore.TxManager the real driver.
type SQLTxProvider interface {
	SQLTx() dbcore.SQLTxDriver
}

// ─── Value conversion (protocol.Value ↔ dbcore.Value) ───────────────────────

// ValueFromDB converts a dbcore.Value into polydb's protocol.Value.
// Blobs cross via the wire contract (spec/encoding.md §5.1 $blob encoding,
// ≤1MB inline base64, >1MB bypass ref when the blob sink is up), not as a
// raw string — the old string(v.B) path embedded arbitrary binary into a
// JSON string, which encoding/json then mangled through U+FFFD replacement
// on the client side. Times keep the RFC3339Nano text form.
func ValueFromDB(v dbcore.Value) protocol.Value {
	switch v.Kind {
	case dbcore.ValNull:
		return protocol.NewNullValue()
	case dbcore.ValInt:
		return protocol.NewIntValue(v.I)
	case dbcore.ValFloat:
		return protocol.NewFloatValue(v.F)
	case dbcore.ValText:
		return protocol.NewStringValue(v.S)
	case dbcore.ValBool:
		return protocol.NewBoolValue(v.Bool)
	case dbcore.ValBlob:
		return polydbdb.BlobValue(v.B)
	case dbcore.ValTime:
		return protocol.NewStringValue(v.T.Format(time.RFC3339Nano))
	case dbcore.ValDecimal:
		// Exact digits cross as text: protocol.Value is JSON-backed and would
		// round them through float64, which is what ValDecimal exists to avoid.
		return protocol.NewStringValue(v.S)
	default:
		return protocol.NewNullValue()
	}
}

// ValueToDB converts a polydb protocol.Value into a dbcore.Value. Numbers that are
// integral become ValInt; others ValFloat. Objects/arrays fall back to ValText.
//
// The decode uses json.Decoder.UseNumber: the default unmarshal turns every
// number into float64, whose 53-bit mantissa silently rounds integers past
// 2^53 — 9007199254740993 arrived as 9007199254740992. The literal digits are
// parsed with strconv instead, so a bigint parameter keeps its low bits.
func ValueToDB(p protocol.Value) dbcore.Value {
	if p.IsNull() {
		return dbcore.Value{Kind: dbcore.ValNull}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return dbcore.Value{Kind: dbcore.ValNull}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return dbcore.Value{Kind: dbcore.ValText, S: string(raw)}
	}
	switch t := x.(type) {
	case bool:
		return dbcore.Value{Kind: dbcore.ValBool, Bool: t}
	case json.Number:
		if i, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			return dbcore.Value{Kind: dbcore.ValInt, I: i}
		}
		if f, err := strconv.ParseFloat(t.String(), 64); err == nil {
			return dbcore.Value{Kind: dbcore.ValFloat, F: f}
		}
		// Not representable as int64 or float64 (a 30-digit integer): pass
		// the digits as text rather than rounding them through a float.
		return dbcore.Value{Kind: dbcore.ValText, S: t.String()}
	case string:
		return dbcore.Value{Kind: dbcore.ValText, S: t}
	case nil:
		return dbcore.Value{Kind: dbcore.ValNull}
	default:
		return dbcore.Value{Kind: dbcore.ValText, S: string(raw)}
	}
}

// ─── QueryResult conversion ──────────────────────────────────────────────────

// QueryResultFromDB maps a dbcore.QueryResult onto polydb's protocol.QueryResult.
// dbcore's columns carry Name/Type/Nullable; protocol's additionally carries
// GenericType/Table which are left at zero values (acceptable for interop).
func QueryResultFromDB(r *dbcore.QueryResult) *protocol.QueryResult {
	if r == nil {
		return nil
	}
	cols := make([]protocol.ResultColumn, len(r.Columns))
	for i, c := range r.Columns {
		cols[i] = protocol.ResultColumn{
			Name:     c.Name,
			DataType: c.Type,
			Nullable: &c.Nullable,
		}
	}
	rows := make([][]protocol.Value, len(r.Rows))
	for i, row := range r.Rows {
		rows[i] = make([]protocol.Value, len(row))
		for j, cell := range row {
			rows[i][j] = ValueFromDB(cell)
		}
	}
	return &protocol.QueryResult{
		Columns:      cols,
		Rows:         rows,
		AffectedRows: r.AffectedRows,
	}
}

// QueryResultToDB maps a polydb protocol.QueryResult onto dbcore.QueryResult.
func QueryResultToDB(r *protocol.QueryResult) *dbcore.QueryResult {
	if r == nil {
		return nil
	}
	cols := make([]dbcore.ColumnInfo, len(r.Columns))
	for i, c := range r.Columns {
		var nullable bool
		if c.Nullable != nil {
			nullable = *c.Nullable
		}
		cols[i] = dbcore.ColumnInfo{
			Name:     c.Name,
			Type:     c.DataType,
			Nullable: nullable,
		}
	}
	rows := make([][]dbcore.Value, len(r.Rows))
	for i, row := range r.Rows {
		rows[i] = make([]dbcore.Value, len(row))
		for j, cell := range row {
			rows[i][j] = ValueToDB(cell)
		}
	}
	return &dbcore.QueryResult{
		Columns:      cols,
		Rows:         rows,
		AffectedRows: r.AffectedRows,
	}
}
