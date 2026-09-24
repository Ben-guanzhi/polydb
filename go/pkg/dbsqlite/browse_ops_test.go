package dbsqlite

import (
	"context"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func boolPtr(b bool) *bool { return &b }

func fixture(t *testing.T) *Conn {
	t.Helper()
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	for _, q := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t (name) VALUES ('Alice'), ('alice'), ('Bob')`,
	} {
		if _, err := c.Execute(ctx, q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	return c
}

func scalar(t *testing.T, c *Conn, sql string, args ...protocol.Value) protocol.Value {
	t.Helper()
	res, err := c.Execute(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		t.Fatalf("exec %q returned no rows", sql)
	}
	return res.Rows[0][0]
}

func text(s string) protocol.Value { return protocol.NewStringValue(s) }

func mustInt(t *testing.T, v protocol.Value) int64 {
	t.Helper()
	i, ok := protocol.TryGetInt(v)
	if !ok {
		t.Fatalf("value is not an integer: %v", v)
	}
	return i
}

// REGEXP 在 SQLite 里只是 regexp(pattern, text) 的语法糖，必须由驱动注册 UDF。
func TestRegexpUdfRegistered(t *testing.T) {
	c := fixture(t)
	matches := func(pattern protocol.Value) int64 {
		return mustInt(t, scalar(t, c, "SELECT CASE WHEN 'hello' REGEXP ? THEN 1 ELSE 0 END", pattern))
	}
	if got := matches(text("^h")); got != 1 {
		t.Errorf("^h should match, got %d", got)
	}
	if got := matches(text("^H")); got != 0 {
		t.Errorf("^H should not match, got %d", got)
	}
	// (?i) 内联标志是两端 UDF 的统一不区分大小写写法
	if got := matches(text("(?i)^H")); got != 1 {
		t.Errorf("(?i)^H should match, got %d", got)
	}
	// 任一参数 NULL → 结果 NULL（SQL 三值逻辑）
	for _, q := range []string{
		"SELECT (NULL REGEXP '^h') IS NULL",
		"SELECT ('hello' REGEXP NULL) IS NULL",
	} {
		if got := mustInt(t, scalar(t, c, q)); got != 1 {
			t.Errorf("%s: want NULL propagation, got %d", q, got)
		}
	}
	// 非法正则必须冒泡错误，而不是静默当成不匹配
	if _, err := c.Execute(context.Background(), "SELECT 'a' REGEXP ?", text("(a")); err == nil {
		t.Error("invalid regex should error")
	}
}

// 18 op 里新增的文本类 op 必须真的能在 SQLite 上跑通（behavior.md §13.5）。
func TestBrowseRowsNewOps(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	browse := func(conds ...protocol.FilterCondition) []string {
		res, err := c.BrowseRows(ctx, "", "t", &protocol.TableRowsRequest{Conditions: conds})
		if err != nil {
			t.Fatalf("browse: %v", err)
		}
		names := make([]string, 0, len(res.Rows))
		for _, r := range res.Rows {
			if s, ok := protocol.TryGetString(r[1]); ok {
				names = append(names, s)
			} else {
				names = append(names, "?")
			}
		}
		return names
	}
	cond := func(op protocol.FilterOperator, v protocol.Value, cs *bool) protocol.FilterCondition {
		return protocol.FilterCondition{Column: "name", Op: op, Value: v, CaseSensitive: cs}
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	// contains 走 LIKE：SQLite 的 LIKE 对 ASCII 不区分，ALI 两种大小写都中
	eq(browse(cond(protocol.FilterOpContains, text("ALI"), nil)), "Alice", "alice")
	// 强制区分走 GLOB：小写 ali 只命中 alice
	eq(browse(cond(protocol.FilterOpContains, text("ali"), boolPtr(true))), "alice")
	// 显式强制不区分仍走 LIKE
	eq(browse(cond(protocol.FilterOpContains, text("ali"), boolPtr(false))), "Alice", "alice")
	// regex 默认区分、不区分靠 (?i)
	eq(browse(cond(protocol.FilterOpRegex, text("^A"), nil)), "Alice")
	eq(browse(cond(protocol.FilterOpRegex, text("^a"), boolPtr(false))), "Alice", "alice")
	// starts_with / ends_with
	eq(browse(cond(protocol.FilterOpStartsWith, text("Bo"), nil)), "Bob")
	eq(browse(cond(protocol.FilterOpEndsWith, text("lice"), nil)), "Alice", "alice")
	// empty：NULL 与空串都算
	if _, err := c.Execute(ctx, `INSERT INTO t (name) VALUES (''), (NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := len(browse(protocol.FilterCondition{Column: "name", Op: protocol.FilterOpEmpty})); got != 2 {
		t.Errorf("empty: got %d rows, want 2", got)
	}

	count, err := c.BrowseRowsCount(ctx, "", "t", &protocol.TableRowsRequest{
		Conditions: []protocol.FilterCondition{cond(protocol.FilterOpRegex, text("^[AB]"), nil)},
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}
