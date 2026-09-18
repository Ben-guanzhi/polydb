package protocol

type ClientMessageType string

const (
	ClientMsgHello       ClientMessageType = "hello"
	ClientMsgQuery       ClientMessageType = "query"
	ClientMsgQueryCancel ClientMessageType = "query_cancel"
)

type ClientMessage struct {
	Type          ClientMessageType `json:"type" msgpack:"type"`
	ConnectionID  string            `json:"connection_id,omitempty" msgpack:"connection_id,omitempty"`
	ClientVersion string            `json:"client_version,omitempty" msgpack:"client_version,omitempty"`
	QueryID       string            `json:"query_id,omitempty" msgpack:"query_id,omitempty"`
	SQL           string            `json:"sql,omitempty" msgpack:"sql,omitempty"`
	Params        []Value           `json:"params,omitempty" msgpack:"params,omitempty"`
	Schema        string            `json:"schema,omitempty" msgpack:"schema,omitempty"`
	Pagination    *PaginationParams `json:"pagination,omitempty" msgpack:"pagination,omitempty"`
	TimeoutMs     *int64            `json:"timeout_ms,omitempty" msgpack:"timeout_ms,omitempty"`
	MaxRows       *int64            `json:"max_rows,omitempty" msgpack:"max_rows,omitempty"`
}

type ServerMessageType string

const (
	ServerMsgHelloAck       ServerMessageType = "hello_ack"
	ServerMsgQueryStarted   ServerMessageType = "query_started"
	ServerMsgQueryProgress  ServerMessageType = "query_progress"
	ServerMsgQueryResult    ServerMessageType = "query_result"
	ServerMsgQueryError     ServerMessageType = "query_error"
	ServerMsgQueryCancelled ServerMessageType = "query_cancelled"
)

type ServerMessage struct {
	Type          ServerMessageType `json:"type" msgpack:"type"`
	QueryID       string            `json:"query_id,omitempty" msgpack:"query_id,omitempty"`
	ServerVersion string            `json:"server_version,omitempty" msgpack:"server_version,omitempty"`
	DbVersion     string            `json:"db_version,omitempty" msgpack:"db_version,omitempty"`
	RowsFetched   *int64            `json:"rows_fetched,omitempty" msgpack:"rows_fetched,omitempty"`
	Message       string            `json:"message,omitempty" msgpack:"message,omitempty"`
	Result        *QueryResult      `json:"result,omitempty" msgpack:"result,omitempty"`
	Error         *PolyDBError      `json:"error,omitempty" msgpack:"error,omitempty"`
	RowsReturned  *int64            `json:"rows_returned,omitempty" msgpack:"rows_returned,omitempty"`
}
