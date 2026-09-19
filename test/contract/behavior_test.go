package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// joinInts 生成 "1),(2),...,(n" 字面量（与调用处的 "VALUES(" 拼接成
// 单列表多值插入；避免用 CTE 种子——见 behavior.md §11 约定）。
func joinInts(from, to int) string {
	parts := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		parts = append(parts, fmt.Sprintf("%d", i))
	}
	return strings.Join(parts, "),(")
}

// behavior_test.go 覆盖 spec/behavior.md 中此前未对拍的行为：
// §2.3 超时（timeout_ms → POLYDB_ERR_TIMEOUT）、§5 行数截断（max_rows → truncated/total_rows）、
// 以及错误码表 §4 的无效 UUID 参数（400 POLYDB_ERR_INVALID_PARAM）。
//
// 慢查询用 2e8 次递归 CTE：两端 debug 构建（Go 纯 Go sqlite / Rust C 未开优化）
// 都远超 300ms 超时窗口，保证"超时先于查询完成"是确定性的。

const slowSQL = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 200000000) SELECT MAX(x) FROM c"

func TestContractBehaviorTimeout(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			behaviorTimeoutScenario(r)
		})
	}
}

func behaviorTimeoutScenario(r *recorder) {
	// 独立连接：超时后被放弃的查询仍占用该连接的驱动锁，
	// 本场景内不再对它执行 SQL，直接删除收尾（Disconnect 不触碰在飞查询）。
	status, got, err := r.s.c.Do("POST", "/api/connections",
		map[string]any{"name": "behavior-timeout", "kind": "sqlite", "database": ":memory:"})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create connection failed: status=%d err=%v", r.s.b.name, status, err)
	}
	id, _ := firstConnID(got)

	r.step("slow query timeout", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": slowSQL, "timeout_ms": int64(300)},
		408, map[string]any{
			"code":      protocol.ErrTimeout,
			"retryable": true,
		})

	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}

func TestContractBehaviorMaxRows(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			behaviorMaxRowsScenario(r)
		})
	}
}

func behaviorMaxRowsScenario(r *recorder) {
	status, got, err := r.s.c.Do("POST", "/api/connections",
		map[string]any{"name": "behavior-maxrows", "kind": "sqlite", "database": ":memory:"})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create connection failed: status=%d err=%v", r.s.b.name, status, err)
	}
	id, _ := firstConnID(got)

	r.step("create seq", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE seq(v INTEGER)"}, 200,
		map[string]any{"statement_type": "ddl"})

	r.step("seed 25 rows", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO seq(v) VALUES(" +
			joinInts(1, 25) + ")"},
		200, map[string]any{"statement_type": "insert", "affected_rows": int64(25)})

	// max_rows=10：截断到 10 行，truncated=true，total_rows=25（§5）。
	r.step("max_rows=10 truncates", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT v FROM seq ORDER BY v", "max_rows": int64(10)},
		200, map[string]any{
			"truncated":  true,
			"total_rows": int64(25),
			"rows": []any{
				[]any{int64(1)}, []any{int64(2)}, []any{int64(3)}, []any{int64(4)}, []any{int64(5)},
				[]any{int64(6)}, []any{int64(7)}, []any{int64(8)}, []any{int64(9)}, []any{int64(10)},
			},
		}, keep("truncated", "total_rows", "rows"))

	// max_rows=0：使用服务端默认（10000），25 行不截断。
	r.step("max_rows=0 uses default", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT v FROM seq ORDER BY v", "max_rows": int64(0)},
		200, map[string]any{"truncated": false}, keep("truncated"))

	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}

// TestContractBehaviorInvalidID 锁定两端对非法 UUID 路径参数的一致性：
// 400 POLYDB_ERR_INVALID_PARAM（spec 中 {id}/{query_id} 均为 uuid 格式）。
func TestContractBehaviorInvalidID(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}

			r.step("invalid connection id", "GET", "/api/connections/not-a-uuid", nil, 400,
				map[string]any{"code": "POLYDB_ERR_INVALID_PARAM"})

			r.step("invalid query id cancel", "POST", "/api/queries/not-a-uuid/cancel", nil, 400,
				map[string]any{"code": "POLYDB_ERR_INVALID_PARAM"})

			// 合法 UUID 但不存在 → 404（与非法格式区分开）。
			r.step("unknown connection", "GET", "/api/connections/99999999-1111-2222-3333-444444444444", nil, 404,
				map[string]any{"code": "POLYDB_ERR_CONNECTION_NOT_FOUND"})
			r.step("unknown query cancel", "POST", "/api/queries/99999999-1111-2222-3333-444444444444/cancel", nil, 404,
				map[string]any{"code": "POLYDB_ERR_QUERY_NOT_FOUND"})
		})
	}
}
