package tui

import (
	"strings"
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
	if len(browseOps) != 18 {
		t.Fatalf("want 18 ops, got %d", len(browseOps))
	}
	seen := map[protocol.FilterOperator]bool{}
	for _, op := range browseOps {
		if seen[op] {
			t.Fatalf("duplicated op %s in browseOps", op)
		}
		seen[op] = true
	}
	for _, op := range []protocol.FilterOperator{
		protocol.FilterOpContains, protocol.FilterOpStartsWith, protocol.FilterOpEndsWith,
		protocol.FilterOpRegex, protocol.FilterOpEmpty,
	} {
		if !seen[op] {
			t.Fatalf("op %s missing from browseOps", op)
		}
	}
	if opNeedsValues(protocol.FilterOpNull) != 0 ||
		opNeedsValues(protocol.FilterOpEmpty) != 0 ||
		opNeedsValues(protocol.FilterOpBetween) != 2 ||
		opNeedsValues(protocol.FilterOpEq) != 1 ||
		opNeedsValues(protocol.FilterOpContains) != 1 {
		t.Fatalf("opNeedsValues mismatch")
	}
	if opLabel(protocol.FilterOpNotLike) != "not like" ||
		opLabel(protocol.FilterOpContains) != "contains" ||
		opLabel(protocol.FilterOpStartsWith) != "starts with" ||
		opLabel(protocol.FilterOpEndsWith) != "ends with" ||
		opLabel(protocol.FilterOpRegex) != "regex" ||
		opLabel(protocol.FilterOpEmpty) != "is empty" {
		t.Fatalf("opLabel mismatch")
	}
	// 文本匹配族恰为六个（behavior.md §13.5），且都要求一个值。
	text := 0
	for _, op := range browseOps {
		if opIsTextMatch(op) {
			text++
			if opNeedsValues(op) != 1 {
				t.Fatalf("text-match op %s should need exactly one value", op)
			}
		}
	}
	if text != 6 {
		t.Fatalf("want 6 text-match ops, got %d", text)
	}
}

func TestOpSupportedMatrix(t *testing.T) {
	for _, k := range []protocol.DatabaseKind{
		protocol.DatabaseKindSQLite, protocol.DatabaseKindMySQL,
		protocol.DatabaseKindPostgres, protocol.DatabaseKindOracle,
	} {
		if !opSupported(k, protocol.FilterOpRegex) {
			t.Fatalf("%s should support regex", k)
		}
		if opUnsupportedHint(k, protocol.FilterOpRegex) != "" {
			t.Fatalf("%s regex hint should be empty", k)
		}
	}
	if opSupported(protocol.DatabaseKindMSSQL, protocol.FilterOpRegex) {
		t.Fatalf("mssql must not support regex (behavior.md §13.6)")
	}
	if h := opUnsupportedHint(protocol.DatabaseKindMSSQL, protocol.FilterOpRegex); h == "" {
		t.Fatalf("mssql regex needs a hint")
	}
	// 除 regex 外全部 op 各方言可用。
	for _, k := range []protocol.DatabaseKind{
		protocol.DatabaseKindSQLite, protocol.DatabaseKindMySQL, protocol.DatabaseKindPostgres,
		protocol.DatabaseKindMSSQL, protocol.DatabaseKindOracle,
	} {
		for _, op := range browseOps {
			if op == protocol.FilterOpRegex {
				continue
			}
			if !opSupported(k, op) {
				t.Fatalf("%s should support %s", k, op)
			}
		}
	}
}

func TestFilterCaseTriState(t *testing.T) {
	if caseDefault.toWire() != nil {
		t.Fatalf("default must omit case_sensitive")
	}
	if caseSensitive.toWire() == nil || *caseSensitive.toWire() != true {
		t.Fatalf("sensitive must map to true")
	}
	if caseInsensitive.toWire() == nil || *caseInsensitive.toWire() != false {
		t.Fatalf("insensitive must map to false")
	}
	// 循环三格回到原点。
	c := caseDefault
	for i := 0; i < 3; i++ {
		c = c.next()
	}
	if c != caseDefault {
		t.Fatalf("case cycle must wrap to default, got %v", c)
	}
	if caseDefault.label() != "默认" || caseSensitive.label() != "区分" || caseInsensitive.label() != "忽略" {
		t.Fatalf("case labels mismatch")
	}
}

func TestDraftCaseOnlyOnTextMatch(t *testing.T) {
	ins := caseInsensitive
	c, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpContains, Raw: "ali", Case: ins})
	if !ok || c.CaseSensitive == nil || *c.CaseSensitive {
		t.Fatalf("contains + insensitive => %+v ok=%v", c, ok)
	}
	// 比较类 op 即便设了大小写也不下发（后端本就忽略，发了会让契约对拍有噪声）。
	c2, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpEq, Raw: "1", Case: caseSensitive})
	if !ok || c2.CaseSensitive != nil {
		t.Fatalf("eq must not carry case_sensitive: %+v", c2)
	}
	// 默认态省略字段。
	c3, _ := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpLike, Raw: "%a%"})
	if c3.CaseSensitive != nil {
		t.Fatalf("default case must be nil: %+v", c3)
	}
	// empty 不需要值。
	if _, ok := draftToCondition(filterDraft{Column: "a", Op: protocol.FilterOpEmpty}); !ok {
		t.Fatalf("empty should be valid without value")
	}
	// 摘要里带上大小写标记，且已提交条件能还原三态。
	s := conditionSummary(c)
	if !strings.Contains(s, "大小写忽略") {
		t.Fatalf("summary should mention case mode, got %q", s)
	}
	if strings.Contains(conditionSummary(c2), "大小写") {
		t.Fatalf("non-text op should not mention case")
	}
}

func TestBrowseFilterFieldsAndCycle(t *testing.T) {
	m := &model{}
	m.brDraft = filterDraft{Op: protocol.FilterOpLike}
	fs := m.browseFilterFields()
	if len(fs) != 4 || fs[3] != 4 {
		t.Fatalf("like should expose case field: %v", fs)
	}
	m.brDraft = filterDraft{Op: protocol.FilterOpBetween}
	if fs = m.browseFilterFields(); len(fs) != 4 || fs[3] != 3 {
		t.Fatalf("between should expose second value: %v", fs)
	}
	m.brDraft = filterDraft{Op: protocol.FilterOpEq}
	if fs = m.browseFilterFields(); len(fs) != 3 {
		t.Fatalf("eq should have 3 fields: %v", fs)
	}
	// 巡览不会落在不存在的字段上。
	m.brField = 2
	m.browseFieldFocus(1)
	if m.brField == 4 {
		t.Fatalf("eq must not cycle into case field")
	}
	// cycleOp 在 mssql 上跳过 regex。
	m.brKind = protocol.DatabaseKindMSSQL
	m.brDraft = filterDraft{Op: protocol.FilterOpEndsWith}
	m.cycleOp(1)
	if m.brDraft.Op != protocol.FilterOpIn {
		t.Fatalf("mssql cycle from ends_with should skip regex, got %s", m.brDraft.Op)
	}
	m.brKind = protocol.DatabaseKindSQLite
	m.brDraft = filterDraft{Op: protocol.FilterOpEndsWith}
	m.cycleOp(1)
	if m.brDraft.Op != protocol.FilterOpRegex {
		t.Fatalf("sqlite cycle should hit regex, got %s", m.brDraft.Op)
	}
	// 已落在不支持 op 上时仍能循环走开。
	m.brKind = protocol.DatabaseKindMSSQL
	m.brDraft = filterDraft{Op: protocol.FilterOpRegex}
	m.cycleOp(1)
	if !opSupported(m.brKind, m.brDraft.Op) {
		t.Fatalf("cycleOp must land on a supported op")
	}
	// cycleFilterCase 三态循环，且非文本 op 上不动。
	m.brDraft = filterDraft{Op: protocol.FilterOpRegex}
	m.cycleFilterCase(1)
	if m.brDraft.Case != caseSensitive {
		t.Fatalf("case should advance, got %v", m.brDraft.Case)
	}
	m.cycleFilterCase(-1)
	if m.brDraft.Case != caseDefault {
		t.Fatalf("case should go back, got %v", m.brDraft.Case)
	}
	m.brDraft = filterDraft{Op: protocol.FilterOpEq, Case: caseSensitive}
	m.cycleFilterCase(1)
	if m.brDraft.Case != caseSensitive {
		t.Fatalf("non-text op should not change case")
	}
}
