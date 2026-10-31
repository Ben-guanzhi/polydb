package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

func setupApp(t *testing.T) *appcore.AppCore {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	// 先注册 close：LIFO 保证 db 在 t.TempDir 删除前关闭（Windows 文件锁）。
	t.Cleanup(func() { _ = db.Close() })
	kr, err := keyring.NewFileKeyring(dir, "mcp-test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	app := appcore.New(db, kr)
	conn, err := app.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "mcp", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create conn: %v", err)
	}
	ctx := context.Background()
	if _, err := app.Execute(ctx, conn.ID, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := app.Execute(ctx, conn.ID, "INSERT INTO t(name) VALUES('a'),('b')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return app
}

// 驱动 MCP server：输入多行 JSON-RPC，逐行解析响应并返回。
func drive(t *testing.T, app *appcore.AppCore, reqs []string) []map[string]any {
	t.Helper()
	in := strings.Join(reqs, "\n") + "\n"
	var out bytes.Buffer
	srv := New(app)
	_ = srv.Serve(context.Background(), strings.NewReader(in), &out)

	var resp []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resp = append(resp, m)
	}
	return resp
}

func TestMCPToolsList(t *testing.T) {
	app := setupApp(t)
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	})
	if len(resp) != 2 {
		t.Fatalf("responses = %d, want 2", len(resp))
	}
	init, _ := json.Marshal(resp[0]["result"])
	if !strings.Contains(string(init), "polydb-mcp") {
		t.Errorf("initialize result missing serverInfo: %s", init)
	}
	list, _ := json.Marshal(resp[1]["result"])
	if !strings.Contains(string(list), "run_readonly_query") || !strings.Contains(string(list), "get_table_schema") {
		t.Errorf("tools/list missing tools: %s", list)
	}
}

func TestMCPReadOnlyQuery(t *testing.T) {
	app := setupApp(t)
	// 找到连接 id
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"SELECT id, name FROM t ORDER BY id"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if isErr {
		t.Fatalf("select marked isError: %s", text)
	}
	if !strings.Contains(text, `"a"`) || !strings.Contains(text, `"b"`) {
		t.Errorf("select result missing rows: %s", text)
	}
}

func TestMCPRejectsWrite(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"DELETE FROM t"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if !isErr {
		t.Fatalf("DELETE should be rejected; text=%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "read") && !strings.Contains(strings.ToLower(text), "readonly") &&
		!strings.Contains(strings.ToLower(text), "只读") && !strings.Contains(text, "拒绝") {
		t.Errorf("rejection message unclear: %s", text)
	}
}

func TestMCPGetTableSchema(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_table_schema","arguments":{"connection_id":"` + id + `","schema":"main","table":"t"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if isErr {
		t.Fatalf("get_table_schema error: %s", text)
	}
	for _, want := range []string{`"columns"`, `"indexes"`, `"foreign_keys"`, `"ddl"`, `CREATE TABLE`} {
		if !strings.Contains(text, want) {
			t.Errorf("schema missing %q: %s", want, text)
		}
	}
}

// tableExists 报告表是否还在（用来断言「写语句没被执行」）。
func tableExists(t *testing.T, app *appcore.AppCore, id, table string) bool {
	t.Helper()
	_, err := app.Execute(context.Background(), id, "SELECT count(*) FROM "+table)
	return err == nil
}

// TestMCPRejectsMultiStatementBypass 钉住已实测的多语句绕过。
//
// 旧闸门是「前缀白名单」（HasPrefix "SELECT"），`SELECT 1; DROP TABLE t`
// 整串前缀匹配直接放行；SQLite 驱动（modernc.org/sqlite）支持多语句执行，
// 实测表被真删（之后 SELECT 报 no such table: t）。
func TestMCPRejectsMultiStatementBypass(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"SELECT 1; DROP TABLE t"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if !isErr {
		t.Fatalf("multi-statement write should be rejected; text=%s", text)
	}
	if !tableExists(t, app, id, "t") {
		t.Fatalf("table t was dropped: read-only red line breached")
	}
}

// TestMCPRejectsDataModifyingCTE 钉住数据修改 CTE 的绕过。
//
// `WITH d AS (DELETE …) SELECT * FROM d` 的首关键字是 WITH，旧前缀白名单
// 直接放行；PostgreSQL 上它真的删数据（SQLite 语法上不支持，但闸门不应
// 依赖「目标库恰好不支持」）。
func TestMCPRejectsDataModifyingCTE(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if !isErr {
		t.Fatalf("data-modifying CTE should be rejected; text=%s", text)
	}
	if !tableExists(t, app, id, "t") {
		t.Fatalf("table t was dropped: read-only red line breached")
	}
}

// TestMCPRejectsExplainAnalyzeWrite 钉住 EXPLAIN ANALYZE 的绕过。
//
// PostgreSQL 的 EXPLAIN ANALYZE 会真的执行语句；首关键字 EXPLAIN 在旧
// 白名单里 → 放行。首关键字白名单与整句关键字扫描缺一不可。
func TestMCPRejectsExplainAnalyzeWrite(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"EXPLAIN ANALYZE DELETE FROM t"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if !isErr {
		t.Fatalf("EXPLAIN ANALYZE write should be rejected; text=%s", text)
	}
	if !tableExists(t, app, id, "t") {
		t.Fatalf("table t was dropped: read-only red line breached")
	}
}

// TestMCPRejectsTrailingWrite 钉住「只读前缀 + 尾随写语句」的绕过。
func TestMCPRejectsTrailingWrite(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"SELECT id FROM t; DELETE FROM t"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if !isErr {
		t.Fatalf("trailing write should be rejected; text=%s", text)
	}
	if !tableExists(t, app, id, "t") {
		t.Fatalf("table t was dropped: read-only red line breached")
	}
}

// TestMCPAllowsCommentPrefixedSelect 确认闸门没把合法只读查询一起拦掉。
//
// 旧的前缀白名单会把带前导注释的 SELECT 拒掉（"TrimSpace 后不是 SELECT"）；
// 新的逐语句判定按 token 类别跳过注释。
func TestMCPAllowsCommentPrefixedSelect(t *testing.T) {
	app := setupApp(t)
	conn, _ := app.ListConnections()
	id := conn[0].ID
	resp := drive(t, app, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_readonly_query","arguments":{"connection_id":"` + id + `","sql":"-- 统计行数\nSELECT id, name FROM t ORDER BY id"}}}`,
	})
	text, isErr := parseToolText(t, resp[0])
	if isErr {
		t.Fatalf("comment-prefixed select should be allowed; text=%s", text)
	}
	if !strings.Contains(text, `"a"`) || !strings.Contains(text, `"b"`) {
		t.Errorf("select result missing rows: %s", text)
	}
}

func parseToolText(t *testing.T, m map[string]any) (string, bool) {
	t.Helper()
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	b, _ := json.Marshal(m["result"])
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("parse result: %v (%s)", err, b)
	}
	if len(res.Content) == 0 {
		return "", res.IsError
	}
	return res.Content[0].Text, res.IsError
}
