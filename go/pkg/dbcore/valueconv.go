package dbcore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// DriverToValueTyped 与 DriverToValue 相同，但额外利用列声明类型：
// go-sql-driver/mysql 对一切非 TEXT 结果集列都返回 []byte，
// 需按 declType 把字节解码为整数/浮点/字符串，与 Rust sqlx 行为对齐。
func DriverToValueTyped(v any, declType string) protocol.Value {
	b, ok := v.([]byte)
	if !ok {
		return DriverToValue(v)
	}
	switch sqlBaseType(declType) {
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "MEDIUMINT", "TINYINT",
		"SERIAL", "BIGSERIAL", "SMALLSERIAL", "INT2", "INT4", "INT8":
		if i, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			return protocol.NewIntValue(i)
		}
	case "FLOAT", "DOUBLE", "REAL", "FLOAT4", "FLOAT8", "DOUBLEPRECISION":
		if f, err := strconv.ParseFloat(string(b), 64); err == nil {
			return protocol.NewFloatValue(f)
		}
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY",
		"BYTEA", "BIT", "IMAGE":
		return protocol.NewStringValue(fmt.Sprintf("<blob %d bytes>", len(b)))
	}
	return protocol.NewStringValue(string(b))
}

// sqlBaseType 归一化列类型名：转大写、去掉参数与修饰词（UNSIGNED/ZEROFILL 等）。
// 注意 go-sql-driver 会给出 "UNSIGNED BIGINT" 这类前置修饰名。
func sqlBaseType(declType string) string {
	t := strings.ToUpper(declType)
	t = strings.ReplaceAll(t, "UNSIGNED", "")
	t = strings.ReplaceAll(t, "ZEROFILL", "")
	t = strings.TrimSpace(t)
	if i := strings.IndexAny(t, "( "); i >= 0 {
		t = t[:i]
	}
	t = strings.ReplaceAll(t, "_", "")
	return t
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
