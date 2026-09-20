package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/polydb/polydb/pkg/protocol"
)

// Remote 通过 REST（msgpack 数据面，JSON 错误体）访问 polydb-server。
//
// 与 Local 的语义差异（服务端懒连接导致）：
//   - Connect 只校验连接记录存在并置本地标记；数据库连接由服务端在首个
//     查询/元数据请求时惰性建立；
//   - IsConnected 是本地进程视角的标记，不代表服务端真实状态；
//   - Ping 走 /test 端点（服务端 connect→ping→disconnect 后返回状态）。
//
// 所有业务错误都以 *protocol.PolyDBError 返回（含 code），便于 errors.As 判断。
type Remote struct {
	base      string
	token     string
	hc        *http.Client
	connected map[string]bool
	mu        sync.Mutex
}

// NewRemote 构造指向 polydb-server 的远程客户端（如 http://127.0.0.1:8080）。
func NewRemote(baseURL string) *Remote {
	return &Remote{
		base:      strings.TrimRight(baseURL, "/"),
		hc:        &http.Client{},
		connected: make(map[string]bool),
	}
}

// SetToken 设置 Bearer token（服务端启用 POLYDB_SERVER_TOKEN 时必需）。
func (r *Remote) SetToken(token string) { r.token = token }

// do 发送请求；body 非 nil 时按 msgpack 编码。2xx 且非 204 时按
// Content-Type 解码到 out；非 2xx 时解码 PolyDBError 并返回（*protocol.PolyDBError）。
func (r *Remote) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := msgpack.Marshal(body)
		if err != nil {
			return fmt.Errorf("transport: marshal: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, r.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/msgpack")
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return err
	}
	// 读完立即关闭；close 错误按约定忽略（连接归还由 http.Client 池管理）。
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("transport: read response: %w", err)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 400 {
		var pe protocol.PolyDBError
		if err := json.Unmarshal(raw, &pe); err != nil {
			pe = protocol.PolyDBError{Code: protocol.ErrUnknown, Message: fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
		}
		return &pe
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "msgpack") {
		return msgpack.Unmarshal(raw, out)
	}
	return json.Unmarshal(raw, out)
}

func (r *Remote) markConnected(id string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if connected {
		r.connected[id] = true
	} else {
		delete(r.connected, id)
	}
}

func (r *Remote) IsConnected(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected[id]
}

// ─── 连接 CRUD ─────────────────────────────────────────────

func (r *Remote) ListConnections() ([]protocol.ConnectionInfo, error) {
	var out []protocol.ConnectionInfo
	if err := r.do(http.MethodGet, "/api/connections", nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []protocol.ConnectionInfo{}
	}
	return out, nil
}

func (r *Remote) GetConnection(id string) (protocol.ConnectionInfo, error) {
	var out protocol.ConnectionInfo
	err := r.do(http.MethodGet, "/api/connections/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (r *Remote) CreateConnection(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
	var out protocol.ConnectionInfo
	err := r.do(http.MethodPost, "/api/connections", req, &out)
	return out, err
}

func (r *Remote) UpdateConnection(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error) {
	var out protocol.ConnectionInfo
	err := r.do(http.MethodPut, "/api/connections/"+url.PathEscape(id), req, &out)
	return out, err
}

func (r *Remote) DeleteConnection(id string) (bool, error) {
	err := r.do(http.MethodDelete, "/api/connections/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return false, err
	}
	r.markConnected(id, false)
	return true, nil
}

// ─── 连通性 ────────────────────────────────────────────────

func (r *Remote) Ping(ctx context.Context, id string) error {
	var st protocol.ConnectionStatus
	if err := r.do(http.MethodPost, "/api/connections/"+url.PathEscape(id)+"/test", nil, &st); err != nil {
		return err
	}
	if !st.Connected {
		return &protocol.PolyDBError{Code: protocol.ErrConnectionFailed, Message: st.Error}
	}
	return nil
}

func (r *Remote) Connect(ctx context.Context, id string) error {
	if _, err := r.GetConnection(id); err != nil {
		return err
	}
	r.markConnected(id, true)
	return nil
}

func (r *Remote) Disconnect(id string) {
	// REST 面没有显式 disconnect 端点（服务端懒连接）；只清本地标记。
	r.markConnected(id, false)
}

// ─── 元数据 ────────────────────────────────────────────────

func (r *Remote) ListSchemas(ctx context.Context, id string) ([]protocol.SchemaInfo, error) {
	var out []protocol.SchemaInfo
	if err := r.do(http.MethodGet, "/api/connections/"+url.PathEscape(id)+"/schemas", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListTables(ctx context.Context, id, schema string) ([]protocol.TableInfo, error) {
	var out []protocol.TableInfo
	path := "/api/connections/" + url.PathEscape(id) + "/schemas/" + url.PathEscape(schema) + "/tables"
	if err := r.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListColumns(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error) {
	var out []protocol.ColumnInfo
	path := "/api/connections/" + url.PathEscape(id) + "/schemas/" + url.PathEscape(schema) + "/tables/" + url.PathEscape(table) + "/columns"
	if err := r.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListIndexes(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error) {
	var out []protocol.IndexInfo
	path := "/api/connections/" + url.PathEscape(id) + "/schemas/" + url.PathEscape(schema) + "/tables/" + url.PathEscape(table) + "/indexes"
	if err := r.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListForeignKeys(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	var out []protocol.ForeignKeyInfo
	path := "/api/connections/" + url.PathEscape(id) + "/schemas/" + url.PathEscape(schema) + "/tables/" + url.PathEscape(table) + "/foreign-keys"
	if err := r.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) CreateTableSQL(ctx context.Context, id, schema, table string) (string, error) {
	var out struct {
		SQL string `json:"sql"`
	}
	path := "/api/connections/" + url.PathEscape(id) + "/schemas/" + url.PathEscape(schema) + "/tables/" + url.PathEscape(table) + "/ddl"
	if err := r.do(http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	return out.SQL, nil
}

// ─── 查询 ──────────────────────────────────────────────────

func (r *Remote) Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	req := protocol.QueryRequest{SQL: sql, Params: args}
	var out protocol.QueryResult
	path := "/api/connections/" + url.PathEscape(id) + "/query"
	if err := r.do(http.MethodPost, path, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ─── Redis KV ──────────────────────────────────────────────

func (r *Remote) SelectDB(ctx context.Context, id string, index int) error {
	req := protocol.RedisSelectDbRequest{Index: index}
	return r.do(http.MethodPost, "/api/connections/"+url.PathEscape(id)+"/kv/select", &req, nil)
}

func (r *Remote) ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	req := protocol.RedisScanRequest{Cursor: cursor, Pattern: pattern, Count: count}
	var out protocol.RedisScanPage
	if err := r.do(http.MethodPost, "/api/connections/"+url.PathEscape(id)+"/kv/scan", &req, &out); err != nil {
		return nil, err
	}
	if out.Keys == nil {
		out.Keys = []protocol.RedisKeyInfo{}
	}
	return &out, nil
}

func (r *Remote) GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error) {
	var out protocol.RedisValue
	err := r.do(http.MethodGet, "/api/connections/"+url.PathEscape(id)+"/kv/keys/"+url.PathEscape(key), nil, &out)
	return out, err
}

func (r *Remote) SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error {
	req := protocol.RedisSetRequest{Key: key, Value: value}
	return r.do(http.MethodPut, "/api/connections/"+url.PathEscape(id)+"/kv/keys/"+url.PathEscape(key), &req, nil)
}

func (r *Remote) ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error) {
	req := protocol.RedisExecCommandRequest{Args: args}
	var out protocol.RedisReply
	if err := r.do(http.MethodPost, "/api/connections/"+url.PathEscape(id)+"/kv/exec", &req, &out); err != nil {
		return protocol.RedisReply{}, err
	}
	return out, nil
}
