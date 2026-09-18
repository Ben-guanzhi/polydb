package protocol

import "time"

type IsolationLevel string

const (
	IsolationReadUncommitted IsolationLevel = "read_uncommitted"
	IsolationReadCommitted   IsolationLevel = "read_committed"
	IsolationRepeatableRead  IsolationLevel = "repeatable_read"
	IsolationSerializable    IsolationLevel = "serializable"
)

type TransactionStatus string

const (
	TxnActive     TransactionStatus = "active"
	TxnCommitted  TransactionStatus = "committed"
	TxnRolledBack TransactionStatus = "rolled_back"
)

type TransactionInfo struct {
	ID             string            `json:"id" msgpack:"id"`
	ConnectionID   string            `json:"connection_id" msgpack:"connection_id"`
	Status         TransactionStatus `json:"status" msgpack:"status"`
	StartedAt      time.Time         `json:"started_at" msgpack:"started_at"`
	IsolationLevel IsolationLevel    `json:"isolation_level,omitempty" msgpack:"isolation_level,omitempty"`
}

type BeginTransactionRequest struct {
	ConnectionID   string         `json:"connection_id" msgpack:"connection_id"`
	IsolationLevel IsolationLevel `json:"isolation_level,omitempty" msgpack:"isolation_level,omitempty"`
}
