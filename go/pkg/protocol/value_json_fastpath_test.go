package protocol

// 手写数据面编码层的**对拍**测试。
//
// 这一层取代了每格一次的 `json.Marshal`/`json.Unmarshal` reflect 调用，代价是
// 「JSON 文本」与「msgpack 帧」两套规则在这里有了第二份实现。wire 字节是跨
// Go/Rust/TS 的契约（spec/encoding.md、test/contract），所以判据不是抽样看着像，
// 而是拿标准库当**参考实现**逐字节对拍：字符串/浮点转义对 encoding/json，
// 帧头对 vmihailenco/msgpack。语料是穷举码点 + 全部宽度边界 + 固定种子随机。

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// referenceJSON 是参考实现：encoding/json 的 Marshal 输出。
func referenceJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("reference marshal %#v: %v", v, err)
	}
	return b
}

func TestAppendJSONStringMatchesEncodingJSON(t *testing.T) {
	// 1) 全部 256 个单字节输入（含非法 UTF-8 字节，必须与 encoding/json 一样
	//    替换成 \ufffd）。
	for i := 0; i < 256; i++ {
		s := string([]byte{byte(i)})
		want := referenceJSON(t, s)
		got := appendJSONString(nil, s)
		if !bytes.Equal(want, got) {
			t.Fatalf("byte %#02x: got %s want %s", i, got, want)
		}
	}
	// 2) 全部 Unicode 码点（代理位由 Go 的 string(rune) 先替换为 U+FFFD，
	//    两侧看到同一份输入）。
	for r := 0; r <= 0x10FFFF; r++ {
		s := string(rune(r))
		want := referenceJSON(t, "a"+s+"b")
		got := appendJSONString(nil, "a"+s+"b")
		if !bytes.Equal(want, got) {
			t.Fatalf("rune U+%04X: got %s want %s", r, got, want)
		}
	}
	// 3) 随机串：字母表挑最容易被写错的那些（引号、反斜杠、HTML 三件套、
	//    控制字节、多字节、行/段分隔符）。
	alphabet := []rune{'"', '\\', '/', '<', '>', '&', '\n', '\r', '\t', '\b', '\f',
		0x00, 0x01, 0x1f, 0x7f, 'a', 'Z', '0', ' ', 0x2028, 0x2029, '€', '中', '\ufffd'}
	rng := rand.New(rand.NewSource(20261013))
	for i := 0; i < 20000; i++ {
		n := rng.Intn(24)
		var sb bytes.Buffer
		for j := 0; j < n; j++ {
			sb.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		s := sb.String()
		want := referenceJSON(t, s)
		got := appendJSONString(nil, s)
		if !bytes.Equal(want, got) {
			t.Fatalf("random %q: got %s want %s", s, got, want)
		}
	}
	// 4) 随机**原始字节**串（不是随机码点！）：非法 UTF-8 的组合形状——截断的
	//    多字节序列、overlong、孤立代理编码——必须与 encoding/json 一样逐字节
	//    换成一个 U+FFFD（原字节 EF BF BD，而不是 \ufffd 转义文法）。
	byteAlphabet := []byte{0x00, 0x1f, 0x22, 0x2f, 0x3c, 0x3e, 0x5c, 0x7f,
		0x80, 0xbf, 0xc0, 0xc2, 0xdf, 0xe0, 0xe4, 0xed, 0xef, 0xf0, 0xf4, 0xf5,
		0x8f, 0x90, 0xa0, 0xad, 'a', '7', 0xef, 0xbf, 0xbd}
	for i := 0; i < 20000; i++ {
		buf := make([]byte, rng.Intn(9))
		for j := range buf {
			buf[j] = byteAlphabet[rng.Intn(len(byteAlphabet))]
		}
		s := string(buf)
		want := referenceJSON(t, s)
		got := appendJSONString(nil, s)
		if !bytes.Equal(want, got) {
			t.Fatalf("随机字节 % x: got % x want % x", buf, got, want)
		}
		if !bytes.Equal(appendJSONString(nil, buf), got) {
			t.Fatalf("随机字节 % x: string/[]byte 入口分叉", buf)
		}
	}
	// 5) 全部两位十六进制前导字节 + 合法/非法续接组合的定点样本。
	for _, lead := range []byte{0xc0, 0xc1, 0xdf, 0xe0, 0xe4, 0xed, 0xef, 0xf0, 0xf3, 0xf4, 0xf5} {
		for _, cont := range []byte{0x00, 0x7f, 0x80, 0x8f, 0x90, 0xa0, 0xbf, 0xbd} {
			for _, s := range []string{
				string([]byte{lead}),
				string([]byte{lead, cont}),
				string([]byte{lead, cont, cont}),
				string([]byte{lead, cont, cont, cont}),
				"x" + string([]byte{lead, cont}) + "y",
			} {
				want := referenceJSON(t, s)
				got := appendJSONString(nil, s)
				if !bytes.Equal(want, got) {
					t.Fatalf("lead %#02x cont %#02x %q: got % x want % x", lead, cont, s, got, want)
				}
			}
		}
	}
	// 6) []byte 入口（解码侧走的就是这条）与 string 入口必须同输出。
	for _, s := range []string{"a", "带中文", "<&>", "\x00\x1f", "\u2028"} {
		if !bytes.Equal(appendJSONString(nil, s), appendJSONString(nil, []byte(s))) {
			t.Fatalf("string/[]byte 入口分叉: %q", s)
		}
	}
}

func TestAppendJSONFloatMatchesEncodingJSON(t *testing.T) {
	cases := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.5, -0.5, 1e-6, 9.99999e-7, 1e-7,
		1e21, 9.999999999999999e20, 1e22, -1e21, 123456789.123456789,
		math.SmallestNonzeroFloat64, math.MaxFloat64, math.MaxFloat32,
		1.100000023841858, 3.4028234663852886e38, 1e15, 1e16, 1e-4, 1e-5,
	}
	for _, f := range cases {
		want := referenceJSON(t, f)
		got, ok := appendJSONFloat(nil, f, 64)
		if !ok || !bytes.Equal(want, got) {
			t.Fatalf("float %v: got %q ok=%v want %q", f, got, ok, want)
		}
	}
	// 随机位型（排除 json 不接受的 Inf/NaN，那两个必须 ok=false）。
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 200000; i++ {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		want := referenceJSON(t, f)
		got, ok := appendJSONFloat(nil, f, 64)
		if !ok || !bytes.Equal(want, got) {
			t.Fatalf("float bits %x: got %q ok=%v want %q", math.Float64bits(f), got, ok, want)
		}
	}
	// float32（解码侧 0xca 分支用 bits=32，升 float64 会多出一串二进制尾数）。
	for i := 0; i < 50000; i++ {
		f := float64(math.Float32frombits(rng.Uint32()))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		want := referenceJSON(t, float32(f))
		got, ok := appendJSONFloat(nil, f, 32)
		if !ok || !bytes.Equal(want, got) {
			t.Fatalf("float32 %v: got %q ok=%v want %q", f, got, ok, want)
		}
	}
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := appendJSONFloat(nil, f, 64); ok {
			t.Fatalf("%v 必须走 ok=false（encoding/json 对它是报错）", f)
		}
	}
}

func TestValueConstructorsMatchJSONMarshal(t *testing.T) {
	strs := []string{"", "x", "带中文", "<a>&\"\\", "\n\t\x00", "\u2028", string([]byte{0xff})}
	for _, s := range strs {
		want := referenceJSON(t, s)
		if got := NewStringValue(s).raw; !bytes.Equal(want, got) {
			t.Errorf("NewStringValue(%q) = %s want %s", s, got, want)
		}
	}
	for _, i := range []int64{0, 1, -1, 127, 128, -128, -129, 1e15, math.MaxInt64, math.MinInt64} {
		want := referenceJSON(t, i)
		if got := NewIntValue(i).raw; !bytes.Equal(want, got) {
			t.Errorf("NewIntValue(%d) = %s want %s", i, got, want)
		}
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		want := referenceJSON(t, f)
		if got := NewFloatValue(f).raw; !bytes.Equal(want, got) {
			t.Errorf("NewFloatValue(%v) = %s want %s", f, got, want)
		}
	}
	// json.Marshal 对 Inf/NaN 是**报错**，旧构造器因此产出 raw=nil（一个 NULL 格）。
	// 新构造器必须保持同一形状，不能悄悄变成一个合法数字。
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if v := NewFloatValue(f); !v.IsNull() {
			t.Errorf("NewFloatValue(%v) = %q, want NULL（与 json.Marshal 报错时的旧语义一致）", f, v.raw)
		}
	}
}

// TestMsgpackScalarFramingMatchesLibrary 逐宽度钉住帧头：整数走 msgpack v5 对
// `any(int64/uint64)` 的**定宽**写法（0xd3/0xcf），str 走最小宽度。
// 这一条是手写帧代码与库的唯一判据，写错一个码就是双实现线上不兼容。
func TestMsgpackScalarFramingMatchesLibrary(t *testing.T) {
	ints := []int64{0, 1, -1, 31, 32, 127, 128, 255, 256, 65535, 65536,
		math.MaxInt32, math.MaxInt32 + 1, math.MinInt32, math.MinInt32 - 1,
		math.MaxInt64, math.MinInt64}
	for _, i := range ints {
		got, ok := appendMsgpackNumber(nil, []byte(strconv.FormatInt(i, 10)))
		if !ok {
			t.Fatalf("int %d 未走直通", i)
		}
		want, err := msgpack.Marshal(i) // 树路径递交的也是 any(int64)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("int64 %d: got %x want %x", i, got, want)
		}
	}
	uints := []uint64{uint64(math.MaxInt64) + 1, math.MaxUint32 + 1, math.MaxUint64, 1 << 40}
	for _, u := range uints {
		raw := strconv.FormatUint(u, 10)
		got, ok := appendMsgpackNumber(nil, []byte(raw))
		if !ok {
			t.Fatalf("uint %d 未走直通", u)
		}
		// JSON 数字没有 int/uint 类型，语义由**树路径**定义（json.Number →
		// Int64 优先，落不进才 uint64/float64）。>MaxInt64 的取值两边都必须是
		// 定宽 0xcf；≤MaxInt64 必须是定宽 0xd3。
		want, err := treeMarshalValue([]byte(raw))
		if err != nil {
			t.Fatalf("tree %d: %v", u, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("无符号 %d: got %x want %x", u, got, want)
		}
		if u > math.MaxInt64 {
			lib, err := msgpack.Marshal(u)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, lib) {
				t.Errorf("无符号 %d: got %x 库(0xcf 定宽) %x", u, got, lib)
			}
		}
	}
	for _, n := range []int{0, 1, 30, 31, 32, 254, 255, 256, 65534, 65535, 65536} {
		s := strings.Repeat("x", n)
		got, ok, err := marshalScalarMsgpack([]byte(strconv.Quote(s)))
		if err != nil {
			t.Fatalf("str len %d: %v", n, err)
		}
		if !ok {
			t.Fatalf("str len %d 未走直通", n)
		}
		want, err := msgpack.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("str len %d: got %d 字节 want %d 字节（前 4 帧头 got=% x want=% x）",
				n, len(got), len(want), got[:min(4, len(got))], want[:min(4, len(want))])
		}
	}
	if got, err := (Value{}).MarshalMsgpack(); err != nil || !bytes.Equal(got, []byte{0xc0}) {
		t.Errorf("nil cell: got %x err=%v, want 0xc0", got, err)
	}
	if want, err := msgpack.Marshal(nil); err != nil || !bytes.Equal(want, []byte{0xc0}) {
		t.Fatalf("库侧 nil 帧不是 0xc0：%x %v", want, err)
	}
}

// marshalScalarForTest 直接调用直通编码入口，返回 (帧, 是否命中直通)。
func marshalScalarForTest(rawJSON string) ([]byte, bool, error) {
	return marshalScalarMsgpack([]byte(rawJSON))
}

var sink []byte

var sink2 string

// TestScalarFastPathIsTaken 是「必须命中」断言：直通路径若被改成永远
// handled=false（或有人把它删了），这里会红——不是靠计时器发现的回归。
func TestScalarFastPathIsTaken(t *testing.T) {
	scalars := []string{
		`""`, `"x"`, `"some-varchar-value-0123456789"`, `"带中文"`, `"a\"b"`, `"a\/b"`,
		`"\u00e9"`, `"\ud83d\ude00"`, `"\n\t"`,
		`0`, `1`, `-1`, `424242`, `9223372036854775807`, `-9223372036854775808`,
		`18446744073709551615`, `1.5`, `-0.5`, `1e3`, `1.7976931348623157e308`,
		`true`, `false`, `null`,
	}
	for _, s := range scalars {
		if _, ok, _ := marshalScalarForTest(s); !ok {
			t.Errorf("%s 没能走直通（marshal 侧）", s)
		}
	}
	// 分配数上限：一个标量单元格只许分配它自己的帧（浮点多一次是
	// strconv.ParseFloat 只吃 string，原文≤24 字节的转换是它的固有成本）。
	// 若直通路径失效，reflect 会带回 4~7 次分配，这条会先红。
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`"some-varchar-value-0123456789"`, 1},
		{`424242`, 1},
		{`-9223372036854775808`, 1},
		{`true`, 1},
		{`1.5`, 2},
	} {
		v := Value{raw: json.RawMessage(tc.raw)}
		n := testing.AllocsPerRun(200, func() {
			b, err := v.MarshalMsgpack()
			if err != nil {
				t.Fatal(err)
			}
			sink = b
		})
		if n > float64(tc.want) {
			t.Errorf("%s: MarshalMsgpack 分配 %.0f 次，want ≤%d（直通路径失效？）", tc.raw, n, tc.want)
		}
	}
	for _, raw := range []string{`"some-varchar-value-0123456789"`, `424242`} {
		v := Value{raw: json.RawMessage(raw)}
		wire, err := v.MarshalMsgpack()
		if err != nil {
			t.Fatal(err)
		}
		n := testing.AllocsPerRun(200, func() {
			var back Value
			if err := back.UnmarshalMsgpack(wire); err != nil {
				t.Fatal(err)
			}
			sink = back.raw
		})
		if n > 1 {
			t.Errorf("%s: UnmarshalMsgpack 分配 %.0f 次，want ≤1（直通路径失效？）", raw, n)
		}
	}
	// 负对照：容器与可疑形状**必须**落回树路径，否则说明直通在越权吞输入。
	declines := []string{
		`{"a":1}`, `[1,2]`, `{"$blob":{"len":2,"base64":"AQI="}}`,
		`"未闭合`, `"内层"引号"`, "串里裸控制字节\x01", `"坏转义\q"`,
		`01`, `1e`, `-`, `+1`, `truex`, `nul`, `1e999`, `-1e999`,
	}
	for _, s := range declines {
		if _, ok, _ := marshalScalarForTest(s); ok {
			t.Errorf("%q 不该走直通（应退回树路径）", s)
		}
	}
	// 与树路径逐字节对拍最容易被改坏的形状：定宽整型帧头、str 帧头、以及
	// 「超出 uint64 的整数落 float」这条边界（旧行为就是 float，不是报错）。
	for _, raw := range []string{
		`0`, `12345`, `-5`, `1.5`, `-0`, `9223372036854775807`, `-9223372036854775808`,
		`9223372036854775808`, `18446744073709551615`, `18446744073709551616`,
		`1e3`, `0.000001`, `""`, `"带中文"`, `true`, `false`, `null`,
	} {
		fast, ok, err := marshalScalarForTest(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		tree, err := treeMarshalValue([]byte(raw))
		if err != nil {
			t.Fatalf("tree %s: %v", raw, err)
		}
		if !ok {
			// 未接管也算等价（树路径接手），但整型/字符串边界必须被接管。
			continue
		}
		if !bytes.Equal(fast, tree) {
			t.Errorf("%s: fast=%x tree=%x", raw, fast, tree)
		}
	}
}

// TestScalarDecodeFastPathIsTaken 钉住解码侧的直通命中与负对照。
func TestScalarDecodeFastPathIsTaken(t *testing.T) {
	natives := []any{
		nil, true, false, int64(0), int64(-1), int64(math.MaxInt64), int64(math.MinInt64),
		uint64(math.MaxUint64), float32(1.5), float64(3.14), "x", "带中文", "<&>",
		strings.Repeat("y", 40),
	}
	for _, n := range natives {
		data, err := msgpack.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := unmarshalScalarMsgpack(data); !ok || err != nil {
			t.Errorf("%#v 的帧 %x 没走直通（ok=%v err=%v）", n, data, ok, err)
		}
	}
	for _, n := range []any{
		[]any{int64(1)}, map[string]any{"a": int64(1)}, []byte{1, 2, 3},
	} {
		data, err := msgpack.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := unmarshalScalarMsgpack(data); ok {
			t.Errorf("%#v 的帧 %x 不该走直通", n, data)
		}
	}
	// 帧长不符（有多余字节 / 被截断）一律退回树路径。
	one, err := msgpack.Marshal(int64(7))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := unmarshalScalarMsgpack(append(one, 0x00)); ok {
		t.Error("尾部多余字节的 int 帧不该走直通")
	}
	if _, ok, _ := unmarshalScalarMsgpack(one[:5]); ok {
		t.Error("被截断的 int 帧不该走直通")
	}
	if _, ok, _ := unmarshalScalarMsgpack([]byte{}); ok {
		t.Error("空输入不该走直通")
	}
}

// TestScalarDecodeMatchesTree 随机帧语料逐字节对拍直通与树路径的 JSON 原文。
func TestScalarDecodeMatchesTree(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	natives := []any{
		nil, true, false,
		int64(0), int64(-1), int64(127), int64(-32), int64(math.MaxInt64), int64(math.MinInt64),
		uint64(math.MaxUint64), uint64(math.MaxInt64) + 1,
		float32(1.1), float32(-0.5), float64(3.14159265358979), math.Copysign(0, -1),
		"", "带中文", "<&>", "\x01\x7f\u2028",
	}
	for _, n := range natives {
		data, err := msgpack.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		want, err := treeUnmarshalValue(data)
		if err != nil {
			t.Fatalf("tree unmarshal %#v: %v", n, err)
		}
		got, ok, err := unmarshalScalarMsgpack(data)
		if err != nil {
			t.Fatalf("fast unmarshal %#v: %v", n, err)
		}
		if !ok {
			t.Fatalf("%#v 的帧没走直通", n)
		}
		if string(want) != string(got) {
			t.Errorf("%#v: fast=%q tree=%q", n, got, want)
		}
	}
	// 随机整数/浮点/字符串帧。
	for i := 0; i < 20000; i++ {
		var n any
		switch i % 4 {
		case 0:
			n = int64(rng.Int63()) * int64(i%97-48)
		case 1:
			n = uint64(rng.Uint64())
		case 2:
			n = math.Float64frombits(rng.Uint64() & (1<<63 - 1))
		case 3:
			var sb bytes.Buffer
			for j := rng.Intn(40); j > 0; j-- {
				sb.WriteRune(rune(rng.Intn(0x10000)))
			}
			n = sb.String()
		}
		data, err := msgpack.Marshal(n)
		if err != nil {
			continue // 随机串可能带代理位，库侧编码报错；树路径同样报错，语义一致
		}
		want, wantErr := treeUnmarshalValue(data)
		got, ok, err := unmarshalScalarMsgpack(data)
		if wantErr != nil {
			// 树路径报错（Inf/NaN 无法用 JSON 表达）时，直通必须**也**报错，
			// 或者干脆不接管；绝不能静默接受。
			if err == nil && ok {
				t.Fatalf("%#v 的帧树路径报错但直通接受了：%s", n, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%#v: 直通在树路径成功的地方报错 %v", n, err)
		}
		if !ok {
			t.Fatalf("%#v 的帧 %x 没走直通", n, data)
		}
		if string(want) != string(got) {
			t.Errorf("%#v: fast=%q tree=%q", n, got, want)
		}
	}
}

// TestValueConstructorsAllocBudget 是构造器侧的「必须命中」断言，带牙齿：
// 走 encoding/json 的旧构造器实测 **2 次**分配（reflect + 内部 buffer 的拷贝），
// 手写路径只许分配原文本身这 **1 次**。把 NewStringValue/NewIntValue/NewFloatValue
// 改回 `json.Marshal`，这里立刻红——而不是等到看基准数字才发现。
func TestValueConstructorsAllocBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func()
		want float64
	}{
		{"NewStringValue", func() { sink = NewStringValue("some-varchar-value-0123456789").raw }, 1},
		{"NewIntValue", func() { sink = NewIntValue(424242).raw }, 1},
		{"NewFloatValue", func() { sink = NewFloatValue(3.5).raw }, 1},
	} {
		if n := testing.AllocsPerRun(500, tc.fn); n > tc.want {
			t.Errorf("%s: 分配 %.0f 次，want ≤%.0f（构造器回到 reflect/json 了？）", tc.name, n, tc.want)
		}
	}
	// 性质断言，不只是均值：三者的输出必须正好是 1 次分配的那一块原文。
	if n := testing.AllocsPerRun(500, func() { sink = NewStringValue("x").raw }); n > 1 {
		t.Errorf("NewStringValue(短串): 分配 %.0f 次，want ≤1", n)
	}
	// TryGetString 的直通分支必须真的被走到：入口判据是 jsonStringPayload
	// 对每一格文本都返回 ok=true（关分支的形态是它开始 decline 文本格）。
	for _, raw := range []string{`""`, `"x"`, `"some-varchar-value-0123456789"`, `"带中文"`, `"a\"b"`, `"\u00e9"`, `"a\n\tb"`, "\"<>&\""} {
		body, ok := jsonStringPayload([]byte(raw))
		if !ok {
			t.Errorf("TryGetString 的入口 %s 没被直通接受", raw)
			continue
		}
		if got, good := TryGetString(Value{raw: json.RawMessage(raw)}); !good || got != string(body) {
			t.Errorf("TryGetString(%s) = %q,%v want %q", raw, got, good, body)
		}
	}
	if _, ok := TryGetString(Value{raw: json.RawMessage(`{"a":1}`)}); ok {
		t.Error("容器不该被 TryGetString 当成字符串")
	}
}

// TryGetString 的两条臂（同进程 A/B）：直通切片 vs 旧的 json.Unmarshal。
// 分配次数两边都是 1，差别全在 CPU（省去一遍完整 JSON 扫描 + 转义解码），
// 所以这里用基准而不是分配断言来钉。
func BenchmarkTryGetString(b *testing.B) {
	v := Value{raw: json.RawMessage(`"some-varchar-value-0123456789"`)}
	b.ReportAllocs()
	for range b.N {
		s, ok := TryGetString(v)
		if !ok {
			b.Fatal("TryGetString")
		}
		sink2 = s
	}
}

func BenchmarkTryGetStringTreePath(b *testing.B) {
	raw := json.RawMessage(`"some-varchar-value-0123456789"`)
	b.ReportAllocs()
	for range b.N {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			b.Fatal(err)
		}
		sink2 = s
	}
}

// TestMarshalJSONValueUnchanged 保证 JSON 通道（TS/Web 与契约测试用它）没被
// 构造器改动影响：Value.MarshalJSON 仍原样吐出 raw。
func TestMarshalJSONValueUnchanged(t *testing.T) {
	var v Value
	if err := json.Unmarshal([]byte(`{"a":[1,"x",null]}`), &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":[1,"x",null]}` {
		t.Fatalf("JSON 通道被改写：%s", b)
	}
}
