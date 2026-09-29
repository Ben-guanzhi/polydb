package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// Value 在 msgpack 数据面上编码为原生值（与 Rust untagged enum 对齐），
// 但内部以 JSON RawMessage 保存以便 JSON / Arrow 双向转换。
//
// 标量值走直通编码：结果集里绝大多数 cell 是字符串/数字/布尔，若无谓通过
// json→any→normalize→msgpack 三趟并整树分配；容器仍走树路径（少见且需要
// Number 语义）。两条路径的 wire 字节逐位一致（value_msgpack_test.go 锁定）。
func (v Value) MarshalMsgpack() ([]byte, error) {
	if v.IsNull() {
		return msgpack.Marshal(nil)
	}
	if b, handled, err := marshalScalarMsgpack(v.raw); handled || err != nil {
		return b, err
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(v.raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("value marshal: %w", err)
	}
	return msgpack.Marshal(normalizeForMsgpack(decoded))
}

// marshalScalarMsgpack 处理 JSON 标量原文；handled=false 表示容器或形状
// 不认识，交回树路径处理（错误信息也在那边给出）。
func marshalScalarMsgpack(raw []byte) (data []byte, handled bool, err error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil, false, nil
	}
	switch c := t[0]; {
	case c == '{' || c == '[' || c == 'n':
		return nil, false, nil
	case c == '"':
		var s string
		if json.Unmarshal(t, &s) != nil {
			return nil, false, nil
		}
		b, err := msgpack.Marshal(s)
		return b, true, err
	case c == 't' || c == 'f':
		var v bool
		if json.Unmarshal(t, &v) != nil {
			return nil, false, nil
		}
		b, err := msgpack.Marshal(v)
		return b, true, err
	case c == '-' || (c >= '0' && c <= '9'):
		var i int64
		if json.Unmarshal(t, &i) == nil {
			b, err := msgpack.Marshal(i)
			return b, true, err
		}
		var f float64
		if json.Unmarshal(t, &f) != nil {
			return nil, false, nil
		}
		b, err := msgpack.Marshal(f)
		return b, true, err
	}
	return nil, false, nil
}

func (v *Value) UnmarshalMsgpack(data []byte) error {
	if raw, handled, err := unmarshalScalarMsgpack(data); handled || err != nil {
		if err != nil {
			return fmt.Errorf("value unmarshal msgpack: %w", err)
		}
		v.raw = raw
		return nil
	}
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

// unmarshalScalarMsgpack 直接按首字节判定 msgpack 标量族并转成 JSON 原文，
// 跳过 any 树。编码族与旧路径 interface{} 解码的渲染逐位一致。
func unmarshalScalarMsgpack(data []byte) (raw json.RawMessage, handled bool, err error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	toRaw := func(x any) json.RawMessage {
		b, _ := json.Marshal(x)
		return b
	}
	switch c := data[0]; {
	case c == 0xc0: // nil：内部以 raw=nil 表示
		return nil, true, nil
	case c == 0xc2 || c == 0xc3: // bool
		var x bool
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c <= 0x7f || c >= 0xe0: // positive/negative fixint
		var x int64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c >= 0xcc && c <= 0xd3 && c != 0xcf: // int8..int64、uint8/16/32（都放得进 int64）
		var x int64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xcf: // uint64
		var x uint64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xca: // float32（必须按 float32 渲染，升 float64 会出 1.100000023841858）
		var x float32
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xcb: // float64
		var x float64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c >= 0xa0 && c <= 0xbf || c == 0xd9 || c == 0xda || c == 0xdb: // str 家族
		var x string
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	}
	return nil, false, nil
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

// TryGetBlob 识别 spec/encoding.md §5.1 的 $blob 内联编码
// {"$blob":{"len":N,"base64":"..."}}；base64 为空表示超阈值仅发长度。
// ok=true 仅当形状匹配且 len 为整数（字段顺序无关）。
func TryGetBlob(v Value) (length int64, base64Str string, ok bool) {
	if v.IsNull() {
		return 0, "", false
	}
	var outer struct {
		Blob *struct {
			Len    *int64 `json:"len"`
			Base64 string `json:"base64"`
		} `json:"$blob"`
	}
	if err := json.Unmarshal(v.raw, &outer); err != nil || outer.Blob == nil || outer.Blob.Len == nil {
		return 0, "", false
	}
	return *outer.Blob.Len, outer.Blob.Base64, true
}
