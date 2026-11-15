package server

import (
	"fmt"
	"testing"
	"time"
)

// TestWSQueryRapidCompletionsNeverCancelled 是 §16.15 竞态修复的回归锁：
// 大量**快速正常完成**的查询，断言每一笔都拿到 query_result。
//
// 修复前这里会偶发 query_cancelled：查询正常完成 → retireQuery 关闭取消通道 →
// watcher goroutine 恰好被调度 → cancelFn 触发 → 查询上下文被取消 → 发射前的
// 「已取消」二次判定误判 → 结果被吞、客户端收到 query_cancelled。负载越高
// 窗口越易命中（这正是它在全量测试里偶发、单跑难复现的原因）。
//
// 修复后：正常完成只关 done 信号（不触发取消），watcher 看到 done 直接退出。
func TestWSQueryRapidCompletionsNeverCancelled(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	w := wsDial(t, srv)
	defer w.close()
	w.send(map[string]any{"type": "hello", "connection_id": id, "client_version": "0.1.0"})
	if typ, _ := w.recv(3 * time.Second); typ != "hello_ack" {
		t.Fatalf("expected hello_ack, got %q", typ)
	}

	const n = 60
	for i := 0; i < n; i++ {
		qid := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", i)
		w.send(map[string]any{
			"type":     "query",
			"query_id": qid,
			"sql":      "SELECT 1",
		})
		typ, msg := w.recv(3 * time.Second)
		if typ != "query_started" {
			t.Fatalf("query %d: expected query_started, got %q: %v", i, typ, msg)
		}
		typ, msg = w.recv(3 * time.Second)
		if typ != "query_result" {
			// 修复前：偶发 query_cancelled（结果被吞）。
			t.Fatalf("query %d: expected query_result, got %q: %v (race regressed?)", i, typ, msg)
		}
	}
}

// TestWSQueryCancelStillWorks 确认信号分离没有破坏真取消路径：
// 在途慢查询收到 query_cancel 后仍应得到 query_cancelled。
func TestWSQueryCancelStillWorks(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	w := wsDial(t, srv)
	defer w.close()
	w.send(map[string]any{"type": "hello", "connection_id": id, "client_version": "0.1.0"})
	if typ, _ := w.recv(3 * time.Second); typ != "hello_ack" {
		t.Fatalf("expected hello_ack, got %q", typ)
	}

	const qid = "aaaaaaaa-0000-0000-0000-000000000001"
	// 与 server_test.go 的 slowSQLiteCTE 同款：足够慢且可被取消。
	w.send(map[string]any{
		"type":     "query",
		"query_id": qid,
		"sql":      "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 100000000) SELECT count(*) FROM n",
	})
	if typ, _ := w.recv(3 * time.Second); typ != "query_started" {
		t.Fatalf("expected query_started, got %q", typ)
	}
	w.send(map[string]any{"type": "query_cancel", "query_id": qid})
	typ, msg := w.recv(10 * time.Second)
	// 取消竞态下也可能拿到完整结果（PD-11 发射边界）；两种都合规，
	// 但**不得**是 query_error。
	if typ == "query_error" {
		t.Fatalf("unexpected query_error after cancel: %v", msg)
	}
	if typ != "query_cancelled" && typ != "query_result" {
		t.Fatalf("unexpected frame after cancel: %q: %v", typ, msg)
	}
}

// TestWSQueryTimeoutStillWorks 确认 timeout 路径仍生效（§16.15 后）。
func TestWSQueryTimeoutStillWorks(t *testing.T) {
	srv := newTestServer(t)
	id := createSQLiteConn(t, srv)

	w := wsDial(t, srv)
	defer w.close()
	w.send(map[string]any{"type": "hello", "connection_id": id, "client_version": "0.1.0"})
	if typ, _ := w.recv(3 * time.Second); typ != "hello_ack" {
		t.Fatalf("expected hello_ack, got %q", typ)
	}

	w.send(map[string]any{
		"type":       "query",
		"query_id":   "aaaaaaaa-0000-0000-0000-000000000002",
		"sql":        "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 100000000) SELECT count(*) FROM n",
		"timeout_ms": 300,
	})
	if typ, _ := w.recv(3 * time.Second); typ != "query_started" {
		t.Fatalf("expected query_started, got %q", typ)
	}
	typ, msg := w.recv(10 * time.Second)
	if typ != "query_error" {
		t.Fatalf("expected query_error(TIMEOUT), got %q: %v", typ, msg)
	}
	err, _ := msg["error"].(map[string]any)
	if c, _ := err["code"].(string); c != "POLYDB_ERR_TIMEOUT" {
		t.Fatalf("error code = %q, want POLYDB_ERR_TIMEOUT", c)
	}
}
