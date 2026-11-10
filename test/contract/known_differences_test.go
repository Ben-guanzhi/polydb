package contract

// 契约层「已知差异登记」（Item 10，2026-11-10）。
//
// 契约测试用 `normalize.go` 的若干归一化来**放弃一部分区分能力**（把两端
// 必然不同或尚未对齐的可观测行为挡在比较之外）。每一处放弃都应当：
//   1. 有据可查——记下两端当前**实际**产出什么；
//   2. 可被察觉——一旦某一端的行为变了，本用例应当红。
//
// 本文件就是那份登记：它**断言**两端当前的实际形态（不是只打日志），于是
// "放弃区分"这件事变成有边界的——边界外的任何漂移都会被抓住。
//
// 三条已登记的差异：
//
//   A. 列类型（SQLite）——**已对齐**（PD-21，2026-11-10）：Rust 侧改读
//      sqlite3_column_decltype 后两端逐字一致，SQLite 场景的断言已把 type
//      纳入比较（不再走 onlyColumnNames）。
//      PG/MySQL/MSSQL 也已对齐（PD-22，2026-11-10）：起真实服务实测后
//      PG（INT4/TEXT/FLOAT4）、MySQL（INT/VARCHAR/BIGINT）两端逐字一致，
//      MSSQL 经 Rust 侧改大写后一致（go-mssqldb 规范拼写是大写）。
//      **Oracle 也已对齐**（PD-28，2026-11-10）：此前 Go 侧报 go-ora v2 的
//      TNS 协议类型码枚举名（VARCHAR2 列报成 "NCHAR"、BINARY_DOUBLE 报成
//      "IBDouble"），Rust 侧报 Debug 形态（Varchar2(50)/Number(10, 0)），
//      两端对不上。现在两端各自把**自己的原生表示**映射成大写 Oracle SQL
//      类型名（Go: pkg/dboracle/typename.go；Rust: db-oracle 的
//      oracle_type_name），实测逐字一致
//      （VARCHAR2/CHAR/NUMBER/DATE/TIMESTAMP/BINARY_DOUBLE/BINARY_FLOAT/RAW）。
//      字符族按**族名**归并（VARCHAR2/CHAR）——Go 侧经 database/sql 拿不到
//      字符集形态，这是两端可达成一致的最大分辨率，不是哪一侧的缺陷。
//      未识别类型两端都回落 "OTHER"，构造性保证奇类型也一致。
//      故**三个 net 引擎（MySQL/MSSQL/Oracle）的列类型现在都纳入断言**，
//      没有引擎再需要 onlyColumnNames。
//
//   B. 错误 message——两端**语义相同、包装不同**：Go 侧 SQLite 报
//      "SQL logic error: no such table: X (1)"（modernc 的原文 + SQLite
//      错误码），Rust 侧报 "prepare failed: no such table: X"
//      （rusqlite 的 prepare 阶段前缀）。核心信息（"no such table: X"）
//      一致 ⇒ dropErrMessages（只锁 code）是恰当的；但本用例登记两端
//      当前的确切文案，防止其中一端悄悄改掉而无人察觉。
//
//   C. DDL SQL——两端**逐字相同**（实测）：collapseSQL 折叠的只是空白，
//      掩盖的并非语义差异。

import (
	"strings"
	"testing"
)

// ─── A. 列类型（SQLite）：两端必须逐字一致 ──────────────────────────────

func TestKnownDifferenceColumnTypesSQLite(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			// 声明原文即期望值：CREATE TABLE 里写的就是 age INTEGER。
			// 若某一端不再报 sqlite3_column_decltype 的原文（回到硬编码
			// "TEXT"，或改成别的归一化），这里立刻红。
			got := columnTypesOf(r, "id", "name", "age")
			want := []string{"INTEGER", "TEXT", "INTEGER"}
			for i, w := range want {
				if i >= len(got) {
					t.Fatalf("%s: 列数 %d，少于期望 %d", b.name, len(got), len(want))
				}
				if got[i] != w {
					t.Errorf("%s: 第 %d 列 type = %q, want %q（两端应逐字一致——"+
						"不一致说明某一端不再报声明类型）", b.name, i, got[i], w)
				}
			}
		})
	}
}

// columnTypesOf 建表并读回，返回各列的 type 拼写。
func columnTypesOf(r *recorder, cols ...string) []string {
	status, got, err := r.s.c.Do("POST", "/api/connections", map[string]any{
		"name": "known-diff-coltypes", "kind": "sqlite", "database": ":memory:",
	})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create sqlite connection: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER)"},
		200, nil)

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT * FROM users ORDER BY id"})
	if err != nil || status != 200 {
		r.t.Fatalf("%s: select: status=%d err=%v body=%v", r.s.b.name, status, err, got)
	}
	m := normalize(got).(map[string]any)
	rawCols, _ := m["columns"].([]any)
	out := make([]string, 0, len(rawCols))
	for _, c := range rawCols {
		cm, _ := c.(map[string]any)
		s, _ := cm["type"].(string)
		out = append(out, s)
	}
	return out
}

// ─── B. 错误 message：两端语义同、包装不同（登记当前确切文案）────────────

func TestKnownDifferenceErrorMessages(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			status, got, err := r.s.c.Do("POST", "/api/connections", map[string]any{
				"name": "known-diff-errmsg", "kind": "sqlite", "database": ":memory:",
			})
			if err != nil || status != 201 {
				r.t.Fatalf("%s: create sqlite connection: status=%d err=%v body=%v", b.name, status, err, got)
			}
			r.s.connID, _ = firstConnID(got)
			id := r.s.connID

			status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "SELECT * FROM missing_table"})
			if err != nil || status != 500 {
				r.t.Fatalf("%s: query missing table: status=%d err=%v body=%v", b.name, status, err, got)
			}
			m := normalize(got).(map[string]any)
			code, _ := m["code"].(string)
			msg, _ := m["message"].(string)

			if code != "POLYDB_ERR_QUERY_FAILED" {
				r.t.Errorf("%s: 错误码 = %q, want POLYDB_ERR_QUERY_FAILED", b.name, code)
			}
			// 核心语义（数据库说的那句）两端必须一致——这才是"两端都错"
			// 能被检出的部分。
			if !strings.Contains(msg, "no such table: missing_table") {
				r.t.Errorf("%s: message = %q，缺少核心语义 \"no such table: missing_table\""+
					"（两端应对数据库错误给同样的语义）", b.name, msg)
			}
			// 包装前缀按驱动不同：登记当前形态，一端改了要能被察觉。
			wantPrefix := "SQL logic error: " // Go 侧 modernc
			if b.name == "rust" {
				wantPrefix = "prepare failed: " // Rust 侧 rusqlite
			}
			if !strings.HasPrefix(msg, wantPrefix) {
				r.t.Errorf("%s: message = %q，包装前缀应为 %q（登记的已知差异；"+
					"前缀变了说明驱动包装变了，须复核 dropErrMessages 是否还恰当）",
					b.name, msg, wantPrefix)
			}
		})
	}
}

// ─── C. DDL SQL：两端逐字相同（collapseSQL 只掩盖空白）────────────────────

func TestKnownDifferenceDDLSQL(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			status, got, err := r.s.c.Do("POST", "/api/connections", map[string]any{
				"name": "known-diff-ddl", "kind": "sqlite", "database": ":memory:",
			})
			if err != nil || status != 201 {
				r.t.Fatalf("%s: create sqlite connection: status=%d err=%v body=%v", b.name, status, err, got)
			}
			r.s.connID, _ = firstConnID(got)
			id := r.s.connID

			r.step("create table", "POST", "/api/connections/"+id+"/query",
				map[string]any{"sql": "CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER)"},
				200, nil)

			status, got, err = r.s.c.Do("GET", "/api/connections/"+id+"/schemas/main/tables/users/ddl", nil)
			if err != nil || status != 200 {
				r.t.Fatalf("%s: get ddl: status=%d err=%v body=%v", b.name, status, err, got)
			}
			m := normalize(got).(map[string]any)
			sql, _ := m["sql"].(string)
			// 两端实测逐字相同；折叠空白后比较（collapseSQL 掩盖的只是格式）。
			want := "CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER)"
			if strings.Join(strings.Fields(sql), " ") != want {
				r.t.Errorf("%s: DDL = %q, want %q（两端应逐字相同）", b.name, sql, want)
			}
		})
	}
}
