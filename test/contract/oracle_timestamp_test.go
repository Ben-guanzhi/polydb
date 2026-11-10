package contract

// PD-19 契约测试：Oracle `TIMESTAMP` 列的双端读取必须**逐字相同**——
// 统一为 oracle crate Timestamp::Display 格式
// （"Y-M-D H:M:S[.prec][ ±HH:MM]"）。
//
// 缺陷（2026-11-10 修复）：Go 侧旧链对 TIMESTAMP* 走 time.Time ⇒
// RFC3339Nano（"2026-01-02T15:04:05.5+08:00"），Rust 侧走 oracle crate
// Display（"2026-01-02 15:04:05.5"）——同一列两端 wire 值不同。
//
// 本机无 Oracle 服务时 soft-skip（与 TestContractOracle 同纪律）：
// 用 POLYDB_TEST_ORACLE 指定 DSN（默认 oracle://system@localhost:1521/ORCL）。

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestContractOracleTimestamp(t *testing.T) {
	dsn := os.Getenv("POLYDB_TEST_ORACLE")
	if dsn == "" {
		dsn = "oracle://system@localhost:1521/ORCL"
	}
	host, port, database, username, password, ok := parseNetDSN(dsn, "localhost", 1521, "ORCL", "system", "")
	if !ok {
		t.Skipf("invalid POLYDB_TEST_ORACLE DSN: %s", dsn)
	}

	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			oracleTimestampScenario(r, host, port, database, username, password)
		})
	}
}

func oracleTimestampScenario(r *recorder, host string, port int64, database, username, password string) {
	table := fmt.Sprintf("ct_ora_ts_%d", time.Now().UnixNano()%100000000)

	createBody := map[string]any{
		"name": "oracle-ts-contract", "kind": "oracle",
		"host": host, "port": port, "database": database, "username": username,
	}
	if password != "" {
		createBody["password"] = password
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil {
		r.t.Fatalf("%s: create oracle connection request failed: %v", r.s.b.name, err)
	}
	if status == 500 {
		r.t.Logf("%s: oracle unavailable at create (%v) — 需本机 Oracle 服务，跳过", r.s.b.name, got)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create oracle connection failed: status=%d body=%v", r.s.b.name, status, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test oracle connection failed: status=%d err=%v", r.s.b.name, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	if connected, _ := m["connected"].(bool); !connected {
		r.t.Logf("%s: oracle unavailable — 跳过 TIMESTAMP 流程", r.s.b.name)
		return
	}

	// TIMESTAMP(6) 列：两端 precision 语义一致（默认 6）。
	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE " + table + "(id NUMBER(10) PRIMARY KEY, ts TIMESTAMP(6))"},
		200, map[string]any{"statement_type": "ddl"})

	// 固定时间值：微秒级（TIMESTAMP(6) 恰好无损）。
	r.step("insert timestamp", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO " + table + " VALUES(1, TIMESTAMP '2026-11-10 07:08:09.123456')"},
		200, map[string]any{"statement_type": "insert"})

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT id, ts FROM " + table + " ORDER BY id"})
	if err != nil || status != 200 {
		r.t.Fatalf("%s: select timestamp failed: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}
	rows := oraTSRows(r, got)
	if len(rows) != 1 {
		r.t.Fatalf("%s: rows = %d, want 1", r.s.b.name, len(rows))
	}
	cell := oraTSCell(r, rows, 0, 1)
	// 期望 Display 格式（precision=6，无时区）。日期/时间部分逐字固定；
	// 若某端还在输出 RFC3339Nano（带 "T" 与 "+08:00"）或时区后缀，
	// 这里会立刻暴露。
	want := "2026-11-10 07:08:09.123456"
	if cell != want {
		r.t.Errorf("%s: TIMESTAMP 读回 = %q, want %q（两端必须同为 oracle crate Display 格式）",
			r.s.b.name, cell, want)
	}

	r.step("drop table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "DROP TABLE " + table}, 200, map[string]any{"statement_type": "ddl"})
}

func oraTSRows(r *recorder, got any) []any {
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

func oraTSCell(r *recorder, rows []any, row, col int) string {
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
