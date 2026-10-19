package tui

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// valueJSON 把值序列化成 JSON 便于断言（protocol.Value 的字段未导出）。
func valueJSON(t *testing.T, v protocol.Value) string {
	t.Helper()
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	return string(b)
}

func TestParseFilterEmpty(t *testing.T) {
	got, err := ParseFilterConditions("   ")
	if err != nil || got != nil {
		t.Fatalf("空输入应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
}

func TestParseFilterBinaryOps(t *testing.T) {
	cases := []struct {
		in   string
		col  string
		op   protocol.FilterOperator
		want string
	}{
		{"age > 30", "age", protocol.FilterOpGt, "30"},
		{"age >= 30", "age", protocol.FilterOpGe, "30"},
		{"age < 30", "age", protocol.FilterOpLt, "30"},
		{"age <= 30", "age", protocol.FilterOpLe, "30"},
		{"age = 30", "age", protocol.FilterOpEq, "30"},
		{"age == 30", "age", protocol.FilterOpEq, "30"},
		{"age != 30", "age", protocol.FilterOpNe, "30"},
		{"age <> 30", "age", protocol.FilterOpNe, "30"},
		{"name = 'Ann'", "name", protocol.FilterOpEq, `"Ann"`},
		{`name = "Ann"`, "name", protocol.FilterOpEq, `"Ann"`},
		{"name like 'a%'", "name", protocol.FilterOpLike, `"a%"`},
		{"name not like 'a%'", "name", protocol.FilterOpNotLike, `"a%"`},
	}
	for _, c := range cases {
		got, err := ParseFilterConditions(c.in)
		if err != nil {
			t.Fatalf("%q 解析失败：%v", c.in, err)
		}
		if len(got) != 1 {
			t.Fatalf("%q 应得 1 个条件，实际 %d", c.in, len(got))
		}
		if got[0].Column != c.col || got[0].Op != c.op {
			t.Fatalf("%q → {%q, %q}，want {%q, %q}", c.in, got[0].Column, got[0].Op, c.col, c.op)
		}
		if v := valueJSON(t, got[0].Value); v != c.want {
			t.Fatalf("%q 的值 = %s, want %s", c.in, v, c.want)
		}
	}
}

// TestParseFilterGeNotSwallowedByGt pins operator precedence by length:
// scanning ">" first would turn ">=" into ">" plus a value of "= 30".
func TestParseFilterGeNotSwallowedByGt(t *testing.T) {
	got, err := ParseFilterConditions("age >= 30")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Op != protocol.FilterOpGe {
		t.Fatalf("op = %q, want ge", got[0].Op)
	}
	if v := valueJSON(t, got[0].Value); v != "30" {
		t.Fatalf("值 = %s, want 30（>= 被拆开会把 '= 30' 当值）", v)
	}
}

func TestParseFilterNull(t *testing.T) {
	for in, op := range map[string]protocol.FilterOperator{
		"deleted_at is null":     protocol.FilterOpNull,
		"deleted_at is not null": protocol.FilterOpNotNull,
	} {
		got, err := ParseFilterConditions(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(got) != 1 || got[0].Column != "deleted_at" || got[0].Op != op {
			t.Fatalf("%q → %+v", in, got)
		}
	}
}

func TestParseFilterIn(t *testing.T) {
	got, err := ParseFilterConditions("id in (1, 2, 3)")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Op != protocol.FilterOpIn {
		t.Fatalf("应得一个 in 条件，实际 %+v", got)
	}
	if len(got[0].Values) != 3 {
		t.Fatalf("in 列表长度 = %d, want 3", len(got[0].Values))
	}
	if v := valueJSON(t, got[0].Values[2]); v != "3" {
		t.Fatalf("第三个值 = %s, want 3", v)
	}
}

// TestParseFilterAnd pins multi-condition AND joining.
func TestParseFilterAnd(t *testing.T) {
	got, err := ParseFilterConditions("age >= 18 and status = 'active'")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应得 2 个条件，实际 %d", len(got))
	}
	if got[0].Column != "age" || got[1].Column != "status" {
		t.Fatalf("字段顺序 = %q, %q", got[0].Column, got[1].Column)
	}
}

// TestParseFilterQuoteProtectsAnd pins that "and" inside a quoted value is
// literal text, not a condition separator.
func TestParseFilterQuoteProtectsAnd(t *testing.T) {
	got, err := ParseFilterConditions("name = 'a and b'")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("引号内的 and 不应切分，实际 %d 个条件", len(got))
	}
	if v := valueJSON(t, got[0].Value); v != `"a and b"` {
		t.Fatalf("值 = %s, want \"a and b\"", v)
	}
}

func TestParseFilterRejectsOr(t *testing.T) {
	if _, err := ParseFilterConditions("a = 1 or b = 2"); err == nil {
		t.Fatal("or 应报错（按 and 处理会静默给出错误结果）")
	}
}

func TestParseFilterRejectsGarbage(t *testing.T) {
	for _, in := range []string{"=", ">= 30", "age >", "just text", "'unterminated"} {
		if _, err := ParseFilterConditions(in); err == nil {
			t.Fatalf("%q 应解析失败", in)
		}
	}
}
