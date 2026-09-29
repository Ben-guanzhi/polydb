package protocol

import "testing"

func blobVal(fields map[string]any) Value {
	return NewObjectValue(map[string]any{"$blob": fields})
}

func TestTryGetBlob(t *testing.T) {
	cases := []struct {
		name       string
		v          Value
		wantLen    int64
		wantBase64 string
		wantOk     bool
	}{
		{"inline", blobVal(map[string]any{"len": int64(2), "base64": "AQI="}), 2, "AQI=", true},
		{"oversized length only", blobVal(map[string]any{"len": int64(2_000_000)}), 2_000_000, "", true},
		{"null", NewNullValue(), 0, "", false},
		{"string", NewStringValue("x"), 0, "", false},
		{"object without marker", NewObjectValue(map[string]any{"len": int64(1)}), 0, "", false},
		{"marker not object", blobValInnerString(), 0, "", false},
		{"missing len", blobVal(map[string]any{"base64": "AQI="}), 0, "", false},
		{"len not integer", blobVal(map[string]any{"len": 1.5}), 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, b64, ok := TryGetBlob(c.v)
			if ok != c.wantOk || n != c.wantLen || b64 != c.wantBase64 {
				t.Errorf("TryGetBlob = (%d, %q, %v), want (%d, %q, %v)", n, b64, ok, c.wantLen, c.wantBase64, c.wantOk)
			}
		})
	}
}

func blobValInnerString() Value {
	return NewObjectValue(map[string]any{"$blob": "not-an-object"})
}
