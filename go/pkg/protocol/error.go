package protocol

import "encoding/json"

type PolyDBError struct {
	Code      string          `json:"code" msgpack:"code"`
	Message   string          `json:"message" msgpack:"message"`
	Detail    json.RawMessage `json:"detail,omitempty" msgpack:"detail,omitempty"`
	Retryable bool            `json:"retryable,omitempty" msgpack:"retryable,omitempty"`
	Cause     string          `json:"cause,omitempty" msgpack:"cause,omitempty"`
}

func NewError(code, message string) PolyDBError {
	return PolyDBError{Code: code, Message: message}
}

func (e PolyDBError) WithRetryable() PolyDBError {
	e.Retryable = true
	return e
}

func (e PolyDBError) WithCause(cause string) PolyDBError {
	e.Cause = cause
	return e
}

func (e PolyDBError) Error() string {
	return "[" + e.Code + "] " + e.Message
}

const (
	ErrUnknown             = "POLYDB_ERR_UNKNOWN"
	ErrConnectionFailed    = "POLYDB_ERR_CONNECTION_FAILED"
	ErrConnectionNotFound  = "POLYDB_ERR_CONNECTION_NOT_FOUND"
	ErrConnectionExists    = "POLYDB_ERR_CONNECTION_EXISTS"
	ErrAuthFailed          = "POLYDB_ERR_AUTH_FAILED"
	ErrTimeout             = "POLYDB_ERR_TIMEOUT"
	ErrQueryFailed         = "POLYDB_ERR_QUERY_FAILED"
	ErrSyntaxError         = "POLYDB_ERR_SYNTAX_ERROR"
	ErrPermissionDenied    = "POLYDB_ERR_PERMISSION_DENIED"
	ErrSchemaNotFound      = "POLYDB_ERR_SCHEMA_NOT_FOUND"
	ErrTableNotFound       = "POLYDB_ERR_TABLE_NOT_FOUND"
	ErrColumnNotFound      = "POLYDB_ERR_COLUMN_NOT_FOUND"
	ErrDuplicateKey        = "POLYDB_ERR_DUPLICATE_KEY"
	ErrConstraintViolation = "POLYDB_ERR_CONSTRAINT_VIOLATION"
	ErrDeadlock            = "POLYDB_ERR_DEADLOCK"
	ErrTransactionFailed   = "POLYDB_ERR_TRANSACTION_FAILED"
	ErrTransactionNotFound = "POLYDB_ERR_TRANSACTION_NOT_FOUND"
	ErrQueryNotFound       = "POLYDB_ERR_QUERY_NOT_FOUND"
	ErrInvalidParam        = "POLYDB_ERR_INVALID_PARAM"
	ErrNotSupported        = "POLYDB_ERR_NOT_SUPPORTED"
	ErrDriverNotAvailable  = "POLYDB_ERR_DRIVER_NOT_AVAILABLE"
	ErrSSHTunnelFailed     = "POLYDB_ERR_SSH_TUNNEL_FAILED"
	ErrStorageFailed       = "POLYDB_ERR_STORAGE_FAILED"
	ErrKeyringFailed       = "POLYDB_ERR_KEYRING_FAILED"
	ErrCancelled           = "POLYDB_ERR_CANCELLED"
)
