package mcp

// MCP 输出的数据掩码（Q-03，interaction：模型看 shape 不看 PII）。
//
// 内核 `dbcore/masking` 是双层纯函数策略：列名识别（email/phone/id_card/
// password/token/api_key 整列遮蔽）+ 模式识别（邮箱/手机号/身份证/卡号兜底，
// 保留首尾字符让模型仍能区分值与格式）。db-dev 的 MCP 面已同构接入（16 处）。
//
// polydb 的行值是 `protocol.Value`（JSON 原文），不是 Go string：掩码在
// "JSON 原文 ↔ 字符串"边界做一次往返——字符串值去掉引号后过策略，掩完再加引号
// 包回 JSON；非字符串值（数字/布尔/NULL）原样保留（掩码策略只对文本形态生效，
// 对数字脱敏没有意义：数字形态本身不含 PII 模式）。

import (
	"encoding/json"
	"strings"

	"github.com/dbcore/dbcore/masking"
	"github.com/polydb/polydb/pkg/protocol"
)

// maskPolicy 是 MCP 输出的数据掩码策略（dbcore/masking 默认档：敏感列名 + 形态层）。
var maskPolicy = masking.Default()

// maskQueryResult 就地掩码一个只读查询结果集：返回带 masked 标记的副本
// （不改调用方手里的原结果——server 的 HTTP 面仍需要未脱敏原文）。
func maskQueryResult(res *protocol.QueryResult) *protocol.QueryResult {
	if res == nil {
		return res
	}
	cols := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		cols[i] = c.Name
	}
	out := *res
	out.Rows = make([][]protocol.Value, len(res.Rows))
	for i, row := range res.Rows {
		out.Rows[i] = maskRow(cols, row)
	}
	out.Truncated = res.Truncated
	return &out
}

// maskRow 掩码一行：字符串值过策略，非字符串值原样。
func maskRow(cols []string, row []protocol.Value) []protocol.Value {
	out := make([]protocol.Value, len(row))
	for i, v := range row {
		name := ""
		if i < len(cols) {
			name = cols[i]
		}
		out[i] = maskValue(name, v)
	}
	return out
}

// maskValue 对单个值套用策略：
//   - NULL / 非字符串 JSON 原文（数字、布尔、对象、数组）原样保留——
//     掩码策略只对文本形态生效；
//   - 敏感列名下的任何值整列遮蔽（策略的主层，形态不完整也遮）；
//   - 其余字符串值按形态兜底（邮箱/手机号/身份证/卡号）。
func maskValue(col string, v protocol.Value) protocol.Value {
	if v.IsNull() {
		return v
	}
	raw := strings.TrimSpace(string(v.Raw()))
	// 只有 JSON 字符串（带引号）才是文本值；数字/布尔/复合值原样。
	if !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		return v
	}
	var s string
	if err := json.Unmarshal(v.Raw(), &s); err != nil {
		return v // 非法 JSON 字符串原文：保守起见原样返回
	}
	names := maskPolicy.Names
	if names != nil && names.IsSensitive(col) {
		return jsonString(maskWhole(s))
	}
	pol := maskPolicy
	if pol.Patterns == nil {
		return v
	}
	masked := pol.Patterns.Value(s)
	if masked == s {
		return v // 无模式命中：原样（避免为每个值重建 JSON 原文）
	}
	return jsonString(masked)
}

// jsonString 用与 protocol.Value 相同的 JSON 编码包回字符串。
func jsonString(s string) protocol.Value {
	b, _ := json.Marshal(s)
	var v protocol.Value
	_ = v.UnmarshalJSON(b)
	return v
}

// maskWhole 转发内核的整列遮蔽（保持与 db-dev 的 maskRows 相同的遮蔽形态）。
func maskWhole(s string) string { return masking.MaskWhole(s) }
