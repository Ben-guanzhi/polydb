package dbsqlite

import (
	"context"
	"testing"

	// Open 只 sql.Open("sqlite", …)，驱动注册由调用方负责；测试里自行注册。
	_ "modernc.org/sqlite"
)

// 回归：SQLite >=3.38 的 PRAGMA foreign_key_list 为 8 列（新增 match）、
// index_list 为 5 列（origin/partial）。Scan 目标数不匹配会让带 FK/索引的表报错。
func TestListForeignKeysAndIndexesPragmaWidth(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pd := NewProtocolDriver(c)
	defer func() { _ = c.Close() }()
	for _, q := range []string{
		`CREATE TABLE dept(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE emp(id INTEGER PRIMARY KEY, dept_id INTEGER NOT NULL REFERENCES dept(id), name TEXT, salary REAL)`,
		`CREATE INDEX idx_emp_dept ON emp(dept_id)`,
		`CREATE UNIQUE INDEX idx_emp_name ON emp(name)`,
	} {
		if _, err := pd.Execute(ctx, q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	fks, err := pd.ListForeignKeys(ctx, "main", "emp")
	if err != nil {
		t.Fatalf("list_foreign_keys: %v", err)
	}
	if len(fks) != 1 {
		t.Fatalf("want 1 fk, got %d", len(fks))
	}
	if fks[0].ReferencedTable != "dept" || len(fks[0].Columns) != 1 || fks[0].Columns[0] != "dept_id" {
		t.Fatalf("fk mismatch: %+v", fks[0])
	}

	idx, err := pd.ListIndexes(ctx, "main", "emp")
	if err != nil {
		t.Fatalf("list_indexes: %v", err)
	}
	names := map[string]bool{}
	for _, i := range idx {
		names[i.Name] = true
		if len(i.Columns) == 0 || i.Columns[0].Name == "" {
			t.Fatalf("index %s missing columns", i.Name)
		}
	}
	if !names["idx_emp_dept"] || !names["idx_emp_name"] {
		t.Fatalf("want idx_emp_dept+idx_emp_name, got %v", names)
	}
}

// 回归：EXPLAIN / PRAGMA 这类语句类型判定为 other，但确实返回行集。
// 此前 SQLite 把它们送去 Exec，行集被整体丢弃（Web/TUI 的 EXPLAIN 面板恒空），
// 其余四家 Go 驱动一直是按查询处理的，这里对齐。
func TestExplainAndPragmaReturnRowsets(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pd := NewProtocolDriver(c)
	defer func() { _ = c.Close() }()
	if _, err := pd.Execute(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pd.Execute(ctx, `CREATE INDEX idx_t ON t(name)`); err != nil {
		t.Fatalf("index: %v", err)
	}

	eq, err := pd.Execute(ctx, `EXPLAIN QUERY PLAN SELECT id FROM t WHERE name='x'`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if len(eq.Columns) != 4 || len(eq.Rows) == 0 {
		t.Fatalf("EXPLAIN 应带回行集: cols=%d rows=%d", len(eq.Columns), len(eq.Rows))
	}

	pg, err := pd.Execute(ctx, `PRAGMA table_info(t)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if len(pg.Rows) != 2 {
		t.Fatalf("PRAGMA table_info 应有 2 行, got %d", len(pg.Rows))
	}

	// DDL / DML 仍走 Exec，affected_rows 语义不变。
	if r, err := pd.Execute(ctx, `INSERT INTO t(name) VALUES('a')`); err != nil || r.AffectedRows != 1 {
		t.Fatalf("INSERT: err=%v affected=%d", err, r.AffectedRows)
	}
}
