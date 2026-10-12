package dbcore

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 只读拦截（behavior.md §12.3）依赖 MutatesData 的整句扫描：
// 数据修改 CTE、TRUNCATE/GRANT 等 select/other 形态必须命中，
// 注释与字符串字面量中的同名词不得误判。与 Rust db-core detect.rs 用例逐条镜像。
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
		if got := MutatesData(c.sql); got != c.want {
			t.Errorf("MutatesData(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

// 判型仍按前导关键字：WITH 一律按 select 走 Query 通道（含 RETURNING 的写语句
// 由 shouldQuery 兜底），此处只验证既有契约未被 MutatesData 改动。
func TestDetectStatementTypeWith(t *testing.T) {
	if got := DetectStatementType("WITH d AS (DELETE FROM t) SELECT * FROM d"); got != protocol.StatementTypeSelect {
		t.Errorf("WITH should classify as select, got %v", got)
	}
}
