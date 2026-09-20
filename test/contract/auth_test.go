package contract

import (
	"fmt"
	"testing"
	"time"
)

// M10 鉴权与只读连接契约：Rust / Go 双后端一致性（spec/behavior.md §12）。
//
// 覆盖：
//   - POLYDB_SERVER_TOKEN 启用时 /api/* 强制 Bearer（health 豁免）：无/错 token → 401 UNAUTHORIZED
//   - WS hello 鉴权：无 auth → query_error(UNAUTHORIZED) 后关闭；正确 token → hello_ack
//   - 连接级 read_only：insert/update/delete/ddl 与事务内写语句 → 409 READ_ONLY；select 放行

func TestContractAuthRequired(t *testing.T) {
	const token = "contract-secret-token"
	bs := backendsWithToken(t, token)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			authREST(t, b, token)
			authWS(t, b, token)
		})
	}
}

// authREST 覆盖 REST 鉴权矩阵。
func authREST(t *testing.T, b *Backend, token string) {
	t.Helper()

	// health 免鉴权（探活必须无凭据可用）
	status, _, err := NewClient(b.base).Do("GET", "/api/health", nil)
	if err != nil || status != 200 {
		t.Fatalf("%s: health without token: status=%d err=%v", b.name, status, err)
	}

	// 无 token → 401 UNAUTHORIZED
	status, got, err := NewClient(b.base).Do("GET", "/api/connections", nil)
	if err != nil {
		t.Fatalf("%s: list without token: %v", b.name, err)
	}
	if status != 401 {
		t.Fatalf("%s: list without token: status = %d, want 401 (body=%v)", b.name, status, got)
	}
	assertErrCode(t, b, got, "POLYDB_ERR_UNAUTHORIZED")

	// 错 token → 401 UNAUTHORIZED
	status, got, err = NewClient(b.base).WithToken("wrong-token").Do("GET", "/api/connections", nil)
	if err != nil {
		t.Fatalf("%s: list with wrong token: %v", b.name, err)
	}
	if status != 401 {
		t.Fatalf("%s: list with wrong token: status = %d, want 401", b.name, status)
	}
	assertErrCode(t, b, got, "POLYDB_ERR_UNAUTHORIZED")

	// 正确 token → 200
	status, got, err = NewClient(b.base).WithToken(token).Do("GET", "/api/connections", nil)
	if err != nil || status != 200 {
		t.Fatalf("%s: list with token: status=%d err=%v body=%v", b.name, status, err, got)
	}
}

// authWS 覆盖 hello 阶段鉴权（behavior.md §12.2）。
func authWS(t *testing.T, b *Backend, token string) {
	t.Helper()

	// 无 auth → query_error(UNAUTHORIZED)，随后服务端关闭连接
	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{
		"type":          "hello",
		"connection_id": "00000000-0000-0000-0000-000000000000",
	})
	typ, got := w.recv(5 * time.Second)
	if typ != "query_error" {
		t.Fatalf("%s: hello without auth: msg type = %s, want query_error (%v)", b.name, typ, got)
	}
	errObj, _ := got["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "POLYDB_ERR_UNAUTHORIZED" {
		t.Fatalf("%s: hello without auth: error code = %v, want POLYDB_ERR_UNAUTHORIZED", b.name, errObj["code"])
	}
	// 服务端应在错误消息后关闭连接：再次读取必须失败
	w.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := w.c.ReadMessage(); err == nil {
		t.Fatalf("%s: hello without auth: server did not close connection", b.name)
	}

	// 正确 token + 已存在的连接 → hello_ack（先经带 token 的 REST 建连接）
	authed := NewClient(b.base).WithToken(token)
	status, created, err := authed.Do("POST", "/api/connections",
		map[string]any{"name": "ws-auth-" + fmt.Sprintf("%d", time.Now().UnixNano()),
			"kind": "sqlite", "database": ":memory:"})
	if err != nil || status != 201 {
		t.Fatalf("%s: create conn for ws auth: status=%d err=%v", b.name, status, err)
	}
	connID, ok := firstConnID(created)
	if !ok || connID == "" {
		t.Fatalf("%s: no conn id: %v", b.name, created)
	}

	w2 := wsDial(t, b.base)
	defer w2.Close()
	w2.send(map[string]any{
		"type":          "hello",
		"connection_id": connID,
		"auth":          map[string]any{"token": token},
	})
	typ, got = w2.recv(5 * time.Second)
	if typ != "hello_ack" {
		t.Fatalf("%s: hello with auth: msg type = %s, want hello_ack (%v)", b.name, typ, got)
	}
}

// TestContractReadOnlyConnection 覆盖连接级只读拦截（behavior.md §12.3）。
func TestContractReadOnlyConnection(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)
			r := &recorder{t: t, s: &st{b: b, c: c}}

			// 只读连接
			status, created, err := c.Do("POST", "/api/connections",
				map[string]any{"name": "ro-sqlite-" + fmt.Sprintf("%d", time.Now().UnixNano()),
					"kind": "sqlite", "database": ":memory:", "read_only": true})
			if err != nil || status != 201 {
				t.Fatalf("%s: create ro conn: status=%d err=%v", b.name, status, err)
			}
			id, ok := firstConnID(created)
			if !ok || id == "" {
				t.Fatalf("%s: no conn id: %v", b.name, created)
			}
			r.s.connID = id

			// ConnectionInfo 回显 read_only
			r.step("get ro conn", "GET", "/api/connections/"+id, nil, 200,
				map[string]any{"read_only": true}, keep("read_only"))

			// 写语句全部拦截（含前导注释剥离后仍判为写的形态，§11+§12.3）
			for _, tc := range []struct {
				name string
				sql  string
			}{
				{"insert blocked", "INSERT INTO t VALUES (1)"},
				{"update blocked", "UPDATE t SET a = 1"},
				{"delete blocked", "DELETE FROM t"},
				{"ddl blocked", "CREATE TABLE t(a INTEGER)"},
				{"commented write blocked", "-- migrate\nCREATE TABLE t(a INTEGER)"},
			} {
				status, got, err := c.Do("POST", "/api/connections/"+id+"/query", map[string]any{"sql": tc.sql})
				if err != nil {
					t.Fatalf("%s: %s: %v", b.name, tc.name, err)
				}
				if status != 409 {
					t.Fatalf("%s: %s: status = %d, want 409 (body=%v)", b.name, tc.name, status, got)
				}
				assertErrCode(t, b, got, "POLYDB_ERR_READ_ONLY")
			}

			// select 放行
			r.step("select allowed", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "SELECT 1 AS x"},
				200, map[string]any{"statement_type": "select"}, keep("statement_type"))

			// 事务内写语句同样拦截；事务保持 active 可回滚
			txnID := beginTxn(r, id)
			if txnID == "" {
				t.Fatalf("%s: no txn id from begin", b.name)
			}
			status, got, err := c.Do("POST", "/api/transactions/"+txnID+"/execute",
				map[string]any{"sql": "CREATE TABLE tx_t(a INTEGER)"})
			if err != nil {
				t.Fatalf("%s: execute in tx: %v", b.name, err)
			}
			if status != 409 {
				t.Fatalf("%s: execute write in tx on ro conn: status = %d, want 409 (body=%v)", b.name, status, got)
			}
			assertErrCode(t, b, got, "POLYDB_ERR_READ_ONLY")
			r.step("tx still active after blocked write", "POST", "/api/transactions/"+txnID+"/rollback", nil, 200,
				map[string]any{"status": "rolled_back"}, keep("status"))

			// 默认（非只读）连接不受影响
			status, created2, err := c.Do("POST", "/api/connections",
				map[string]any{"name": "rw-sqlite-" + fmt.Sprintf("%d", time.Now().UnixNano()),
					"kind": "sqlite", "database": ":memory:",
					"group": "dev", "color": "#123456"})
			if err != nil || status != 201 {
				t.Fatalf("%s: create rw conn: status=%d err=%v", b.name, status, err)
			}
			id2, ok := firstConnID(created2)
			if !ok || id2 == "" {
				t.Fatalf("%s: no rw conn id: %v", b.name, created2)
			}
			// M15：group/color 随 ConnectionInfo 回显（双端一致）
			r.step("get rw conn echoes group/color", "GET", "/api/connections/"+id2, nil, 200,
				map[string]any{"group": "dev", "color": "#123456"}, keep("group", "color"))
			r.step("rw create table allowed", "POST", "/api/connections/"+id2+"/query",
				map[string]any{"sql": "CREATE TABLE t(a INTEGER)"},
				200, map[string]any{"statement_type": "ddl"}, keep("statement_type"))
		})
	}
}

// assertErrCode 断言错误响应体（JSON）的 code 字段。
func assertErrCode(t *testing.T, b *Backend, got any, want string) {
	t.Helper()
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("%s: error body not an object: %v", b.name, got)
	}
	if code, _ := m["code"].(string); code != want {
		t.Fatalf("%s: error code = %v, want %s (body=%v)", b.name, m["code"], want, got)
	}
}
