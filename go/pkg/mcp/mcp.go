// Package mcp 把 polydb 的「元数据浏览 + 只读查询」暴露为 MCP（Model Context Protocol）
// 工具，供 Cursor / Claude Desktop 等 MCP 客户端直连。
//
// 传输：stdio，newline-delimited JSON-RPC 2.0（MCP 默认 stdio 帧）。
// 只读红线：唯一能执行 SQL 的工具 run_readonly_query 只放行 SELECT / EXPLAIN
// （经 dbcore.DetectStatementType 判定），写语句一律拒绝——MCP 侧不暴露任何写能力。
// 连接与凭据归属本进程（app-core + keyring），与 polydb-server 相同装配。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/polydb/polydb/pkg/appcore"
)

// Server 把 app-core 能力映射为 MCP 工具（stdio JSON-RPC）。
type Server struct {
	app *appcore.AppCore
}

// New 构造 MCP server。
func New(app *appcore.AppCore) *Server {
	return &Server{app: app}
}

// JSON-RPC 消息。ID 用 *RawMessage 区分「请求（有 id）」与「通知（无 id）」。
type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve 读取 in（每行一条 JSON-RPC），把响应写到 out。ctx 取消即返回。
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sc.Scan() {
			return sc.Err() // EOF 或读错误
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = s.write(out, &rpcMessage{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		// 通知（无 id）：处理后不响应
		if msg.ID == nil {
			continue
		}
		resp := s.handle(ctx, msg)
		_ = s.write(out, resp)
	}
}

func (s *Server) write(w io.Writer, m *rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func (s *Server) handle(ctx context.Context, msg rpcMessage) *rpcMessage {
	raw, rpcErr := s.route(ctx, msg)
	out := &rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	if rpcErr != nil {
		out.Error = rpcErr
		return out
	}
	enc := json.RawMessage(raw)
	out.Result = &enc
	return out
}

// route 返回 result 的原始 JSON 字节（或 JSON-RPC 错误）。
func (s *Server) route(ctx context.Context, msg rpcMessage) ([]byte, *rpcError) {
	switch msg.Method {
	case "initialize":
		return []byte(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"polydb-mcp","version":"1.0.0"}}`), nil
	case "tools/list":
		b, _ := json.Marshal(map[string]any{"tools": s.toolList()})
		return b, nil
	case "tools/call":
		text, isErr := s.callTool(ctx, msg.Params)
		b, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		})
		return b, nil
	case "ping":
		return []byte(`{}`), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + msg.Method}
	}
}
