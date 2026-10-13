package protocol

// 数据面单元代价基线（behavior.md §5/encoding.md §5）：
// 一个结果集页 = rows×cols 个 Value，构造（驱动侧）、msgpack 编码（服务端）、
// msgpack 解码（前端侧）三段各走一次。这里钉住每段的 ns/op 与 allocs/op，
// 任何把标量重新推回 reflect/json 全树的改动都会在数字上现形。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// cellKind 把列轮转成真实表里的三类：文本 / 整数 / 浮点（6:3:1）。
type cellKind int

const (
	kindText cellKind = iota
	kindInt
	kindFloat
)

// gridPayload 只造一次（fmt.Sprintf 不进测量循环），测量的是纯构造/编解码。
type gridPayload struct {
	text  string
	i     int64
	f     float64
	kind  cellKind
	value Value // 已构造好的单元格，供编解码基准复用
}

func gridPayloads(rows, cols int) []gridPayload {
	out := make([]gridPayload, 0, rows*cols)
	for r := range rows {
		for c := range cols {
			p := gridPayload{text: fmt.Sprintf("cell-%d-%d-some-varchar-payload", r, c),
				i: int64(r*1000 + c), f: float64(r)/7 + 0.5}
			switch (r*cols + c) % 10 {
			case 6, 7, 8, 9:
				p.kind = kindInt
			case 0, 1, 2, 3, 4, 5:
				p.kind = kindText
			default:
				p.kind = kindFloat
			}
			switch p.kind {
			case kindInt:
				p.value = NewIntValue(p.i)
			case kindFloat:
				p.value = NewFloatValue(p.f)
			default:
				p.value = NewStringValue(p.text)
			}
			out = append(out, p)
		}
	}
	return out
}

// gridShape 组装一张浏览页的 Value 网格（rows×cols，混合三类单元格）。
func gridShape(rows, cols int) [][]Value {
	ps := gridPayloads(rows, cols)
	g := make([][]Value, rows)
	for r := range g {
		row := make([]Value, cols)
		for c := range cols {
			row[c] = ps[r*cols+c].value
		}
		g[r] = row
	}
	return g
}

func BenchmarkGridConstruct(b *testing.B) {
	const rows, cols = 2000, 10
	ps := gridPayloads(rows, cols)
	b.ReportAllocs()
	for range b.N {
		g := make([][]Value, rows)
		for r := range g {
			row := make([]Value, cols)
			for c := range cols {
				switch p := ps[r*cols+c]; p.kind {
				case kindInt:
					row[c] = NewIntValue(p.i)
				case kindFloat:
					row[c] = NewFloatValue(p.f)
				default:
					row[c] = NewStringValue(p.text)
				}
			}
			g[r] = row
		}
		if len(g) != rows {
			b.Fatal("grid")
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

func BenchmarkGridMarshalMsgpack(b *testing.B) {
	const rows, cols = 2000, 10
	grid := gridShape(rows, cols)
	b.ReportAllocs()
	for range b.N {
		if _, err := msgpack.Marshal(grid); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

func BenchmarkGridUnmarshalMsgpack(b *testing.B) {
	const rows, cols = 2000, 10
	data, err := msgpack.Marshal(gridShape(rows, cols))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		var grid [][]Value
		if err := msgpack.Unmarshal(data, &grid); err != nil {
			b.Fatal(err)
		}
		if len(grid) != rows {
			b.Fatal("grid")
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

// BenchmarkQueryResultSetMarshal 给出「一整页经 REST 编码」的端到端形状
// （server 每个 /query 响应都走这里）。
func BenchmarkQueryResultSetMarshal(b *testing.B) {
	const rows, cols = 2000, 10
	res := &QueryResult{
		Columns: make([]ResultColumn, cols),
		Rows:    gridShape(rows, cols),
	}
	for i := range res.Columns {
		res.Columns[i] = ResultColumn{Name: fmt.Sprintf("c%d", i), DataType: "TEXT"}
	}
	b.ReportAllocs()
	for range b.N {
		if _, err := msgpack.Marshal(res); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

// BenchmarkCellRoundTrip 是「每格」的最小口径：一次编码、一次解码，
// 与整页基准互为交叉验证（整页受 GC 影响大，单格看分配次数）。
func BenchmarkCellRoundTrip(b *testing.B) {
	text := NewStringValue("cell-0-1-some-varchar-payload")
	wireText, err := text.MarshalMsgpack()
	if err != nil {
		b.Fatal(err)
	}
	num := NewIntValue(424242)
	wireNum, err := num.MarshalMsgpack()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		if _, err := text.MarshalMsgpack(); err != nil {
			b.Fatal(err)
		}
		var a Value
		if err := a.UnmarshalMsgpack(wireText); err != nil {
			b.Fatal(err)
		}
		if _, err := num.MarshalMsgpack(); err != nil {
			b.Fatal(err)
		}
		var c Value
		if err := c.UnmarshalMsgpack(wireNum); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(2, "cells/op")
}

// TestGridWireStability 锁住三段的互操作：构造 → msgpack → JSON 必须原样回来，
// 且与 JSON 通道（同一条数据的另一份编码）语义一致。
func TestGridWireStability(t *testing.T) {
	grid := gridShape(50, 10)
	data, err := msgpack.Marshal(grid)
	if err != nil {
		t.Fatal(err)
	}
	var back [][]Value
	if err := msgpack.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for r := range grid {
		for c := range grid[r] {
			j0, err0 := json.Marshal(grid[r][c])
			j1, err1 := json.Marshal(back[r][c])
			if err0 != nil || err1 != nil {
				t.Fatalf("r=%d c=%d: %v / %v", r, c, err0, err1)
			}
			if string(j0) != string(j1) {
				t.Errorf("r=%d c=%d: round trip changed value\n got=%s\n want=%s", r, c, j1, j0)
			}
		}
	}
}

// ─── 同进程 A/B 对照臂：手写快路径**之前**的单元格编解码 ──────────────
//
// legacy* 全部是 git HEAD（改动前）的标量分支逐字拷贝：`json.Unmarshal` 进
// 具体类型 + `msgpack.Marshal` 递交库，以及构造器里的 `json.Marshal`。
// 之所以不用 value_msgpack_test.go 里现成的 treeMarshalValue 当「之前」：
// 那是容器回落路径（decoder + normalize + Marshal(any)），比旧的标量分支更贵，
// 拿它对照会把收益夸大。这里的判据是 TestLegacyArmsByteIdentical：
// 对照臂与现网路径**必须**产出同一份 wire 与同一份 JSON 原文，否则 A/B 数字
// 说的就不是「省了什么」而是「两边在编不同的东西」。

type legacyValue struct{ raw json.RawMessage }

func legacyIsNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func legacyMarshalScalarMsgpack(raw []byte) (data []byte, handled bool, err error) {
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
		var u uint64
		if json.Unmarshal(t, &u) == nil {
			b, err := msgpack.Marshal(u)
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

func (v legacyValue) MarshalMsgpack() ([]byte, error) {
	if legacyIsNull(v.raw) {
		return msgpack.Marshal(nil)
	}
	if b, handled, err := legacyMarshalScalarMsgpack(v.raw); handled || err != nil {
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

func (v *legacyValue) UnmarshalMsgpack(data []byte) error {
	if raw, handled, err := legacyUnmarshalScalarMsgpack(data); handled || err != nil {
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

func legacyUnmarshalScalarMsgpack(data []byte) (raw json.RawMessage, handled bool, err error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	toRaw := func(x any) json.RawMessage {
		b, _ := json.Marshal(x)
		return b
	}
	switch c := data[0]; {
	case c == 0xc0:
		return nil, true, nil
	case c == 0xc2 || c == 0xc3:
		var x bool
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c <= 0x7f || c >= 0xe0:
		var x int64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c >= 0xcc && c <= 0xd3 && c != 0xcf:
		var x int64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xcf:
		var x uint64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xca:
		var x float32
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c == 0xcb:
		var x float64
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	case c >= 0xa0 && c <= 0xbf || c == 0xd9 || c == 0xda || c == 0xdb:
		var x string
		if err := msgpack.Unmarshal(data, &x); err != nil {
			return nil, false, nil
		}
		return toRaw(x), true, nil
	}
	return nil, false, nil
}

// 构造器对照臂：改动前 NewIntValue/NewFloatValue/NewStringValue 都是 json.Marshal。
func legacyNewIntValue(v int64) json.RawMessage     { b, _ := json.Marshal(v); return b }
func legacyNewFloatValue(v float64) json.RawMessage { b, _ := json.Marshal(v); return b }
func legacyNewStringValue(v string) json.RawMessage { b, _ := json.Marshal(v); return b }

func legacyGridShape(rows, cols int) [][]legacyValue {
	ps := gridPayloads(rows, cols)
	g := make([][]legacyValue, rows)
	for r := range g {
		row := make([]legacyValue, cols)
		for c := range cols {
			switch p := ps[r*cols+c]; p.kind {
			case kindInt:
				row[c] = legacyValue{raw: legacyNewIntValue(p.i)}
			case kindFloat:
				row[c] = legacyValue{raw: legacyNewFloatValue(p.f)}
			default:
				row[c] = legacyValue{raw: legacyNewStringValue(p.text)}
			}
		}
		g[r] = row
	}
	return g
}

// TestLegacyArmsByteIdentical 钉住对照臂的忠实度：同一张网格，旧路径与新路径
// 的 wire 字节必须逐位相同，解码回的 JSON 原文也必须逐格相同。这条红了就说明
// 对照臂在编不同的东西，A/B 数字作废。
func TestLegacyArmsByteIdentical(t *testing.T) {
	const rows, cols = 200, 10
	fast := gridShape(rows, cols)
	old := legacyGridShape(rows, cols)
	for r := range old {
		for c := range old[r] {
			if string(fast[r][c].raw) != string(old[r][c].raw) {
				t.Fatalf("构造器分叉 r=%d c=%d: new=%q legacy=%q",
					r, c, fast[r][c].raw, old[r][c].raw)
			}
		}
	}
	fastWire, err := msgpack.Marshal(fast)
	if err != nil {
		t.Fatal(err)
	}
	oldWire, err := msgpack.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fastWire, oldWire) {
		t.Fatalf("整页 wire 分叉：new %d 字节 legacy %d 字节", len(fastWire), len(oldWire))
	}
	var backFast [][]Value
	var backOld [][]legacyValue
	if err := msgpack.Unmarshal(fastWire, &backFast); err != nil {
		t.Fatal(err)
	}
	if err := msgpack.Unmarshal(oldWire, &backOld); err != nil {
		t.Fatal(err)
	}
	for r := range backFast {
		for c := range backFast[r] {
			if string(backFast[r][c].raw) != string(backOld[r][c].raw) {
				t.Fatalf("解码分叉 r=%d c=%d: new=%q legacy=%q",
					r, c, backFast[r][c].raw, backOld[r][c].raw)
			}
		}
	}
}

func BenchmarkGridConstructLegacy(b *testing.B) {
	const rows, cols = 2000, 10
	ps := gridPayloads(rows, cols)
	b.ReportAllocs()
	for range b.N {
		g := make([][]legacyValue, rows)
		for r := range g {
			row := make([]legacyValue, cols)
			for c := range cols {
				switch p := ps[r*cols+c]; p.kind {
				case kindInt:
					row[c] = legacyValue{raw: legacyNewIntValue(p.i)}
				case kindFloat:
					row[c] = legacyValue{raw: legacyNewFloatValue(p.f)}
				default:
					row[c] = legacyValue{raw: legacyNewStringValue(p.text)}
				}
			}
			g[r] = row
		}
		if len(g) != rows {
			b.Fatal("grid")
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

func BenchmarkGridMarshalMsgpackLegacy(b *testing.B) {
	const rows, cols = 2000, 10
	grid := legacyGridShape(rows, cols)
	b.ReportAllocs()
	for range b.N {
		if _, err := msgpack.Marshal(grid); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}

func BenchmarkGridUnmarshalMsgpackLegacy(b *testing.B) {
	const rows, cols = 2000, 10
	data, err := msgpack.Marshal(legacyGridShape(rows, cols))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		var grid [][]legacyValue
		if err := msgpack.Unmarshal(data, &grid); err != nil {
			b.Fatal(err)
		}
		if len(grid) != rows {
			b.Fatal("grid")
		}
	}
	b.ReportMetric(float64(rows*cols), "cells/op")
}
