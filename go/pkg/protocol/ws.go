package protocol

type ClientMessageType string

const (
	ClientMsgHello          ClientMessageType = "hello"
	ClientMsgQuery          ClientMessageType = "query"
	ClientMsgQueryCancel    ClientMessageType = "query_cancel"
	ClientMsgBrokerRegister ClientMessageType = "broker_register"
	ClientMsgBrokerRevoke   ClientMessageType = "broker_revoke"
	ClientMsgBrokerSetGrant ClientMessageType = "broker_set_granted"
	ClientMsgProviderAttach ClientMessageType = "provider_attach"
	ClientMsgProviderDetach ClientMessageType = "provider_detach"
	ClientMsgProviderResult ClientMessageType = "provider_result"
)

// WsAuth 承载 hello 阶段的服务端鉴权凭据（behavior.md §12.2）。
// 服务端未启用 POLYDB_SERVER_TOKEN 时忽略该字段。
type WsAuth struct {
	Token string `json:"token,omitempty" msgpack:"token,omitempty"`
}

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
	Auth          *WsAuth           `json:"auth,omitempty" msgpack:"auth,omitempty"`
	// ProviderKey 是 query 消息上的可选 broker key（behavior.md §16.8）。
	// 与 QueryRequest.ProviderKey 同一 wire 字段；拍平 struct 必须显式声明，
	// 否则 msgpack 解码会丢掉它。
	ProviderKey string `json:"provider_key,omitempty" msgpack:"provider_key,omitempty"`
	// ─── 进程外 driver broker（behavior.md §16）─────────
	Provider *DriverProvider   `json:"provider,omitempty" msgpack:"provider,omitempty"`
	Key      DriverProviderKey `json:"key,omitempty" msgpack:"key,omitempty"`
	// ProviderResult（provider → 宿主）复用拍平 struct 的结果/错误字段。
	Result *QueryResult `json:"result,omitempty" msgpack:"result,omitempty"`
	Error  *PolyDBError `json:"error,omitempty" msgpack:"error,omitempty"`
	// GrantedCapabilities 是 broker_set_granted 的生效能力集合（§16.10.2）。
	GrantedCapabilities []DriverCapability `json:"granted_capabilities,omitempty" msgpack:"granted_capabilities,omitempty"`
	// Commutative 是 provider_attach 的 witnessed coeffect（§16.18，Thm 47）：
	// 实例可互换，多实例并存（负载分担）。缺省 false。
	Commutative bool `json:"commutative,omitempty" msgpack:"commutative,omitempty"`
}

type ServerMessageType string

const (
	ServerMsgHelloAck       ServerMessageType = "hello_ack"
	ServerMsgQueryStarted   ServerMessageType = "query_started"
	ServerMsgQueryProgress  ServerMessageType = "query_progress"
	ServerMsgQueryResult    ServerMessageType = "query_result"
	ServerMsgQueryError     ServerMessageType = "query_error"
	ServerMsgQueryCancelled ServerMessageType = "query_cancelled"
	ServerMsgBrokerState    ServerMessageType = "broker_state"
	ServerMsgBrokerEvent    ServerMessageType = "broker_event"
	ServerMsgProviderAck    ServerMessageType = "provider_ack"
	ServerMsgProviderExec   ServerMessageType = "provider_exec"
	ServerMsgProviderCancel ServerMessageType = "provider_cancel"
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
	// ─── 进程外 driver broker（behavior.md §16）─────────
	Providers []DriverProviderState `json:"providers,omitempty" msgpack:"providers,omitempty"`
	Event     DriverBrokerEvent     `json:"event,omitempty" msgpack:"event,omitempty"`
	Key       DriverProviderKey     `json:"key,omitempty" msgpack:"key,omitempty"`
	Reason    string                `json:"reason,omitempty" msgpack:"reason,omitempty"`
	// Attached 是 ProviderAck 的宿主视图：当前所有已登记的执行端 keys。
	Attached []DriverProviderKey `json:"attached,omitempty" msgpack:"attached,omitempty"`
	// CallbackURL 是阶段 9 的仅回环回调端点地址（仅进程端点收到；§16.14.2）。
	CallbackURL string `json:"callback_url,omitempty" msgpack:"callback_url,omitempty"`
	// CallbackToken 是本次 attach 的回调令牌（仅进程端点收到；§16.14.2）。
	CallbackToken string `json:"callback_token,omitempty" msgpack:"callback_token,omitempty"`
	// Handle 是阶段 9 的不透明连接句柄（仅进程端点收到；§16.14.1）。
	Handle string `json:"handle,omitempty" msgpack:"handle,omitempty"`
	// ProviderExec（宿主 → provider）复用拍平 struct 的 SQL/Params 字段。
	SQL    string  `json:"sql,omitempty" msgpack:"sql,omitempty"`
	Params []Value `json:"params,omitempty" msgpack:"params,omitempty"`
}
