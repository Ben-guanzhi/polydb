package protocol

import (
	"encoding/json"

	"github.com/vmihailenco/msgpack/v5"
)

type StatementType string

const (
	StatementTypeSelect StatementType = "select"
	StatementTypeInsert StatementType = "insert"
	StatementTypeUpdate StatementType = "update"
	StatementTypeDelete StatementType = "delete"
	StatementTypeDDL    StatementType = "ddl"
	StatementTypeOther  StatementType = "other"
)

type QueryRequest struct {
	SQL          string            `json:"sql" msgpack:"sql"`
	Params       []Value           `json:"params,omitempty" msgpack:"params,omitempty"`
	ConnectionID string            `json:"connection_id,omitempty" msgpack:"connection_id,omitempty"`
	QueryID      string            `json:"query_id,omitempty" msgpack:"query_id,omitempty"`
	Schema       string            `json:"schema,omitempty" msgpack:"schema,omitempty"`
	Pagination   *PaginationParams `json:"pagination,omitempty" msgpack:"pagination,omitempty"`
	TimeoutMs    *int64            `json:"timeout_ms,omitempty" msgpack:"timeout_ms,omitempty"`
	MaxRows      *int64            `json:"max_rows,omitempty" msgpack:"max_rows,omitempty"`
}

type QueryResult struct {
	Columns         []ResultColumn `json:"columns" msgpack:"columns"`
	Rows            [][]Value      `json:"rows" msgpack:"rows"`
	AffectedRows    int64          `json:"affected_rows" msgpack:"affected_rows"`
	ExecutionTimeMs float64        `json:"execution_time_ms" msgpack:"execution_time_ms"`
	Truncated       bool           `json:"truncated" msgpack:"truncated"`
	TotalRows       *int64         `json:"total_rows,omitempty" msgpack:"total_rows,omitempty"`
	HasMore         bool           `json:"has_more" msgpack:"has_more"`
	StatementType   StatementType  `json:"statement_type,omitempty" msgpack:"statement_type,omitempty"`
}

type ResultColumn struct {
	Name        string      `json:"name" msgpack:"name"`
	Table       *string     `json:"table,omitempty" msgpack:"table,omitempty"`
	DataType    string      `json:"type" msgpack:"type"`
	GenericType GenericType `json:"generic_type,omitempty" msgpack:"generic_type,omitempty"`
	Nullable    *bool       `json:"nullable,omitempty" msgpack:"nullable,omitempty"`
}

type BatchQueryRequest struct {
	Statements   []QueryRequest `json:"statements" msgpack:"statements"`
	ConnectionID string         `json:"connection_id,omitempty" msgpack:"connection_id,omitempty"`
	StopOnError  bool           `json:"stop_on_error" msgpack:"stop_on_error"`
}

type BatchQueryResult struct {
	Results              []BatchResultItem `json:"results" msgpack:"results"`
	TotalExecutionTimeMs float64           `json:"total_execution_time_ms" msgpack:"total_execution_time_ms"`
}

type BatchResultItem struct {
	Ok  *QueryResult `json:"ok" msgpack:"ok"`
	Err *PolyDBError `json:"err" msgpack:"err"`
}

// 契约（spec/schemas/query.json）规定结果项是 oneOf 裸对象（QueryResult 或 PolyDBError），
// 不是 {ok,err} 包装。自定义编解码以匹配 spec 与其他实现。
func (b BatchResultItem) MarshalJSON() ([]byte, error) {
	if b.Err != nil {
		return json.Marshal(b.Err)
	}
	if b.Ok != nil {
		return json.Marshal(b.Ok)
	}
	return []byte("null"), nil
}

func (b BatchResultItem) MarshalMsgpack() ([]byte, error) {
	var v any
	switch {
	case b.Err != nil:
		v = b.Err
	case b.Ok != nil:
		v = b.Ok
	}
	return msgpack.Marshal(v)
}
