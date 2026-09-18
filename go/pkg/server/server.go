package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// httpInFlight 保存正在执行的 HTTP 查询：queryID → context.CancelFunc。
	// 客户端调 POST /api/queries/{query_id}/cancel 时通过此 registry 触发 cancel。
	// WS 走 wsSession.inFlight（独立管理），不走此处。
	httpInFlight map[string]context.CancelFunc
	inFlightMu   sync.Mutex
}

func New(app *appcore.AppCore) *Server {
	return &Server{app: app, httpInFlight: map[string]context.CancelFunc{}}
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

	return logMiddleware(panicMiddleware(mux))
}

// ─── system ────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": "0.1.0"})
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
	info, err := s.app.GetConnection(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateConnectionRequest
	if err := decodeMsgpack(r, &req); err != nil {
		writeError(w, err)
		return
	}
	info, err := s.app.UpdateConnection(r.PathValue("id"), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, info)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	ok, err := s.app.DeleteConnection(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection not found: " + r.PathValue("id")})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	out, err := s.app.ListSchemas(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listTables(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.ListTables(r.Context(), r.PathValue("id"), r.PathValue("schema"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listColumns(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.ListColumns(r.Context(), r.PathValue("id"), r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listIndexes(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.ListIndexes(r.Context(), r.PathValue("id"), r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) listForeignKeys(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.ListForeignKeys(r.Context(), r.PathValue("id"), r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

func (s *Server) getDDL(w http.ResponseWriter, r *http.Request) {
	sql, err := s.app.CreateTableSQL(r.Context(), r.PathValue("id"), r.PathValue("schema"), r.PathValue("table"))
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
	id := req.ConnectionID
	if id == "" {
		id = r.PathValue("id")
	}
	// 注册 in-flight query，以便 /api/queries/{query_id}/cancel 能取消。
	// 用 Background() 派生而非 r.Context()，避免 handler 返回后自动取消尚未完成的任务
	// （若客户端已断开但请求未收到，仍应完成；由 r.Context() 的 Cancel 兜底是可选优化）。
	queryID, ctx, cancel := s.registerHTTPQuery(req.QueryID)
	defer func() { cancel(); s.unregisterHTTPQuery(queryID) }()
	// 所有响应路径都回显 X-Query-ID，便于客户端关联响应。
	w.Header().Set("X-Query-ID", queryID)
	result, err := s.app.Execute(ctx, id, req.SQL, req.Params...)
	if err != nil {
		// 若是 ctx 被取消（来自 /cancel 端点或客户端断连），返回 CANCELLED 错误码
		if ctx.Err() != nil {
			writeError(w, &protocol.PolyDBError{Code: protocol.ErrCancelled, Message: "query cancelled: " + queryID})
			return
		}
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, result)
}

// cancelQuery 取消一个正在执行的 HTTP 查询（best-effort）。
// 命中返回 204；未知/已完成返回 404 POLYDB_ERR_QUERY_NOT_FOUND。
func (s *Server) cancelQuery(w http.ResponseWriter, r *http.Request) {
	queryID := r.PathValue("query_id")
	if queryID == "" {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "missing query_id"})
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
	id := req.ConnectionID
	if id == "" {
		id = r.PathValue("id")
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
		results = append(results, protocol.BatchResultItem{Ok: res})
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
	if req.ConnectionID == "" {
		req.ConnectionID = r.PathValue("id")
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
	writeMsgpack(w, http.StatusOK, res)
}

func (s *Server) kvSelectDb(w http.ResponseWriter, r *http.Request) {
	var req protocol.RedisSelectDbRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.app.SelectDB(r.Context(), r.PathValue("id"), req.Index); err != nil {
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
	page, err := s.app.ScanKeys(r.Context(), r.PathValue("id"), req.Cursor, req.Pattern, req.Count)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, page)
}

func (s *Server) kvGetValue(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.GetValue(r.Context(), r.PathValue("id"), r.PathValue("key"))
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
	if err := s.app.SetValue(r.Context(), r.PathValue("id"), req.Key, req.Value); err != nil {
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
	reply, err := s.app.ExecCommand(r.Context(), r.PathValue("id"), req.Args)
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
	protocol.ErrConnectionNotFound:   http.StatusNotFound,
	protocol.ErrConnectionFailed:    http.StatusBadGateway,
	protocol.ErrNotSupported:        http.StatusNotImplemented,
	protocol.ErrDriverNotAvailable:  http.StatusNotImplemented,
	protocol.ErrInvalidParam:        http.StatusBadRequest,
	protocol.ErrTransactionNotFound: http.StatusNotFound,
	protocol.ErrQueryNotFound:       http.StatusNotFound,
	protocol.ErrCancelled:           http.StatusRequestTimeout,
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
