package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ─── 保序 JSON 解析 ─────────────────────────────────────────

// omap 保序对象：契约文件的 definition 顺序需要保留（生成 TS 时按 spec 顺序输出）。
type omap struct {
	keys []string
	vals map[string]any
}

func (m *omap) get(key string) any { return m.vals[key] }
func (m *omap) set(key string, v any) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
}

func newOmap() *omap { return &omap{vals: map[string]any{}} }

// parseOrderedJSON 把 JSON 文档解析为 omap / []any / string / float64 / bool / nil。
func parseOrderedJSON(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	v, err := parseOrderedValue(dec)
	return v, err
}

func parseOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseOrderedFrom(dec, tok)
}

func parseOrderedFrom(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := newOmap()
			for {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := keyTok.(json.Delim); ok && d == '}' {
					return m, nil
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("expected object key, got %v", keyTok)
				}
				val, err := parseOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				m.set(key, val)
			}
		case '[':
			var arr []any
			for {
				tok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				if d, ok := tok.(json.Delim); ok && d == ']' {
					return arr, nil
				}
				v, err := parseOrderedFrom(dec, tok)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		default:
			return nil, fmt.Errorf("unexpected delim %v", t)
		}
	default:
		return tok, nil
	}
}

// helpers ────────────────────────────────────────────────────

func asOmap(v any) *omap { m, _ := v.(*omap); return m }
func asArr(v any) []any  { a, _ := v.([]any); return a }
func asStr(v any) string { s, _ := v.(string); return s }
func asBool(v any) bool  { b, _ := v.(bool); return b }
func asNumber(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

func strSlice(v any) []string {
	var out []string
	for _, e := range asArr(v) {
		out = append(out, asStr(e))
	}
	return out
}

// specObj 取 omap 中某键再断言为对象。
func specObj(m *omap, key string) *omap {
	if m == nil {
		return nil
	}
	return asOmap(m.get(key))
}

func compactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.String()
}
