package transport

// 运维四件套的 transport 面（§377）：Local 直调 appcore；Remote 打 §376
// 注册的 6 个端点。两实现与 Client 接口同步补齐（vet 的接口断言钉住）。

import (
	"context"
	"net/http"
	"net/url"

	"github.com/polydb/polydb/pkg/protocol"
)

// ─── Local ──────────────────────────────────────────────────

func (l *Local) ListServerProcesses(ctx context.Context, id string) ([]protocol.ServerProcess, error) {
	return l.app.ListServerProcesses(ctx, id)
}

func (l *Local) KillServerProcess(ctx context.Context, id, pid string) error {
	return l.app.KillServerProcess(ctx, id, pid)
}

func (l *Local) ListServerVariables(ctx context.Context, id string) ([]protocol.ServerVariable, error) {
	return l.app.ListServerVariables(ctx, id)
}

func (l *Local) ListServerUsers(ctx context.Context, id string) ([]protocol.ServerUser, error) {
	return l.app.ListServerUsers(ctx, id)
}

func (l *Local) GenerateUserDDL(ctx context.Context, id string, req *protocol.UserOpsRequest) (string, error) {
	return l.app.GenerateUserDDL(ctx, id, *req)
}

func (l *Local) GenerateTestData(ctx context.Context, id string, req *protocol.GenDataRequest) (int, error) {
	return l.app.GenerateTestData(ctx, id, *req)
}

// ─── Remote ─────────────────────────────────────────────────

func (r *Remote) ListServerProcesses(ctx context.Context, id string) ([]protocol.ServerProcess, error) {
	var out []protocol.ServerProcess
	path := "/api/connections/" + url.PathEscape(id) + "/processes"
	if err := r.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) KillServerProcess(ctx context.Context, id, pid string) error {
	path := "/api/connections/" + url.PathEscape(id) + "/processes/" + url.PathEscape(pid) + "/kill"
	return r.do(ctx, http.MethodPost, path, nil, nil)
}

func (r *Remote) ListServerVariables(ctx context.Context, id string) ([]protocol.ServerVariable, error) {
	var out []protocol.ServerVariable
	path := "/api/connections/" + url.PathEscape(id) + "/variables"
	if err := r.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListServerUsers(ctx context.Context, id string) ([]protocol.ServerUser, error) {
	var out []protocol.ServerUser
	path := "/api/connections/" + url.PathEscape(id) + "/users"
	if err := r.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) GenerateUserDDL(ctx context.Context, id string, req *protocol.UserOpsRequest) (string, error) {
	var out struct {
		SQL string `json:"sql"`
	}
	path := "/api/connections/" + url.PathEscape(id) + "/users/ddl"
	if err := r.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", err
	}
	return out.SQL, nil
}

func (r *Remote) GenerateTestData(ctx context.Context, id string, req *protocol.GenDataRequest) (int, error) {
	var out struct {
		Rows int `json:"rows"`
	}
	path := "/api/connections/" + url.PathEscape(id) + "/datagen"
	if err := r.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return 0, err
	}
	return out.Rows, nil
}
