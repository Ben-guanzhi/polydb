package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// ToolDef 是 MCP tools/list 的一个工具（含 JSON Schema 入参）。
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func strSchema(props map[string]any, required ...string) any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// toolList 声明暴露的只读工具。
func (s *Server) toolList() []ToolDef {
	connID := map[string]any{"type": "string", "description": "连接 id（来自 list_connections）"}
	schema := map[string]any{"type": "string", "description": "schema/database 名"}
	return []ToolDef{
		{
			Name:        "list_connections",
			Description: "列出所有已保存的数据库连接（id、名称、类型、主机、库名）。",
			InputSchema: strSchema(map[string]any{}),
		},
		{
			Name:        "list_schemas",
			Description: "列出某连接的 schema/database。",
			InputSchema: strSchema(connID, "connection_id"),
		},
		{
			Name:        "list_tables",
			Description: "列出某 schema 下的表。",
			InputSchema: strSchema(map[string]any{"connection_id": connID, "schema": schema}, "connection_id", "schema"),
		},
		{
			Name:        "get_table_schema",
			Description: "获取一张表的列、索引、外键与建表 DDL。",
			InputSchema: strSchema(map[string]any{"connection_id": connID, "schema": schema, "table": map[string]any{"type": "string"}}, "connection_id", "schema", "table"),
		},
		{
			Name:        "run_readonly_query",
			Description: "执行只读 SQL（仅 SELECT / EXPLAIN / WITH），返回结果集。写语句被拒绝。",
			InputSchema: strSchema(map[string]any{
				"connection_id": connID,
				"sql":           map[string]any{"type": "string", "description": "要执行的只读 SQL"},
				"params":        map[string]any{"type": "array", "items": map[string]any{}, "description": "可选的位置参数"},
			}, "connection_id", "sql"),
		},
	}
}

// toolArgs 解析 tools/call 参数：{name, arguments:{...}}。
func callParams(params json.RawMessage) (string, map[string]any) {
	var p struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	args := map[string]any{}
	for k, v := range p.Arguments {
		var dv any
		_ = json.Unmarshal(v, &dv)
		args[k] = dv
	}
	return p.Name, args
}

func argStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// callTool 执行工具，返回文本内容与是否 isError。
func (s *Server) callTool(ctx context.Context, params json.RawMessage) (string, bool) {
	name, args := callParams(params)
	switch name {
	case "list_connections":
		conns, err := s.app.ListConnections()
		if err != nil {
			return err.Error(), true
		}
		return jsonText(conns), false
	case "list_schemas":
		id := argStr(args, "connection_id")
		ss, err := s.app.ListSchemas(ctx, id)
		if err != nil {
			return err.Error(), true
		}
		return jsonText(ss), false
	case "list_tables":
		id, schema := argStr(args, "connection_id"), argStr(args, "schema")
		tt, err := s.app.ListTables(ctx, id, schema)
		if err != nil {
			return err.Error(), true
		}
		return jsonText(tt), false
	case "get_table_schema":
		id, schema, table := argStr(args, "connection_id"), argStr(args, "schema"), argStr(args, "table")
		cols, err := s.app.ListColumns(ctx, id, schema, table)
		if err != nil {
			return err.Error(), true
		}
		idx, _ := s.app.ListIndexes(ctx, id, schema, table)
		fks, _ := s.app.ListForeignKeys(ctx, id, schema, table)
		ddl, _ := s.app.CreateTableSQL(ctx, id, schema, table)
		out := map[string]any{"columns": cols, "indexes": idx, "foreign_keys": fks, "ddl": ddl}
		return jsonText(out), false
	case "run_readonly_query":
		id, sql := argStr(args, "connection_id"), argStr(args, "sql")
		if err := assertReadOnly(sql); err != nil {
			return err.Error(), true
		}
		var pvals []protocol.Value
		if raw, ok := args["params"]; ok {
			if arr, ok := raw.([]any); ok {
				for _, e := range arr {
					b, _ := json.Marshal(e)
					var v protocol.Value
					_ = json.Unmarshal(b, &v)
					pvals = append(pvals, v)
				}
			}
		}
		res, err := s.app.Execute(ctx, id, sql, pvals...)
		if err != nil {
			return err.Error(), true
		}
		return jsonText(res), false
	default:
		return "unknown tool: " + name, true
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// assertReadOnly 只放行 SELECT / EXPLAIN / WITH（只读红线）。
func assertReadOnly(sql string) error {
	t := strings.ToUpper(strings.TrimSpace(sql))
	switch {
	case strings.HasPrefix(t, "SELECT"), strings.HasPrefix(t, "WITH"), strings.HasPrefix(t, "EXPLAIN"), strings.HasPrefix(t, "SHOW"), strings.HasPrefix(t, "DESCRIBE"), strings.HasPrefix(t, "DESC "):
		return nil
	default:
		if dbcore.DetectStatementType(sql) != protocol.StatementTypeSelect {
			return &protocol.PolyDBError{Code: protocol.ErrReadOnly, Message: "MCP 仅支持只读查询（SELECT/EXPLAIN/WITH），写语句被拒绝"}
		}
		return nil
	}
}
