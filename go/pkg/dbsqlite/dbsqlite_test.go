package dbsqlite

import (
	"context"
	"fmt"
	"testing"

	pdb "github.com/polydb/polydb/pkg/dbcore"

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

// 回归：驱动层必须自行封顶物化行数（behavior.md §5），否则大表会把整表读进
// 内存后才由服务端事后裁剪。Rust 侧 DRIVER_MAX_ROWS 一直在做这件事，Go 侧此前
// 五家驱动全部无上限，属双实现漂移（AGENTS.md §7）。
func TestExecuteCapsMaterializedRows(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pd := NewProtocolDriver(c)
	defer func() { _ = c.Close() }()

	// 小结果集不截断。
	sr, err := pd.Execute(ctx, `WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<5) SELECT x FROM seq`)
	if err != nil {
		t.Fatalf("select small: %v", err)
	}
	if len(sr.Rows) != 5 || sr.Truncated || sr.HasMore || sr.TotalRows != nil {
		t.Fatalf("small result: rows=%d truncated=%v has_more=%v total=%v",
			len(sr.Rows), sr.Truncated, sr.HasMore, sr.TotalRows)
	}

	// 超过驱动上限：物化封顶在 pdb.DriverMaxRows，截断并置 has_more。
	over := pdb.DriverMaxRows + 7
	cte := fmt.Sprintf(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<%d) SELECT x FROM seq`, over)
	br, err := pd.Execute(ctx, cte)
	if err != nil {
		t.Fatalf("select big: %v", err)
	}
	if len(br.Rows) != pdb.DriverMaxRows {
		t.Fatalf("rows = %d, want cap %d", len(br.Rows), pdb.DriverMaxRows)
	}
	if !br.Truncated || !br.HasMore {
		t.Fatalf("truncated=%v has_more=%v, want both true", br.Truncated, br.HasMore)
	}
	// 驱动不设 total_rows，交由服务端 applyMaxRows 记录。
	if br.TotalRows != nil {
		t.Fatalf("driver must not set total_rows (server does): %d", *br.TotalRows)
	}
}

// F10 回归：PRAGMA table_info/index_list/index_info/foreign_key_list 的表名
// 参数必须用 SQL 标识符双写（"my""table"），不是 Go 的 %q 字符串字面量转义
// （"my\"table"）。此前用 %q，含 " 的表名拼出语法错误的 SQL，内省错位或报错。
func TestListMetadataWithQuoteInName(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pd := NewProtocolDriver(c)
	defer func() { _ = c.Close() }()

	// 表名含 " —— 必须用 SQL 标识符双写创建。
	tbl := `my"table`
	create := fmt.Sprintf(`CREATE TABLE %s (id INTEGER PRIMARY KEY, name TEXT)`, quoteIdent(tbl))
	if _, err := pd.Execute(ctx, create); err != nil {
		t.Fatalf("create table with quote: %v", err)
	}
	// 索引名也含 "。
	idx := `idx_"col`
	createIdx := fmt.Sprintf(`CREATE INDEX %s ON %s(name)`, quoteIdent(idx), quoteIdent(tbl))
	if _, err := pd.Execute(ctx, createIdx); err != nil {
		t.Fatalf("create index with quote: %v", err)
	}

	// 列内省：若用 %q 拼 PRAGMA，会得到语法错误或空结果。
	cols, err := pd.ListColumns(ctx, "main", tbl)
	if err != nil {
		t.Fatalf("list_columns with quote in name: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("want 2 columns, got %d", len(cols))
	}
	if cols[0].Name != "id" || cols[1].Name != "name" {
		t.Fatalf("column names: %s, %s", cols[0].Name, cols[1].Name)
	}

	// 索引内省
	idxs, err := pd.ListIndexes(ctx, "main", tbl)
	if err != nil {
		t.Fatalf("list_indexes with quote in name: %v", err)
	}
	if len(idxs) != 1 || idxs[0].Name != idx {
		t.Fatalf("want 1 index named %s, got %+v", idx, idxs)
	}
	if len(idxs[0].Columns) != 1 || idxs[0].Columns[0].Name != "name" {
		t.Fatalf("index columns: %+v", idxs[0].Columns)
	}

	// 外键内省（无 FK 的表返回空列表，不应报错）
	fks, err := pd.ListForeignKeys(ctx, "main", tbl)
	if err != nil {
		t.Fatalf("list_foreign_keys with quote in name: %v", err)
	}
	if len(fks) != 0 {
		t.Fatalf("want 0 fks, got %d", len(fks))
	}
}
