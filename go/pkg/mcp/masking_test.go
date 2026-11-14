package mcp

// maskQueryResult 的单值/整行行为测试（Q-03）：
//   - 敏感列名（email/phone/id_card/password/token/api_key）整列遮蔽；
//   - 无辜列名下的形态兜底（邮箱/手机号/身份证/卡号）；
//   - 非字符串值（数字/布尔/NULL）原样；
//   - 无模式命中的普通文本原样（不重建 JSON 原文）。

import (
	"strconv"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func numVal(n int) protocol.Value {
	var v protocol.Value
	_ = v.UnmarshalJSON([]byte(strconv.Itoa(n)))
	return v
}

func strVal(s string) protocol.Value {
	var v protocol.Value
	_ = v.UnmarshalJSON([]byte(`"` + s + `"`))
	return v
}

func rawOf(v protocol.Value) string {
	return strings.Trim(string(v.Raw()), `"`)
}

func TestMaskQueryResultSensitiveColumns(t *testing.T) {
	res := &protocol.QueryResult{
		Columns: []protocol.ResultColumn{{Name: "id"}, {Name: "email"}, {Name: "password"}},
		Rows: [][]protocol.Value{
			{numVal(1), strVal("alice@example.com"), strVal("hunter2")},
		},
	}
	out := maskQueryResult(res)
	row := out.Rows[0]
	// id 是数字原文，原样（IsNull 为 false 但非字符串 JSON）。
	if row[0].IsNull() {
		t.Error("数字 id 不应被判为 NULL")
	}
	if got := rawOf(row[1]); !strings.Contains(got, "*") || strings.Contains(got, "alice@example.com") {
		t.Errorf("email 列应整列遮蔽: %q", got)
	}
	if got := rawOf(row[2]); strings.Contains(got, "hunter2") {
		t.Errorf("password 列应整列遮蔽: %q", got)
	}
	// 原结果不被修改（server 的 HTTP 面需要原文）。
	if got := rawOf(res.Rows[0][1]); got != "alice@example.com" {
		t.Errorf("原结果不应被就地修改: %q", got)
	}
}

func TestMaskQueryResultPatternFallback(t *testing.T) {
	res := &protocol.QueryResult{
		Columns: []protocol.ResultColumn{{Name: "note"}},
		Rows: [][]protocol.Value{
			{strVal("联系 13812345678 或 alice@example.com")},
			{strVal("普通备注，无敏感形态")},
		},
	}
	out := maskQueryResult(res)
	first := rawOf(out.Rows[0][0])
	if strings.Contains(first, "13812345678") || strings.Contains(first, "alice@example.com") {
		t.Errorf("形态兜底未生效: %q", first)
	}
	if !strings.Contains(first, "*") {
		t.Errorf("形态兜底应保留首尾字符并遮蔽中段: %q", first)
	}
	second := rawOf(out.Rows[1][0])
	if second != "普通备注，无敏感形态" {
		t.Errorf("无命中的普通文本应原样: %q", second)
	}
}

func TestMaskRowKeepsNonStringValues(t *testing.T) {
	cols := []string{"id", "active", "note"}
	row := []protocol.Value{}
	_ = row // 构造见下
	var num, boolv protocol.Value
	_ = num.UnmarshalJSON([]byte(`42`))
	_ = boolv.UnmarshalJSON([]byte(`true`))
	row = []protocol.Value{num, boolv, protocol.Value{}}
	out := maskRow(cols, row)
	if out[0].IsNull() || string(out[0].Raw()) != "42" {
		t.Errorf("数字应原样: %s", string(out[0].Raw()))
	}
	if string(out[1].Raw()) != "true" {
		t.Errorf("布尔应原样: %s", string(out[1].Raw()))
	}
	if !out[2].IsNull() {
		t.Error("NULL 应原样")
	}
}
