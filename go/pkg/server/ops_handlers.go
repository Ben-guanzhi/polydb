package server

// 运维四件套的 HTTP 面（§376）：会话 / 服务器变量 / 用户管理 / 造数——
// listSchemas 同款 handler 形态（connID → appcore → writeMsgpack）。
// 用户 DDL 与造数是 POST（带 body）；写操作经前端确认后走 /api/execute
// 既有闸门（内核 UserAdmin 只生成不执行的分工在 appcore 已保留）。

import (
	"encoding/json"
	"net/http"

	"github.com/polydb/polydb/pkg/protocol"
)

// handleProcesses GET /api/connections/{id}/processes
func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListServerProcesses(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

// handleKillProcess POST /api/connections/{id}/processes/{pid}/kill
func (s *Server) handleKillProcess(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	if err := s.app.KillServerProcess(r.Context(), id, r.PathValue("pid")); err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, map[string]string{"killed": r.PathValue("pid")})
}

// handleVariables GET /api/connections/{id}/variables
func (s *Server) handleVariables(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListServerVariables(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

// handleUsers GET /api/connections/{id}/users
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	out, err := s.app.ListServerUsers(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, out)
}

// handleUserDDL POST /api/connections/{id}/users/ddl（生成不执行）
func (s *Server) handleUserDDL(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	var req protocolUserOps
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, err)
		return
	}
	stmt, err := s.app.GenerateUserDDL(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, map[string]string{"sql": stmt})
}

// handleGenData POST /api/connections/{id}/datagen（生成并执行，报行数）
func (s *Server) handleGenData(w http.ResponseWriter, r *http.Request) {
	id, ok := connID(w, r)
	if !ok {
		return
	}
	var req protocolGenData
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.app.GenerateTestData(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeMsgpack(w, http.StatusOK, map[string]int{"rows": n})
}

// protocolUserOps/protocolGenData 是请求 body 的 JSON 形态（msgpack 端点
// 的入参走 JSON body——与既有 POST 端点的 decode 一致）。
type protocolUserOps = protocol.UserOpsRequest

type protocolGenData = protocol.GenDataRequest
