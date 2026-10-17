package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/vmihailenco/msgpack/v5"
)

// Value 在 msgpack 数据面上编码为原生值（与 Rust untagged enum 对齐），
// 但内部以 JSON RawMessage 保存以便 JSON / Arrow 双向转换。
//
// 标量值走直通编码：结果集里绝大多数 cell 是字符串/数字/布尔，若无谓通过
// json→any→normalize→msgpack 三趟并整树分配；容器仍走树路径（少见且需要
// Number 语义）。两条路径的 wire 字节逐位一致（value_msgpack_test.go 锁定）。
//
// 直通路径不借道 reflect：msgpack 的帧头取值与 vmihailenco/msgpack v5.4.1 的
// 默认编码逐字节对齐（spec/encoding.md §2），JSON 文本由 value_json.go 手写、
// 与 encoding/json 逐字节对拍（value_json_fastpath_test.go）。一次 MarshalMsgpack
// 只分配结果帧本身这一块（TestScalarFastPathIsTaken 钉住 ≤1 次）；整页 2 万格
// 混合网格的编码分配从 11.8 万次降到 4.3 万次、字节数从 4.9 MB 降到 2.3 MB
// （BenchmarkGridMarshalMsgpack 对照 BenchmarkGridMarshalMsgpackLegacy，
// 对照臂是改动前代码的逐字拷贝，忠实度由 TestLegacyArmsByteIdentical 钉）。
func (v Value) MarshalMsgpack() ([]byte, error) {
	if v.IsNull() {
		// msgpack.Marshal(nil) 恒等于单字节 0xc0；手写省掉编码器池 + 拷贝。
		// 对拍见 TestNullCellWireByteMatchesLibrary。
		return []byte{0xc0}, nil
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
//
// 铁律：直通路径**只处理能被严格判定为合法 JSON 标量的输入**，任何可疑形状
// （未闭合字符串、裸控制字节、未知转义、超 uint64 整数、前导 0）一律 declined，
// 由树路径给出与历史逐字一致的报错，绝不在此处自造错误文案。
func marshalScalarMsgpack(raw []byte) (data []byte, handled bool, err error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil, false, nil
	}
	switch c := t[0]; {
	case c == '{' || c == '[':
		return nil, false, nil
	case c == '"':
		payload, ok := jsonStringPayload(t)
		if !ok {
			return nil, false, nil
		}
		return appendMsgpackString(nil, payload), true, nil
	case c == 't':
		if string(t) != "true" {
			return nil, false, nil
		}
		return []byte{0xc3}, true, nil
	case c == 'f':
		if string(t) != "false" {
			return nil, false, nil
		}
		return []byte{0xc2}, true, nil
	case c == 'n':
		if string(t) != "null" {
			return nil, false, nil
		}
		return []byte{0xc0}, true, nil
	case c == '-' || (c >= '0' && c <= '9'):
		b, ok := appendMsgpackNumber(nil, t)
		return b, ok, nil
	}
	return nil, false, nil
}

// ─── JSON 标量 → msgpack ────────────────────────────────────

// jsonStringPayload 校验一段 JSON 字符串原文并给出**解码后**的字节。
// 无转义时直接返回原文内层切片（零拷贝）；带转义时解进新切片。
func jsonStringPayload(t []byte) ([]byte, bool) {
	if len(t) < 2 || t[len(t)-1] != '"' {
		return nil, false
	}
	body := t[1 : len(t)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		// 无转义：仍要拒绝内层裸引号与裸控制字节（json.Unmarshal 对它们报错）。
		for _, b := range body {
			if b == '"' || b < 0x20 {
				return nil, false
			}
		}
		return body, true
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		b := body[i]
		if b == '"' || b < 0x20 {
			return nil, false
		}
		if b != '\\' {
			out = append(out, b)
			i++
			continue
		}
		i++
		if i >= len(body) {
			return nil, false
		}
		switch e := body[i]; e {
		case '"', '\\', '/':
			out = append(out, e)
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'u':
			r, next, ok := decodeJSONEscapeU(body, i)
			if !ok {
				return nil, false
			}
			out = utf8.AppendRune(out, r)
			i = next
		default:
			return nil, false
		}
	}
	return out, true
}

// decodeJSONEscapeU 从 body[i]=='u' 处解一个 \uXXXX（含代理对），返回码点与
// 消费到的下标。孤立/非法代理换成 U+FFFD，与 json.Unmarshal 的解码一致。
func decodeJSONEscapeU(body []byte, i int) (rune, int, bool) {
	v1, ok := parseHex4(body, i+1)
	if !ok {
		return 0, 0, false
	}
	next := i + 5
	if !isHighSurrogate(v1) {
		return rune(v1), next, true
	}
	if next+6 > len(body) || body[next] != '\\' || body[next+1] != 'u' {
		return utf8.RuneError, next, true
	}
	v2, ok := parseHex4(body, next+2)
	if !ok || !isLowSurrogate(v2) {
		return utf8.RuneError, next, true
	}
	return rune(0x10000 + (v1-0xD800)<<10 + (v2 - 0xDC00)), next + 6, true
}

func isHighSurrogate(v uint32) bool { return v >= 0xd800 && v <= 0xdbff }
func isLowSurrogate(v uint32) bool  { return v >= 0xdc00 && v <= 0xdfff }

func parseHex4(b []byte, at int) (uint32, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	var v uint32
	for _, c := range b[at : at+4] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// appendMsgpackString 写 str 帧头 + 原文。头部宽度规则与 msgpack v5.4.1 的
// encodeStringLen 逐字节一致（<32 fixstr、<256 str8、≤uint16 str16、否则 str32）。
//
// dst 为 nil 时按帧总长一次分配，避免 append 的扩容链把「一帧一分配」变成
// 「一帧两三次」。
func appendMsgpackString(dst, payload []byte) []byte {
	n := len(payload)
	hdr := 1
	switch {
	case n < 32:
	case n < 256:
		hdr = 2
	case n <= 0xffff:
		hdr = 3
	default:
		hdr = 5
	}
	if dst == nil {
		dst = make([]byte, 0, hdr+n)
	}
	switch {
	case n < 32:
		dst = append(dst, byte(0xa0|n))
	case n < 256:
		dst = append(dst, 0xd9, byte(n))
	case n <= 0xffff:
		dst = append(dst, 0xda, byte(n>>8), byte(n))
	default:
		dst = append(dst, 0xdb, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(dst, payload...)
}

// appendMsgpackUint 写 uint64 帧。这里**不是**最小表示：msgpack v5 对
// `any(uint64)` 走 EncodeUint64，恒写 0xcf + 8 字节大端（容器路径同样如此，
// 见 TestMsgpackScalarFramingMatchesLibrary 逐宽度对拍）。最小表示只在
// 编码器直接收到 `int`/`uint` 静态类型时才用，本包的取值一律按 any 递交。
func appendMsgpackUint(dst []byte, n uint64) []byte {
	return append(dst, 0xcf,
		byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
		byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// appendMsgpackInt 写 int64 帧（0xd3 + 8 字节大端），理由同 appendMsgpackUint。
func appendMsgpackInt(dst []byte, n int64) []byte {
	u := uint64(n)
	return append(dst, 0xd3,
		byte(u>>56), byte(u>>48), byte(u>>40), byte(u>>32),
		byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
}

// appendMsgpackFloat 写 float64 帧：0xcb + 8 字节大端，共 9 字节定长。
// 一次 append 出去；逐字节 append 会让 nil dst 走 1→2→4→8→16 四次分配。
func appendMsgpackFloat(dst []byte, f float64) []byte {
	bits := math.Float64bits(f)
	return append(dst, 0xcb,
		byte(bits>>56), byte(bits>>48), byte(bits>>40), byte(bits>>32),
		byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))
}

// appendMsgpackNumber 按 JSON 数字文法分整数/浮点两族编帧。
// 覆盖不了的形状返回 ok=false 退回树路径，保持旧语义：
// 整数超 uint64 → 树路径按 float64 编码；非法文本报错。
//
// 整数字面量**手工累加数字**而不是 strconv.ParseInt(string(t),…)：后者每格
// 都要把原文拷成字符串（一次额外分配），而手工累加是纯算术。溢出即 ok=false
// 退回树路径，行为不变。
func appendMsgpackNumber(dst []byte, t []byte) ([]byte, bool) {
	i := 0
	neg := false
	if t[0] == '-' {
		i, neg = 1, true
	}
	if i >= len(t) || !isDigit(t[i]) {
		return nil, false
	}
	// 纯整数才允许没有小数点/指数；先扫一遍定位分隔符（等价于 IndexAny(".eE")
	// 但不构造 cutset 表）。
	for k := i; k < len(t); k++ {
		if c := t[k]; c == '.' || c == 'e' || c == 'E' {
			return appendMsgpackFloatLiteral(dst, t)
		}
	}
	// JSON 文法禁止前导 0（"0" 除外），否则 json.Unmarshal 报错。
	if len(t)-i > 1 && t[i] == '0' {
		return nil, false
	}
	u, overflow := digitsToUint64(t[i:])
	if overflow {
		// 超 uint64：旧路径 normalize 后落 float64，这里同判（保持等价）。
		return appendMsgpackFloatLiteral(dst, t)
	}
	if neg {
		switch {
		case u <= math.MaxInt64:
			return appendMsgpackInt(dst, -int64(u)), true
		case u == uint64(math.MaxInt64)+1:
			return appendMsgpackInt(dst, math.MinInt64), true
		default:
			// 低于 MinInt64：旧路径按 float64 编码。
			return appendMsgpackFloatLiteral(dst, t)
		}
	}
	if u <= math.MaxInt64 {
		return appendMsgpackInt(dst, int64(u)), true
	}
	// Unsigned values above MaxInt64 stay exact as msgpack uint64; falling
	// straight through to float64 would drop low bits (and would disagree
	// with the container path, which normalizes json.Number the same way).
	return appendMsgpackUint(dst, u), true
}

// digitsToUint64 累加一串十进制数字（无符号、无前导 0 校验），溢出时置
// overflow 并停止。
func digitsToUint64(t []byte) (u uint64, overflow bool) {
	for _, b := range t {
		if !isDigit(b) {
			return 0, true
		}
		if u > (math.MaxUint64-uint64(b-'0'))/10 {
			return 0, true
		}
		u = u*10 + uint64(b-'0')
	}
	return u, false
}

func appendMsgpackFloatLiteral(dst []byte, t []byte) ([]byte, bool) {
	if !validJSONNumberLiteral(t) {
		return nil, false
	}
	f, err := strconv.ParseFloat(string(t), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}
	return appendMsgpackFloat(dst, f), true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// validJSONNumberLiteral 校验 RFC 8259 §6 的数字文法（前导 0、空小数位、
// 空指数位都判非法——与 encoding/json 的解码器一致）。
func validJSONNumberLiteral(t []byte) bool {
	i := 0
	if i < len(t) && t[i] == '-' {
		i++
	}
	start := i
	for i < len(t) && isDigit(t[i]) {
		i++
	}
	if i == start || (i-start > 1 && t[start] == '0') {
		return false
	}
	if i < len(t) && t[i] == '.' {
		i++
		frac := i
		for i < len(t) && isDigit(t[i]) {
			i++
		}
		if i == frac {
			return false
		}
	}
	if i < len(t) && (t[i] == 'e' || t[i] == 'E') {
		i++
		if i < len(t) && (t[i] == '+' || t[i] == '-') {
			i++
		}
		exp := i
		for i < len(t) && isDigit(t[i]) {
			i++
		}
		if i == exp {
			return false
		}
	}
	return i == len(t)
}

// ─── msgpack → JSON 标量 ────────────────────────────────────

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
// 帧长必须**正好**吃掉整个 data（旧路径的 msgpack.Unmarshal 同样拒绝多余字节），
// 任何不满足的形状一律 declined 退回树路径，错误文案保持原样。
func unmarshalScalarMsgpack(data []byte) (raw json.RawMessage, handled bool, err error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	c := data[0]
	switch {
	case c == 0xc0: // nil：内部以 raw=nil 表示
		return nil, true, nil
	case c == 0xc2:
		return json.RawMessage("false"), true, nil
	case c == 0xc3:
		return json.RawMessage("true"), true, nil
	case c <= 0x7f: // positive fixint
		return appendJSONNumberInt(nil, int64(c)), true, nil
	case c >= 0xe0: // negative fixint
		return appendJSONNumberInt(nil, int64(int8(c))), true, nil
	case c >= 0xa0 && c <= 0xbf: // fixstr
		n := int(c & 0x1f)
		if 1+n != len(data) {
			return nil, false, nil
		}
		return appendJSONString(nil, data[1:1+n]), true, nil
	case c == 0xd9 || c == 0xda || c == 0xdb: // str8 / str16 / str32
		n, hdr, ok := msgpackStrLen(data)
		if !ok || hdr+n != len(data) {
			return nil, false, nil
		}
		return appendJSONString(nil, data[hdr:n+hdr]), true, nil
	case c >= 0xcc && c <= 0xd3 && c != 0xcf: // int8..int64、uint8/16/32（都放得进 int64）
		w := msgpackPayloadLen(c)
		if len(data) != 1+w {
			return nil, false, nil
		}
		return appendJSONNumberInt(nil, msgpackUnpackInt(c, unpackBE(data, 1, w))), true, nil
	case c == 0xcf: // uint64
		if len(data) != 9 {
			return nil, false, nil
		}
		return appendJSONNumberUint(nil, unpackBE(data, 1, 8)), true, nil
	case c == 0xca: // float32（必须按 float32 渲染，升 float64 会出 1.100000023841858）
		if len(data) != 5 {
			return nil, false, nil
		}
		return appendJSONFloatStrict(nil, float64(math.Float32frombits(uint32(unpackBE(data, 1, 4)))), 32)
	case c == 0xcb: // float64
		if len(data) != 9 {
			return nil, false, nil
		}
		return appendJSONFloatStrict(nil, math.Float64frombits(unpackBE(data, 1, 8)), 64)
	}
	return nil, false, nil
}

// appendJSONFloatStrict 与标量族其它分支不同，Inf/NaN 是 msgpack 能表达、
// 而 encoding/json 会**报错**的取值：旧树路径正是在这里返回
// "value re-encode json: ..."。这里保持同样的错误语义。
func appendJSONFloatStrict(dst []byte, f float64, bits int) (json.RawMessage, bool, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false, fmt.Errorf("value re-encode json: json: unsupported value: %s", strconv.FormatFloat(f, 'g', -1, bits))
	}
	b, _ := appendJSONFloat(dst, f, bits)
	return json.RawMessage(b), true, nil
}

func msgpackUnpackInt(c byte, n uint64) int64 {
	switch c {
	case 0xcc, 0xcd, 0xce, 0xd3:
		return int64(n)
	case 0xd0:
		return int64(int8(n))
	case 0xd1:
		return int64(int16(n))
	case 0xd2:
		return int64(int32(n))
	}
	return 0
}

func msgpackPayloadLen(c byte) int {
	switch c {
	case 0xcc, 0xd0:
		return 1
	case 0xcd, 0xd1:
		return 2
	case 0xce, 0xd2, 0xca:
		return 4
	default:
		return 8
	}
}

func msgpackStrLen(data []byte) (n, hdr int, ok bool) {
	switch data[0] {
	case 0xd9:
		if len(data) < 2 {
			return 0, 0, false
		}
		return int(data[1]), 2, true
	case 0xda:
		if len(data) < 3 {
			return 0, 0, false
		}
		return int(unpackBE(data, 1, 2)), 3, true
	case 0xdb:
		if len(data) < 5 {
			return 0, 0, false
		}
		return int(unpackBE(data, 1, 4)), 5, true
	}
	return 0, 0, false
}

func unpackBE(data []byte, at, width int) uint64 {
	var v uint64
	for _, b := range data[at : at+width] {
		v = v<<8 | uint64(b)
	}
	return v
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
		if u, err := strconv.ParseUint(t.String(), 10, 64); err == nil {
			// Must come before Float64: an unsigned value above MaxInt64 is
			// exact as msgpack uint64, while float64 silently drops low bits.
			// The scalar unmarshal path already keeps it exact (0xcf), so the
			// container tree must not lose it on the way back out.
			return u
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
	// 无转义的字符串单元格占绝大多数：直接切原文，不进 reflect。
	// jsonStringPayload 的校验与 json.Unmarshal 的接受集一致，可疑形状照旧回落。
	if body, ok := jsonStringPayload(v.raw); ok {
		return string(body), true
	}
	return jsonUnquote(v)
}

func jsonUnquote(v Value) (string, bool) {
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
	// 整数字面量单元格占绝大多数：直接扫原文求值，不进 json.Unmarshal
	// （与 TryGetString 的直通同型，数字文法扫描复用 encode 侧的
	// digitsToUint64）。接受集与 Unmarshal 到 int64 完全一致：无小数/指数、
	// 无前导 0、带符号、在 int64 范围内；任何可疑形状照旧回落。
	if i, ok := parseInt64JSONLiteral(bytes.TrimSpace(v.raw)); ok {
		return i, true
	}
	var i int64
	if err := json.Unmarshal(v.raw, &i); err == nil {
		return i, true
	}
	return 0, false
}

// parseInt64JSONLiteral 求值一个 JSON 整数字面量；不认识就返回 false，由
// 调用方回落 json.Unmarshal（错误文案也在那边给出，此处绝不自造）。
func parseInt64JSONLiteral(t []byte) (int64, bool) {
	if len(t) == 0 {
		return 0, false
	}
	i := 0
	neg := false
	if t[0] == '-' {
		i, neg = 1, true
	}
	if i >= len(t) || !isDigit(t[i]) {
		return 0, false
	}
	// 整数才可能被 int64 接受；小数点/指数一律回落。
	for k := i; k < len(t); k++ {
		if c := t[k]; c == '.' || c == 'e' || c == 'E' {
			return 0, false
		}
	}
	// JSON 文法禁止前导 0（"0" 除外），json.Unmarshal 也报错。
	if len(t)-i > 1 && t[i] == '0' {
		return 0, false
	}
	u, overflow := digitsToUint64(t[i:])
	if overflow {
		return 0, false
	}
	if neg {
		switch {
		case u <= math.MaxInt64:
			return -int64(u), true
		case u == uint64(math.MaxInt64)+1:
			// MinInt64 的绝对值没有对应的正数，Unmarshal 接受它。
			return math.MinInt64, true
		}
		return 0, false
	}
	if u <= math.MaxInt64 {
		return int64(u), true
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
