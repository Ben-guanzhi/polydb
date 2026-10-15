package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// maxBodyBytes 是 HTTP 请求体的上限。此前 decodeMsgpack/decodeBody 对 r.Body
// 无限读取，一个恶意客户端可以用超大载荷把服务端拖爆。这里取 16 MiB：请求体只
// 装 SQL 文本与参数，正常量级在 KB 级，16 MiB 留足余量又不至于成为放大器。
// 上传类大文件走 blobs 旁路（§5.2），不走请求体。
const maxBodyBytes = int64(16 << 20)

// allowedOrigin 判定一个 Origin 是否可信。HTTP CORS 与 WS 升级共用这一份，否则
// 两处各写一遍会逐渐漂移。规则：无 Origin（TUI/CLI 等非浏览器客户端）放行；
// 同源（Origin 的 host 与请求 Host 一致，覆盖服务端自托管 Web 前端）放行；
// POLYDB_ALLOWED_ORIGINS（逗号分隔）显式白名单放行（vite dev 等跨源前端）；
// 其余一律拒绝。
func allowedOrigin(origin, host string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("POLYDB_ALLOWED_ORIGINS"), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// corsMiddleware 给可信 Origin 回 CORS 头。此前 HTTP 侧完全没有 CORS：WS 有
// checkWSOrigin，但生产环境把前端放到独立域名后所有跨源请求都会被浏览器拦下。
// 必须放在 panic/bodyLimit/auth 的外层，这样错误响应也带 CORS 头（否则浏览器
// 只报一个笼统的跨域错误），且 OPTIONS 预检能在任何校验之前被应答。
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && allowedOrigin(origin, r.Host) {
			// 回具体 Origin，不用通配符：通配符会把 API 重新开放给所有网页。
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			// Authorization 与带 charset 的 Content-Type 都不是 CORS 安全列表内的
			// 请求头，缺了它们浏览器预检会直接失败。
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMiddleware 在交给业务处理器之前把请求体读入内存并加上限。用
// http.MaxBytesReader 做不到：它只在读取时报错，处理器会把超限当成普通解码失败
// 返回 400，而不是一个明确的 413。这里一次性读完，超限直接 413，并且同时挡住
// 声明 Content-Length 与走 chunked 不带 Content-Length 两种情况。
func bodyLimitMiddleware(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			writeError(w, &protocol.PolyDBError{
				Code:    protocol.ErrPayloadTooLarge,
				Message: fmt.Sprintf("request body too large: %d bytes exceeds %d", r.ContentLength, limit),
			})
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			writeError(w, &protocol.PolyDBError{
				Code:    protocol.ErrPayloadTooLarge,
				Message: "failed to read request body",
			})
			return
		}
		if int64(len(data)) > limit {
			writeError(w, &protocol.PolyDBError{
				Code:    protocol.ErrPayloadTooLarge,
				Message: fmt.Sprintf("request body too large: more than %d bytes", limit),
			})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		next.ServeHTTP(w, r)
	})
}
