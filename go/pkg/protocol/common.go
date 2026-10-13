package protocol

import "encoding/json"

type DatabaseKind string

const (
	DatabaseKindSQLite   DatabaseKind = "sqlite"
	DatabaseKindMySQL    DatabaseKind = "mysql"
	DatabaseKindPostgres DatabaseKind = "postgres"
	DatabaseKindMSSQL    DatabaseKind = "mssql"
	DatabaseKindOracle   DatabaseKind = "oracle"
	DatabaseKindRedis    DatabaseKind = "redis"
)

type ConnectionID = string

// BlobRefPrefix 是 spec/encoding.md §5.2 旁路引用 ID 的前缀：
// 完整引用形如 $polydb_blob:<uuid>，REST 取回路径使用其后的 UUID。
const BlobRefPrefix = "$polydb_blob:"

type Value struct {
	raw json.RawMessage
}

func NewNullValue() Value { return Value{raw: json.RawMessage("null")} }
func NewBoolValue(v bool) Value {
	if v {
		return Value{raw: json.RawMessage("true")}
	}
	return Value{raw: json.RawMessage("false")}
}

// 三个标量构造器是**每格一次**的热路径（驱动把扫描出来的值转成 Value 都走这里），
// 所以不再借道 encoding/json 的 reflect：输出由 value_json.go 手写，与
// `json.Marshal` 逐字节对拍（TestValueConstructorsMatchJSONMarshal）。
// NewFloatValue 对 Inf/NaN 保持旧语义——json.Marshal 报错时返回 nil 原文，
// 也就是一个 NULL 单元格。
func NewIntValue(v int64) Value {
	return Value{raw: appendJSONNumberInt(nil, v)}
}
func NewFloatValue(v float64) Value {
	b, ok := appendJSONFloat(nil, v, 64)
	if !ok {
		return Value{}
	}
	return Value{raw: json.RawMessage(b)}
}
func NewStringValue(v string) Value {
	return Value{raw: appendJSONString(nil, v)}
}

// NewObjectValue 构造 JSON 对象 Value（如 spec/encoding.md §5 的 $blob 编码）。
func NewObjectValue(fields map[string]any) Value {
	b, _ := json.Marshal(fields)
	return Value{raw: json.RawMessage(b)}
}

func (v Value) MarshalJSON() ([]byte, error) {
	if v.raw == nil {
		return []byte("null"), nil
	}
	return v.raw, nil
}

func (v *Value) UnmarshalJSON(data []byte) error {
	v.raw = append(json.RawMessage(nil), data...)
	return nil
}

func (v Value) IsNull() bool {
	return len(v.raw) == 0 || string(v.raw) == "null"
}

type SshTunnelConfig struct {
	Host                    string `json:"host" msgpack:"host"`
	Port                    int    `json:"port" msgpack:"port"`
	Username                string `json:"username" msgpack:"username"`
	PasswordRef             string `json:"password_ref,omitempty" msgpack:"password_ref,omitempty"`
	Password                string `json:"password,omitempty" msgpack:"password,omitempty"`
	PrivateKeyPath          string `json:"private_key_path,omitempty" msgpack:"private_key_path,omitempty"`
	PrivateKeyPassphraseRef string `json:"private_key_passphrase_ref,omitempty" msgpack:"private_key_passphrase_ref,omitempty"`
	PrivateKeyPassphrase    string `json:"private_key_passphrase,omitempty" msgpack:"private_key_passphrase,omitempty"`
}

type PaginationParams struct {
	Offset int64 `json:"offset,omitempty" msgpack:"offset,omitempty"`
	Limit  int   `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type SortOrder string

const (
	SortOrderAsc  SortOrder = "asc"
	SortOrderDesc SortOrder = "desc"
)

type GenericType string

const (
	GenericTypeBoolean   GenericType = "boolean"
	GenericTypeSmallint  GenericType = "smallint"
	GenericTypeInteger   GenericType = "integer"
	GenericTypeBigint    GenericType = "bigint"
	GenericTypeFloat     GenericType = "float"
	GenericTypeDouble    GenericType = "double"
	GenericTypeDecimal   GenericType = "decimal"
	GenericTypeNumeric   GenericType = "numeric"
	GenericTypeChar      GenericType = "char"
	GenericTypeVarchar   GenericType = "varchar"
	GenericTypeText      GenericType = "text"
	GenericTypeBinary    GenericType = "binary"
	GenericTypeVarbinary GenericType = "varbinary"
	GenericTypeBlob      GenericType = "blob"
	GenericTypeDate      GenericType = "date"
	GenericTypeTime      GenericType = "time"
	GenericTypeDatetime  GenericType = "datetime"
	GenericTypeTimestamp GenericType = "timestamp"
	GenericTypeInterval  GenericType = "interval"
	GenericTypeJSON      GenericType = "json"
	GenericTypeJSONB     GenericType = "jsonb"
	GenericTypeXML       GenericType = "xml"
	GenericTypeUUID      GenericType = "uuid"
	GenericTypeArray     GenericType = "array"
	GenericTypeEnum      GenericType = "enum"
	GenericTypeOther     GenericType = "other"
)
