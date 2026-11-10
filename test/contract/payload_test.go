package contract

import (
	"bytes"
	"encoding/json"
	"testing"
)

// 请求体上限契约（behavior.md §4）：双端一致回 413 + POLYDB_ERR_PAYLOAD_TOO_LARGE
// 包络（code + message）。此前 Rust 用 axum 的 DefaultBodyLimit 回裸 413（无包络），
// 前端拿不到 code/message，与 Go 侧契约漂移。
func TestContractPayloadTooLarge(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			// 16 MiB + 1：Go 与 Rust 的 MAX_BODY_BYTES 都是 16 MiB。
			big := bytes.Repeat([]byte{0}, 16<<20+1)
			status, _, raw, err := NewClient(b.base).DoRawWithBody(
				"POST", "/api/connections/00000000-0000-0000-0000-000000000000/query",
				map[string]string{"Content-Type": "application/msgpack"}, big)
			if err != nil {
				t.Fatalf("%s: request: %v", b.name, err)
			}
			if status != 413 {
				t.Fatalf("%s: status = %d, want 413", b.name, status)
			}
			var env struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("%s: 413 body not a JSON envelope: %v (raw=%q)", b.name, err, raw)
			}
			if env.Code != "POLYDB_ERR_PAYLOAD_TOO_LARGE" {
				t.Fatalf("%s: code = %q, want POLYDB_ERR_PAYLOAD_TOO_LARGE", b.name, env.Code)
			}
			if env.Message == "" {
				t.Fatalf("%s: 413 envelope missing message", b.name)
			}

			// 小请求不受影响（404 连接不存在，关键是被 413 拦下的路径没误伤）。
			status, got, err := NewClient(b.base).Do(
				"POST", "/api/connections/00000000-0000-0000-0000-000000000000/query",
				map[string]any{"sql": "SELECT 1"})
			if err != nil {
				t.Fatalf("%s: small request: %v", b.name, err)
			}
			if status != 404 {
				t.Fatalf("%s: small request: status = %d, want 404 (body=%v)", b.name, status, got)
			}
			assertErrCode(t, b, got, "POLYDB_ERR_CONNECTION_NOT_FOUND")
		})
	}
}
