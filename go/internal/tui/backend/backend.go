// Package backend defines the interface between the TUI and live database
// connections. The only implementation is Adapter, which bridges to
// polydb's transport.Client (AGENTS.md 铁律 2: UI never touches drivers).
package backend

import (
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/protocol"
)

// ExecResult reports the outcome of a non-query statement.
type ExecResult struct {
	RowsAffected  int64
	LastInsertID  int64
	HasLastInsert bool // false for engines without insert ids (e.g. postgres)
}

// Result is either a row set (Frame != nil) or an exec outcome.
type Result struct {
	Frame *tuidata.Frame
	Exec  *ExecResult
}

// ColumnMeta describes one column of a live table, for the info overlay
// and the row sheet type display. DataType is the engine-native type name
// (e.g. "int", "varchar(255)", "text"); IsNullable is "YES"/"NO" or "" when
// unknown; Default is the column default literal or ""; Comment is the
// column comment or "".
type ColumnMeta struct {
	Name       string
	DataType   string
	IsNullable string
	Default    string
	Comment    string
}

// Backend is a live SQL database connection.
type Backend interface {
	// Kind identifies the engine: "mysql", "postgres", "sqlite", ...
	Kind() string
	// Title is a short human label for the connection (e.g. "mysql://host/db").
	Title() string
	// Run executes a statement, routing queries vs. mutations automatically.
	Run(stmt string) (Result, error)
	// Namespaces lists databases (mysql) or schemas (postgres). Engines
	// without namespaces return a single empty string.
	Namespaces() ([]string, error)
	// Tables lists the tables in a namespace.
	Tables(namespace string) ([]string, error)
	// FetchTable loads up to limit rows of a table.
	FetchTable(namespace, table string, limit int) (*tuidata.Frame, error)
	// PrimaryKeys lists the primary-key column names of namespace.table
	// (empty when the table has no primary key). Engines without namespaces
	// ignore namespace.
	PrimaryKeys(namespace, table string) ([]string, error)
	// ColumnsMeta returns column metadata for namespace.table, in ordinal
	// order. Engines without namespaces ignore namespace.
	ColumnsMeta(namespace, table string) ([]ColumnMeta, error)
	// ColumnIndexTypes returns a map of column name → index type label for each
	// indexed column in namespace.table. Labels are "PK", "UNIQUE", or "INDEX"
	// (highest priority wins when a column appears in multiple indexes).
	// Returns an empty map (not an error) when the table has no indexes.
	ColumnIndexTypes(namespace, table string) (map[string]string, error)
	Close() error
}

// Canceller is an optional Backend capability: run a statement under a
// client-assigned queryID so an in-flight query can be aborted (F8).
//
// It is deliberately separate from Backend because not every transport can
// cancel: pkg/transport's Local has no server-side in-flight registry, so
// Cancel there always reports "not hit" and cancellation is really the
// caller cancelling its own context. The UI probes with a type assertion and
// falls back to a plain Run when the backend does not implement it.
type Canceller interface {
	// RunQuery runs stmt under queryID (a UUID). The ID is what Cancel later
	// presents to the server; see transport.Client.ExecuteQuery.
	RunQuery(queryID, stmt string) (Result, error)
	// Cancel asks to abort the query running under queryID. The bool reports
	// whether the server had it in flight; a false with nil error means
	// "nothing to cancel" (best-effort, behavior.md §2.2) — not a failure.
	Cancel(queryID string) (bool, error)
}

// Blobber is an optional Backend capability: fetch the content behind a
// $polydb_blob reference (spec/encoding.md §5.2 — large values are stored
// out of band and the cell only carries the reference).
//
// Optional for the same reason as Canceller: it is a transport-level
// convenience (Client.GetBlob), not something every backend can promise.
type Blobber interface {
	// FetchBlob returns the bytes stored under blobID, which is the full
	// reference string ($polydb_blob:<uuid>).
	FetchBlob(blobID string) ([]byte, error)
}

// Filtered is an optional Backend capability: fetch a table with the filter
// pushed down to the server instead of pulling rows and filtering in the UI.
//
// Optional because the pushdown needs the engine to understand the conditions;
// without it the UI keeps filtering client-side on the rows it already has.
type Filtered interface {
	// FetchTableWithFilters is FetchTable plus server-side conditions, joined
	// with AND (see tui.ParseFilterConditions).
	FetchTableWithFilters(namespace, table string, limit int, conds []protocol.FilterCondition) (*tuidata.Frame, error)
}

// TxRunner is an optional Backend capability: run statements inside an
// explicit transaction, so a write is atomic (all-or-nothing) instead of a
// bare statement that partially applies if something fails midway.
//
// Optional because not every engine/transport offers transactions (and the
// Local transport's semantics differ); the UI falls back to a plain Run.
type TxRunner interface {
	// Begin opens a transaction and returns its id.
	Begin() (string, error)
	// ExecuteInTx runs one statement inside the transaction.
	ExecuteInTx(txID, stmt string) (Result, error)
	Commit(txID string) error
	Rollback(txID string) error
}

// KVBackend is a live key-value store connection (Redis).
type KVBackend interface {
	// Title is a short human label for the connection.
	Title() string
	// Do runs a raw command and returns a rendered result.
	Do(args []string) (string, error)
	// ScanKeys lists keys of one type, up to a server-side cap.
	ScanKeys(keyType string) ([]string, error)
	// Value fetches and renders the value stored at key.
	Value(key string) (string, error)
	Close() error
}

// Namespaced reports the namespace (database or schema) the connection is
// currently operating against. Optional: the UI probes it with a type
// assertion and degrades gracefully when a backend lacks it.
type Namespaced interface {
	CurrentNamespace() string
}
