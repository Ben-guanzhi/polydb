package tui

// 过滤表达式解析：把用户在过滤行里输入的文本解析成服务端过滤条件
// （protocol.FilterCondition），让过滤下推到数据库执行，而不是把行拉回 UI 再筛。
//
// 只做「够用且明确」的子集，解析不了就报错，绝不静默给出自以为是的条件：
//
//	col = v        col == v      col != v      col <> v
//	col > v        col >= v      col < v       col <= v
//	col like v     col not like v
//	col in (v1, v2)
//	col is null    col is not null
//
// 多个条件用 and 连接（大小写不限）。暂不支持 or —— 遇到就报错，而不是按 and
// 处理，否则会静默给出错误的结果。

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// ParseFilterConditions 解析过滤表达式。空输入返回 nil（表示不过滤），调用方
// 据此跳过服务端过滤。
func ParseFilterConditions(input string) ([]protocol.FilterCondition, error) {
	parts, err := splitAnd(strings.TrimSpace(input))
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]protocol.FilterCondition, 0, len(parts))
	for _, p := range parts {
		c, err := parseCondition(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// splitAnd 按词边界切分 and，同时拒绝 or。切分要避开引号内文本，否则
// name = 'a and b' 会被切成两半。
func splitAnd(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var (
		parts   []string
		cur     strings.Builder
		inQuote rune
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inQuote != 0:
			cur.WriteRune(r)
			if r == inQuote {
				inQuote = 0
			}
		case r == '\'' || r == '"':
			inQuote = r
			cur.WriteRune(r)
		default:
			if isWordAt(runes, i, "and") {
				parts = append(parts, cur.String())
				cur.Reset()
				i += len("and") - 1
				continue
			}
			if isWordAt(runes, i, "or") {
				return nil, fmt.Errorf("暂不支持 or：当前只下推 and 相连的条件")
			}
			cur.WriteRune(r)
		}
	}
	if inQuote != 0 {
		return nil, fmt.Errorf("引号未闭合")
	}
	parts = append(parts, cur.String())
	return parts, nil
}

// isWordAt 报告 word 是否作为一个独立词出现在 i 处（前后不能是标识符字符，
// 否则 "android" 里的 and 会被当成关键字）。
func isWordAt(runes []rune, i int, word string) bool {
	if i+len(word) > len(runes) {
		return false
	}
	if !strings.EqualFold(string(runes[i:i+len(word)]), word) {
		return false
	}
	if i > 0 && isIdentRune(runes[i-1]) {
		return false
	}
	if i+len(word) < len(runes) && isIdentRune(runes[i+len(word)]) {
		return false
	}
	return true
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// parseCondition 解析单个条件。
func parseCondition(s string) (protocol.FilterCondition, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return protocol.FilterCondition{}, fmt.Errorf("空条件")
	}
	lower := strings.ToLower(s)

	// is [not] null：没有值。
	for _, p := range []struct {
		suffix string
		op     protocol.FilterOperator
	}{
		{"is not null", protocol.FilterOpNotNull},
		{"is null", protocol.FilterOpNull},
	} {
		if strings.HasSuffix(lower, p.suffix) {
			col := strings.TrimSpace(s[:len(s)-len(p.suffix)])
			if col == "" {
				return protocol.FilterCondition{}, fmt.Errorf("%q 缺少字段名", p.suffix)
			}
			return protocol.FilterCondition{Column: col, Op: p.op}, nil
		}
	}

	// in (…)
	if i := indexWord(lower, "in"); i > 0 {
		col := strings.TrimSpace(s[:i])
		rest := strings.TrimSpace(s[i+len("in"):])
		if col != "" && strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")") {
			vals, err := parseList(rest[1 : len(rest)-1])
			if err != nil {
				return protocol.FilterCondition{}, err
			}
			return protocol.FilterCondition{Column: col, Op: protocol.FilterOpIn, Values: vals}, nil
		}
	}

	// 二元操作符：长的先匹配，避免 ">=" 被 ">" 抢走。
	for _, o := range []struct {
		sym string
		op  protocol.FilterOperator
	}{
		{"not like", protocol.FilterOpNotLike},
		{"like", protocol.FilterOpLike},
		{">=", protocol.FilterOpGe},
		{"<=", protocol.FilterOpLe},
		{"!=", protocol.FilterOpNe},
		{"<>", protocol.FilterOpNe},
		{"==", protocol.FilterOpEq},
		{"=", protocol.FilterOpEq},
		{">", protocol.FilterOpGt},
		{"<", protocol.FilterOpLt},
	} {
		if i := strings.Index(lower, o.sym); i > 0 {
			col := strings.TrimSpace(s[:i])
			// 字段侧不能再含操作符字符：否则 ">= 30" 会被拆成字段 ">"、值 "30"，
			// 静默产出一个没人想要的条件。
			if col == "" || strings.ContainsAny(col, "=<>!") {
				continue
			}
			val := strings.TrimSpace(s[i+len(o.sym):])
			if col == "" {
				return protocol.FilterCondition{}, fmt.Errorf("操作符 %q 前缺少字段名", o.sym)
			}
			if val == "" {
				return protocol.FilterCondition{}, fmt.Errorf("操作符 %q 后缺少值", o.sym)
			}
			return protocol.FilterCondition{Column: col, Op: o.op, Value: parseValue(val)}, nil
		}
	}
	return protocol.FilterCondition{}, fmt.Errorf("无法解析过滤条件：%q", s)
}

// indexWord 找出独立词 word 的起始下标，找不到返回 -1。
func indexWord(s, word string) int {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if isWordAt(runes, i, word) {
			return i
		}
	}
	return -1
}

// parseList 解析 in 的逗号分隔值列表。
func parseList(s string) ([]protocol.Value, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("in () 里没有值")
	}
	out := []protocol.Value{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("in 列表里有空值")
		}
		out = append(out, parseValue(item))
	}
	return out, nil
}

// parseValue 把字面量转成协议值：带引号一律当字符串，其余按
// null / bool / int / float 依次尝试，都不匹配才退回字符串。
func parseValue(s string) protocol.Value {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return protocol.NewStringValue(s[1 : len(s)-1])
		}
	}
	switch strings.ToLower(s) {
	case "null":
		return protocol.NewNullValue()
	case "true":
		return protocol.NewBoolValue(true)
	case "false":
		return protocol.NewBoolValue(false)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return protocol.NewIntValue(n)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return protocol.NewFloatValue(f)
	}
	return protocol.NewStringValue(s)
}
