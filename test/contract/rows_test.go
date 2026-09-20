package contract

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// M11 表数据浏览契约：Rust / Go 双后端一致性（spec/behavior.md §13）。
//
// 覆盖：分页（offset/limit/has_more）、排序（asc/desc/多列）、13 种过滤操作符、
// AND/OR 组合、rows/count 精确计数、非法请求 → INVALID_PARAM。

func TestContractTableRows(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)

			// 建连接 + 建表 + 灌数据（1..12）
			status, created, err := c.Do("POST", "/api/connections",
				map[string]any{"name": "rows-sqlite-" + fmt.Sprintf("%d", time.Now().UnixNano()),
					"kind": "sqlite", "database": ":memory:"})
			if err != nil || status != 201 {
				t.Fatalf("%s: create conn: status=%d err=%v", b.name, status, err)
			}
			id, ok := firstConnID(created)
			if !ok || id == "" {
				t.Fatalf("%s: no conn id: %v", b.name, created)
			}
			setup := [][]string{
				{"CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, score INTEGER)",
					"INSERT INTO t(name, score) VALUES('a', 1), ('b', 2), ('c', 3)"},
			}
			for _, pair := range setup {
				for _, sql := range pair {
					if st, got, err := c.Do("POST", "/api/connections/"+id+"/query", map[string]any{"sql": sql}); err != nil || st != 200 {
						t.Fatalf("%s: setup %q: status=%d err=%v body=%v", b.name, sql, st, err, got)
					}
				}
			}
			// 追加 9 行（score 4..12，name d..l）
			if st, got, err := c.Do("POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "WITH seq(n) AS (SELECT 4 UNION ALL SELECT n+1 FROM seq WHERE n < 12) INSERT INTO t(name, score) SELECT printf('%c', 96+n), n FROM seq"}); err != nil || st != 200 {
				t.Fatalf("%s: seed rows: status=%d err=%v body=%v", b.name, st, err, got)
			}

			rowsPath := "/api/connections/" + id + "/schemas/main/tables/t/rows/query"
			countPath := "/api/connections/" + id + "/schemas/main/tables/t/rows/count"

			// ─── 分页 ────────────────────────────────────────
			// 第一页（limit 5）：has_more=true，5 行，按 PK 顺序
			st, got, err := c.Do("POST", rowsPath, map[string]any{"limit": 5})
			if err != nil || st != 200 {
				t.Fatalf("%s: rows page1: status=%d err=%v body=%v", b.name, st, err, got)
			}
			assertRowsPage(t, b, got, 5, 0, true)

			// 第二页（offset 5）：剩 7 行 > 5 → has_more 仍 true
			st, got, _ = c.Do("POST", rowsPath, map[string]any{"limit": 5, "offset": 5})
			assertRowsPage(t, b, got, 5, 5, true)
			// 第三页（offset 10）：只剩 2 行 → has_more=false
			st, got, _ = c.Do("POST", rowsPath, map[string]any{"limit": 5, "offset": 10})
			assertRowsPage(t, b, got, 2, 10, false)

			// ─── 排序 ────────────────────────────────────────
			st, got, _ = c.Do("POST", rowsPath, map[string]any{
				"limit": 3, "order_by": []any{map[string]any{"column": "score", "dir": "desc"}}})
			if err != nil || st != 200 {
				t.Fatalf("%s: rows desc: status=%d err=%v", b.name, st, err)
			}
			rows := got.(map[string]any)["rows"].([]any)
			if v := firstCellInt(t, b, rows[0]); v != 12 {
				t.Fatalf("%s: desc order: first score = %v, want 12", b.name, v)
			}
			// 多列排序
			st, got, _ = c.Do("POST", rowsPath, map[string]any{
				"limit": 2,
				"order_by": []any{
					map[string]any{"column": "score", "dir": "desc"},
					map[string]any{"column": "name", "dir": "asc"},
				}})
			if err != nil || st != 200 {
				t.Fatalf("%s: rows multi-order: status=%d err=%v", b.name, st, err)
			}

			// ─── 投影 ────────────────────────────────────────
			st, got, _ = c.Do("POST", rowsPath, map[string]any{"columns": []any{"name"}})
			if err != nil || st != 200 {
				t.Fatalf("%s: projection: status=%d err=%v", b.name, st, err)
			}
			cols := got.(map[string]any)["columns"].([]any)
			if len(cols) != 1 {
				t.Fatalf("%s: projection: got %d columns, want 1", b.name, len(cols))
			}

			// ─── 操作符矩阵 ──────────────────────────────────
			for _, tc := range []struct {
				name   string
				cond   map[string]any
				count  int
			}{
				{"eq", map[string]any{"column": "name", "op": "eq", "value": "a"}, 1},
				{"ne", map[string]any{"column": "name", "op": "ne", "value": "a"}, 11},
				{"lt", map[string]any{"column": "score", "op": "lt", "value": 3}, 2},
				{"le", map[string]any{"column": "score", "op": "le", "value": 3}, 3},
				{"gt", map[string]any{"column": "score", "op": "gt", "value": 10}, 2},
				{"ge", map[string]any{"column": "score", "op": "ge", "value": 10}, 3},
				{"like", map[string]any{"column": "name", "op": "like", "value": "%a%"}, 1},   // a..l 中仅 "a" 含 a
				{"not_like", map[string]any{"column": "name", "op": "not_like", "value": "%a%"}, 11},
				{"in", map[string]any{"column": "score", "op": "in", "values": []any{1, 3, 5}}, 3},
				{"not_in", map[string]any{"column": "score", "op": "not_in", "values": []any{1, 3, 5}}, 9},
				{"between", map[string]any{"column": "score", "op": "between", "value": 2, "second_value": 4}, 3},
				{"null", map[string]any{"column": "name", "op": "null"}, 0},
				{"not_null", map[string]any{"column": "name", "op": "not_null"}, 12},
			} {
				st, got, err := c.Do("POST", countPath, map[string]any{"conditions": []any{tc.cond}})
				if err != nil || st != 200 {
					t.Fatalf("%s: count op %s: status=%d err=%v body=%v", b.name, tc.name, st, err, got)
				}
				count := got.(map[string]any)["count"]
				if int(toInt64(t, b, count)) != tc.count {
					t.Fatalf("%s: count op %s = %v, want %d", b.name, tc.name, count, tc.count)
				}
			}

			// ─── AND / OR 组合 ───────────────────────────────
			// AND: score>=2 AND score<=4 → 3 行
			andConds := []any{
				map[string]any{"column": "score", "op": "ge", "value": 2},
				map[string]any{"column": "score", "op": "le", "value": 4},
			}
			st, got, _ = c.Do("POST", countPath, map[string]any{"conditions": andConds})
			if err != nil || st != 200 || int(toInt64(t, b, got.(map[string]any)["count"])) != 3 {
				t.Fatalf("%s: count AND: status=%d body=%v", b.name, st, got)
			}
			// OR: name='a' OR score=12 → 2 行
			orConds := []any{
				map[string]any{"column": "name", "op": "eq", "value": "a"},
				map[string]any{"column": "score", "op": "eq", "value": 12},
			}
			st, got, _ = c.Do("POST", countPath, map[string]any{"conditions": orConds, "logic": "or"})
			if err != nil || st != 200 || int(toInt64(t, b, got.(map[string]any)["count"])) != 2 {
				t.Fatalf("%s: count OR: status=%d body=%v", b.name, st, got)
			}

			// ─── 非法请求 → 400 INVALID_PARAM ───────────────
			for _, bad := range []map[string]any{
				{"conditions": []any{map[string]any{"column": "name", "op": "bogus"}}},
				{"conditions": []any{map[string]any{"column": "name", "op": "in", "values": []any{}}}},
				{"columns": []any{""}},
			} {
				st, got, _ := c.Do("POST", rowsPath, bad)
				if st != 400 {
					t.Fatalf("%s: invalid req %v: status = %d, want 400 (body=%v)", b.name, bad, st, got)
				}
				assertErrCode(t, b, got, "POLYDB_ERR_INVALID_PARAM")
			}

			// ─── count 与 rows 分页自洽 ─────────────────────
			st, got, _ = c.Do("POST", countPath, map[string]any{})
			if err != nil || st != 200 || int(toInt64(t, b, got.(map[string]any)["count"])) != 12 {
				t.Fatalf("%s: count all: status=%d body=%v", b.name, st, got)
			}
		})
	}
}

func assertRowsPage(t *testing.T, b *Backend, got any, wantRows int, wantOffset int, wantHasMore bool) {
	t.Helper()
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("%s: rows page not an object: %v", b.name, got)
	}
	rows, _ := m["rows"].([]any)
	if len(rows) != wantRows {
		t.Fatalf("%s: rows page: got %d rows, want %d (body=%v)", b.name, len(rows), wantRows, got)
	}
	if int(toInt64(t, b, m["offset"])) != wantOffset {
		t.Fatalf("%s: rows page: offset = %v, want %d", b.name, m["offset"], wantOffset)
	}
	if m["has_more"] != wantHasMore {
		t.Fatalf("%s: rows page: has_more = %v, want %v", b.name, m["has_more"], wantHasMore)
	}
}

func firstCellInt(t *testing.T, b *Backend, row any) int64 {
	t.Helper()
	cells, ok := row.([]any)
	if !ok || len(cells) == 0 {
		t.Fatalf("%s: bad row: %v", b.name, row)
	}
	return toInt64(t, b, cells[0])
}

func toInt64(t *testing.T, b *Backend, v any) int64 {
	t.Helper()
	// msgpack 解码到 any 时整数可能是 int8/int16/int32/int64/uint* 中最小可容纳类型，
	// JSON 路径则可能是 float64；用反射统一处理。
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return int64(rv.Float())
	default:
		t.Fatalf("%s: not a number: %v (%T)", b.name, v, v)
		return 0
	}
}
