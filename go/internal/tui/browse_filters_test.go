package tui

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func TestParseScalarValue(t *testing.T) {
	cases := []struct {
		in    string
		check func(protocol.Value) bool
	}{
		{"42", func(v protocol.Value) bool { return valueDisplay(v) == "42" }},
		{"-7", func(v protocol.Value) bool { return valueDisplay(v) == "-7" }},
		{"3.14", func(v protocol.Value) bool { return valueDisplay(v) == "3.14" }},
		{"true", func(v protocol.Value) bool { return valueDisplay(v) == "true" }},
		{"hello", func(v protocol.Value) bool { return valueDisplay(v) == `"hello"` }},
		{"1e5", func(v protocol.Value) bool { return valueDisplay(v) == "100000" }},
	}
	for _, c := range cases {
		if !c.check(parseScalarValue(c.in)) {
			t.Errorf("parseScalarValue(%q) = %s", c.in, valueDisplay(parseScalarValue(c.in)))
		}
	}
}

func TestDraftToCondition(t *testing.T) {
	// 缺值不可构造。
	if _, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpEq, Raw: "  "}); ok {
		t.Fatalf("eq with empty value should be invalid")
	}
	// 普通标量。
	c, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpGt, Raw: "10"})
	if !ok || c.Column != "a" || c.Op != protocol.FilterOpGt || valueDisplay(c.Value) != "10" {
		t.Fatalf("gt 10 => %+v ok=%v", c, ok)
	}
	// is null 无需值。
	if _, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpNull}); !ok {
		t.Fatalf("is null should be valid without value")
	}
	// between 需两个值。
	if _, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpBetween, Raw: "1", Raw2: ""}); ok {
		t.Fatalf("between missing second value should be invalid")
	}
	c2, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpBetween, Raw: "1", Raw2: "5"})
	if !ok || valueDisplay(c2.Value) != "1" || valueDisplay(c2.SecondValue) != "5" {
		t.Fatalf("between 1..5 => %+v", c2)
	}
	// in 逗号分隔多值。
	c3, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpIn, Raw: "1, 2,x"})
	if !ok || len(c3.Values) != 3 || valueDisplay(c3.Values[2]) != `"x"` {
		t.Fatalf("in 1,2,x => %+v", c3)
	}
}

func TestOpHelpers(t *testing.T) {
	if len(browseOps) != 13 {
		t.Fatalf("want 13 ops, got %d", len(browseOps))
	}
	if opNeedsValues(protocol.FilterOpNull) != 0 ||
		opNeedsValues(protocol.FilterOpBetween) != 2 ||
		opNeedsValues(protocol.FilterOpEq) != 1 {
		t.Fatalf("opNeedsValues mismatch")
	}
	if opLabel(protocol.FilterOpNotLike) != "not like" {
		t.Fatalf("opLabel not_like = %q", opLabel(protocol.FilterOpNotLike))
	}
}
