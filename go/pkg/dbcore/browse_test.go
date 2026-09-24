package dbcore

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func strVal(t string) protocol.Value { return protocol.NewStringValue(t) }

func boolPtr(b bool) *bool { return &b }

func oneCondReq(cond protocol.FilterCondition) *protocol.TableRowsRequest {
	return &protocol.TableRowsRequest{Conditions: []protocol.FilterCondition{cond}}
}

func colCond(column string, op protocol.FilterOperator, v protocol.Value) protocol.FilterCondition {
	return protocol.FilterCondition{Column: column, Op: op, Value: v}
}

func renderOne(t *testing.T, d SQLDialect, cond protocol.FilterCondition) (string, []protocol.Value) {
	t.Helper()
	sql, args, err := BuildRowsWhere(d, oneCondReq(cond))
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return sql, args
}

func assertOne(t *testing.T, d SQLDialect, cond protocol.FilterCondition, wantSQL string, wantArg protocol.Value) {
	t.Helper()
	sql, args := renderOne(t, d, cond)
	if sql != wantSQL {
		t.Errorf("sql = %q, want %q", sql, wantSQL)
	}
	if len(args) != 1 {
		t.Fatalf("args = %d, want 1", len(args))
	}
	got, _ := args[0].MarshalJSON()
	want, _ := wantArg.MarshalJSON()
	if string(got) != string(want) {
		t.Errorf("arg = %s, want %s", got, want)
	}
}

func expectErrCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	if !strings.Contains(err.Error(), code) {
		t.Errorf("error = %v, want code %s", err, code)
	}
}

func TestEscapeHelpers(t *testing.T) {
	if got := EscapeLikeText("a%b_c!d"); got != "a!%b!_c!!d" {
		t.Errorf("EscapeLikeText = %q", got)
	}
	if got := LikePatternToGlob(`a%b_c*d?e[f`); got != `a*b?c\*d\?e\[f` {
		t.Errorf("LikePatternToGlob = %q", got)
	}
	// 字面文本里的 % 对 GLOB 无语义，只有 * ? [ \ 要转义
	if got := GlobQuoteText("50%*a"); got != `50%\*a` {
		t.Errorf("GlobQuoteText = %q", got)
	}
}

func TestLikeFamilyPassthrough(t *testing.T) {
	assertOne(t, DefaultSQLDialect(), colCond("name", protocol.FilterOpLike, strVal("%a_")),
		`"name" LIKE ?`, strVal("%a_"))
	assertOne(t, MSSQLSQLDialect(), colCond("name", protocol.FilterOpNotLike, strVal("%a_")),
		"[name] NOT LIKE ?", strVal("%a_"))
}

func TestWrappedOpsEscapeAndWrap(t *testing.T) {
	assertOne(t, DefaultSQLDialect(), colCond("x", protocol.FilterOpContains, strVal("a%b")),
		`"x" LIKE ? ESCAPE '!'`, strVal("%a!%b%"))
	assertOne(t, MySQLSQLDialect(), colCond("x", protocol.FilterOpStartsWith, strVal("a_b")),
		"`x` LIKE ? ESCAPE '!'", strVal("a!_b%"))
	assertOne(t, OracleSQLDialect(), colCond("x", protocol.FilterOpEndsWith, strVal("a!b")),
		`"x" LIKE ? ESCAPE '!'`, strVal("%a!!b"))
}

func TestCaseInsensitiveRendering(t *testing.T) {
	ci := boolPtr(false)
	assertOne(t, DefaultSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a")), ci),
		`"x" ILIKE ?`, strVal("a"))
	assertOne(t, DefaultSQLDialect(), withCS(colCond("x", protocol.FilterOpNotLike, strVal("a")), ci),
		`"x" NOT ILIKE ?`, strVal("a"))
	assertOne(t, MySQLSQLDialect(), withCS(colCond("x", protocol.FilterOpContains, strVal("a")), ci),
		"LOWER(`x`) LIKE LOWER(?) ESCAPE '!'", strVal("%a%"))
	assertOne(t, MSSQLSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a")), ci),
		"LOWER([x]) LIKE LOWER(?)", strVal("a"))
	assertOne(t, OracleSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a")), ci),
		`LOWER("x") LIKE LOWER(?)`, strVal("a"))
	// SQLite 的 LIKE 本就不区分 ASCII：不套 LOWER
	assertOne(t, SQLiteSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a")), ci),
		`"x" LIKE ?`, strVal("a"))
}

func TestCaseSensitiveRendering(t *testing.T) {
	cs := boolPtr(true)
	assertOne(t, DefaultSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a")), cs),
		`"x" LIKE ?`, strVal("a"))
	assertOne(t, MySQLSQLDialect(), withCS(colCond("x", protocol.FilterOpContains, strVal("a")), cs),
		"CAST(`x` AS BINARY) LIKE CAST(? AS BINARY) ESCAPE '!'", strVal("%a%"))
	assertOne(t, MSSQLSQLDialect(), withCS(colCond("x", protocol.FilterOpNotLike, strVal("a")), cs),
		"[x] COLLATE Latin1_General_CS_AS NOT LIKE ?", strVal("a"))
	// SQLite 强制区分走 GLOB，模式按 GLOB 规则重转义
	assertOne(t, SQLiteSQLDialect(), withCS(colCond("x", protocol.FilterOpContains, strVal("a*b")), cs),
		`"x" GLOB ?`, strVal(`*a\*b*`))
	assertOne(t, SQLiteSQLDialect(), withCS(colCond("x", protocol.FilterOpLike, strVal("a*b%")), cs),
		`"x" GLOB ?`, strVal(`a\*b*`))
}

func withCS(c protocol.FilterCondition, cs *bool) protocol.FilterCondition {
	c.CaseSensitive = cs
	return c
}

func TestEmptyIgnoresValue(t *testing.T) {
	sql, args := renderOne(t, DefaultSQLDialect(), colCond("x", protocol.FilterOpEmpty, protocol.NewNullValue()))
	if sql != `("x" IS NULL OR "x" = '')` {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("args = %d, want 0", len(args))
	}
}

func TestRegexPerFlavor(t *testing.T) {
	d := DefaultSQLDialect()
	assertOne(t, d, colCond("x", protocol.FilterOpRegex, strVal("^a")), `"x" ~ ?`, strVal("^a"))
	assertOne(t, d, withCS(colCond("x", protocol.FilterOpRegex, strVal("^a")), boolPtr(false)),
		`"x" ~* ?`, strVal("^a"))
	assertOne(t, MySQLSQLDialect(), colCond("x", protocol.FilterOpRegex, strVal("^a")),
		"`x` REGEXP ?", strVal("^a"))
	assertOne(t, MySQLSQLDialect(), withCS(colCond("x", protocol.FilterOpRegex, strVal("^a")), boolPtr(true)),
		"CAST(`x` AS BINARY) REGEXP CAST(? AS BINARY)", strVal("^a"))
	assertOne(t, OracleSQLDialect(), withCS(colCond("x", protocol.FilterOpRegex, strVal("^a")), boolPtr(false)),
		`REGEXP_LIKE("x", ?, 'i')`, strVal("^a"))
	assertOne(t, SQLiteSQLDialect(), colCond("x", protocol.FilterOpRegex, strVal("^a")),
		`"x" REGEXP ?`, strVal("^a"))
	assertOne(t, SQLiteSQLDialect(), withCS(colCond("x", protocol.FilterOpRegex, strVal("^a")), boolPtr(false)),
		`"x" REGEXP '(?i)' || ?`, strVal("^a"))
}

func TestRegexOnMssqlNotSupported(t *testing.T) {
	_, _, err := BuildRowsWhere(MSSQLSQLDialect(), oneCondReq(colCond("x", protocol.FilterOpRegex, strVal("a"))))
	expectErrCode(t, err, protocol.ErrNotSupported)
	if MSSQLSQLDialect().Flavor.SupportsRegex() {
		t.Error("mssql must report regex unsupported")
	}
	if !SQLiteSQLDialect().Flavor.SupportsRegex() {
		t.Error("sqlite must report regex supported")
	}
}

func TestTextOpsRejectNullValue(t *testing.T) {
	for _, op := range []protocol.FilterOperator{
		protocol.FilterOpContains, protocol.FilterOpStartsWith, protocol.FilterOpEndsWith,
		protocol.FilterOpRegex, protocol.FilterOpLike,
	} {
		_, _, err := BuildRowsWhere(DefaultSQLDialect(), oneCondReq(colCond("x", op, protocol.NewNullValue())))
		expectErrCode(t, err, protocol.ErrInvalidParam)
	}
}

func TestNonScalarValueRejected(t *testing.T) {
	var arr protocol.Value
	if err := arr.UnmarshalJSON([]byte(`[1,2]`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildRowsWhere(DefaultSQLDialect(),
		oneCondReq(colCond("x", protocol.FilterOpContains, arr)))
	expectErrCode(t, err, protocol.ErrInvalidParam)
}

func TestCaseSensitiveIgnoredByComparisonOps(t *testing.T) {
	assertOne(t, MySQLSQLDialect(), withCS(colCond("n", protocol.FilterOpEq, protocol.NewIntValue(7)), boolPtr(false)),
		"`n` = ?", protocol.NewIntValue(7))
}

func TestValidateAcceptsNewOps(t *testing.T) {
	req := &protocol.TableRowsRequest{
		Columns: []string{"x"},
		Conditions: []protocol.FilterCondition{
			withCS(colCond("x", protocol.FilterOpContains, strVal("a")), boolPtr(true)),
			colCond("y", protocol.FilterOpEmpty, protocol.NewNullValue()),
			colCond("z", protocol.FilterOpRegex, strVal("^b")),
		},
	}
	if err := BrowseRowsValidate(req); err != nil {
		t.Errorf("validate: %v", err)
	}
	// regex/contains 缺值必须报错
	if err := BrowseRowsValidate(&protocol.TableRowsRequest{
		Conditions: []protocol.FilterCondition{colCond("z", protocol.FilterOpRegex, protocol.NewNullValue())},
	}); err == nil {
		t.Error("expected invalid-param for regex without value")
	}
	// 未知 op 必须报错
	if err := BrowseRowsValidate(&protocol.TableRowsRequest{
		Conditions: []protocol.FilterCondition{colCond("z", protocol.FilterOperator("nope"), strVal("a"))},
	}); err == nil {
		t.Error("expected invalid-param for unknown op")
	}
}
