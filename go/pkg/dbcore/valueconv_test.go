package dbcore

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

func blobCell(b []byte) protocol.Value {
	inner := map[string]any{"len": len(b)}
	if len(b) <= MaxInlineBlobBytes {
		inner["base64"] = base64.StdEncoding.EncodeToString(b)
	}
	return protocol.NewObjectValue(map[string]any{"$blob": inner})
}

func TestDriverToValueTyped(t *testing.T) {
	cases := []struct {
		name     string
		in       any
		declType string
		want     protocol.Value
	}{
		{"int bytes", []byte("1234567"), "BIGINT", protocol.NewIntValue(1234567)},
		{"int unsigned", []byte("42"), "INT UNSIGNED", protocol.NewIntValue(42)},
		{"unsigned prefix", []byte("1877526210314362882"), "UNSIGNED BIGINT", protocol.NewIntValue(1877526210314362882)},
		{"zerofill", []byte("7"), "INT ZEROFILL", protocol.NewIntValue(7)},
		{"int with display width", []byte("7"), "TINYINT(1)", protocol.NewIntValue(7)},
		{"float bytes", []byte("3.14"), "DOUBLE", protocol.NewFloatValue(3.14)},
		{"decimal as string", []byte("10.50"), "DECIMAL(10,2)", protocol.NewStringValue("10.50")},
		{"datetime as string", []byte("2024-01-02 03:04:05"), "DATETIME", protocol.NewStringValue("2024-01-02 03:04:05")},
		{"varchar as string", []byte("hello"), "VARCHAR(255)", protocol.NewStringValue("hello")},
		{"blob inlines base64", []byte{0x00, 0x01, 0x02}, "BLOB", blobCell([]byte{0x00, 0x01, 0x02})},
		{"binary inlines base64", []byte{0xff, 0xfe}, "VARBINARY(16)", blobCell([]byte{0xff, 0xfe})},
		{"non-bytes delegates", int64(99), "BIGINT", protocol.NewIntValue(99)},
		{"nil delegates", nil, "INT", protocol.NewNullValue()},
		{"unparseable int falls back string", []byte("not-a-number"), "INT", protocol.NewStringValue("not-a-number")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DriverToValueTyped(tc.in, tc.declType)
			if !valuesEqual(got, tc.want) {
				t.Errorf("DriverToValueTyped(%v, %q) = %+v, want %+v", tc.in, tc.declType, got, tc.want)
			}
		})
	}
}

func valuesEqual(a, b protocol.Value) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

// 超过 spec/encoding.md §5 的 1MB 阈值：不内联 base64，走旁路引用或降级。
// 无 sink 时降级为 len + truncated 标记。
func TestBlobValueOverThresholdOmitsBase64(t *testing.T) {
	SetBlobSink(nil)
	big := make([]byte, MaxInlineBlobBytes+1)
	want := protocol.NewObjectValue(map[string]any{
		"$blob": map[string]any{"len": len(big), "truncated": true},
	})
	if got := BlobValue(big); !valuesEqual(got, want) {
		t.Errorf("oversized BlobValue kept base64: %v", got)
	}
}

// 同一引擎的时间戳在 wire 上必须只有一个拼写：直查路径 (DriverToValue) 与
// 事务路径 (bridge.ValueFromDB 的 ValTime 分支) 各自转换一次, 两条路径分叉
// 就是客户端按前缀解析时间时随机失效。内核的权威拼写是 RFC3339Nano
// (dbcore.Value.String 与 bridge.ValueFromDB 同式)。这里的断言是字面量——
// valueconv 不 import 内核, 内核拼写漂移时 bridge_test 的同式对照会红。
func TestDriverToValueTimeMatchesKernelSpelling(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if got := spellOf(DriverToValue(ts)); got != "2026-01-02T03:04:05.123456Z" {
		t.Fatalf("timestamp spelling = %q, want kernel RFC3339Nano", got)
	}
	// 整秒值同样对齐 (Nano 省略零小数)。
	whole := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := spellOf(DriverToValue(whole)); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("whole-second spelling = %q", got)
	}
}

func spellOf(v protocol.Value) string {
	s, _ := protocol.TryGetString(v)
	return s
}

// DATE 列按"日期"拼写：pgx stdlib 把 PG date 扫描成 time.Time（午夜 UTC），
// 不按列声明剥时间就输出 "2026-01-02T00:00:00Z"——把日期渲染成时刻，且与
// Rust 侧 NaiveDate 的裸拼写（"2026-01-02"）漂移。双端必须同值。
func TestDriverToValueTypedDateIsBareDate(t *testing.T) {
	midnight := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := spellOf(DriverToValueTyped(midnight, "DATE")); got != "2026-01-02" {
		t.Fatalf("DATE(time.Time) = %q, want bare 2026-01-02", got)
	}
	// 文本协议交来的带时间文本同样剥到日期。
	if got := spellOf(DriverToValueTyped("2026-01-02 00:00:00", "DATE")); got != "2026-01-02" {
		t.Fatalf("DATE(text with time) = %q, want bare date", got)
	}
	if got := spellOf(DriverToValueTyped("2026-01-02", "DATE")); got != "2026-01-02" {
		t.Fatalf("DATE(bare text) = %q", got)
	}
	// 非 DATE 列的 time.Time 仍是完整时刻（不受影响）。
	if got := spellOf(DriverToValueTyped(midnight, "TIMESTAMPTZ")); got != "2026-01-02T00:00:00Z" {
		t.Fatalf("TIMESTAMPTZ midnight = %q, want full instant", got)
	}
	// Rust 侧大小写归一：date/Date 同样命中（pgx 上报 "DATE"，sqlite 报 "date"）。
	if got := spellOf(DriverToValueTyped(midnight, "date")); got != "2026-01-02" {
		t.Fatalf("DATE lowercase = %q", got)
	}
}
