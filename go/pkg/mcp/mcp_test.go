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
