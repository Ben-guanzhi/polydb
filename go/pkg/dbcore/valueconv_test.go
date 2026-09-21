package dbcore

import (
	"encoding/json"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

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
		{"blob stays placeholder", []byte{0x00, 0x01, 0x02}, "BLOB", protocol.NewStringValue("<blob 3 bytes>")},
		{"binary stays placeholder", []byte{0xff, 0xfe}, "VARBINARY(16)", protocol.NewStringValue("<blob 2 bytes>")},
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
