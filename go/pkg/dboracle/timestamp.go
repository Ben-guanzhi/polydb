package dboracle

// Oracle 专属的时间列渲染（PD-19，2026-11-10）。
//
// 为什么不能放在共享的 `pkg/dbcore.DriverToValueTyped` 里：PG 与 Oracle
// 的 TIMESTAMP 列**列声明同名**——pgx stdlib 上报 PG 类型名
// （`strings.ToUpper(dt.Name)`，pgx stdlib/sql.go:651 ⇒ "TIMESTAMP" /
// "TIMESTAMPTZ"），go-ora 上报 Oracle 线协议类型（180/181/231/232 ⇒
// "TIMESTAMPDTY" / "TIMESTAMPTZDTY" / "TIMESTAMPELTZ"，见
// go-ora oracletype_string.go:59）。两者都可能叫 "TIMESTAMP"，共享层
// 仅凭 declType **无法区分引擎**。
//
// 故 Oracle 的 precision / 时区后缀语义放在本包（驱动专属层），
// 共享层只处理与引擎无关的 time.Time 基础形态。
//
// 锚点是 Rust 侧 `db-oracle` 的 `or_value`：它走 oracle crate 的
// `SqlValue::to_string`（oracle-0.6.3 sql_value.rs:1035
// `NativeType::Timestamp ⇒ get_timestamp_unchecked()?.to_string()`），
// 即 `Timestamp::Display`（sql_type/timestamp.rs:351-385）：
//
//	"{Y}-{M:02}-{D:02} {H:02}:{M:02}:{S:02}[.{prec 位小数}][ ±HH:MM]"
//
// 小数按 precision **截断**（不四舍五入——Display 用
// `nanosecond / 10^(9-prec)` 整除，timestamp.rs:358-369）；
// precision=0 不输出小数点；with_tz=true 时尾缀 " ±HH:MM"。

import (
	"strconv"
	"strings"
	"time"

	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// oracleValue 把扫描出的值转成协议 Value，与 Rust 侧 db-oracle 的
// `or_value` 对齐。
//
// 与共享层 `pdb.DriverToValueTyped` 的唯一差异：**时间列**。
// go-ora 把 Oracle 的 DATE/TIMESTAMP* 一律解码成 time.Time
// （converters.DecodeDate；command.go:930-946 各类型 fallthrough），
// 而 Rust 侧走 oracle crate Timestamp::Display —— 故这里用
// timestampValue 渲染，而非共享层的引擎无关形态。
//
// DATE 列仍走共享层（其 base=="DATE" 分支剥成裸日期，与 Rust 侧
// NaiveDate 一致）。
func oracleValue(v any, declType string) protocol.Value {
	if t, ok := v.(time.Time); ok {
		base := strings.ToUpper(declType)
		if i := strings.IndexAny(base, "( "); i >= 0 {
			base = base[:i]
		}
		base = strings.ReplaceAll(base, "_", "")
		if base != "DATE" {
			return protocol.NewStringValue(timestampValue(t, declType))
		}
	}
	return pdb.DriverToValueTyped(v, declType)
}

// timestampValue 把 go-ora 扫出的 time.Time 渲染成 Rust 侧 oracle crate
// Timestamp::Display 的同形文本。
//
// declType 是列声明（go-ora 的 DatabaseTypeName），用于取 precision 与
// 是否带时区。
func timestampValue(t time.Time, declType string) string {
	return formatTimestamp(t, timestampPrecision(declType), timestampWithTZ(declType))
}

// timestampPrecision 从列声明里提取 TIMESTAMP 的小数位数。
//
// oracle crate 的语义（sql_type/timestamp.rs:174-194）：
// OracleType::Timestamp(prec) / TimestampTZ(prec) / TimestampLTZ(prec)
// ⇒ precision = prec；Display 按 precision 截断小数，precision=0 无小数点。
//
// go-ora 的列声明拼写（oracletype_string.go:59）：
// "TIMESTAMP"（187）/ "TIMESTAMPDTY"（180）/ "TIMESTAMPTZDTY"（181）/
// "TIMESTAMPELTZ"（232）/ "TIMESTAMPLTZDTY"（231）/ "TIMETZ"（186）。
// 其中带 (n) 的形态只出现在带精度声明的列上；无 (n) 时 Oracle 的
// TIMESTAMP 缺省精度为 6（与 crate 的缺省一致）。
func timestampPrecision(declType string) int {
	up := strings.ToUpper(declType)
	i := strings.IndexByte(up, '(')
	if i < 0 {
		return 6
	}
	j := strings.IndexByte(up[i:], ')')
	if j < 0 {
		return 6
	}
	n := 0
	for _, c := range up[i+1 : i+j] {
		if c < '0' || c > '9' {
			return 6
		}
		n = n*10 + int(c-'0')
	}
	if n > 9 {
		n = 9
	}
	return n
}

// timestampWithTZ 报告列是否带时区（Rust 侧 with_tz）：TIMESTAMP WITH
// [LOCAL] TIME ZONE ⇒ true，TIMESTAMP / TIMESTAMP(n) ⇒ false。
//
// go-ora 的列声明是**枚举拼写**（oracletype_string.go:59），不含
// "WITH TIME ZONE" 文字：
//
//	180 TIMESTAMPDTY      — TIMESTAMP（无时区）
//	181 TIMESTAMPTZDTY    — TIMESTAMP WITH TIME ZONE
//	186 TIMETZ           — TIME（纯时间，非时间戳）
//	188 TIMESTAMPTZ      — TIMESTAMP WITH TIME ZONE
//	231 TIMESTAMPLTZDTY  — TIMESTAMP WITH LOCAL TIME ZONE
//	232 TIMESTAMPELTZ    — TIMESTAMP WITH LOCAL TIME ZONE
//
// 故按拼写判定：含 "TZ" 的 TIMESTAMP* ⇒ 带时区；"TIMETZ" 是 TIME 不是
// 带时区的时间戳 ⇒ false。
func timestampWithTZ(declType string) bool {
	up := strings.ToUpper(declType)
	if up == "TIMETZ" {
		return false
	}
	return strings.Contains(up, "TIMESTAMPTZ") || strings.Contains(up, "TIMESTAMPELTZ")
}

// formatTimestamp 按 oracle crate Timestamp::Display 的格式渲染。
func formatTimestamp(t time.Time, precision int, withTZ bool) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(t.Year()))
	b.WriteByte('-')
	write2(&b, int(t.Month()))
	b.WriteByte('-')
	write2(&b, t.Day())
	b.WriteByte(' ')
	write2(&b, t.Hour())
	b.WriteByte(':')
	write2(&b, t.Minute())
	b.WriteByte(':')
	write2(&b, t.Second())
	if precision > 0 {
		b.WriteByte('.')
		ns := t.Nanosecond()
		// 按 precision 截断（Display 用整除，非四舍五入）：先取全部 9 位
		// 十进制数字，再砍尾。
		var digits [9]byte
		for i := 8; i >= 0; i-- {
			digits[i] = byte('0' + ns%10)
			ns /= 10
		}
		b.Write(digits[:precision])
	}
	if withTZ {
		_, offset := t.Zone()
		var sign byte = '+'
		if offset < 0 {
			sign = '-'
			offset = -offset
		}
		b.WriteByte(' ')
		b.WriteByte(sign)
		write2(&b, offset/3600)
		b.WriteByte(':')
		write2(&b, (offset%3600)/60)
	}
	return b.String()
}

func write2(b *strings.Builder, v int) {
	if v < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.Itoa(v))
}
