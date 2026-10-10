package dbcore

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

// MaxInlineBlobBytes 是二进制单元格内联 base64 的阈值（spec/encoding.md §5），
// 与 Rust polydb_protocol::common::MAX_INLINE_BLOB_BYTES 必须一致。
const MaxInlineBlobBytes = 1024 * 1024

// BlobValue 按 spec/encoding.md §5 约定编码二进制单元格：
// ≤阈值 {"$blob": {"len": N, "base64": "..."}}；超过阈值且进程启用旁路
// 存储（§5.2）时落盘并改发引用 {"len": N, "ref": "$polydb_blob:<uuid>"}，
// 旁路不可用（无 sink / 写失败）回落仅 len 占位。ref 与 base64 互斥，
// 阈值单点判定。用既有 Object 承载，msgpack / JSON 通道都能无损往返，
// 与 Rust db_core::blob_value 对齐。
func BlobValue(bytes []byte) protocol.Value {
	if len(bytes) > MaxInlineBlobBytes {
		inner := map[string]any{"len": len(bytes)}
		if sink := currentBlobSink(); sink != nil {
			if ref, err := sink.PutBlob(bytes); err == nil && ref != "" {
				inner["ref"] = ref
			}
		}
		return protocol.NewObjectValue(map[string]any{"$blob": inner})
	}
	inner := map[string]any{"len": len(bytes), "base64": base64.StdEncoding.EncodeToString(bytes)}
	return protocol.NewObjectValue(map[string]any{"$blob": inner})
}

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
		return BlobValue(t)
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
		return BlobValue(b)
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
