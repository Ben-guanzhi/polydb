package contract

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// ─── spec/encoding.md §5.2 旁路引用 + Range 取回的双端对拍 ───────────────
//
// 双端（Go 进程内 / Rust 二进制）各自实现取回路由；本用例把 §5.2 的
// 「ref 形状 + 阈值边界 + Range 矩阵 + 404/416 错误码」在同一 wire 层钉死，
// 与单元级 blob_test.rs / blob_test.go（各自内部对拍）互为交叉验证。

const (
	blobRefLen = 1024*1024 + 1 // > 阈值：结果集走旁路引用
	blobInlLen = 1024 * 1024   // = 阈值：仍内联 base64（§5.1 边界）
)

var blobUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func TestContractBlobRef(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			blobRefScenario(r)
		})
	}
}

func blobRefScenario(r *recorder) {
	c := r.s.c
	status, got, err := c.Do("POST", "/api/connections",
		map[string]any{"name": "sqlite-blobref", "kind": "sqlite", "database": ":memory:"})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create connection: status=%d err=%v", r.s.b.name, status, err)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID
	if id == "" {
		r.t.Fatalf("%s: no id in create response: %v", r.s.b.name, got)
	}

	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE ct_blobref(id INTEGER PRIMARY KEY, b BLOB)"},
		200, map[string]any{"statement_type": "ddl"})

	// zeroblob 造确定性全零数据：1 行超阈值、1 行恰好阈值，一并锁定边界行为。
	r.step("insert boundary blobs", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": fmt.Sprintf(
			"INSERT INTO ct_blobref VALUES(1, zeroblob(%d)), (2, zeroblob(%d))",
			blobRefLen, blobInlLen)},
		200, map[string]any{"statement_type": "insert"})

	status, got, err = c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT id, b FROM ct_blobref ORDER BY id"})
	if err != nil || status != 200 {
		r.t.Fatalf("%s: select blobs: status=%d err=%v", r.s.b.name, status, err)
	}
	rows := blobRows(r, got)
	if len(rows) != 2 {
		r.t.Fatalf("%s: rows = %d, want 2", r.s.b.name, len(rows))
	}

	// >1MB：ref 形状 $polydb_blob:<uuid>，无 base64。
	refCell := blobInner(r, rows, 0, 1)
	if n := blobLen(r, refCell); n != blobRefLen {
		r.t.Errorf("%s: ref len = %d, want %d", r.s.b.name, n, blobRefLen)
	}
	if _, has := refCell["base64"]; has {
		r.t.Errorf("%s: oversized blob kept base64 (spec §5.2 旁路引用)", r.s.b.name)
	}
	ref, _ := refCell["ref"].(string)
	if !strings.HasPrefix(ref, "$polydb_blob:") {
		r.t.Fatalf("%s: ref = %q, want $polydb_blob:<uuid>", r.s.b.name, ref)
	}
	blobID := strings.TrimPrefix(ref, "$polydb_blob:")
	if !blobUUIDRe.MatchString(blobID) {
		r.t.Fatalf("%s: ref uuid = %q", r.s.b.name, blobID)
	}

	// =1MB：仍内联 base64（阈值闭区间），无 ref。
	inlCell := blobInner(r, rows, 1, 1)
	if n := blobLen(r, inlCell); n != blobInlLen {
		r.t.Errorf("%s: inline len = %d, want %d", r.s.b.name, n, blobInlLen)
	}
	if _, has := inlCell["ref"]; has {
		r.t.Errorf("%s: threshold-sized blob took ref path (spec §5.1 边界)", r.s.b.name)
	}
	wantInl := base64.StdEncoding.EncodeToString(make([]byte, blobInlLen))
	if inlCell["base64"] != wantInl {
		r.t.Errorf("%s: inline base64 mismatch (len=%v)", r.s.b.name, inlCell["base64"])
	}

	blobPath := "/api/connections/" + id + "/blobs/" + blobID
	fetch := func(name, rng string, wantStatus int) (http.Header, []byte) {
		r.t.Helper()
		hdr := map[string]string{}
		if rng != "" {
			hdr["Range"] = rng
		}
		st, h, body, err := c.DoRaw("GET", blobPath, hdr)
		if err != nil {
			r.t.Fatalf("%s / %s: request failed: %v", r.s.b.name, name, err)
		}
		if st != wantStatus {
			r.t.Fatalf("%s / %s: status = %d, want %d", r.s.b.name, name, st, wantStatus)
		}
		return h, body
	}
	assertFull := func(name string, h http.Header, body []byte) {
		r.t.Helper()
		if v := h.Get("Accept-Ranges"); v != "bytes" {
			r.t.Errorf("%s / %s: Accept-Ranges = %q, want bytes", r.s.b.name, name, v)
		}
		if ct := h.Get("Content-Type"); !strings.Contains(ct, "octet-stream") {
			r.t.Errorf("%s / %s: Content-Type = %q, want octet-stream", r.s.b.name, name, ct)
		}
		assertAllZeros(r, name, body, int64(blobRefLen))
	}

	// 无 Range → 200 全量（§5.2 表首行）。
	h, body := fetch("full 200", "", 200)
	assertFull("full 200", h, body)

	// bytes=0-9 → 206 + Content-Range: bytes 0-9/len。
	h, body = fetch("bytes=0-9", "bytes=0-9", 206)
	if v, w := h.Get("Content-Range"), fmt.Sprintf("bytes 0-9/%d", blobRefLen); v != w {
		r.t.Errorf("%s: Content-Range = %q, want %q", r.s.b.name, v, w)
	}
	assertAllZeros(r, "bytes=0-9", body, 10)

	// bytes=-5 → 206 末尾 5 字节。
	h, body = fetch("bytes=-5", "bytes=-5", 206)
	if v, w := h.Get("Content-Range"),
		fmt.Sprintf("bytes %d-%d/%d", blobRefLen-5, blobRefLen-1, blobRefLen); v != w {
		r.t.Errorf("%s: Content-Range = %q, want %q", r.s.b.name, v, w)
	}
	assertAllZeros(r, "bytes=-5", body, 5)

	// 末字节（b 超界钳位的退化写法 a=b=len-1）。
	last := fmt.Sprintf("bytes=%d-%d", blobRefLen-1, blobRefLen-1)
	h, body = fetch("last byte", last, 206)
	if v, w := h.Get("Content-Range"),
		fmt.Sprintf("bytes %d-%d/%d", blobRefLen-1, blobRefLen-1, blobRefLen); v != w {
		r.t.Errorf("%s: Content-Range = %q, want %q", r.s.b.name, v, w)
	}
	assertAllZeros(r, "last byte", body, 1)

	// a ≥ len / bytes=-0 → 416 + Content-Range: bytes */len（体为错误 JSON）。
	for _, rng := range []string{fmt.Sprintf("bytes=%d-", blobRefLen), "bytes=-0"} {
		h, body = fetch("416 "+rng, rng, 416)
		if v, w := h.Get("Content-Range"), fmt.Sprintf("bytes */%d", blobRefLen); v != w {
			r.t.Errorf("%s (%s): Content-Range = %q, want %q", r.s.b.name, rng, v, w)
		}
		if code := errCodeOf(r, h, body); code != "POLYDB_ERR_INVALID_PARAM" {
			r.t.Errorf("%s (%s): 416 code = %q, want POLYDB_ERR_INVALID_PARAM", r.s.b.name, rng, code)
		}
	}

	// 多段 / a>b / 语法错误 → 忽略 Range，按 200 全量（§5.2 表末行）。
	for _, rng := range []string{"bytes=0-1,5-6", "bytes=5-3", "nonsense"} {
		h, body = fetch("ignore "+rng, rng, 200)
		assertFull("ignore "+rng, h, body)
	}

	// 未知引用 → 404 POLYDB_ERR_BLOB_NOT_FOUND（前端据此展示「已过期，请重新执行查询」）。
	st, h, body, err := c.DoRaw("GET",
		"/api/connections/"+id+"/blobs/00000000-0000-0000-0000-000000000000", nil)
	if err != nil {
		r.t.Fatalf("%s / unknown 404: request failed: %v", r.s.b.name, err)
	}
	if st != 404 {
		r.t.Fatalf("%s / unknown 404: status = %d, want 404 (body=%q)", r.s.b.name, st, body)
	}
	if code := errCodeOf(r, h, body); code != "POLYDB_ERR_BLOB_NOT_FOUND" {
		r.t.Errorf("%s / unknown 404: code = %q, want POLYDB_ERR_BLOB_NOT_FOUND", r.s.b.name, code)
	}
}

// blobRows 提取 rows 并归一化（数值统一为 int64 便于断言）。
func blobRows(r *recorder, got any) []any {
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

// blobInner 返回 rows[row][col] 的 {"$blob": {...}} 内层 map（ref 或内联形状）。
func blobInner(r *recorder, rows []any, row, col int) map[string]any {
	r.t.Helper()
	cells, ok := rows[row].([]any)
	if !ok || col >= len(cells) {
		r.t.Fatalf("%s: row %d missing", r.s.b.name, row)
	}
	cell, ok := cells[col].(map[string]any)
	if !ok {
		r.t.Fatalf("%s: cell[%d][%d] not a map: %T", r.s.b.name, row, col, cells[col])
	}
	inner, ok := cell["$blob"].(map[string]any)
	if !ok {
		r.t.Fatalf("%s: cell[%d][%d] not $blob: %v", r.s.b.name, row, col, cell)
	}
	return inner
}

func blobLen(r *recorder, cell map[string]any) int {
	r.t.Helper()
	n, ok := cell["len"].(int64)
	if !ok {
		r.t.Fatalf("%s: blob len = %v (%T)", r.s.b.name, cell["len"], cell["len"])
	}
	return int(n)
}

func assertAllZeros(r *recorder, name string, body []byte, wantLen int64) {
	r.t.Helper()
	if int64(len(body)) != wantLen {
		r.t.Errorf("%s / %s: body len = %d, want %d", r.s.b.name, name, len(body), wantLen)
		return
	}
	for i, b := range body {
		if b != 0 {
			r.t.Errorf("%s / %s: byte[%d] = %#x, want 0 (zeroblob data)", r.s.b.name, name, i, b)
			return
		}
	}
}

// errCodeOf 解码错误响应体（双端此端点均为 JSON；msgpack 仅作通道容错）。
func errCodeOf(r *recorder, h http.Header, body []byte) string {
	r.t.Helper()
	var out map[string]any
	ct := h.Get("Content-Type")
	if strings.Contains(ct, "msgpack") {
		if err := msgpack.Unmarshal(body, &out); err != nil {
			r.t.Fatalf("%s: decode msgpack error body: %v", r.s.b.name, err)
		}
	} else if err := json.Unmarshal(body, &out); err != nil {
		if err2 := msgpack.Unmarshal(body, &out); err2 != nil {
			r.t.Fatalf("%s: decode error body (ct=%q): json=%v msgpack=%v", r.s.b.name, ct, err, err2)
		}
	}
	code, _ := out["code"].(string)
	return code
}
