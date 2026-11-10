package contract

// PD-17 契约测试：MySQL `BIGINT UNSIGNED` 超 i64::MAX 的值，双端读取必须
// 一致且**不丢数据**。
//
// 缺陷背景（Rust 侧 P1 数据损坏 + 双实现不一致）：
//
//	sqlx 的 i64 兼容检查排除 UNSIGNED flag（sqlx-mysql types/int.rs:
//	int_compatible 的 `&& !ty.flags.contains(ColumnFlags::UNSIGNED)`），
//	try_get::<f64> 又只接受 Float|Double（float.rs:real_compatible）——
//	整条读取链对 BIGINT UNSIGNED 列全部落空，旧实现最终返回
//	Value::String("<LongLong>")（类型名占位符，**数据完全丢失**）。
//
//	Go 侧同场景（go/pkg/dbcore/valueconv.go 的 DriverToValueTyped）：
//	ParseInt 超 i64 失败 ⇒ 末尾 NewStringValue(string(b)) ⇒
//	**精确十进制字符串** "18446744073709551615"。
//
// 故两端在 wire 上都必须给出同一个精确十进制字符串。本用例直接对拍。
//
// 本机无 MySQL 服务时 soft-skip（与既有 TestContractMySQL 同纪律）：
// 用 POLYDB_TEST_MYSQL 指定 DSN（默认 mysql://root@localhost:3306/mysql）。

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestContractMySQLBigIntUnsigned 钉住 BIGINT UNSIGNED 的读取形态。
func TestContractMySQLBigIntUnsigned(t *testing.T) {
	dsn := os.Getenv("POLYDB_TEST_MYSQL")
	if dsn == "" {
		dsn = "mysql://root@localhost:3306/mysql"
	}
	host, port, database, username, password, ok := parseNetDSN(dsn, "localhost", 3306, "mysql", "root", "")
	if !ok {
		t.Skipf("invalid POLYDB_TEST_MYSQL DSN: %s", dsn)
	}

	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			mysqlBigIntUnsignedScenario(r, host, port, database, username, password)
		})
	}
}

func mysqlBigIntUnsignedScenario(r *recorder, host string, port int64, database, username, password string) {
	table := fmt.Sprintf("ct_bigu_%d", time.Now().UnixNano()%100000000)

	createBody := map[string]any{
		"name": "mysql-bigu-contract", "kind": "mysql",
		"host": host, "port": port, "database": database, "username": username,
	}
	if password != "" {
		createBody["password"] = password
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil {
		r.t.Fatalf("%s: create mysql connection request failed: %v", r.s.b.name, err)
	}
	if status == 500 {
		r.t.Logf("%s: mysql unavailable at create (%v) — 需本机 MySQL 服务，跳过", r.s.b.name, got)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create mysql connection failed: status=%d body=%v", r.s.b.name, status, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test mysql connection failed: status=%d err=%v", r.s.b.name, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	if connected, _ := m["connected"].(bool); !connected {
		r.t.Logf("%s: mysql unavailable — 跳过 BIGINT UNSIGNED 流程", r.s.b.name)
		return
	}

	// 建表：BIGINT UNSIGNED 列。
	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE " + table + "(id BIGINT UNSIGNED PRIMARY KEY, v BIGINT UNSIGNED)"},
		200, map[string]any{"statement_type": "ddl"})

	// 插入 u64::MAX（超 i64::MAX）与 i64::MAX+1。
	const u64Max = "18446744073709551615"
	const justOver = "9223372036854775808"
	r.step("insert big unsigned", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO " + table + "(id, v) VALUES(1, " + u64Max + "), (2, " + justOver + ")"},
		200, map[string]any{"statement_type": "insert"})

	// 读回：两端必须给出**同一个精确十进制字符串**。
	//
	// 期望形态经 Go 侧实测钉住（go/pkg/dbcore/valueconv_biguint_test.go）：
	// BIGINT UNSIGNED 超 i64 ⇒ NewStringValue ⇒ JSON 字符串。
	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT id, v FROM " + table + " ORDER BY id"})
	if err != nil || status != 200 {
		r.t.Fatalf("%s: select bigint unsigned failed: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}

	rows := bigUnsignedRows(r, got)
	if len(rows) != 2 {
		r.t.Fatalf("%s: rows = %d, want 2", r.s.b.name, len(rows))
	}

	for i, want := range []string{u64Max, justOver} {
		got := bigUnsignedCell(r, rows, i, 1)
		if got != want {
			r.t.Errorf("%s: row %d BIGINT UNSIGNED = %q, want %q（超 i64 必须精确十进制文本，不得是类型名占位符或浮点）",
				r.s.b.name, i, got, want)
		}
	}

	r.step("drop table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "DROP TABLE " + table}, 200, map[string]any{"statement_type": "ddl"})
}

// bigUnsignedRows 提取 rows 并归一化（数值统一为 int64 便于断言）。
func bigUnsignedRows(r *recorder, got any) []any {
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

// bigUnsignedCell 取某行某列的字符串形态。BIGINT UNSIGNED 超 i64 在两端
// 都是 JSON 字符串，故此处按字符串取；非字符串形态（如数字）也接受并
// 转成十进制文本后比较，便于定位是"形态不同"还是"值不同"。
func bigUnsignedCell(r *recorder, rows []any, row, col int) string {
	r.t.Helper()
	cells, ok := rows[row].([]any)
	if !ok || col >= len(cells) {
		r.t.Fatalf("%s: row %d missing", r.s.b.name, row)
	}
	switch v := cells[col].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		s := fmt.Sprintf("%v", cells[col])
		// 浮点形态说明走了丢精度的路径——原样返回让断言给出可读差异。
		if strings.Contains(s, "e+") || strings.Contains(s, "E+") {
			return s
		}
		return s
	}
}
