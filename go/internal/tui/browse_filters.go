package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// browse 过滤栏：把终端草稿逐条构造为 protocol.FilterCondition。
// 独立实现，语义对齐后端 WHERE 构造，不移植 TableProGo 任何代码。

// browseOps 数据浏览可用的 13 个操作符（与 spec 枚举一致，按交互常用度排序）。
var browseOps = []protocol.FilterOperator{
	protocol.FilterOpEq,
	protocol.FilterOpNe,
	protocol.FilterOpGt,
	protocol.FilterOpGe,
	protocol.FilterOpLt,
	protocol.FilterOpLe,
	protocol.FilterOpLike,
	protocol.FilterOpNotLike,
	protocol.FilterOpIn,
	protocol.FilterOpNotIn,
	protocol.FilterOpBetween,
	protocol.FilterOpNull,
	protocol.FilterOpNotNull,
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
	}
	return string(op)
}

// opNeedsValues 该操作符需要几个值字段：0（is null 类）、1、2（between）。
// in/not_in 虽逻辑上多值，但终端里用单字段逗号分隔输入，计为 1。
func opNeedsValues(op protocol.FilterOperator) int {
	switch op {
	case protocol.FilterOpNull, protocol.FilterOpNotNull:
		return 0
	case protocol.FilterOpBetween:
		return 2
	default:
		return 1
	}
}

// filterDraft 一条可编辑的过滤条件草稿（Raw/Raw2 为用户原文）。
type filterDraft struct {
	Column string
	Op     protocol.FilterOperator
	Raw    string
	Raw2   string
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
func draftToCondition(d filterDraft) (protocol.FilterCondition, bool) {
	c := protocol.FilterCondition{Column: d.Column, Op: d.Op}
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
	switch opNeedsValues(d.Op) {
	case 0:
		return d.Column + " " + opLabel(d.Op)
	case 2:
		return d.Column + " " + opLabel(d.Op) + " " + d.Raw + " .. " + d.Raw2
	default:
		if d.Op == protocol.FilterOpIn || d.Op == protocol.FilterOpNotIn {
			return d.Column + " " + opLabel(d.Op) + " (" + d.Raw + ")"
		}
		return d.Column + " " + opLabel(d.Op) + " " + d.Raw
	}
}

// conditionSummary 渲染一条已提交条件的可读摘要（复用草稿摘要）。
func conditionSummary(c protocol.FilterCondition) string {
	d := filterDraft{Column: c.Column, Op: c.Op}
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
		return "过滤: " + strings.Join(parts, " · ") +
			"\n  Tab 切字段 · ←/→ 调整列或操作符 · ↑/↓ 换列 · Enter 应用 · Esc 取消"
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
	if m.brCountOK && m.brCount != nil {
		seg = append(seg, fmt.Sprintf("共 %d 行", *m.brCount))
	}
	return strings.Join(seg, "  ")
}
