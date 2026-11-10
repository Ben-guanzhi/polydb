package dbcore

import (
	"testing"
)

// 诊断辅助：确认 Go 侧对 MySQL "BIGINT UNSIGNED" 超 i64::MAX 值的最终 wire 形态。
//
// DriverToValueTyped 的路径（valueconv.go:147-161）：
//   base == "BIGINT" → strconv.ParseInt(b, 10, 64) 失败（超 i64）
//     → 跳出 switch → 末尾 return protocol.NewStringValue(string(b))
// 即：JSON **字符串** "18446744073709551615"。
//
// Rust 侧 db-mysql/src/lib.rs 的 my_value 旧实现没有 u64 分支，
// try_get::<i64> 因 UNSIGNED flag 不兼容、try_get::<f64> 因列类型不是
// Float/Double 不兼容，整条链失败后落到 <LongLong> 占位符——数据全丢。
//
// 本测试把 Go 侧的既有形态钉住，作为 Rust 侧修复的对齐目标。

func TestBigIntUnsignedOverflowForm(t *testing.T) {
	// go-sql-driver/mysql 对一切非 TEXT 结果集列都返回 []byte。
	raw := []byte("18446744073709551615") // u64::MAX

	got := DriverToValueTyped(raw, "BIGINT UNSIGNED")

	// 形态是 JSON 字符串。
	var s string
	if err := got.UnmarshalJSON([]byte(got.Raw())); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, got.Raw())
	}
	_ = s

	if want := `"18446744073709551615"`; string(got.Raw()) != want {
		t.Errorf("BIGINT UNSIGNED 超 i64 的形态 = %s，want %s", got.Raw(), want)
	}
}
