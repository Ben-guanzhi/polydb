package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// browse 过滤栏：把终端草稿逐条构造为 protocol.FilterCondition。
// 独立实现，语义对齐后端 WHERE 构造，不移植 TableProGo 任何代码。

// browseOps 数据浏览可用的 18 个操作符（与 spec 枚举一致，按交互常用度排序）。
var browseOps = []protocol.FilterOperator{
	protocol.FilterOpEq,
	protocol.FilterOpNe,
	protocol.FilterOpGt,
	protocol.FilterOpGe,
	protocol.FilterOpLt,
	protocol.FilterOpLe,
	protocol.FilterOpLike,
	protocol.FilterOpNotLike,
	protocol.FilterOpContains,
	protocol.FilterOpStartsWith,
	protocol.FilterOpEndsWith,
	protocol.FilterOpRegex,
	protocol.FilterOpIn,
	protocol.FilterOpNotIn,
	protocol.FilterOpBetween,
	protocol.FilterOpNull,
	protocol.FilterOpNotNull,
	protocol.FilterOpEmpty,
}

// opLabel 操作符的终端友好标签。
func opLabel(op protocol.FilterOperator) string {
	switch op {
	case protocol.FilterOpEq:
		return "="
	case protocol.FilterOpNe:
		return "!="
	case protocol.FilterOpGt:
		return ">"
	case protocol.FilterOpGe:
		return ">="
	case protocol.FilterOpLt:
		return "<"
	case protocol.FilterOpLe:
		return "<="
	case protocol.FilterOpLike:
		return "like"
	case protocol.FilterOpNotLike:
		return "not like"
	case protocol.FilterOpContains:
		return "contains"
	case protocol.FilterOpStartsWith:
		return "starts with"
	case protocol.FilterOpEndsWith:
		return "ends with"
	case protocol.FilterOpRegex:
		return "regex"
	case protocol.FilterOpIn:
		return "in"
	case protocol.FilterOpNotIn:
		return "not in"
	case protocol.FilterOpBetween:
		return "between"
	case protocol.FilterOpNull:
		return "is null"
	case protocol.FilterOpNotNull:
		return "is not null"
	case protocol.FilterOpEmpty:
		return "is empty"
	}
	return string(op)
}

// opNeedsValues 该操作符需要几个值字段：0（is null / is empty 类）、1、2（between）。
// in/not_in 虽逻辑上多值，但终端里用单字段逗号分隔输入，计为 1。
func opNeedsValues(op protocol.FilterOperator) int {
	switch op {
	case protocol.FilterOpNull, protocol.FilterOpNotNull, protocol.FilterOpEmpty:
		return 0
	case protocol.FilterOpBetween:
		return 2
	default:
		return 1
	}
}

// opIsTextMatch 文本匹配类 op：唯一接受大小写三态的一族（behavior.md §13.5）。
func opIsTextMatch(op protocol.FilterOperator) bool {
	switch op {
	case protocol.FilterOpLike, protocol.FilterOpNotLike, protocol.FilterOpContains,
		protocol.FilterOpStartsWith, protocol.FilterOpEndsWith, protocol.FilterOpRegex:
		return true
	}
	return false
}

// opSupported 当前连接方言是否支持该 op（§13.6：MSSQL 无原生正则）。
func opSupported(kind protocol.DatabaseKind, op protocol.FilterOperator) bool {
	return op != protocol.FilterOpRegex || kind != protocol.DatabaseKindMSSQL
}

// opUnsupportedHint 不支持时的提示文案；可用则空串。
func opUnsupportedHint(kind protocol.DatabaseKind, op protocol.FilterOperator) string {
	if opSupported(kind, op) {
		return ""
	}
	if op == protocol.FilterOpRegex {
		return "该数据库无原生正则，后端会返回 POLYDB_ERR_NOT_SUPPORTED"
	}
	return "该数据库不支持此操作符"
}

// filterCase 大小写三态（默认 = 沿用方言排序规则，线格式省略字段）。
type filterCase int

const (
	caseDefault filterCase = iota
	caseSensitive
	caseInsensitive
)

func (c filterCase) label() string {
	switch c {
	case caseSensitive:
		return "区分"
	case caseInsensitive:
		return "忽略"
	}
	return "默认"
}

func (c filterCase) next() filterCase {
	return (c + 1) % 3
}

// toWire 三态→可选布尔；默认返回 nil 让后端沿用方言排序规则。
func (c filterCase) toWire() *bool {
	switch c {
	case caseSensitive:
		t := true
		return &t
	case caseInsensitive:
		f := false
		return &f
	}
	return nil
}

// filterDraft 一条可编辑的过滤条件草稿（Raw/Raw2 为用户原文）。
type filterDraft struct {
	Column string
	Op     protocol.FilterOperator
	Raw    string
	Raw2   string
	Case   filterCase
}

// parseScalarValue 按最可能类型解析标量：bool 仅认字面 true/false，
// 之后 int64 → float64 → string。避免 "1"/"0" 被 ParseBool 误判为布尔。
// 空串解析为字符串 ""（供 like '%' 等显式输入使用）。
func parseScalarValue(s string) protocol.Value {
	t := strings.TrimSpace(s)
	switch strings.ToLower(t) {
	case "true":
		return protocol.NewBoolValue(true)
	case "false":
		return protocol.NewBoolValue(false)
	}
	if i, err := strconv.ParseInt(t, 10, 64); err == nil {
		return protocol.NewIntValue(i)
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		return protocol.NewFloatValue(f)
	}
	return protocol.NewStringValue(s)
}

// draftToCondition 构造 FilterCondition；ok=false 表示缺必要值（值字段为空）。
// 大小写三态只对文本匹配类 op 下发，默认态留 nil 以沿用方言排序规则。
func draftToCondition(d filterDraft) (protocol.FilterCondition, bool) {
	c := protocol.FilterCondition{Column: d.Column, Op: d.Op}
	if opIsTextMatch(d.Op) {
		c.CaseSensitive = d.Case.toWire()
	}
	switch opNeedsValues(d.Op) {
	case 0:
		return c, true
	case 2:
		if strings.TrimSpace(d.Raw) == "" || strings.TrimSpace(d.Raw2) == "" {
			return c, false
		}
		c.Value = parseScalarValue(d.Raw)
		c.SecondValue = parseScalarValue(d.Raw2)
		return c, true
	default: // 1
		if strings.TrimSpace(d.Raw) == "" {
			return c, false
		}
		if d.Op == protocol.FilterOpIn || d.Op == protocol.FilterOpNotIn {
			parts := strings.Split(d.Raw, ",")
			vals := make([]protocol.Value, 0, len(parts))
			for _, p := range parts {
				vals = append(vals, parseScalarValue(strings.TrimSpace(p)))
			}
			c.Values = vals
			return c, true
		}
		c.Value = parseScalarValue(d.Raw)
		return c, true
	}
}

// draftSummary 渲染一条草稿的可读摘要（用于过滤栏展示）。
func draftSummary(d filterDraft) string {
	var s string
	switch opNeedsValues(d.Op) {
	case 0:
		s = d.Column + " " + opLabel(d.Op)
	case 2:
		s = d.Column + " " + opLabel(d.Op) + " " + d.Raw + " .. " + d.Raw2
	default:
		if d.Op == protocol.FilterOpIn || d.Op == protocol.FilterOpNotIn {
			s = d.Column + " " + opLabel(d.Op) + " (" + d.Raw + ")"
		} else {
			s = d.Column + " " + opLabel(d.Op) + " " + d.Raw
		}
	}
	if opIsTextMatch(d.Op) && d.Case != caseDefault {
		s += " [大小写" + d.Case.label() + "]"
	}
	return s
}

// conditionSummary 渲染一条已提交条件的可读摘要（复用草稿摘要）。
func conditionSummary(c protocol.FilterCondition) string {
	d := filterDraft{Column: c.Column, Op: c.Op}
	switch {
	case c.CaseSensitive == nil:
		d.Case = caseDefault
	case *c.CaseSensitive:
		d.Case = caseSensitive
	default:
		d.Case = caseInsensitive
	}
	switch opNeedsValues(c.Op) {
	case 0:
	case 2:
		d.Raw = valueDisplay(c.Value)
		d.Raw2 = valueDisplay(c.SecondValue)
	default:
		if c.Op == protocol.FilterOpIn || c.Op == protocol.FilterOpNotIn {
			parts := make([]string, 0, len(c.Values))
			for _, v := range c.Values {
				parts = append(parts, valueDisplay(v))
			}
			d.Raw = strings.Join(parts, ", ")
		} else {
			d.Raw = valueDisplay(c.Value)
		}
	}
	return draftSummary(d)
}

// valueDisplay 把 protocol.Value 转回展示文本（best-effort，走 JSON 原文）。
func valueDisplay(v protocol.Value) string {
	b, err := v.MarshalJSON()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// browseFilterBar 渲染浏览过滤栏：编辑态显示草稿（高亮聚焦字段），
// 否则显示已应用条件 + 排序 + 精确计数摘要。
func (m *model) browseFilterBar() string {
	if m.brEditing {
		labeled := func(field int, text string) string {
			if m.brField == field {
				return selected(text)
			}
			return text
		}
		var parts []string
		parts = append(parts, "列 "+labeled(0, m.brDraft.Column))
		parts = append(parts, "op "+labeled(1, opLabel(m.brDraft.Op)))
		parts = append(parts, "值 "+labeled(2, "["+m.brVal.Value()+"]"))
		if m.brDraft.Op == protocol.FilterOpBetween {
			parts = append(parts, "值2 "+labeled(3, "["+m.brDraft.Raw2+"]"))
		}
		if opIsTextMatch(m.brDraft.Op) {
			parts = append(parts, "大小写 "+labeled(4, "["+m.brDraft.Case.label()+"]"))
		}
		hint := ""
		if !opSupported(m.brKind, m.brDraft.Op) {
			hint = "\n  ⚠ " + opUnsupportedHint(m.brKind, m.brDraft.Op)
		}
		return "过滤: " + strings.Join(parts, " · ") +
			"\n  Tab 切字段 · ←/→ 调整列/操作符/大小写 · ↑/↓ 换列 · Enter 应用 · Esc 取消" + hint
	}
	var seg []string
	if len(m.brConds) > 0 {
		logic := " AND "
		if m.brLogic == protocol.FilterLogicOr {
			logic = " OR "
		}
		sums := make([]string, 0, len(m.brConds))
		for _, c := range m.brConds {
			sums = append(sums, conditionSummary(c))
		}
		seg = append(seg, "过滤:"+logic+strings.Join(sums, logic))
	} else {
		seg = append(seg, "过滤:无")
	}
	if len(m.brSort) > 0 {
		s := m.brSort[0]
		seg = append(seg, "排序:"+s.Column+" "+string(s.Dir))
	}
	seg = append(seg, fmt.Sprintf("页 %d", m.brLimit))
	if m.brCountOK && m.brCount != nil {
		seg = append(seg, fmt.Sprintf("共 %d 行", *m.brCount))
	}
	return strings.Join(seg, "  ")
}
