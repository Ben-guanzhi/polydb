package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// The scalar fast paths must be byte-identical to the previous tree path:
// the wire is the cross-implementation contract (spec/behavior.md), so an
// equivalence test against a frozen copy of the old code is the gate.

func treeMarshalValue(raw []byte) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return msgpack.Marshal(nil)
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	return msgpack.Marshal(normalizeForMsgpack(decoded))
}

func treeUnmarshalValue(data []byte) (json.RawMessage, error) {
	var decoded any
	if err := msgpack.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(decoded); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(buf.Bytes())
	if string(raw) == "null" {
		return nil, nil
	}
	return append(json.RawMessage(nil), raw...), nil
}

func TestScalarFastPathMarshalEquivalence(t *testing.T) {
	corpus := []string{
		`"hello"`, `""`, `"带中文"`, `"a\"b"`, `"1e5 inside string"`,
		`0`, `123`, `-5`, `1.5`, `1e3`, `123456789012345678901`,
		`9223372036854775807`, `-9223372036854775808`, `1.7976931348623157e308`,
		`true`, `false`,
		`{"a":1,"b":[1,2,{"c":"x"}],"d":1e3}`,
		`[1,"two",null,true,{"x":[1.5]}]`,
		`{"$blob":{"len":2,"base64":"AQI="}}`,
	}
	for _, c := range corpus {
		var v Value
		if err := json.Unmarshal([]byte(c), &v); err != nil {
			t.Fatalf("corpus %s: %v", c, err)
		}
		want, err := treeMarshalValue(v.raw)
		if err != nil {
			t.Fatalf("tree marshal %s: %v", c, err)
		}
		got, err := v.MarshalMsgpack()
		if err != nil {
			t.Fatalf("fast marshal %s: %v", c, err)
		}
		scalar := len(v.raw) > 0 && v.raw[0] != '{' && v.raw[0] != '['
		if scalar {
			// Scalars must be byte-identical: they are what Rust compares by.
			if !bytes.Equal(want, got) {
				t.Errorf("%s: wire differs\n tree=%x\n fast=%x", c, want, got)
			}
			continue
		}
		// Containers: msgpack v5 does not sort map keys — even two runs of the
		// old code differ. The contract is semantic equality (decode → JSON).
		if !msgpackSemanticallyEqual(want, got) {
			t.Errorf("%s: semantic mismatch\n tree=%x\n fast=%x", c, want, got)
		}
	}
}

func msgpackSemanticallyEqual(a, b []byte) bool {
	ja, err := treeUnmarshalValue(a)
	if err != nil {
		return false
	}
	jb, err := treeUnmarshalValue(b)
	if err != nil {
		return false
	}
	return string(ja) == string(jb)
}

func TestScalarFastPathUnmarshalEquivalence(t *testing.T) {
	// Payloads produced by msgpack.Marshal of native values cover every scalar
	// family the Rust side can send, incl. float32 and the uint64 boundary.
	natives := []any{
		nil, true, false, "hello", "", "带中文", "<&>",
		int8(-8), int16(-300), int32(-70000), int64(-5e9),
		1, 42, 1000,
		uint64(math.MaxUint64), uint64(math.MaxInt64) + 1,
		float32(1.1), float32(-0.5), float64(3.14159265358979),
		[]byte{1, 2, 3},
		map[string]any{"a": int64(1), "b": "x"},
		[]any{int64(1), "two", nil},
	}
	for _, n := range natives {
		data, err := msgpack.Marshal(n)
		if err != nil {
			t.Fatalf("marshal native %#v: %v", n, err)
		}
		want, err := treeUnmarshalValue(data)
		if err != nil {
			t.Fatalf("tree unmarshal %#v: %v", n, err)
		}
		var v Value
		if err := v.UnmarshalMsgpack(data); err != nil {
			t.Fatalf("fast unmarshal %#v: %v", n, err)
		}
		if string(want) != string(v.raw) {
			t.Errorf("%#v: raw differs tree=%q fast=%q", n, want, v.raw)
		}
	}
}

func BenchmarkMarshalMsgpackStringCell(b *testing.B) {
	var v Value
	_ = json.Unmarshal([]byte(`"some-varchar-value-0123456789"`), &v)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := v.MarshalMsgpack(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalMsgpackStringCellTreePath(b *testing.B) {
	raw := json.RawMessage(`"some-varchar-value-0123456789"`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := treeMarshalValue(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalMsgpackIntCell(b *testing.B) {
	var v Value
	_ = json.Unmarshal([]byte(`424242`), &v)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := v.MarshalMsgpack(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalMsgpackIntCellTreePath(b *testing.B) {
	raw := json.RawMessage(`424242`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := treeMarshalValue(raw); err != nil {
			b.Fatal(err)
		}
	}
}
