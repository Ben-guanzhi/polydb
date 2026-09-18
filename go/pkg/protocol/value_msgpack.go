package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// Value 在 msgpack 数据面上编码为原生值（与 Rust untagged enum 对齐），
// 但内部以 JSON RawMessage 保存以便 JSON / Arrow 双向转换。
func (v Value) MarshalMsgpack() ([]byte, error) {
	if v.IsNull() {
		return msgpack.Marshal(nil)
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(v.raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("value marshal: %w", err)
	}
	return msgpack.Marshal(normalizeForMsgpack(decoded))
}

func (v *Value) UnmarshalMsgpack(data []byte) error {
	var decoded any
	if err := msgpack.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("value unmarshal msgpack: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(decoded); err != nil {
		return fmt.Errorf("value re-encode json: %w", err)
	}
	raw := bytes.TrimSpace(buf.Bytes())
	if string(raw) == "null" {
		raw = nil
	}
	v.raw = append(json.RawMessage(nil), raw...)
	return nil
}

func normalizeForMsgpack(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeForMsgpack(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeForMsgpack(val)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return v
	}
}

func TryGetString(v Value) (string, bool) {
	if v.IsNull() {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v.raw, &s); err == nil {
		return s, true
	}
	return "", false
}

func TryGetInt(v Value) (int64, bool) {
	if v.IsNull() {
		return 0, false
	}
	var i int64
	if err := json.Unmarshal(v.raw, &i); err == nil {
		return i, true
	}
	return 0, false
}
