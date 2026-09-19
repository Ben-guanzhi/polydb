package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

const (
	contentMsgpack = "application/msgpack"
	contentJSON    = "application/json"
)

type Server struct {
	app *appcore.AppCore
	// token 非空时启用 Bearer 鉴权（behavior.md §12.1）；空串表示不鉴权（本机开发默认）。
	token string
	// httpInFlight 保存正在执行的 HTTP 查询：queryID → context.CancelFunc。
	// 客户端调 POST /api/queries/{query_id}/cancel 时通过此 registry 触发 cancel。
	// WS 走 wsSession.inFlight（独立管理），不走此处。
	httpInFlight map[string]context.CancelFunc
	inFlightMu   sync.Mutex
}

func New(app *appcore.AppCore) *Server {
	return &Server{app: app, httpInFlight: map[string]context.CancelFunc{}}
}

// NewWithToken 以启用 Bearer 鉴权的方式创建 Server（POLYDB_SERVER_TOKEN）。
func NewWithToken(app *appcore.AppCore, token string) *Server {
	s := New(app)
	s.token = token
	return s
}

// registerHTTPQuery 把 queryID 注册到 in-flight registry，返回可取消 ctx 与注销函数。
// queryID 为空时服务端生成一个。
//
// 使用 context.Background() 而非 r.Context()：behavior.md §2.1 明确「客户端断开 HTTP 连接
// 不自动取消查询」，取消必须走 POST /api/queries/{query_id}/cancel 显式触发。r.Context 的
// value（trace 等）在此阶段没有跨包传递需求，故直接 Background 语义最清晰。
func (s *Server) registerHTTPQuery(queryID string) (string, context.Context, context.CancelFunc) {
	if queryID == "" {
		queryID = uuid.NewString()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.inFlightMu.Lock()
	s.httpInFlight[queryID] = cancel
	s.inFlightMu.Unlock()
	return queryID, ctx, cancel
}

func (s *Server) unregisterHTTPQuery(queryID string) {
	s.inFlightMu.Lock()
	delete(s.httpInFlight, queryID)
	s.inFlightMu.Unlock()
}

// cancelHTTPQuery 取消指定 queryID 的 HTTP 查询，返回是否命中。
// 已完成的查询在 registry 中已被清除，同样返回 false。
func (s *Server) cancelHTTPQuery(queryID string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	c, ok := s.httpInFlight[queryID]
	if !ok {
		return false
	}
	c()
	delete(s.httpInFlight, queryID)
	return true
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/connections", s.listConnections)
	mux.HandleFunc("POST /api/connections", s.createConnection)
	mux.HandleFunc("GET /api/connections/{id}", s.getConnection)
	mux.HandleFunc("PUT /api/connections/{id}", s.updateConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", s.deleteConnection)
	mux.HandleFunc("POST /api/connections/{id}/test", s.testConnection)

	mux.HandleFunc("GET /api/connections/{id}/schemas", s.listSchemas)
	mux.HandleFunc("GET /api/connections/{id}/schemas/{schema}/tables", s.listTables)
	mux.HandleFunc("GET /api/connections/{id}/schemas/{schema}/tables/{table}/columns", s.listColumns)
	mux.HandleFunc("GET /api/connections/{id}/schemas/{schema}/tables/{table}/indexes", s.listIndexes)
	mux.HandleFunc("GET /api/connections/{id}/schemas/{schema}/tables/{table}/foreign-keys", s.listForeignKeys)

	mux.HandleFunc("POST /api/connections/{id}/query", s.executeQuery)
	mux.HandleFunc("POST /api/connections/{id}/query/batch", s.executeBatchQuery)
	mux.HandleFunc("POST /api/queries/{query_id}/cancel", s.cancelQuery)

	mux.HandleFunc("GET /api/connections/{id}/schemas/{schema}/tables/{table}/ddl", s.getDDL)

	mux.HandleFunc("POST /api/connections/{id}/transactions", s.beginTransaction)
	mux.HandleFunc("POST /api/transactions/{txnId}/commit", s.commitTransaction)
	mux.HandleFunc("POST /api/transactions/{txnId}/rollback", s.rollbackTransaction)
	mux.HandleFunc("POST /api/transactions/{txnId}/execute", s.executeInTx)

	mux.HandleFunc("POST /api/connections/{id}/kv/select", s.kvSelectDb)
	mux.HandleFunc("POST /api/connections/{id}/kv/scan", s.kvScanKeys)
	mux.HandleFunc("GET /api/connections/{id}/kv/keys/{key}", s.kvGetValue)
	mux.HandleFunc("PUT /api/connections/{id}/kv/keys/{key}", s.kvSetValue)
	mux.HandleFunc("POST /api/connections/{id}/kv/exec", s.kvExecCommand)

	return logMiddleware(panicMiddleware(s.authMiddleware(mux)))
}

// authMiddleware 按 behavior.md §12.1 校验 Bearer token。豁免：/api/health 与 /ws
// （WS 在 hello 阶段校验）。token 为空时直通（未启用鉴权）。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.URL.Path == "/api/health" || r.URL.Path == "/ws" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(s.token)) != 1 {
			writeError(w, &protocol.PolyDBError{Code: protocol.ErrUnauthorized, Message: "missing or invalid bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── system ────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": "0.1.0"})
}

// connID 取路径中的连接 id 并校验为合法 UUID（spec 中 {id} 为 uuid 格式）。
// 非法时写 400 POLYDB_ERR_INVALID_PARAM 并返回 false（与 Rust 侧 parse_conn_id 一致）。
func connID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "invalid connection id: " + id})
		return "", false
	}
	return id, true
}

// ─── connections ───────────────────────────────────────────

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := s.app.ListConnections()
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, conns)
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateConnectionRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	info, err := s.app.CreateConnection(&req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusCreated, info)
}

func (s *Server) getConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	info, err := s.app.GetConnection(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	var req protocol.UpdateConnectionRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	info, err := s.app.UpdateConnection(id, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	deleted, err := s.app.DeleteConnection(id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !deleted {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection not found: " + id})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	status := protocol.ConnectionStatus{ID: id}
	if _, err := s.app.GetConnection(id); err != nil {
		status.Error = err.Error()
		writeMsgpack(w, http.StatusOK, status)
		return
	}
	start := time.Now()
	if err := s.app.Connect(r.Context(), id); err != nil {
		status.Error = err.Error()
		writeMsgpack(w, http.StatusOK, status)
		return
	}
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	status.Connected = true
	status.LatencyMs = latency
	s.app.Disconnect(id)
	writeMsgpack(w, http.StatusOK, status)
}

// ─── metadata ──────────────────────────────────────────────

func (s *Server) listSchemas(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListSchemas(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listTables(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListTables(r.Context(), id, r.PathValue("schema"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listColumns(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListColumns(r.Context(), id, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listIndexes(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListIndexes(r.Context(), id, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listForeignKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListForeignKeys(r.Context(), id, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) getDDL(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	sql, err := s.app.CreateTableSQL(r.Context(), id, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sql": sql})
}

// ─── query ─────────────────────────────────────────────────

func (s *Server) executeQuery(w http.ResponseWriter, r *http.Request) {
	var req protocol.QueryRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	// 与 Rust 一致：路径 id 无论 body 是否覆盖都先校验；body 未带时用路径 id。
	pathID, ok := connID(w, r)
	if !ok {
		return
	}
	id := req.ConnectionID
	if id == "" {
		id = pathID
	}
	// 注册 in-flight query，以便 /api/queries/{query_id}/cancel 能取消。
	// 用 Background() 派生而非 r.Context()，避免 handler 返回后自动取消尚未完成的任务
	// （若客户端已断开但请求未收到，仍应完成；由 r.Context() 的 Cancel 兜底是可选优化）。
	queryID, ctx, cancel := s.registerHTTPQuery(req.QueryID)
	defer func() { cancel(); s.unregisterHTTPQuery(queryID) }()
	// 所有响应路径都回显 X-Query-ID，便于客户端关联响应。
	w.Header().Set("X-Query-ID", queryID)

	// 超时（behavior.md §2.3）：timeout_ms>0 生效，=0/缺省表示无超时，超时不自动重试。
	// 驱动普遍无法中断已在执行的 SQL（如 sqlite 递归 CTE），因此采用
	// 「后台执行 + 截止时间竞争」：到点先返回 POLYDB_ERR_TIMEOUT，被放弃的
	// 执行在后台自然结束（best-effort，与 cancel 的语义一致）。
	type execOutcome struct {
		res *protocol.QueryResult
		err error
	}
	ch := make(chan execOutcome, 1)
	go func() {
		res, err := s.app.Execute(ctx, id, req.SQL, req.Params...)
		ch <- execOutcome{res, err}
	}()
	var timeoutCh <-chan time.Time
	if req.TimeoutMs != nil && *req.TimeoutMs > 0 {
		timer := time.NewTimer(time.Duration(*req.TimeoutMs) * time.Millisecond)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	select {
	case out := <-ch:
		if out.err != nil {
			// 若是 ctx 被取消（来自 /cancel 端点），返回 CANCELLED 错误码
			if ctx.Err() != nil {
				writeError(w, &protocol.PolyDBError{Code: protocol.ErrCancelled, Message: "query cancelled: " + queryID})
				return
			}
			writeError(w, out.err)
			return
		}
		writeMsgpack(w, http.StatusOK, applyMaxRows(out.res, queryRowLimit(&req)))
	case <-timeoutCh:
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrTimeout, Retryable: true, Message: fmt.Sprintf("query timed out: %s", queryID)})
	}
}

// queryRowLimit 计算 effective 行数上限（behavior.md §5）：默认 10000，
// max_rows>0 覆盖，硬上限同为 10000；0 表示用默认。
func queryRowLimit(req *protocol.QueryRequest) int64 {
	limit := int64(10000)
	if req.MaxRows != nil && *req.MaxRows > 0 {
		limit = *req.MaxRows
	}
	if limit > 10000 {
		limit = 10000
	}
	return limit
}

// applyMaxRows 按 behavior.md §5 截断结果行：超过 limit 时截断并置
// truncated=true、total_rows 记录原始行数（不额外 count）。
func applyMaxRows(res *protocol.QueryResult, limit int64) *protocol.QueryResult {
	if res == nil || int64(len(res.Rows)) <= limit {
		return res
	}
	total := int64(len(res.Rows))
	res.Rows = res.Rows[:limit]
	res.Truncated = true
	res.TotalRows = &total
	return res
}

// cancelQuery 取消一个正在执行的 HTTP 查询（best-effort）。
// 命中返回 204；未知/已完成返回 404 POLYDB_ERR_QUERY_NOT_FOUND。
func (s *Server) cancelQuery(w http.ResponseWriter, r *http.Request) {
	queryID := r.PathValue("query_id")
	if _, err := uuid.Parse(queryID); err != nil {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "invalid query id: " + queryID})
		return
	}
	if !s.cancelHTTPQuery(queryID) {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrQueryNotFound, Message: "query not found: " + queryID})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) executeBatchQuery(w http.ResponseWriter, r *http.Request) {
	var req protocol.BatchQueryRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	pathID, ok := connID(w, r)
	if !ok {
		return
	}
	id := req.ConnectionID
	if id == "" {
		id = pathID
	}
	start := time.Now()
	var results []protocol.BatchResultItem
	for _, stmt := range req.Statements {
		res, err := s.app.Execute(r.Context(), id, stmt.SQL, stmt.Params...)
		if err != nil {
			item := protocol.BatchResultItem{}
			if pe, ok := err.(*protocol.PolyDBError); ok {
				item.Err = pe
			} else {
				item.Err = &protocol.PolyDBError{Code: protocol.ErrUnknown, Message: err.Error()}
			}
			results = append(results, item)
			if req.StopOnError {
				break
			}
			continue
		}
		results = append(results, protocol.BatchResultItem{Ok: applyMaxRows(res, queryRowLimit(&stmt))})
	}
	writeMsgpack(w, http.StatusOK, protocol.BatchQueryResult{
		Results:              results,
		TotalExecutionTimeMs: float64(time.Since(start).Microseconds()) / 1000.0,
	})
}

// ─── transactions ─────────────────────────────────────────

func (s *Server) beginTransaction(w http.ResponseWriter, r *http.Request) {
	var req protocol.BeginTransactionRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	// 与 Rust 一致：body 未带 connection_id 时校验并使用路径 id。
	if req.ConnectionID == "" {
		id, ok := connID(w, r)
		if !ok {
			return
		}
		req.ConnectionID = id
	}
	info, err := s.app.BeginTransaction(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusCreated, info)
}

func (s *Server) commitTransaction(w http.ResponseWriter, r *http.Request) {
	info, err := s.app.CommitTransaction(r.PathValue("txnId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) rollbackTransaction(w http.ResponseWriter, r *http.Request) {
	info, err := s.app.RollbackTransaction(r.PathValue("txnId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) executeInTx(w http.ResponseWriter, r *http.Request) {
	var req protocol.QueryRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.app.ExecuteInTx(r.Context(), r.PathValue("txnId"), req.SQL, req.Params...)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, applyMaxRows(res, queryRowLimit(&req)))
}

func (s *Server) kvSelectDb(w http.ResponseWriter, r *http.Request) {
	var req protocol.RedisSelectDbRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	id, ok := connID(w, r)
	if !ok {
		return
	}
	if err := s.app.SelectDB(r.Context(), id, req.Index); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) kvScanKeys(w http.ResponseWriter, r *http.Request) {
	var req protocol.RedisScanRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	id, ok := connID(w, r)
	if !ok {
		return
	}
	page, err := s.app.ScanKeys(r.Context(), id, req.Cursor, req.Pattern, req.Count)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, page)
}

func (s *Server) kvGetValue(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	v, err := s.app.GetValue(r.Context(), id, r.PathValue("key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, v)
}

func (s *Server) kvSetValue(w http.ResponseWriter, r *http.Request) {
	var req protocol.RedisSetRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	id, ok := connID(w, r)
	if !ok {
		return
	}
	if err := s.app.SetValue(r.Context(), id, req.Key, req.Value); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) kvExecCommand(w http.ResponseWriter, r *http.Request) {
	var req protocol.RedisExecCommandRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	id, ok := connID(w, r)
	if !ok {
		return
	}
	reply, err := s.app.ExecCommand(r.Context(), id, req.Args)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, reply)
}

// ─── wire helpers ──────────────────────────────────────────

func decodeMsgpack(r *http.Request, v any) error {
	dec := msgpack.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode msgpack body: %w", err)
	}
	return nil
}

func decodeBody(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct == contentMsgpack || ct == "application/x-msgpack" || ct == "" || !isJSONContentType(ct) {
		return decodeMsgpack(r, v)
	}
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func isJSONContentType(ct string) bool {
	return ct == contentJSON || ct == "application/json; charset=utf-8"
}

func writeMsgpack(w http.ResponseWriter, status int, v any) {
	data, err := msgpack.Marshal(v)
	if err != nil {
		writeError(w, fmt.Errorf("encode msgpack: %w", err))
		return
	}
	w.Header().Set("Content-Type", contentMsgpack)
	w.WriteHeader(status)
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentJSON)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var errToStatus = map[string]int{
	protocol.ErrConnectionNotFound:  http.StatusNotFound,
	protocol.ErrConnectionFailed:    http.StatusBadGateway,
	protocol.ErrNotSupported:        http.StatusNotImplemented,
	protocol.ErrDriverNotAvailable:  http.StatusNotImplemented,
	protocol.ErrInvalidParam:        http.StatusBadRequest,
	protocol.ErrTransactionNotFound: http.StatusNotFound,
	protocol.ErrQueryNotFound:       http.StatusNotFound,
	protocol.ErrCancelled:           http.StatusRequestTimeout,
	protocol.ErrTimeout:             http.StatusRequestTimeout,
	protocol.ErrUnauthorized:        http.StatusUnauthorized,
	protocol.ErrReadOnly:            http.StatusConflict,
}

func writeError(w http.ResponseWriter, err error) {
	pe, ok := err.(*protocol.PolyDBError)
	if !ok {
		if errors.Is(err, storage.ErrNotFound) {
			pe = &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: err.Error()}
		} else {
			pe = &protocol.PolyDBError{Code: protocol.ErrUnknown, Message: err.Error()}
		}
	}
	status := errToStatus[pe.Code]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, pe)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func panicMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeJSON(w, http.StatusInternalServerError,
					&protocol.PolyDBError{Code: protocol.ErrUnknown, Message: fmt.Sprintf("%v", rec)})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
