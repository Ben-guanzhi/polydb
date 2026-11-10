package contract

// PD-20 契约测试：SQL 数据列里的时间值，双端读回必须表示**同一时刻**。
//
// 缺陷（2026-11-10 修复）：Rust 侧各驱动的数据列时间用
// "%Y-%m-%d %H:%M:%S"（空格分隔、无时区），Go 侧走 RFC3339Nano
// ——同一时间列两端 wire 值不同（AGENTS.md §14 双实现一致性红线）。
//
// 修复后两端统一为 RFC3339Nano（权威侧依据见 WORKSPACE §132：元数据
// 时间两端本就是 RFC3339，spec 的 format:date-time 亦同源）。
//
// **比较口径**：两端表示**同一时刻**即可，不要求字面相同。Go 侧用
// RFC3339Nano 会带**本地时区**偏移（如 "2026-11-10T15:08:09+08:00"），
// Rust 侧用 `NaiveDateTime::and_utc().to_rfc3339()`（UTC，
// "…T07:08:09Z"）——两者是同一时刻的两种合法 RFC3339 表达。
// 先前的断言要求以 "Z"/"+00:00" 结尾，把合法的带偏移 RFC3339 判为错。
//
// 时区语义已逐引擎核实（补 Z 不是编造，是陈述两端的既有假设）：
//   - PG：pgx 把无时区的 `timestamp` 按 **UTC 挂钟**解读
//     （pgtype/timestamp.go:120 ParseInLocation(..., time.UTC)）；
//   - MySQL：sqlx 的 NaiveDateTime 注释明写「assumes the connection's
//     `time_zone` is set to `+00:00` (UTC)」（sqlx-mysql-0.8.6
//     src/types/chrono.rs:23）；
//   - MSSQL：go-mssqldb 未设 Timezone 时默认 UTC（timezone.go getTimezone
//     兜底 time.UTC），且 polydb 两端连接都没设 Timezone。
//
// 各引擎服务不可用时 soft-skip（与既有 TestContractMySQL 等同纪律）：
// 用 POLYDB_TEST_PG / _MYSQL / _MSSQL / _ORACLE 指定 DSN。

import (
	"fmt"
	"testing"
	"time"
)

// TestContractTimeColumns 覆盖三个引擎的时间列。
func TestContractTimeColumns(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		netTimeColumns(t, "postgres",
			"postgres://postgres@localhost:5432/postgres?sslmode=disable",
			"CREATE TABLE %s(id INT PRIMARY KEY, ts TIMESTAMP, tstz TIMESTAMPTZ)",
			"INSERT INTO %s VALUES(1, TIMESTAMP '2026-11-10 07:08:09', TIMESTAMPTZ '2026-11-10 07:08:09+00')")
	})
	t.Run("mysql", func(t *testing.T) {
		netTimeColumns(t, "mysql",
			"mysql://root@localhost:3307/mysql",
			"CREATE TABLE %s(id INT PRIMARY KEY, dt DATETIME)",
			"INSERT INTO %s VALUES(1, '2026-11-10 07:08:09')")
	})
	t.Run("mssql", func(t *testing.T) {
		netTimeColumns(t, "mssql",
			"mssql://sa:PolyDb_Test_2026@localhost:1433/master",
			"CREATE TABLE %s(id INT PRIMARY KEY, dt DATETIME2)",
			"INSERT INTO %s VALUES(1, '2026-11-10 07:08:09')")
	})
}

// netTimeColumns 在给定引擎上建表 → 插入固定时刻 → 读回，断言两端读出的
// 时间列都表示该时刻。
func netTimeColumns(t *testing.T, kind, dsn, createFmt, insertFmt string) {
	var host string
	var port int64
	var database, username, password string
	var ok bool
	if kind == "postgres" {
		// PG 用自己的解析器（parseNetDSN 只认 mysql/mssql/oracle）。
		host, port, database, username, ok = parsePGDSN(dsn)
	} else {
		host, port, database, username, password, ok = parseNetDSN(dsn, "localhost", 3306, "mysql", "root", "")
	}
	if !ok {
		t.Skipf("invalid %s DSN: %s", kind, dsn)
	}
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			status, got, err := r.s.c.Do("POST", "/api/connections", map[string]any{
				"name": kind + "-time-contract", "kind": kind,
				"host": host, "port": port, "database": database, "username": username,
				"password": password,
			})
			if err != nil || status != 201 {
				t.Skipf("%s: create %s connection: status=%d err=%v body=%v", b.name, kind, status, err, got)
			}
			r.s.connID, _ = firstConnID(got)
			id := r.s.connID

			// 表名带唯一后缀：两端各自建表，避免互相干扰。
			table := fmt.Sprintf("ct_time_%d", time.Now().UnixNano()%100000000)
			status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": fmt.Sprintf(createFmt, table)})
			if err != nil || status != 200 {
				t.Skipf("%s/%s: 建表失败（status=%d body=%v）— 跳过", kind, b.name, status, got)
			}
			status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": fmt.Sprintf(insertFmt, table)})
			if err != nil || status != 200 {
				t.Skipf("%s/%s: 插入失败（status=%d body=%v）— 跳过", kind, b.name, status, got)
			}
			status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "SELECT * FROM " + table + " ORDER BY id"})
			if err != nil || status != 200 {
				t.Fatalf("%s/%s: select time failed: status=%d err=%v body=%v", kind, b.name, status, err, got)
			}
			rows := timeRows(r, got)
			if len(rows) != 1 {
				r.t.Fatalf("%s/%s: rows = %d, want 1", kind, b.name, len(rows))
			}
			cells, ok := rows[0].([]any)
			if !ok {
				r.t.Fatalf("%s/%s: row is not a list: %T", kind, b.name, rows[0])
			}

			// 第 0 列是 id，其余是时间列。
			//
			// 断言口径：**两端表示同一时刻**即可，不要求字面相同。Go 侧走
			// RFC3339Nano 会带**本地时区**偏移（如
			// "2026-11-10T15:08:09+08:00"），Rust 侧走
			// `NaiveDateTime::and_utc().to_rfc3339()`（UTC，
			// "…T07:08:09Z"）——两者是**同一时刻**的两种合法 RFC3339 表达。
			want := time.Date(2026, 11, 10, 7, 8, 9, 0, time.UTC)
			for i := 1; i < len(cells); i++ {
				s, ok := cells[i].(string)
				if !ok {
					r.t.Errorf("%s/%s: time column %d is %T, want string", kind, b.name, i, cells[i])
					continue
				}
				tv, err := time.Parse(time.RFC3339, s)
				if err != nil {
					r.t.Errorf("%s/%s: time column %d = %q, want RFC3339（parse 失败：%v）",
						kind, b.name, i, s, err)
					continue
				}
				if !tv.Equal(want) {
					r.t.Errorf("%s/%s: time column %d = %q（%s），want 同一时刻 %s",
						kind, b.name, i, s, tv.UTC().Format(time.RFC3339),
						want.Format(time.RFC3339))
				}
			}

			r.step("drop table", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "DROP TABLE " + table}, 200, map[string]any{"statement_type": "ddl"})
		})
	}
}

func timeRows(r *recorder, got any) []any {
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
