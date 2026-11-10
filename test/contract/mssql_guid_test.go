package contract

// PD-18 契约测试：MSSQL `UNIQUEIDENTIFIER` 列的双端读取必须一致，且是
// **标准 UUID 文本**（不是乱码字节串、不是 NULL）。
//
// 缺陷（2026-11-10 修复）：
//   - Rust（旧）：tiberius 的 &[u8]/Vec<u8> 只匹配 ColumnData::Binary，
//     Guid 列整条链落空 → Value::Null（数据丢失）。
//   - Go（旧）：go-mssqldb decodeGuid 给 16 字节 []byte，旧链当普通字节串
//     输出 16 个原始字节的乱码字符串。
//
// 修复后两端统一为标准小写 UUID 文本（Rust: tiberius::Uuid::to_string；
// Go: uuid.UUID.String）。
//
// 本机无 MSSQL 服务时 soft-skip（与 TestContractMSSQL 同纪律）：
// 用 POLYDB_TEST_MSSQL 指定 DSN（默认 mssql://sa@localhost:1433/master）。

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestContractMSSQLGuid(t *testing.T) {
	dsn := os.Getenv("POLYDB_TEST_MSSQL")
	if dsn == "" {
		dsn = "mssql://sa@localhost:1433/master"
	}
	host, port, database, username, password, ok := parseNetDSN(dsn, "localhost", 1433, "master", "sa", "")
	if !ok {
		t.Skipf("invalid POLYDB_TEST_MSSQL DSN: %s", dsn)
	}

	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			mssqlGuidScenario(r, host, port, database, username, password)
		})
	}
}

func mssqlGuidScenario(r *recorder, host string, port int64, database, username, password string) {
	table := fmt.Sprintf("ct_guid_%d", time.Now().UnixNano()%100000000)

	createBody := map[string]any{
		"name": "mssql-guid-contract", "kind": "mssql",
		"host": host, "port": port, "database": database, "username": username,
	}
	if password != "" {
		createBody["password"] = password
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil {
		r.t.Fatalf("%s: create mssql connection request failed: %v", r.s.b.name, err)
	}
	if status == 500 {
		r.t.Logf("%s: mssql unavailable at create (%v) — 需本机 MSSQL 服务，跳过", r.s.b.name, got)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create mssql connection failed: status=%d body=%v", r.s.b.name, status, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test mssql connection failed: status=%d err=%v", r.s.b.name, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	if connected, _ := m["connected"].(bool); !connected {
		r.t.Logf("%s: mssql unavailable — 跳过 GUID 流程", r.s.b.name)
		return
	}

	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE " + table + "(id INT PRIMARY KEY, g UNIQUEIDENTIFIER)"},
		200, map[string]any{"statement_type": "ddl"})

	// 固定 GUID：两端读回的文本必须逐字相同。
	const guidText = "6F9619FF-8B86-D011-B42D-00C04FC964FF"
	r.step("insert guid", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO " + table + "(id, g) VALUES(1, '" + guidText + "')"},
		200, map[string]any{"statement_type": "insert"})

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT id, g FROM " + table + " ORDER BY id"})
	if err != nil || status != 200 {
		r.t.Fatalf("%s: select guid failed: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}
	rows := guidRows(r, got)
	if len(rows) != 1 {
		r.t.Fatalf("%s: rows = %d, want 1", r.s.b.name, len(rows))
	}
	cell := guidCell(r, rows, 0, 1)
	// 不得是 NULL / 乱码。文本形态统一小写标准 UUID（uuid.UUID.String）。
	if cell == "" {
		r.t.Fatalf("%s: GUID 读回为空——两端至少一端丢失数据", r.s.b.name)
	}
	want := "6f9619ff-8b86-d011-b42d-00c04fc964ff"
	if cell != want {
		r.t.Errorf("%s: GUID 读回 = %q, want %q（两端必须同为标准小写 UUID 文本）",
			r.s.b.name, cell, want)
	}

	r.step("drop table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "DROP TABLE " + table}, 200, map[string]any{"statement_type": "ddl"})
}

func guidRows(r *recorder, got any) []any {
	r.t.Helper()
	m, ok := normalize(got).(map[string]any)
	if !ok {
		r.t.Fatalf("%s: response not a map: %T", r.s.b.name, got)
	}
	rows, ok := m["rows"].([]any)
	if !ok {
		r.t.Fatalf("%s: no rows in response: %v", r.s.b.name, m)
	}
	return rows
}

func guidCell(r *recorder, rows []any, row, col int) string {
	r.t.Helper()
	cells, ok := rows[row].([]any)
	if !ok || col >= len(cells) {
		r.t.Fatalf("%s: row %d missing", r.s.b.name, row)
	}
	if s, ok := cells[col].(string); ok {
		return s
	}
	return fmt.Sprintf("%v", cells[col])
}
