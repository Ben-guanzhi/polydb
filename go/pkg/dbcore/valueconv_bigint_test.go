// 该文件是 GO-VALUECONV-BIGINT 的回归测试：uint64 超过 MaxInt64 时
// float64 会丢低 53 位，且 driver 的 DefaultParameterConverter 对
// uint64 同样走 float64——精确十进制字符串是唯一保真形态。
// int64 范围内的值经 TryGetInt 已是精确 int64，无需字符串化。

package dbcore

import (
	"encoding/json"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// rawUint64 构造 uint64 字面量的 Value（Value 无 uint64 构造器，走 JSON
// 解码器把字面量存进 raw）。
func rawUint64(t *testing.T, lit string) protocol.Value {
	t.Helper()
	var v protocol.Value
	if err := json.Unmarshal([]byte(lit), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestValueToDriverBigintPrecision 钉住大整数参数的保真形态。
func TestValueToDriverBigintPrecision(t *testing.T) {
	// int64 范围内的整数走 int64（驱动原生路径，精确）。
	if got := ValueToDriver(protocol.NewIntValue(9007199254740992)); got.(int64) != 9007199254740992 {
		t.Fatalf("int64 value = %v", got)
	}
	if got := ValueToDriver(rawUint64(t, "9007199254740993")); got.(int64) != 9007199254740993 {
		t.Fatalf("2^53+1 = %T %v, want int64 9007199254740993", got, got)
	}

	// uint64 > MaxInt64 必须以精确十进制字符串返回，不得经 float64。
	v := rawUint64(t, "9223372036854775808") // MaxInt64 + 1
	got := ValueToDriver(v)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("MaxInt64+1 = %T %v, want string (float64 would be %.0f, low bit lost)", got, got, 9223372036854775808.0)
	}
	if s != "9223372036854775808" {
		t.Fatalf("MaxInt64+1 digits = %q, want exact %d", s, uint64(9223372036854775808))
	}

	// MaxUint64 同样精确。
	if s2, ok := ValueToDriver(rawUint64(t, "18446744073709551615")).(string); !ok || s2 != "18446744073709551615" {
		t.Fatalf("MaxUint64 = %T %v, want exact string", ValueToDriver(rawUint64(t, "18446744073709551615")), ValueToDriver(rawUint64(t, "18446744073709551615")))
	}

	// 普通浮点仍走 float64。
	if got := ValueToDriver(protocol.NewFloatValue(3.5)); got.(float64) != 3.5 {
		t.Fatalf("float = %v", got)
	}

	// 字符串与 null 不受影响。
	if got := ValueToDriver(protocol.NewStringValue("x")); got.(string) != "x" {
		t.Fatalf("string = %v", got)
	}
	if got := ValueToDriver(protocol.NewNullValue()); got != nil {
		t.Fatalf("null = %v", got)
	}
}
