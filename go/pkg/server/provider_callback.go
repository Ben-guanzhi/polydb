package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 9：受限回调面（behavior.md §16.14）。与 Rust 侧
// rust/crates/server/src/provider_callback.rs 严格镜像。
//
// 进程端点的 provider **不管理数据库连接、不接触凭据**：它收到 provider/exec
// 后，经一个**仅回环可达**的回调端点把 SQL 交回宿主执行（§16.14.2）。
//
// 安全属性（§16.14.4）：provider 永远看不到 connection_id / 主机 / 端口 /
// 用户名 / 密码，只看到不透明 handle 与 SQL/结果。

// providerCallbackPath 是回调端点的唯一路由。
const providerCallbackPath = "/provider/exec"

// 回调面限制（§16.14.2）。
const (
	providerCallbackMaxBody     = 1 << 20 // 1 MiB
	providerCallbackMaxInflight = 4
	providerCallbackTimeout     = 30 * time.Second
)

// callbackRegistry 是宿主侧的回调面：handle ↔ connection_id 映射 + 令牌 +
// loopback 监听。
type callbackRegistry struct {
	app *appcore.AppCore

	mu sync.RWMutex
	// handles 是 handle → connection_id 的映射（§16.14.1）。
	handles map[string]string
	// byConn 反向映射：connection_id → handle（同一连接复用同一 handle）。
	byConn map[string]string
	// token 是本次进程唯一的回调令牌（每个 provider attach 时下发，§16.14.2）。
	tokenValue string

	inflight chan struct{}
	srv      *http.Server
	ln       net.Listener
}

// newCallbackRegistry 构造回调面并监听 loopback（随机端口）。
func newCallbackRegistry(app *appcore.AppCore) (*callbackRegistry, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen callback: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("callback token: %w", err)
	}
	reg := &callbackRegistry{
		app:        app,
		handles:    map[string]string{},
		byConn:     map[string]string{},
		tokenValue: token,
		inflight:   make(chan struct{}, providerCallbackMaxInflight),
		ln:         ln,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(providerCallbackPath, reg.handleExec)
	reg.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = reg.srv.Serve(ln) }()
	return reg, nil
}

// callbackURL 是回调端点的完整地址（含路径）。
func (r *callbackRegistry) callbackURL() string {
	return "http://" + r.ln.Addr().String() + providerCallbackPath
}

// token 返回本次进程的回调令牌（发给 provider 进程）。
func (r *callbackRegistry) token() string { return r.tokenValue }

// close 停止回调面。
func (r *callbackRegistry) close() {
	if r.srv != nil {
		_ = r.srv.Close()
	}
}

// handleFor 返回某连接的 handle（不存在则惰性分配，§16.14.1）。
//
// handle 与 connection_id 无算术关系，provider 无法猜测别的连接的 handle。
func (r *callbackRegistry) handleFor(connectionID string) (string, error) {
	r.mu.RLock()
	h, ok := r.byConn[connectionID]
	r.mu.RUnlock()
	if ok {
		return h, nil
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate handle: %w", err)
	}
	h = hex.EncodeToString(raw)
	r.mu.Lock()
	// 双检：并发下同一连接可能已分配过，复用先到的那个。
	if prev, exists := r.byConn[connectionID]; exists {
		h = prev
	} else {
		r.byConn[connectionID] = h
		r.handles[h] = connectionID
	}
	r.mu.Unlock()
	return h, nil
}

// resolve 把 handle 解析回 connection_id。
func (r *callbackRegistry) resolve(handle string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.handles[handle]
	return id, ok
}

// handleExec 是回调端点的处理器：校验令牌与 handle，执行 SQL，返回结果。
func (r *callbackRegistry) handleExec(w http.ResponseWriter, req *http.Request) {
	// 令牌校验（§16.14.2）：不匹配一律 401。
	if req.Header.Get("Authorization") != "Bearer "+r.tokenValue {
		writeCallbackError(w, http.StatusUnauthorized,
			&protocol.PolyDBError{Code: protocol.ErrUnauthorized, Message: "invalid callback token"})
		return
	}
	// 并发闸门（§16.14.2）。
	select {
	case r.inflight <- struct{}{}:
		defer func() { <-r.inflight }()
	default:
		writeCallbackError(w, http.StatusTooManyRequests,
			&protocol.PolyDBError{Code: protocol.ErrDriverNotAvailable, Message: "too many concurrent provider callbacks"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, providerCallbackMaxBody+1))
	if err != nil {
		writeCallbackError(w, http.StatusBadRequest,
			&protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "read callback body: " + err.Error()})
		return
	}
	if len(body) > providerCallbackMaxBody {
		writeCallbackError(w, http.StatusRequestEntityTooLarge,
			&protocol.PolyDBError{Code: protocol.ErrPayloadTooLarge, Message: "callback body too large"})
		return
	}
	var req2 struct {
		Handle string           `json:"handle"`
		SQL    string           `json:"sql"`
		Params []protocol.Value `json:"params"`
	}
	if err := json.Unmarshal(body, &req2); err != nil {
		writeCallbackError(w, http.StatusBadRequest,
			&protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "decode callback body: " + err.Error()})
		return
	}
	connectionID, ok := r.resolve(req2.Handle)
	if !ok {
		writeCallbackError(w, http.StatusBadRequest,
			&protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "unknown handle"})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), providerCallbackTimeout)
	defer cancel()
	// 执行走宿主的 app-core：只读判定、限行、事务等既有逻辑全部复用
	// （§16.14.2「不开任何后门」）。
	res, execErr := r.app.Execute(ctx, connectionID, req2.SQL, req2.Params...)
	if execErr != nil {
		writeCallbackError(w, http.StatusOK, toProtocolError(execErr))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": res})
}

// toProtocolError 把执行错误转成 PolyDBError（错误码原样映射，§16.14.2）。
func toProtocolError(err error) *protocol.PolyDBError {
	if pe, ok := err.(*protocol.PolyDBError); ok {
		return pe
	}
	return &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
}

func writeCallbackError(w http.ResponseWriter, status int, pe *protocol.PolyDBError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": pe})
}

// randomToken 生成随机令牌（每进程一个，§16.14.2）。
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// isLoopbackAddr 判定监听地址是否仅回环可达（安全属性自检）。
func isLoopbackAddr(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
