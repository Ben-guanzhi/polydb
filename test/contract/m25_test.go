package contract

import (
	"testing"
)

// M25 事务契约：Rust / Go 双后端一致性。
// 覆盖：begin → execute × N → commit/rollback，重复 finalize 返回 404，
// 事务内失败保持 active，缺事务返回 POLYDB_ERR_TRANSACTION_NOT_FOUND。

func TestContractSQLiteTransactions(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)
			r := &recorder{t: t, s: &st{b: b, c: c}}

			// 建连接 + 建表
			status, created, err := c.Do("POST", "/api/connections",
				map[string]any{"name": "m25-sqlite", "kind": "sqlite", "database": ":memory:"})
			if err != nil || status != 201 {
				t.Fatalf("%s: create conn failed: status=%d err=%v", b.name, status, err)
			}
			id, ok := firstConnID(created)
			if !ok || id == "" {
				t.Fatalf("%s: no conn id: %v", b.name, created)
			}
			r.s.connID = id

			r.step("create table", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "CREATE TABLE t(id INTEGER PRIMARY KEY, v INTEGER)"},
				200, map[string]any{"statement_type": "ddl"})

			// ─── 提交路径 ──────────────────────────────────
			txnID := beginTxn(r, id)
			if txnID == "" {
				t.Fatalf("%s: no txn id from begin", b.name)
			}

			r.step("insert in tx 1", "POST", "/api/transactions/"+txnID+"/execute",
				map[string]any{"sql": "INSERT INTO t(v) VALUES(1)"},
				200, map[string]any{"statement_type": "insert", "affected_rows": int64(1)})
			r.step("insert in tx 2", "POST", "/api/transactions/"+txnID+"/execute",
				map[string]any{"sql": "INSERT INTO t(v) VALUES(2)"},
				200, map[string]any{"statement_type": "insert", "affected_rows": int64(1)})

			r.step("commit", "POST", "/api/transactions/"+txnID+"/commit", nil, 200,
				map[string]any{"status": "committed"},
				keep("id", "connection_id", "status", "isolation_level"))

			// 提交后数据可见
			r.step("select after commit", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "SELECT v FROM t ORDER BY v"},
				200, map[string]any{"rows": []any{[]any{int64(1)}, []any{int64(2)}}},
				keep("rows"))

			// 重复 commit 返回 404
			r.step("commit again", "POST", "/api/transactions/"+txnID+"/commit", nil, 404,
				map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})

			// ─── 回滚路径 ──────────────────────────────────
			txn2 := beginTxn(r, id)
			r.step("insert rollback path", "POST", "/api/transactions/"+txn2+"/execute",
				map[string]any{"sql": "INSERT INTO t(v) VALUES(99)"},
				200, map[string]any{"statement_type": "insert", "affected_rows": int64(1)})

			// 事务内查询可见未提交数据
			r.step("select uncommitted in tx", "POST", "/api/transactions/"+txn2+"/execute",
				map[string]any{"sql": "SELECT count(*) FROM t"},
				200, map[string]any{"rows": []any{[]any{int64(3)}}}, keep("rows"))

			r.step("rollback", "POST", "/api/transactions/"+txn2+"/rollback", nil, 200,
				map[string]any{"status": "rolled_back"},
				keep("id", "connection_id", "status"))

			r.step("select after rollback", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "SELECT count(*) FROM t"},
				200, map[string]any{"rows": []any{[]any{int64(2)}}}, keep("rows"))

			// 重复 rollback 返回 404
			r.step("rollback again", "POST", "/api/transactions/"+txn2+"/rollback", nil, 404,
				map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})

			// ─── 事务内失败保持 active ─────────────────────
			txn3 := beginTxn(r, id)
			r.step("bad sql in tx", "POST", "/api/transactions/"+txn3+"/execute",
				map[string]any{"sql": "SELECT * FROM missing_table"},
				500, map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"})

			// 事务仍可 finalize（说明 handle 未被丢弃）
			r.step("commit after error", "POST", "/api/transactions/"+txn3+"/commit", nil, 200,
				map[string]any{"status": "committed"}, keep("status"))

			// ─── 未知 txn_id ─────────────────────────────
			r.step("unknown txn execute", "POST", "/api/transactions/00000000-0000-0000-0000-000000000000/execute",
				map[string]any{"sql": "SELECT 1"},
				404, map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})
			r.step("unknown txn commit", "POST", "/api/transactions/00000000-0000-0000-0000-000000000000/commit",
				nil, 404, map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})
			r.step("unknown txn rollback", "POST", "/api/transactions/00000000-0000-0000-0000-000000000000/rollback",
				nil, 404, map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})

			// ─── 断开连接清理活动事务 ─────────────────────
			txn4 := beginTxn(r, id)
			r.step("delete conn", "DELETE", "/api/connections/"+id, nil, 204, nil)
			// 事务被丢弃：finalize 返回 404
			r.step("finalized after disconnect", "POST", "/api/transactions/"+txn4+"/commit", nil, 404,
				map[string]any{"code": "POLYDB_ERR_TRANSACTION_NOT_FOUND"})
		})
	}
}

// beginTxn 通过 POST /api/connections/{id}/transactions 开启事务，返回 txn_id。
// isolation_level 缺省（后端使用默认 read_committed），双端行为一致。
func beginTxn(r *recorder, connID string) string {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/connections/"+connID+"/transactions",
		map[string]any{})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: begin transaction failed: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}
	m, ok := got.(map[string]any)
	if !ok {
		r.t.Fatalf("%s: begin txn response not object: %v", r.s.b.name, got)
	}
	id, ok := m["id"].(string)
	if !ok || id == "" {
		r.t.Fatalf("%s: no id in begin txn response: %v", r.s.b.name, got)
	}
	if status_, _ := m["status"].(string); status_ != "active" {
		r.t.Errorf("%s: begin txn status = %v, want active", r.s.b.name, m["status"])
	}
	return id
}
