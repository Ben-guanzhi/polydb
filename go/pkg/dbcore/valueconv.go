package dbcore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

// ValueToDriver 将协议 Value 转为 database/sql 驱动参数。
func ValueToDriver(v protocol.Value) any {
	if v.IsNull() {
		return nil
	}
	if s, ok := protocol.TryGetString(v); ok {
		return s
	}
	if i, ok := protocol.TryGetInt(v); ok {
		return i
	}
	raw, _ := json.Marshal(v)
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	return string(raw)
}

// DriverToValue 将 database/sql 扫描出的值转为协议 Value。
func DriverToValue(v any) protocol.Value {
	switch t := v.(type) {
	case nil:
		return protocol.NewNullValue()
	case int64:
		return protocol.NewIntValue(t)
	case int:
		return protocol.NewIntValue(int64(t))
	case float64:
		return protocol.NewFloatValue(t)
	case float32:
		return protocol.NewFloatValue(float64(t))
	case bool:
		return protocol.NewBoolValue(t)
	case string:
		return protocol.NewStringValue(t)
	case []byte:
		return protocol.NewStringValue(fmt.Sprintf("<blob %d bytes>", len(t)))
	case time.Time:
		return protocol.NewStringValue(t.Format("2006-01-02 15:04:05"))
	case fmt.Stringer:
		return protocol.NewStringValue(t.String())
	default:
		return protocol.NewStringValue(fmt.Sprintf("%v", t))
	}
}

// DeclTypeOf 返回数据库列类型名，取不到时回退为 TEXT。
func DeclTypeOf(ct *sql.ColumnType) string {
	if t := ct.DatabaseTypeName(); t != "" {
		return t
	}
	return "TEXT"
}

func ValueArgs(args []protocol.Value) []any {
	out := make([]any, len(args))
	for i, v := range args {
		out[i] = ValueToDriver(v)
	}
	return out
}
