package dboracle

import (
	"strings"
	"testing"
	"time"
)

// PD-19（2026-11-10）回归测试：Oracle TIMESTAMP 列的读取形态。
//
// 缺陷：go-ora 把 Oracle 的 TIMESTAMP*（含 DATE 的时刻语义列）解码成
// time.Time（converters.DecodeDate）。旧链对 base == "TIMESTAMP*" 没有
// 分支，落到 time.Time 分支输出 RFC3339Nano
// （"2026-01-02T15:04:05.5+08:00"）；而 Rust 侧走 oracle crate
// Timestamp 的 Display，输出空格分隔、无时区标记的
// "2026-01-02 15:04:05.5"——同一列两端 wire 值不同（AGENTS.md §14 红线）。
//
// 修复：Go 侧 TIMESTAMP* 分支统一为 oracle crate Timestamp::Display 的
// 格式（Y-M-D H:M:S[.prec][±HH:MM]）。precision 随列声明
// （DATE→无小数；TIMESTAMP(n)→n 位小数），与 Rust 侧 from_dpi_timestamp
// 的 precision 语义一致（oracle-0.6.3 sql_type/timestamp.rs:174-194）。

func oracleTS(y int, mo time.Month, d, h, mi, s, ns int, loc *time.Location) time.Time {
	return time.Date(y, mo, d, h, mi, s, ns, loc)
}

func TestOracleTimestampFormatMatchesRustDisplay(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name     string
		declType string
		ts       time.Time
		want     string
	}{
		// TIMESTAMP(0) / DATE：无小数部分（Rust precision=0 ⇒ 无 "."）。
		{"timestamp0", "TIMESTAMP(0)", oracleTS(2026, 1, 2, 15, 4, 5, 0, utc), "2026-01-02 15:04:05"},
		// TIMESTAMP(3)：毫秒。
		{"timestamp3", "TIMESTAMP(3)", oracleTS(2026, 1, 2, 15, 4, 5, 500000000, utc), "2026-01-02 15:04:05.500"},
		// TIMESTAMP(6)：微秒。
		{"timestamp6", "TIMESTAMP(6)", oracleTS(2026, 11, 10, 7, 8, 9, 123456000, utc), "2026-11-10 07:08:09.123456"},
		// TIMESTAMP(9)：纳秒。
		{"timestamp9", "TIMESTAMP(9)", oracleTS(2026, 11, 10, 7, 8, 9, 123456789, utc), "2026-11-10 07:08:09.123456789"},
		// 带时区：go-ora 上报的是**枚举拼写**（oracletype_string.go:59）——
		// 181 TIMESTAMPTZDTY（WITH TIME ZONE）/ 188 TIMESTAMPTZ /
		// 231 TIMESTAMPLTZDTY / 232 TIMESTAMPELTZ（WITH LOCAL TIME ZONE）。
		// Rust with_tz=true ⇒ 尾缀 " ±HH:MM"。
		{"timestamptz-dty", "TIMESTAMPTZDTY",
			oracleTS(2026, 11, 10, 7, 8, 9, 123456000, time.FixedZone("+08:00", 8*3600)),
			"2026-11-10 07:08:09.123456 +08:00"},
		// 188 TIMESTAMPTZ：TZ 时间戳，无 (n) ⇒ precision 缺省 6
		//（Oracle 的 TIMESTAMP 缺省 scale 是 6，Rust 侧 from_dpi_timestamp
		//  取 ODPI 的 fsPrecision，同为 6）。
		{"timestamptz", "TIMESTAMPTZ",
			oracleTS(2026, 11, 10, 7, 8, 9, 0, time.FixedZone("-05:00", -5*3600)),
			"2026-11-10 07:08:09.000000 -05:00"},
		{"timestampeltz", "TIMESTAMPELTZ",
			oracleTS(2026, 11, 10, 7, 8, 9, 123456000, time.FixedZone("+08:00", 8*3600)),
			"2026-11-10 07:08:09.123456 +08:00"},
		// 180 TIMESTAMPDTY：TIMESTAMP（无时区）⇒ 无尾缀。
		{"timestampdty", "TIMESTAMPDTY",
			oracleTS(2026, 11, 10, 7, 8, 9, 123456000, time.UTC),
			"2026-11-10 07:08:09.123456"},
		// 186 TIMETZ 是 TIME（纯时间）不是带时区的时间戳 ⇒ 无尾缀；
		// 无 (n) ⇒ precision 缺省 6。
		{"timetz", "TIMETZ",
			oracleTS(2026, 11, 10, 7, 8, 9, 0, time.FixedZone("+08:00", 8*3600)),
			"2026-11-10 07:08:09.000000"},
		// DATE 列名不匹配 TIMESTAMP 前缀，走原 DATE 分支（不在此断言）。
	}
	for _, c := range cases {
		got := oracleValue(any(c.ts), c.declType)
		gotStr := got.Raw()
		if want := `"` + c.want + `"`; string(gotStr) != want {
			t.Errorf("%s: TIMESTAMP 形态 = %s, want %s（必须与 oracle crate Timestamp::Display 一致）",
				c.name, gotStr, want)
		}
	}
}

// precision 提取：declType 里的 (n) 决定小数位数；无 (n) 或 WITH TIME ZONE
// 后缀都要正确处理。
func TestOracleTimestampPrecisionFromDeclType(t *testing.T) {
	cases := []struct {
		declType string
		want     int
	}{
		{"TIMESTAMP(0)", 0},
		{"TIMESTAMP(3)", 3},
		{"TIMESTAMP(6)", 6},
		{"TIMESTAMP(9)", 9},
		{"TIMESTAMP(6) WITH TIME ZONE", 6},
		{"TIMESTAMP(6) WITH LOCAL TIME ZONE", 6},
		{"TimeStampDTY", 6}, // go-ora 的命名变体：默认 6
		{"TIMESTAMP", 6},    // 无精度声明：Oracle 默认 6
	}
	for _, c := range cases {
		got := timestampPrecision(c.declType)
		if got != c.want {
			t.Errorf("timestampPrecision(%q) = %d, want %d", c.declType, got, c.want)
		}
	}
}

// 纳秒截断方向：Display 按 precision 截断（不四舍五入）。
func TestOracleTimestampTruncatesNotRounds(t *testing.T) {
	// precision=3，纳秒 999_999_999 ⇒ 截断成 .999（不是四舍五入到 1.000）。
	ts := oracleTS(2026, 1, 2, 15, 4, 5, 999999999, time.UTC)
	got := oracleValue(any(ts), "TIMESTAMP(3)")
	if want := `"2026-01-02 15:04:05.999"`; string(got.Raw()) != want {
		t.Errorf("截断方向 = %s, want %s（Display 按 precision 截断，不四舍五入）", got.Raw(), want)
	}
}

// 编译期护栏：TIMESTAMP 前缀路由假设成立（实测 sqlBaseType 输出，
// 见波 64 探针：go-ora 全部变体都落到 "TIMESTAMP*" 前缀）。
func TestOracleTimestampPrefixRouting(t *testing.T) {
	for _, dt := range []string{"TIMESTAMP(6)", "TIMESTAMP", "TimeStampDTY",
		"TimeStampTZ_DTY", "TimeStampeLTZ", "TIMESTAMP(6) WITH TIME ZONE"} {
		if !strings.HasPrefix(strings.ToUpper(dt), "TIMESTAMP") {
			t.Errorf("%q 不在 TIMESTAMP 前缀路由内（与实测 sqlBaseType 输出矛盾）", dt)
		}
	}
}
