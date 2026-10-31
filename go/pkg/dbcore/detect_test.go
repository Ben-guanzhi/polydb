package dbcore

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 只读拦截（behavior.md §12.3）依赖 MutatesData 的整句扫描：
// 数据修改 CTE、TRUNCATE/GRANT 等 select/other 形态必须命中，
// 注释与字符串字面量中的同名词不得误判。与 Rust db-core detect.rs 用例逐条镜像。
//
// 这一组用例用**未知方言**（kind 为空）：判定取全部词法剖面的并集，
// 是最保守的一档。方言已知时的行为见 TestMutatesDataDialects。
func TestMutatesData(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", false},
		{"SELECT * FROM users WHERE name = 'delete'", false},
		{"-- DROP TABLE users\nSELECT 1", false},
		{"/* update */ SELECT 1", false},
		{"SELECT 'insert' AS note", false},
		{"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", true},
		{"WITH u AS (UPDATE t SET x = 1 RETURNING x) SELECT * FROM u", true},
		{"WITH i AS (INSERT INTO t VALUES (1) RETURNING id) SELECT * FROM i", true},
		{"TRUNCATE TABLE t", true},
		{"GRANT SELECT ON t TO u", true},
		{"CALL do_write()", true},
		{"EXEC sp_adduser", true},
		{"SELECT 1; DROP TABLE t", true},
		{"SELECT * FROM inserted_events", false},
		{"SELECT updated_at FROM t", false},
		{`SELECT "delete" FROM t`, false},
		{"SELECT [delete] FROM t", false},
		{"select * from t where note = 'it''s not a delete'", false},
		// 首关键字被 §11 判型归入 select/other 的写语句。
		{"SELECT setval('seq1', 100)", true},
		{"REFRESH MATERIALIZED VIEW mv", true},
		{"REFRESH TABLE t", true},
		{"SELECT * INTO newt FROM t", true},
		{"SELECT * FROM t INTO OUTFILE '/tmp/dump.csv'", true},
		{"LOAD DATA INFILE '/tmp/x' INTO TABLE t", true},
		{"DO $$ BEGIN UPDATE t SET x = 1; END $$", true},
		{"OPTIMIZE TABLE t", true},
		{"SELECT nextval('seq1') AS n", false},
		{`SELECT * FROM "intoval" WHERE note = 'into the woods'`, false},
		{"SELECT * FROM setval_log WHERE load_status = 1", false},
		{"SELECT do_count, refresher, optimize_time FROM t", false},
	}
	for _, c := range cases {
		if got := MutatesData(c.sql, ""); got != c.want {
			t.Errorf("MutatesData(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

// TestMutatesDataDialects 钉住「字面量在哪结束」是**方言决定**这件事。
//
// 只读红线的正确性完全押在这条线上：我们认为字面量在哪结束，必须与数据库
// 认为的一致。不一致时，后面的写语句会被当成字面量吞掉——不是理论风险，
// 是已实测过的真实绕过（修复前：`SELECT 1 # it's ok\n; DROP TABLE t` 在
// MySQL 上被判成只读，表被真删）。
func TestMutatesDataDialects(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		kind protocol.DatabaseKind
		want bool
	}{
		// ── '#'：MySQL/MariaDB 是行注释，PostgreSQL 是位异或运算符 ──
		// MySQL：'#' 到行尾是注释 → 撇号被注释吃掉，下一行的 DROP 会执行。
		{"mysql/hash-comment-then-write", "SELECT 1 # it's ok\n; DROP TABLE t", protocol.DatabaseKindMySQL, true},
		// MySQL：同一行里 '#' 之后整体是注释，写语句在注释里 → 不算写。
		{"mysql/hash-comment-swallows-write", "SELECT 1 # ok; DROP TABLE t", protocol.DatabaseKindMySQL, false},
		// PostgreSQL：'#' 是运算符 → 后面的 DROP 是真的语句。
		{"pg/hash-is-xor-not-comment", "SELECT 1 # ok; DROP TABLE t", protocol.DatabaseKindPostgres, true},
		// PostgreSQL：撇号照样开启字面量，'#' 不改变这一点。
		{"pg/hash-then-apostrophe", "SELECT 1 # it's ok\n; DROP TABLE t", protocol.DatabaseKindPostgres, false},

		// ── '--'：MySQL 必须跟空白/控制符才是注释，1--2 是减法 ──
		{"mysql/dash-dash-needs-space", "SELECT 1--2; DELETE FROM t", protocol.DatabaseKindMySQL, true},
		{"mysql/dash-dash-with-space-is-comment", "SELECT 1 -- 2; DELETE FROM t", protocol.DatabaseKindMySQL, false},
		{"pg/dash-dash-always-comment", "SELECT 1--2; DELETE FROM t", protocol.DatabaseKindPostgres, false},

		// ── 反斜杠转义：语义由服务端设置决定，两种解释都扫 ──
		// MySQL 默认把 \' 当转义（整段是一个未闭合的字符串，服务端报语法错、
		// 什么都不执行）；开了 NO_BACKSLASH_ESCAPES 时它是真写语句。
		// 两种解释都扫、取并集 → 判写。
		{"mysql/backslash-escape-ambiguous", `SELECT 'a\'; DROP TABLE t`, protocol.DatabaseKindMySQL, true},
		// PostgreSQL 默认 standard_conforming_strings=on：普通字符串里反斜杠
		// 是普通字符 → 字面量在 \ 后的 ' 处结束，后面是独立语句。
		{"pg/plain-string-no-backslash-escape", `SELECT 'a\'; DROP TABLE t`, protocol.DatabaseKindPostgres, true},
		// 但 E'…' 认反斜杠转义（PG 只有 E'' / U&'' 认）→ 整段是一个字面量。
		{"pg/e-string-honors-backslash", `SELECT E'a\'; DROP TABLE t`, protocol.DatabaseKindPostgres, false},

		// ── PostgreSQL dollar-quote ──
		{"pg/dollar-quote-hides-write", "SELECT $tag$ DROP TABLE t $tag$", protocol.DatabaseKindPostgres, false},
		{"pg/dollar-quote-body-has-apostrophe", "SELECT $tag$ it's a trap $tag$; DROP TABLE t", protocol.DatabaseKindPostgres, true},
		// $1 是占位符不是 dollar-quote：认错会吞掉后面的语句。
		{"pg/dollar-placeholder-not-quote", "SELECT * FROM t WHERE id = $1; DROP TABLE t", protocol.DatabaseKindPostgres, true},
		// 标签首字符必须是字母或下划线：$1$ 不是定界符。
		{"pg/dollar-tag-needs-letter", "SELECT $1$; DROP TABLE t", protocol.DatabaseKindPostgres, true},
		// '$' 前不能紧邻标识符字符：a$b$c 是一个标识符。
		{"pg/dollar-not-after-identifier", "SELECT a$b$c; DROP TABLE t", protocol.DatabaseKindPostgres, true},

		// ── Oracle q'…'：正文里可以含引号 ──
		{"oracle/q-quote-hides-write", "SELECT q'[it's ok]' FROM dual; DROP TABLE t", protocol.DatabaseKindOracle, true},
		// 非 Oracle 方言没有 q-quote：引号按普通规则闭合，后面的内容是别的语句。
		{"pg/q-quote-is-not-special", "SELECT q'[it's ok]' FROM dual; DROP TABLE t", protocol.DatabaseKindPostgres, false},

		// ── MySQL 可执行注释 /*! … */：服务端会执行注释体 ──
		{"mysql/exec-comment-is-executed", "/*!50000 DROP TABLE t */", protocol.DatabaseKindMySQL, true},
		{"mysql/exec-comment-maria", "/*M! DELETE FROM t */", protocol.DatabaseKindMySQL, true},
		{"mysql/exec-comment-no-version", "/*! DROP TABLE t */", protocol.DatabaseKindMySQL, true},
		// 非 MySQL 方言里它是普通注释。
		{"pg/exec-comment-is-plain-comment", "/*!50000 DROP TABLE t */", protocol.DatabaseKindPostgres, false},

		// ── 未知方言：取各剖面并集（fail-closed）──
		{"unknown/dollar-quote-not-recognized", "SELECT $tag$ DROP TABLE t $tag$", "", true},
	}
	for _, c := range cases {
		if got := MutatesData(c.sql, c.kind); got != c.want {
			t.Errorf("%s: MutatesData(%q, %q) = %v, want %v", c.name, c.sql, c.kind, got, c.want)
		}
	}
}

// TestMutatesDataDialectDivergence：同一段 SQL 在不同方言下结论不同。
//
// 这不是「实现有 bug」，而是「字面量在哪结束由方言决定」的必然结果。
// 如果闸门不认方言，这些用例里至少有一个结论是错的——而错的方向是
// 漏拦（把写语句当字面量吞掉 → 真丢数据）。
func TestMutatesDataDialectDivergence(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want map[protocol.DatabaseKind]bool
	}{
		{
			name: "hash-comment",
			sql:  "SELECT 1 # ok; DROP TABLE t",
			want: map[protocol.DatabaseKind]bool{
				protocol.DatabaseKindMySQL:    false, // '#' 注释吃掉整行
				protocol.DatabaseKindPostgres: true,  // '#' 是位异或，语句独立
				protocol.DatabaseKindSQLite:   true,
			},
		},
		{
			name: "dash-dash",
			sql:  "SELECT 1--2; DELETE FROM t",
			want: map[protocol.DatabaseKind]bool{
				protocol.DatabaseKindMySQL:    true,  // '--' 后无空白 → 是减法
				protocol.DatabaseKindPostgres: false, // '--' 无条件是注释
				protocol.DatabaseKindSQLite:   false,
			},
		},
		{
			name: "dollar-quote",
			sql:  "SELECT $tag$ DROP TABLE t $tag$",
			want: map[protocol.DatabaseKind]bool{
				protocol.DatabaseKindPostgres: false, // PG 认 dollar-quote
				protocol.DatabaseKindMySQL:    true,
				protocol.DatabaseKindSQLite:   true,
			},
		},
		{
			name: "q-quote",
			sql:  "SELECT q'[it's ok]' FROM dual; DROP TABLE t",
			want: map[protocol.DatabaseKind]bool{
				protocol.DatabaseKindOracle:   true,  // Oracle 认 q'…'
				protocol.DatabaseKindPostgres: false,
				protocol.DatabaseKindMySQL:    false,
			},
		},
		{
			name: "exec-comment",
			sql:  "/*!50000 DROP TABLE t */",
			want: map[protocol.DatabaseKind]bool{
				protocol.DatabaseKindMySQL:    true,  // MySQL 会执行注释体
				protocol.DatabaseKindPostgres: false,
				protocol.DatabaseKindSQLite:   false,
			},
		},
	}
	for _, c := range cases {
		for kind, want := range c.want {
			if got := MutatesData(c.sql, kind); got != want {
				t.Errorf("%s: MutatesData(%q, %q) = %v, want %v", c.name, c.sql, kind, got, want)
			}
		}
	}
}

// SplitStatements 是只读闸门的切分器：字面量与注释里的 ';' 不是分隔符。
// 切错的后果与词法错一样严重——把写语句藏进只读片段里。
func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		kind protocol.DatabaseKind
		want []string
	}{
		{"single", "SELECT 1", protocol.DatabaseKindSQLite, []string{"SELECT 1"}},
		{"two", "SELECT 1; SELECT 2", protocol.DatabaseKindSQLite, []string{"SELECT 1", "SELECT 2"}},
		{"trailing-semicolon", "SELECT 1;", protocol.DatabaseKindSQLite, []string{"SELECT 1"}},
		{"semicolon-in-literal", "SELECT 'a;b' FROM t", protocol.DatabaseKindSQLite, []string{"SELECT 'a;b' FROM t"}},
		{"semicolon-in-line-comment", "SELECT 1 -- a;b\nFROM t", protocol.DatabaseKindSQLite, []string{"SELECT 1 -- a;b\nFROM t"}},
		{"semicolon-in-block-comment", "SELECT 1 /* a;b */ FROM t", protocol.DatabaseKindSQLite, []string{"SELECT 1 /* a;b */ FROM t"}},
		{"pg-dollar-quote", "SELECT $$a;b$$", protocol.DatabaseKindPostgres, []string{"SELECT $$a;b$$"}},
		{"mysql-hash-comment", "SELECT 1 # a;b\nFROM t", protocol.DatabaseKindMySQL, []string{"SELECT 1 # a;b\nFROM t"}},
		{"pg-hash-is-operator", "SELECT 1 # 2; SELECT 3", protocol.DatabaseKindPostgres, []string{"SELECT 1 # 2", "SELECT 3"}},
		{"mysql-dash-dash-is-operator", "SELECT 1--2; SELECT 3", protocol.DatabaseKindMySQL, []string{"SELECT 1--2", "SELECT 3"}},
		{"pg-dash-dash-is-comment", "SELECT 1--2; SELECT 3", protocol.DatabaseKindPostgres, []string{"SELECT 1--2; SELECT 3"}},
		// 未知方言取并集里最细的一份：切得多只会让闸门更严。
		{"unknown-splits-finer", "SELECT $$a;b$$", "", []string{"SELECT $$a", "b$$"}},
	}
	for _, c := range cases {
		got := SplitStatements(c.sql, c.kind)
		if len(got) != len(c.want) {
			t.Errorf("%s: SplitStatements(%q, %q) = %q, want %q", c.name, c.sql, c.kind, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: SplitStatements(%q, %q) = %q, want %q", c.name, c.sql, c.kind, got, c.want)
				break
			}
		}
	}
}

// LeadingKeyword 返回语句的首个关键字（MCP 只读闸门用它做白名单判定）。
func TestLeadingKeyword(t *testing.T) {
	cases := []struct {
		sql  string
		kind protocol.DatabaseKind
		want string
	}{
		{"SELECT 1", protocol.DatabaseKindSQLite, "SELECT"},
		{"   select 1", protocol.DatabaseKindSQLite, "SELECT"},
		{"-- c\nSELECT 1", protocol.DatabaseKindSQLite, "SELECT"},
		{"/* c */ SELECT 1", protocol.DatabaseKindSQLite, "SELECT"},
		{"# c\nSELECT 1", protocol.DatabaseKindMySQL, "SELECT"},
		{"WITH d AS (SELECT 1) SELECT * FROM d", protocol.DatabaseKindSQLite, "WITH"},
		{"DELETE FROM t", protocol.DatabaseKindSQLite, "DELETE"},
		{"(SELECT 1)", protocol.DatabaseKindSQLite, ""},
		{"'x'", protocol.DatabaseKindSQLite, ""},
		{"", protocol.DatabaseKindSQLite, ""},
	}
	for _, c := range cases {
		if got := LeadingKeyword(c.sql, c.kind); got != c.want {
			t.Errorf("LeadingKeyword(%q, %q) = %q, want %q", c.sql, c.kind, got, c.want)
		}
	}
}

// 判型按首个关键字整词判定（不再是前缀匹配）：WITH 一律按 select 走 Query
// 通道（含 RETURNING 的写语句由 shouldQuery 兜底），此处只验证既有契约未被
// MutatesData 改动。与 Rust db-core detect.rs 用例逐条镜像。
func TestDetectStatementType(t *testing.T) {
	cases := []struct {
		sql  string
		kind protocol.DatabaseKind
		want protocol.StatementType
	}{
		{"SELECT 1", protocol.DatabaseKindSQLite, protocol.StatementTypeSelect},
		{"WITH d AS (DELETE FROM t) SELECT * FROM d", protocol.DatabaseKindSQLite, protocol.StatementTypeSelect},
		{"INSERT INTO t VALUES (1)", protocol.DatabaseKindSQLite, protocol.StatementTypeInsert},
		{"UPDATE t SET x = 1", protocol.DatabaseKindSQLite, protocol.StatementTypeUpdate},
		{"DELETE FROM t", protocol.DatabaseKindSQLite, protocol.StatementTypeDelete},
		{"CREATE TABLE t (id INT)", protocol.DatabaseKindSQLite, protocol.StatementTypeDDL},
		{"ALTER TABLE t ADD COLUMN c INT", protocol.DatabaseKindSQLite, protocol.StatementTypeDDL},
		{"DROP TABLE t", protocol.DatabaseKindSQLite, protocol.StatementTypeDDL},
		// 前缀匹配的老毛病："SELECTED" 会被旧实现判成 Select。
		{"SELECTED FROM t", protocol.DatabaseKindSQLite, protocol.StatementTypeOther},
		{"INSERTED FROM t", protocol.DatabaseKindSQLite, protocol.StatementTypeOther},
		{"SHOW TABLES", protocol.DatabaseKindMySQL, protocol.StatementTypeOther},
		{"EXPLAIN SELECT 1", protocol.DatabaseKindSQLite, protocol.StatementTypeOther},
		{"-- c\nSELECT 1", protocol.DatabaseKindSQLite, protocol.StatementTypeSelect},
	}
	for _, c := range cases {
		if got := DetectStatementType(c.sql, c.kind); got != c.want {
			t.Errorf("DetectStatementType(%q, %q) = %v, want %v", c.sql, c.kind, got, c.want)
		}
	}
}

// StripLeadingComments 剥掉前导注释（-- 行、# 行（MySQL）、/* */ 块）与空白。
func TestStripLeadingComments(t *testing.T) {
	cases := []struct {
		sql  string
		kind protocol.DatabaseKind
		want string
	}{
		{"-- c\nSELECT 1", protocol.DatabaseKindSQLite, "SELECT 1"},
		{"/* c */ SELECT 1", protocol.DatabaseKindSQLite, "SELECT 1"},
		{"/* c */ -- d\nSELECT 1", protocol.DatabaseKindSQLite, "SELECT 1"},
		{"# c\nSELECT 1", protocol.DatabaseKindMySQL, "SELECT 1"},
		{"SELECT 1", protocol.DatabaseKindSQLite, "SELECT 1"},
		{"  SELECT 1", protocol.DatabaseKindSQLite, "SELECT 1"},
	}
	for _, c := range cases {
		if got := StripLeadingComments(c.sql, c.kind); got != c.want {
			t.Errorf("StripLeadingComments(%q, %q) = %q, want %q", c.sql, c.kind, got, c.want)
		}
	}
}

// StripComments 去掉所有注释，保留其余字符（含空白与字面量）；方言感知。
func TestStripComments(t *testing.T) {
	cases := []struct {
		sql  string
		kind protocol.DatabaseKind
		want string
	}{
		{"SELECT 1 -- tail", protocol.DatabaseKindSQLite, "SELECT 1 "},
		{"SELECT/* hi */ 1", protocol.DatabaseKindSQLite, "SELECT 1"},
		// MySQL 的 '#' 是行注释（标准剖面不认，应保留为位异或）。
		{"SELECT 1 # tail", protocol.DatabaseKindMySQL, "SELECT 1 "},
		{"SELECT 1 # tail", protocol.DatabaseKindSQLite, "SELECT 1 # tail"},
		// PG 的 $$…$$ 是字面量：整段保留，里面的 ' 不是引号边界。
		{"SELECT $$a'b$$ AS x", protocol.DatabaseKindPostgres, "SELECT $$a'b$$ AS x"},
		// 字面量里的注释记号不算注释。
		{"SELECT '--x' AS a", protocol.DatabaseKindSQLite, "SELECT '--x' AS a"},
		{"SELECT '/*a*/' AS a", protocol.DatabaseKindSQLite, "SELECT '/*a*/' AS a"},
		// 行尾换行属于空白、不在注释 token 内，原样保留。
		{"SELECT 1 -- tail\n", protocol.DatabaseKindSQLite, "SELECT 1 \n"},
		// 仅注释 → 只剩尾随换行。
		{"-- nothing\n", protocol.DatabaseKindSQLite, "\n"},
	}
	for _, c := range cases {
		if got := StripComments(c.sql, c.kind); got != c.want {
			t.Errorf("StripComments(%q, %q) = %q, want %q", c.sql, c.kind, got, c.want)
		}
	}
}
