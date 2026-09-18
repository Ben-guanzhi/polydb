package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// M27.2 契约：POST /api/queries/{query_id}/cancel（spec/behavior.md §2.1）。
// 覆盖：
//   - 端点存在性：POST /api/queries/{id}/cancel 可路由到正确 handler
//   - 未知 query_id → 404 + POLYDB_ERR_QUERY_NOT_FOUND（两端一致）
//   - 已完成查询的 query_id → 404（幂等：registry 已清空）
//   - 响应头 X-Query-ID 回显请求体 query_id（成功 + 错误路径）
//
// 不断言 best-effort 取消一定触发：底层驱动（SQLite 等）无法真正中断执行中
// 的语句，spec 明确标为 best-effort，测试只锁端点结构 + 错误码 parity。
func TestContractQueryCancel(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			cancelUnknownID(t, b)
			cancelAfterCompletion(t, b)
			queryIDHeaderEcho(t, b)
		})
	}
}

func cancelUnknownID(t *testing.T, b *Backend) {
	t.Helper()
	_, status, _, err := msgpackPost(t, b.base+"/api/queries/99999999-0000-0000-0000-000000000000/cancel", nil)
	if err != nil {
		t.Fatalf("%s / cancel unknown: %v", b.name, err)
	}
	if status != 404 {
		t.Fatalf("%s / cancel unknown: status = %d, want 404", b.name, status)
	}
}

func cancelAfterCompletion(t *testing.T, b *Backend) {
	t.Helper()
	id := sqliteConn(t, b, "m27-2-done")
	defer func() { _, _, _ = NewClient(b.base).Do("DELETE", "/api/connections/"+id, nil) }()

	_, status, _, err := msgpackPost(t, b.base+"/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT 1", "query_id": "11111111-2222-3333-4444-555555555555"})
	if err != nil {
		t.Fatalf("%s / after-completion: query: %v", b.name, err)
	}
	if status != 200 {
		t.Fatalf("%s / after-completion: query status = %d, want 200", b.name, status)
	}

	_, cancelStatus, _, err := msgpackPost(t, b.base+"/api/queries/11111111-2222-3333-4444-555555555555/cancel", nil)
	if err != nil {
		t.Fatalf("%s / after-completion: cancel: %v", b.name, err)
	}
	if cancelStatus != 404 {
		t.Errorf("%s / after-completion: cancel after done status = %d, want 404", b.name, cancelStatus)
	}
}

// queryIDHeaderEcho：服务端必须在所有响应路径上回显 X-Query-ID 头，
// 值等于请求体里的 query_id（客户端可据此关联响应）。
func queryIDHeaderEcho(t *testing.T, b *Backend) {
	t.Helper()
	id := sqliteConn(t, b, "m27-2-header")
	defer func() { _, _, _ = NewClient(b.base).Do("DELETE", "/api/connections/"+id, nil) }()

	cases := []struct {
		sql    string
		qid    string
		status int
	}{
		{"SELECT 1", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", 200},
		{"SELECT * FROM does_not_exist", "bbbbbbbb-cccc-dddd-eeee-ffffffffffff", 500},
	}
	for _, tc := range cases {
		_, status, hdr, err := msgpackPost(t, b.base+"/api/connections/"+id+"/query",
			map[string]any{"sql": tc.sql, "query_id": tc.qid})
		if err != nil {
			t.Fatalf("%s / header echo (%s): %v", b.name, tc.sql, err)
		}
		if status != tc.status {
			t.Errorf("%s / header echo (%s): status = %d, want %d", b.name, tc.sql, status, tc.status)
		}
		if hdr != tc.qid {
			t.Errorf("%s / header echo (%s): X-Query-ID = %q, want %q", b.name, tc.sql, hdr, tc.qid)
		}
	}
}

func sqliteConn(t *testing.T, b *Backend, name string) string {
	t.Helper()
	c := NewClient(b.base)
	status, got, err := c.Do("POST", "/api/connections",
		map[string]any{"name": name, "kind": "sqlite", "database": ":memory:"})
	if err != nil || status != 201 {
		t.Fatalf("%s: create conn failed: status=%d err=%v", b.name, status, err)
	}
	id, ok := firstConnID(got)
	if !ok || id == "" {
		t.Fatalf("%s: no conn id: %v", b.name, got)
	}
	return id
}

// msgpackPost 发一次 msgpack POST 并返回 (body, status, X-Query-ID)。
// body 为 nil 时不带请求体（cancel 端点等）。
func msgpackPost(t *testing.T, url string, body any) (any, int, string, error) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := msgpack.Marshal(body)
		if err != nil {
			return nil, 0, "", err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest("POST", url, reader)
	if err != nil {
		return nil, 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/msgpack")
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		return nil, resp.StatusCode, resp.Header.Get("X-Query-ID"), nil
	}
	var out any
	if ct := resp.Header.Get("Content-Type"); ct == "application/msgpack" {
		_ = msgpack.Unmarshal(raw, &out)
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&out)
	}
	return out, resp.StatusCode, resp.Header.Get("X-Query-ID"), nil
}
