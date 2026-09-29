package dbcore

import (
	"encoding/base64"
	"encoding/json"
	"testing"

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

// 超过 spec/encoding.md §5 的 1MB 阈值：只回长度，不内联 base64。
func TestBlobValueOverThresholdOmitsBase64(t *testing.T) {
	big := make([]byte, MaxInlineBlobBytes+1)
	want := protocol.NewObjectValue(map[string]any{
		"$blob": map[string]any{"len": len(big)},
	})
	if got := BlobValue(big); !valuesEqual(got, want) {
		t.Errorf("oversized BlobValue kept base64: %v", got)
	}
}
