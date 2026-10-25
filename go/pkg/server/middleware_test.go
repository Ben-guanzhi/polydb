package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ─── bodyLimitMiddleware ────────────────────────────────────

func TestBodyLimitRejectsOversizedRequests(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Fatalf("inner read: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "read %d", n)
	})
	handler := bodyLimitMiddleware(10, inner)

	for _, tc := range []struct {
		name string
		body []byte
		want int
	}{
		{"empty", nil, http.StatusOK},
		{"at limit", bytes.Repeat([]byte("a"), 10), http.StatusOK},
		{"over limit", bytes.Repeat([]byte("a"), 11), http.StatusRequestEntityTooLarge},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/connections/x/query", bytes.NewReader(tc.body))
		req.ContentLength = int64(len(tc.body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// 声明 Content-Length 远超上限却一个字节都不发：必须在读任何数据之前就拒绝。
// 如果只有读到一半才发现超限，攻击者仍然能占满连接与带宽。
func TestBodyLimitRejectsDeclaredSizeBeforeReading(t *testing.T) {
	srv := newTestServer(t)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "POST /api/connections/x/query HTTP/1.1\r\nHost: localhost\r\nContent-Length: 999999999\r\nContent-Type: application/json\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// ─── allowedOrigin / corsMiddleware ──────────────────────────

func TestAllowedOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		host   string
		want   bool
	}{
		{"", "localhost:8080", true},                      // 非浏览器客户端（TUI/CLI）
		{"http://localhost:8080", "localhost:8080", true}, // 同源
		{"http://EVIL.EXAMPLE", "localhost:8080", false},  // 跨域
	} {
		if got := allowedOrigin(tc.origin, tc.host); got != tc.want {
			t.Errorf("allowedOrigin(%q, %q) = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
	t.Setenv("POLYDB_ALLOWED_ORIGINS", "http://vite.dev:5173, http://other:123")
	if !allowedOrigin("http://vite.dev:5173", "localhost:8080") {
		t.Errorf("POLYDB_ALLOWED_ORIGINS entry must be allowed")
	}
	if allowedOrigin("http://not-listed:9999", "localhost:8080") {
		t.Errorf("unlisted origin must be refused")
	}
}

func TestCORSGrantsTrustedOriginsOnly(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := corsMiddleware(inner)

	for _, tc := range []struct {
		origin string
		want   string
	}{
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://evil.example", ""},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
		req.Host = "localhost:8080"
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
			t.Errorf("origin %q: grant = %q, want %q", tc.origin, got, tc.want)
		}
	}
}

// 预检不带 Bearer token，必须在任何鉴权之前被应答，否则浏览器拒绝后续请求。
func TestCORSAnswersPreflight(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight must not reach the handler")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/connections/x/query", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// Authorization 不是 CORS 安全列表内的请求头；漏掉它 Bearer 鉴权在跨源部署下
// 会直接卡死在预检。
func TestCORSGrantsAuthorizationHeader(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://localhost:8080")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	for _, need := range []string{"Authorization", "Content-Type"} {
		found := false
		for _, item := range strings.Split(rec.Header().Get("Access-Control-Allow-Headers"), ",") {
			if strings.TrimSpace(item) == need {
				found = true
			}
		}
		if !found {
			t.Errorf("Access-Control-Allow-Headers = %q, want it to include %s",
				rec.Header().Get("Access-Control-Allow-Headers"), need)
		}
	}
}

// ─── WebSocket 消息上限 ──────────────────────────────────────

// 服务端 gorilla 缺省不限消息大小；超限消息必须被拒并断开，否则一个大消息就能
// 吃光内存。
func TestWSRejectsOversizedMessages(t *testing.T) {
	srv := newTestServer(t)
	w := wsDial(t, srv)
	defer w.close()

	payload := bytes.Repeat([]byte{1}, int(maxWSMessageBytes)+1)
	if err := w.c.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_ = w.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := w.c.ReadMessage(); err == nil {
		t.Errorf("message over %d bytes must be rejected, got a full read", maxWSMessageBytes)
	}
}
