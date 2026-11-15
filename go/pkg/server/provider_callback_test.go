package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

// newCallbackTestRegistry 造一个带回调面的测试宿主（真实 app-core + 真实存储）。
func newCallbackTestRegistry(t *testing.T) (*callbackRegistry, *appcore.AppCore) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(dir + "/polydb.db")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(dir, "callback-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	app := appcore.New(db, kr)
	reg, err := newCallbackRegistry(app)
	if err != nil {
		t.Fatalf("callback registry: %v", err)
	}
	t.Cleanup(reg.close)
	return reg, app
}

// TestCallbackRegistryIsLoopbackOnly 验证回调面只监听回环（§16.14.2）。
func TestCallbackRegistryIsLoopbackOnly(t *testing.T) {
	reg, _ := newCallbackTestRegistry(t)
	if !isLoopbackAddr(reg.ln.Addr()) {
		t.Fatalf("callback listener must be loopback-only: %v", reg.ln.Addr())
	}
}

// TestCallbackRejectsWrongToken 验证令牌错误时回调被拒（§16.14.2 / §16.14.6）。
func TestCallbackRejectsWrongToken(t *testing.T) {
	reg, _ := newCallbackTestRegistry(t)
	body := bytes.NewBufferString(`{"handle":"x","sql":"SELECT 1"}`)
	req, err := http.NewRequest(http.MethodPost, reg.callbackURL(), body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestCallbackRejectsUnknownHandle 验证未知 handle 被拒。
func TestCallbackRejectsUnknownHandle(t *testing.T) {
	reg, _ := newCallbackTestRegistry(t)
	body := bytes.NewBufferString(`{"handle":"deadbeef","sql":"SELECT 1"}`)
	req, err := http.NewRequest(http.MethodPost, reg.callbackURL(), body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+reg.token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCallbackHandleIsOpaque 验证 handle 不可从 connection_id 推导
// （§16.14.1）：不同连接得到不同 handle，且 handle 里不含 connection_id。
func TestCallbackHandleIsOpaque(t *testing.T) {
	reg, _ := newCallbackTestRegistry(t)
	cid1 := "11111111-1111-1111-1111-111111111111"
	cid2 := "22222222-2222-2222-2222-222222222222"
	h1, err := reg.handleFor(cid1)
	if err != nil {
		t.Fatalf("handleFor: %v", err)
	}
	h2, err := reg.handleFor(cid2)
	if err != nil {
		t.Fatalf("handleFor: %v", err)
	}
	if h1 == h2 {
		t.Fatal("different connections must get different handles")
	}
	if bytes.Contains([]byte(h1), []byte(cid1)) {
		t.Fatalf("handle must not embed connection_id: %q", h1)
	}
	// 同一连接复用同一 handle。
	again, _ := reg.handleFor(cid1)
	if again != h1 {
		t.Fatalf("same connection must reuse handle: %q vs %q", again, h1)
	}
	// handle 能解析回 connection_id（宿主侧）。
	if got, ok := reg.resolve(h1); !ok || got != cid1 {
		t.Fatalf("resolve = %q ok=%v", got, ok)
	}
}

// TestCallbackExecutesRealSQL 验证回调真的执行 SQL 并返回真实结果，
// 且错误码原样映射（§16.14.2）。
func TestCallbackExecutesRealSQL(t *testing.T) {
	reg, app := newCallbackTestRegistry(t)
	// 建一个 SQLite 连接并插入一行。
	info, err := app.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "cb",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	cid := info.ID
	if _, err := app.Execute(context.Background(), cid, "CREATE TABLE t(x INTEGER)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := app.Execute(context.Background(), cid, "INSERT INTO t VALUES(7)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	handle, err := reg.handleFor(cid)
	if err != nil {
		t.Fatalf("handleFor: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"handle": handle, "sql": "SELECT x FROM t"})
	req, _ := http.NewRequest(http.MethodPost, reg.callbackURL(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+reg.token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Result *protocol.QueryResult `json:"result"`
		Error  *protocol.PolyDBError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Result == nil || len(out.Result.Rows) != 1 {
		t.Fatalf("want 1 row, got %+v", out.Result)
	}
}

// TestCallbackEnforcesReadOnly 验证宿主的只读判定对 provider 的 SQL 同样
// 生效（§16.14.2「不开任何后门」）。
func TestCallbackEnforcesReadOnly(t *testing.T) {
	reg, app := newCallbackTestRegistry(t)
	ro := true
	info, err := app.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "ro",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
		ReadOnly: &ro,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	handle, err := reg.handleFor(info.ID)
	if err != nil {
		t.Fatalf("handleFor: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"handle": handle, "sql": "CREATE TABLE t(x INTEGER)"})
	req, _ := http.NewRequest(http.MethodPost, reg.callbackURL(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+reg.token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Result *protocol.QueryResult `json:"result"`
		Error  *protocol.PolyDBError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error == nil || out.Error.Code != protocol.ErrReadOnly {
		t.Fatalf("want READ_ONLY error, got %+v", out.Error)
	}
}
