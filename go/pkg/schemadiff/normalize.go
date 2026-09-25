// Package schemadiff 是结构比对（Compare & Sync）的纯函数层 Go 实现。
//
// Rust 侧 rust/crates/schema-diff 是同一套语义的第二实现；两端由
// test/schema-diff-vectors/*.json 共享语料锁死输出（含中文 reason 逐字一致）。
// 因此本文件**只用 ASCII 规则**：小写折叠只处理 ASCII 字母，空白只认 ASCII 空白集，
// 以保证与 Rust 逐字节等价。非 ASCII 标识符不做大小写折叠，属 v1 已知限制。
package schemadiff

import (
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

func isWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == 0x0b || b == 0x0c
}

func isWordByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// lowerASCII 只做 ASCII 小写折叠，非 ASCII 字节原样保留（与 Rust 一致）。
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// foldSpaces 把连续空白压成单个空格并去掉首尾空白。
func foldSpaces(s string) string {
	src := []byte(s)
	out := make([]byte, 0, len(src))
	pending := false
	for _, c := range src {
		if isWS(c) {
			if len(out) > 0 {
				pending = true
			}
			continue
		}
		if pending {
			out = append(out, ' ')
			pending = false
		}
		out = append(out, c)
	}
	return string(out)
}

// squeezePunct 去掉标点旁的空白：numeric (10, 2) → numeric(10,2)，text [ ] → text[]。
func squeezePunct(s string) string {
	src := []byte(s)
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		if !isWS(src[i]) {
			out = append(out, src[i])
			i++
			continue
		}
		if len(out) == 0 {
			i++
			continue
		}
		prev := out[len(out)-1]
		j := i
		for j < len(src) && isWS(src[j]) {
			j++
		}
		var next byte = ' '
		if j < len(src) {
			next = src[j]
		}
		drop := prev == '(' || prev == '[' || prev == ',' ||
			next == '(' || next == ')' || next == '[' || next == ']' || next == ','
		if !drop {
			out = append(out, ' ')
		}
		i = j
	}
	return string(out)
}

// matchingClose 返回与 b[open]=='(' 匹配的 ')' 下标；找不到返回 -1。引号内括号不计。
func matchingClose(b []byte, open int) int {
	depth := 0
	inQuote := false
	for i := open; i < len(b); i++ {
		c := b[i]
		if c == '\'' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var mysqlIntTypes = []string{"tinyint", "smallint", "mediumint", "int", "integer", "bigint"}

func isMySQLIntType(word []byte) bool {
	for _, t := range mysqlIntTypes {
		if string(word) == t {
			return true
		}
	}
	return false
}

func tailWordStart(res []byte) int {
	k := len(res)
	for k > 0 && isWordByte(res[k-1]) {
		k--
	}
	return k
}

// stripMySQLIntWidth 丢弃整数类型的显示宽度：int(11) → int（保留类型名）。
func stripMySQLIntWidth(s string) string {
	b := []byte(s)
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] == '(' {
			if close := matchingClose(b, i); close >= 0 {
				inner := b[i+1 : close]
				ws := tailWordStart(out)
				word := out[ws:]
				widthOnly := len(inner) > 0
				for _, c := range inner {
					if c < '0' || c > '9' {
						widthOnly = false
						break
					}
				}
				boundary := ws == 0 || !isWordByte(out[ws-1])
				if widthOnly && boundary && isMySQLIntType(word) {
					i = close + 1
					continue
				}
			}
		}
		out = append(out, b[i])
		i++
	}
	return foldSpaces(string(out))
}

// splitToken 把 `numeric(10,2)` 拆成词干 `numeric` 与尾巴 `(10,2)`。
func splitToken(tok string) (string, string) {
	b := []byte(tok)
	k := 0
	for k < len(b) && isWordByte(b[k]) {
		k++
	}
	return tok[:k], tok[k:]
}

func dropWord(s, word string) string {
	parts := strings.Split(s, " ")
	kept := make([]string, 0, len(parts))
	for _, t := range parts {
		if base, _ := splitToken(t); base != word {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " ")
}

func replaceWord(s, from, to string) string {
	parts := strings.Split(s, " ")
	for i, t := range parts {
		base, rest := splitToken(t)
		if base == from {
			parts[i] = to + rest
		}
	}
	return strings.Join(parts, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func allZeros(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// stripOracleScale0：number(10,0) → number(10)（scale 全为 0 时两者等价）。
func stripOracleScale0(s string) string {
	parts := strings.Split(s, " ")
	for i, t := range parts {
		base, rest := splitToken(t)
		if (base != "number" && base != "numeric") || len(rest) < 4 {
			continue
		}
		if rest[0] != '(' || rest[len(rest)-1] != ')' {
			continue
		}
		inner := rest[1 : len(rest)-1]
		fields := strings.Split(inner, ",")
		if len(fields) == 2 && allDigits(fields[0]) && allZeros(fields[1]) {
			parts[i] = base + "(" + fields[0] + ")"
		}
	}
	return strings.Join(parts, " ")
}

// NormalizeType 归一类型字面量：小写 + 折空白 + 去标点旁空白，再按种类去噪声。
//
// 规则（只折叠「同一数据库内部的不同写法」，不做跨种类等价）：
//   - 通用：NUMERIC (10, 2) → numeric(10,2)；TEXT [ ] → text[]
//   - mysql：整数显示宽度 int(11) → int；丢弃 zerofill（MySQL 8 已弃用，unsigned 保留）
//   - oracle：bpchar → char（Oracle 内部名）；number(p,0) → number(p)
func NormalizeType(kind protocol.DatabaseKind, raw string) string {
	s := squeezePunct(foldSpaces(lowerASCII(strings.TrimSpace(raw))))
	if kind == protocol.DatabaseKindMySQL {
		s = foldSpaces(dropWord(stripMySQLIntWidth(s), "zerofill"))
	}
	if kind == protocol.DatabaseKindOracle {
		s = foldSpaces(stripOracleScale0(replaceWord(s, "bpchar", "char")))
	}
	return s
}

// lowerOutsideQuotes 只折叠引号外的大小写：字符串字面量内容保持原样。
func lowerOutsideQuotes(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			out.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			out.WriteByte(c)
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out.WriteByte(c)
	}
	return out.String()
}

// stripOuterParens 剥掉成对的最外层括号：(( now() )) → now()；(a) + (b) 不动。
func stripOuterParens(s string) string {
	b := []byte(s)
	if len(b) == 0 || b[0] != '(' {
		return s
	}
	if idx := matchingClose(b, 0); idx == len(b)-1 {
		return string(b[1:idx])
	}
	return s
}

// isCastTail 判断投射目标类型字面量：text、character varying、numeric(10,2)、varchar[]。
func isCastTail(t string) bool {
	if t == "" {
		return false
	}
	for _, tok := range strings.Split(t, " ") {
		base, rest := splitToken(tok)
		if base == "" {
			return false
		}
		head := base[0]
		if !(head >= 'a' && head <= 'z') && head != '_' {
			return false
		}
		okRest := rest == "" ||
			(strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")")) ||
			rest == "[]" ||
			(strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")[]"))
		if !okRest {
			return false
		}
	}
	return true
}

// stripTrailingCast 剥掉表达式末尾、括号深度 0 处的类型投射：'0'::text → '0'。
// 括号内的投射（PG 的 nextval('s'::regclass)）保持原样；多层投射由外层循环逐个剥。
func stripTrailingCast(s string) string {
	b := []byte(s)
	depth := 0
	var quote byte
	last := -1
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ':' && depth == 0 && i+1 < len(b) && b[i+1] == ':':
			last = i
		}
	}
	if last < 0 {
		return s
	}
	tail := foldSpaces(string(b[last+2:]))
	if isCastTail(tail) {
		return foldSpaces(string(b[:last]))
	}
	return s
}

// NormalizeDefault 归一默认值表达式；返回 nil 表示「无默认值」。
//
// 规则：NULL 字面量视为无默认；反复剥最外层括号与尾部 ::type 投射；
// 引号外小写、折空白；引号内容保持原样。不做等价改写
// （now() 与 current_timestamp 在 v1 视为不同默认值）。
func NormalizeDefault(raw *string) *string {
	if raw == nil {
		return nil
	}
	s := foldSpaces(lowerOutsideQuotes(strings.TrimSpace(*raw)))
	if s == "" {
		return nil
	}
	for {
		prev := s
		s = foldSpaces(stripOuterParens(s))
		s = foldSpaces(stripTrailingCast(s))
		if s == prev {
			break
		}
	}
	if s == "" || s == "null" {
		return nil
	}
	return &s
}
