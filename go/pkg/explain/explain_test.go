package explain

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func sv(s string) protocol.Value { return protocol.NewStringValue(s) }
func iv(i int64) protocol.Value  { return protocol.NewIntValue(i) }

func mkResult(cols []string, rows [][]protocol.Value) *protocol.QueryResult {
	rc := make([]protocol.ResultColumn, len(cols))
	for i, c := range cols {
		rc[i] = protocol.ResultColumn{Name: c}
	}
	return &protocol.QueryResult{Columns: rc, Rows: rows}
}

func TestBuildSQL(t *testing.T) {
	cases := []struct {
		name, raw string
		kind      protocol.DatabaseKind
		want      string
	}{
		{"sqlite", "SELECT * FROM t;", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT * FROM t"},
		{"postgres", "  select 1 ", protocol.DatabaseKindPostgres, "EXPLAIN (FORMAT JSON) select 1"},
		{"mysql", "SELECT 1", protocol.DatabaseKindMySQL, "EXPLAIN FORMAT=JSON SELECT 1"},
		{"oracle", "SELECT 1", protocol.DatabaseKindOracle, "EXPLAIN PLAN FOR SELECT 1"},
		{"mssql falls back", "SELECT 1", protocol.DatabaseKindMSSQL, "EXPLAIN SELECT 1"},
		{"already explained", "EXPLAIN ANALYZE SELECT 1", protocol.DatabaseKindPostgres, "EXPLAIN ANALYZE SELECT 1"},
		{"explain word prefix not matched", "EXPLAINED_THING", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN EXPLAINED_THING"},
		{"line comment stripped", "SELECT 1 -- tail\n;", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT 1"},
		{"block comment stripped", "SELECT/* hi */ 1", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT 1"},
		{"dash inside literal kept", "SELECT '--x' AS a", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT '--x' AS a"},
		{"comment inside literal kept", "SELECT '/*a*/' AS a", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT '/*a*/' AS a"},
		{"doubled quote literal", "SELECT 'it''s' AS a", protocol.DatabaseKindSQLite, "EXPLAIN QUERY PLAN SELECT 'it''s' AS a"},
		{"only comments", "  -- nothing\n", protocol.DatabaseKindSQLite, ""},
	}
	for _, c := range cases {
		if got := BuildSQL(c.raw, c.kind); got != c.want {
			t.Errorf("%s: BuildSQL(%q,%s) = %q, want %q", c.name, c.raw, c.kind, got, c.want)
		}
	}
}

func TestRenderSQLiteTree(t *testing.T) {
	res := mkResult([]string{"id", "parent", "notused", "detail"}, [][]protocol.Value{
		{iv(1), iv(0), iv(216), sv("SCAN t")},
		{iv(2), iv(1), protocol.NewNullValue(), sv("USE INDEX (idx_t)")},
		{iv(3), iv(0), protocol.NewNullValue(), sv("EXECUTE SUBQUERY")},
	})
	r := RenderResult(res, protocol.DatabaseKindSQLite)
	if !r.Tree() {
		t.Fatalf("sqlite rows should render as tree, got text %+v", r.Text)
	}
	got := strings.Join(r.Lines(), "\n")
	// notused 是 SQLite 的内部标志位（实测值漂移），不进计划文本。
	want := strings.Join([]string{
		"├─ Step 1  SCAN t",
		"│  └─ Step 2  USE INDEX (idx_t)",
		"└─ Step 3  EXECUTE SUBQUERY",
	}, "\n")
	if got != want {
		t.Fatalf("lines =\n%s\nwant\n%s", got, want)
	}
}

// SQLite 缺 detail 列（如直接 EXPLAIN 的 VDBE 输出）时回落表格文本。
func TestRenderSQLiteMissingColumnsFallsBack(t *testing.T) {
	res := mkResult([]string{"addr", "opcode"}, [][]protocol.Value{{iv(0), sv("Init")}})
	r := RenderResult(res, protocol.DatabaseKindSQLite)
	if r.Tree() {
		t.Fatalf("should fall back to text, got tree")
	}
	if got := strings.Join(r.Lines(), "\n"); got != "addr | opcode\n0 | Init" {
		t.Fatalf("text =\n%s", got)
	}
}

func TestRenderPostgresTree(t *testing.T) {
	// PG 服务端真实键名带空格（Node Type / Total Cost / Plan Rows）。
	const plan = `[{
		"Plan": {
			"Node Type": "Limit",
			"Total Cost": 23.47,
			"Plan Rows": 100,
			"Plans": [{
				"Node Type": "Sort",
				"Total Cost": 23.47,
				"Plan Rows": 100,
				"Plans": [{
					"Node Type": "Seq Scan",
					"Relation Name": "t",
					"Total Cost": 20.20,
					"Plan Rows": 320,
					"Filter": "(id > 5)"
				}]
			}]
		}
	}]`
	res := mkResult([]string{"QUERY PLAN"}, [][]protocol.Value{{sv(plan)}})
	r := RenderResult(res, protocol.DatabaseKindPostgres)
	if !r.Tree() {
		t.Fatalf("pg json should render as tree, got text %+v", r.Text)
	}
	got := strings.Join(r.Lines(), "\n")
	want := strings.Join([]string{
		"└─ Limit [cost=23.47 · rows≈100]",
		"   └─ Sort [cost=23.47 · rows≈100]",
		"      └─ Seq Scan [cost=20.20 · rows≈320]  on t (id > 5)",
	}, "\n")
	if got != want {
		t.Fatalf("lines =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderMySQLSingleTable(t *testing.T) {
	const doc = `{"query_block":{"select_id":1,"cost_info":{"query_cost":"1.40"},` +
		`"table":{"table_name":"t","access_type":"ALL","rows_examined_per_scan":1,` +
		`"cost_info":{"read_cost":"1.00","eval_cost":"0.20"}}}}`
	res := mkResult([]string{"EXPLAIN"}, [][]protocol.Value{{sv(doc)}})
	r := RenderResult(res, protocol.DatabaseKindMySQL)
	if !r.Tree() {
		t.Fatalf("mysql json should render as tree, got text %+v", r.Text)
	}
	if got := strings.Join(r.Lines(), "\n"); got != "└─ t [cost=1.40 · rows≈1]  ALL" {
		t.Fatalf("lines =\n%s", got)
	}
}

func TestRenderMySQLNestedLoop(t *testing.T) {
	const doc = `{"query_block":{"cost_info":{"query_cost":"44.60"},"nested_loop":[` +
		`{"table":{"table_name":"a","access_type":"ALL","rows_examined_per_scan":10}},` +
		`{"table":{"table_name":"b","access_type":"ref","key":"idx_b","rows_examined_per_scan":1}}]}}`
	res := mkResult([]string{"EXPLAIN"}, [][]protocol.Value{{sv(doc)}})
	r := RenderResult(res, protocol.DatabaseKindMySQL)
	got := strings.Join(r.Lines(), "\n")
	want := strings.Join([]string{
		"└─ Nested loop [cost=44.60]",
		"   ├─ a [rows≈10]  ALL",
		"   └─ b [rows≈1]  ref key=idx_b",
	}, "\n")
	if got != want {
		t.Fatalf("lines =\n%s\nwant\n%s", got, want)
	}
}

// MySQL 排序包装块：Sort 作为父节点，内层表作为子节点。
func TestRenderMySQLOrdering(t *testing.T) {
	const doc = `{"query_block":{"cost_info":{"query_cost":"3.10"},"ordering_operation":{` +
		`"using_filesort":true,"table":{"table_name":"t","access_type":"ALL"}}}}`
	res := mkResult([]string{"EXPLAIN"}, [][]protocol.Value{{sv(doc)}})
	r := RenderResult(res, protocol.DatabaseKindMySQL)
	got := strings.Join(r.Lines(), "\n")
	want := "└─ Sort [cost=3.10]  filesort\n   └─ t  ALL"
	if got != want {
		t.Fatalf("lines =\n%s\nwant\n%s", got, want)
	}
}

// 无计划的方言（MSSQL 消息文本 / Oracle 语句 ID 表）与解析失败一律表格文本。
func TestRenderTextFallback(t *testing.T) {
	res := mkResult([]string{"id", "operation"}, [][]protocol.Value{
		{sv("1"), sv("Table Scan")},
		{sv("2"), sv("Index Seek")},
	})
	for _, kind := range []protocol.DatabaseKind{protocol.DatabaseKindMSSQL, protocol.DatabaseKindOracle, protocol.DatabaseKindRedis} {
		r := RenderResult(res, kind)
		if r.Tree() {
			t.Errorf("%s should not build a tree", kind)
		}
		if got := strings.Join(r.Lines(), "\n"); got != "id | operation\n1 | Table Scan\n2 | Index Seek" {
			t.Errorf("%s text =\n%s", kind, got)
		}
	}
	// PG 返回非 JSON（例如误用 EXPLAIN (TEXT)）时回落表格。
	text := mkResult([]string{"QUERY PLAN"}, [][]protocol.Value{{sv("Limit  (cost=0.00..23.47 rows=100 width=36)")}})
	if r := RenderResult(text, protocol.DatabaseKindPostgres); r.Tree() {
		t.Fatalf("non-json pg output should fall back to text")
	}
}

func TestRenderEmpty(t *testing.T) {
	for _, res := range []*protocol.QueryResult{nil, mkResult([]string{"a"}, nil)} {
		r := RenderResult(res, protocol.DatabaseKindSQLite)
		if r.Tree() || len(r.Text) != 1 || r.Text[0] != "(空结果)" {
			t.Fatalf("empty result should render (空结果), got %+v", r)
		}
	}
}

// SQLite parent 自引用 / 指向未知 id / 互指成环时不得死循环，也不得丢计划行。
func TestRenderSQLiteBrokenParents(t *testing.T) {
	res := mkResult([]string{"id", "parent", "detail"}, [][]protocol.Value{
		{iv(1), iv(9), sv("A")}, // parent 不存在 → 作为根
		{iv(2), iv(2), sv("B")}, // 自引用 → 作为根
		{iv(3), iv(4), sv("C")},
		{iv(4), iv(3), sv("D")}, // 3↔4 互指成环 → 补为顶层，路径环保护
	})
	r := RenderResult(res, protocol.DatabaseKindSQLite)
	got := strings.Join(r.Lines(), "\n")
	want := strings.Join([]string{
		"├─ Step 1  A",
		"├─ Step 2  B",
		"└─ Step 3  C",
		"   └─ Step 4  D",
		"      └─ Step 3  C …", // 环在此截断，不再展开
	}, "\n")
	if got != want {
		t.Fatalf("lines =\n%s\nwant\n%s", got, want)
	}
}
