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
func NewIntValue(v int64) Value {
	b, _ := json.Marshal(v)
	return Value{raw: json.RawMessage(b)}
}
func NewFloatValue(v float64) Value {
	b, _ := json.Marshal(v)
	return Value{raw: json.RawMessage(b)}
}
func NewStringValue(v string) Value {
	b, _ := json.Marshal(v)
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
