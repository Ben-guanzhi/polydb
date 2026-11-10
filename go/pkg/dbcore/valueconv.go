package dbcore

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
)

// MaxInlineBlobBytes 是二进制单元格内联 base64 的阈值（spec/encoding.md §5），
// 与 Rust polydb_protocol::common::MAX_INLINE_BLOB_BYTES 必须一致。
const MaxInlineBlobBytes = 1024 * 1024

// BlobValue 按 spec/encoding.md §5 约定编码二进制单元格：
// ≤阈值 {"$blob": {"len": N, "base64": "..."}}；超过阈值且进程启用旁路
// 存储（§5.2）时落盘并改发引用 {"len": N, "ref": "$polydb_blob:<uuid>"}，
// 旁路不可用（无 sink / 写失败）回落仅 len 占位并标记 "truncated": true。
// ref 与 base64 互斥，阈值单点判定。用既有 Object 承载，msgpack / JSON
// 通道都能无损往返，与 Rust db_core::blob_value 对齐。
func BlobValue(bytes []byte) protocol.Value {
	if len(bytes) > MaxInlineBlobBytes {
		inner := map[string]any{"len": len(bytes)}
		if sink := currentBlobSink(); sink != nil {
			if ref, err := sink.PutBlob(bytes); err == nil && ref != "" {
				inner["ref"] = ref
			} else {
				inner["truncated"] = true
			}
		} else {
			inner["truncated"] = true
		}
		return protocol.NewObjectValue(map[string]any{"$blob": inner})
	}
	inner := map[string]any{"len": len(bytes), "base64": base64.StdEncoding.EncodeToString(bytes)}
	return protocol.NewObjectValue(map[string]any{"$blob": inner})
}

// ValueToDriver 将协议 Value 转为 database/sql 驱动参数。
//
// 整数路径用 UseNumber 解码后先试 int64/uint64：uint64 值超过 MaxInt64 时
// float64 会静默丢掉低 53 位（9007199254740993 → 9007199254740992），而
// driver 的 DefaultParameterConverter 对 uint64 同样走 float64——所以仍超界
// 的无小数/指数整数以**精确十进制字符串**返回（MySQL/PG 大整数参数的
// 保真形态，与 TableProGo 的 jsonSafeInt 对齐）。真实浮点仍走 float64。
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
	dec := json.NewDecoder(bytes.NewReader(v.Raw()))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return string(v.Raw())
	}
	switch t := decoded.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(t.String(), 10, 64); err == nil {
			if u > math.MaxInt64 {
				return t.String()
			}
			return int64(u)
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return string(v.Raw())
	}
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
		// RFC3339Nano — the kernel's one time spelling (dbcore Value.String
		// and bridge.ValueFromDB both use it). The old "2006-01-02 15:04:05"
		// made the same engine's timestamp spell two ways on the wire: the
		// direct query path (this function) vs the transaction path
		// (bridge.ValueFromDB) — a client parsing timestamps forked on which
		// path served the row.
		//
		// 注：Rust 侧各驱动的数据路径用的是 "%Y-%m-%d %H:%M:%S"
		//（db-postgres lib.rs:126/129、db-mysql lib.rs:136、db-mssql
		// lib.rs:255/258），与本拼写不同——那是另一处跨语言漂移
		//（不在本波范围，见 WORKSPACE §131 第 8 节"遗留"）。
		// Oracle 列的时间拼写由 pkg/dboracle 自己处理（PD-19）——本函数是
		// 跨引擎共享层，无法据 declType 区分 PG 与 Oracle 的 "TIMESTAMP"。
		return protocol.NewStringValue(t.Format(time.RFC3339Nano))
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
	base := sqlBaseType(declType)
	// DATE 列：pgx stdlib 把 PG date 扫描成 time.Time（午夜 UTC），直接走
	// time.Time 分支会输出 "2026-01-02T00:00:00Z"——把"日期"渲染成了"时刻"，
	// 且与 Rust 侧 NaiveDate 的裸拼写（"2026-01-02"）漂移。按列声明剥掉
	// 时间部分，双端都输出裸日期。
	if base == "DATE" {
		switch t := v.(type) {
		case time.Time:
			return protocol.NewStringValue(t.Format("2006-01-02"))
		case string:
			// MySQL/PG 文本协议交来的日期文本可能已带时间部分。
			if i := strings.IndexByte(t, ' '); i > 0 {
				return protocol.NewStringValue(t[:i])
			}
			return protocol.NewStringValue(t)
		}
	}
	// GUID 列（PD-18，2026-11-10）：go-mssqldb 把 uniqueidentifier 解码成
	// 16 字节 []byte（types.go:decodeGuid）。旧链把它当普通字节串输出——
	// 16 个原始字节的乱码字符串，用户读不到 UUID。与 Rust 侧（tiberius
	// Uuid → 标准文本）统一为标准小写 UUID 文本形态。
	//
	// **字节序（PD-24，2026-11-10）**：MSSQL 的 GUID 在 wire 上是
	// **mixed-endian**（前三组小端：Data1/Data2/Data3 按小端，Data4 按大端）。
	// go-mssqldb 的 `decodeGuid` 默认 `GuidConversion=false` ⇒ **原样拷贝**
	// （types.go:932-941），故这 16 字节**不是** UUID 的标准布局——直接
	// `copy` 进 uuid.UUID 会按标准布局解读，读出**错序**的 UUID
	// （实测 "6f9619ff-8b86-d011-…" 变成 "ff19966f-868b-11d0-…"）。
	// Rust 侧 tiberius 给的是标准布局字节，故两端一度不一致。
	// 这里先把前三组转成标准布局再交给 uuid.UUID。
	if base == "UNIQUEIDENTIFIER" {
		if b, ok := v.([]byte); ok && len(b) == 16 {
			var u uuid.UUID
			copy(u[:], b)
			// mixed-endian → 标准布局（前三组小端字节序反转）。
			u[0], u[1], u[2], u[3] = u[3], u[2], u[1], u[0]
			u[4], u[5] = u[5], u[4]
			u[6], u[7] = u[7], u[6]
			return protocol.NewStringValue(u.String())
		}
	}
	b, ok := v.([]byte)
	if !ok {
		return DriverToValue(v)
	}
	switch base {
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
