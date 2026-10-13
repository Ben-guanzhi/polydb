package protocol

import (
	"math"
	"strconv"
	"unicode/utf8"
)

// 本文件是 Value 内部表示（JSON 原文）的**手写**编码层，取代数据面上每格的
// `json.Marshal` reflect 调用。三条规则严格照 encoding/json 的转义表实现，
// 由 value_json_fastpath_test.go 拿 `encoding/json` 做逐字节对拍（含随机语料
// 与全部边界码点），因此这里不是「近似 JSON」，而是同一段输出的第二次实现。
//
// 与 encoding/json 的默认 `Marshal` 一致：escapeHTML=true（转义 <、>、&）、
// U+2028/U+2029 转义、非法 UTF-8 字节**逐个**替换成 U+FFFD 的原字节（EF BF BD，
// 注意不是 6 字符的 \ufffd 转义文法——这一点由全 256 字节 + 全码点穷举对拍钉住）。

const hexDigits = "0123456789abcdef"

// utf8Replacement 是 U+FFFD 的 UTF-8 编码，encoding/json 对每个非法字节写一次。
var utf8Replacement = [...]byte{0xef, 0xbf, 0xbd}

// appendJSONString 追加一段带引号的 JSON 字符串。
//
// 先按「引号 + 原文 + 引号」预留容量：调用方在热路径上传 nil，若不预留，
// append 会走 1→2→4→…的扩容链，每个多字节单元格凭空多 1~2 次分配。
func appendJSONString[Bytes ~[]byte | ~string](dst []byte, src Bytes) []byte {
	if cap(dst)-len(dst) < len(src)+2 {
		buf := make([]byte, len(dst), len(dst)+len(src)+2)
		copy(buf, dst)
		dst = buf
	}
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(src); {
		b := src[i]
		if b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}
			dst = append(dst, src[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xf])
			}
			i++
			start = i
			continue
		}
		// 多字节：只看当前起的最多 4 字节。用一个栈上窗口解码，避免 []byte↔string
		// 转换在每格分配（stdlib 内部同样是「只截一小段」的写法）。
		var win [utf8.UTFMax]byte
		n := copy(win[:], src[i:min(len(src), i+utf8.UTFMax)])
		c, size := utf8.DecodeRune(win[:n])
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, src[start:i]...)
			dst = append(dst, utf8Replacement[:]...)
			i++
			start = i
			continue
		}
		if c == '\u2028' || c == '\u2029' {
			dst = append(dst, src[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, src[start:]...)
	return append(dst, '"')
}

// appendJSONNumberInt 追加十进制整数原文（与 json.Marshal(int64) 同输出）。
func appendJSONNumberInt(dst []byte, v int64) []byte {
	return strconv.AppendInt(dst, v, 10)
}

// appendJSONNumberUint 追加十进制无符号整数原文（与 json.Marshal(uint64) 同输出）。
func appendJSONNumberUint(dst []byte, v uint64) []byte {
	return strconv.AppendUint(dst, v, 10)
}

// appendJSONFloat 按 encoding/json 的 floatEncoder 追加浮点原文：
// abs∈(0) 且 <1e-6 或 ≥1e21（32 位按 float32 精度比较）用 'e'，指数不补零。
// Inf/NaN 是 encoding/json 的**报错**取值，返回 ok=false 让调用方沿用旧行为。
func appendJSONFloat(dst []byte, f float64, bits int) ([]byte, bool) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return dst, false
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, format, -1, bits)
	if format == 'e' {
		// encoding/json 把 e-09 修成 e-9（指数不补两位）。
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, true
}
