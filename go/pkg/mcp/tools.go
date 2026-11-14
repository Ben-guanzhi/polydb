package mcp

import (
	"context"
	"encoding/json"

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
			Description: "执行只读 SQL（首关键字限 SELECT / WITH / EXPLAIN / SHOW / DESCRIBE / DESC，且整条语句不含任何变更关键字），返回结果集。写语句被拒绝；多语句脚本逐条判定。",
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
		// 方言决定词法剖面（'#' 行注释、反斜杠转义、dollar-quote…）。
		// 连接不存在时 kind 为空 → 闸门取各剖面并集（更严）。
		kind := protocol.DatabaseKind("")
		if info, err := s.app.GetConnection(id); err == nil {
			kind = info.Kind
		}
		if err := assertReadOnly(sql, kind); err != nil {
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
		// Q-03：行内容出机器前脱敏（列名层 + 形态层），结果带 masked 标记供
		// 客户端区分——与 db-dev 的 MCP 面同构（dbcore/masking 默认档）。
		return jsonText(map[string]any{"result": maskQueryResult(res), "masked": true}), false
	default:
		return "unknown tool: " + name, true
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// readOnlyLeadingKeywords 是 MCP 只读闸门放行的**首关键字**白名单。
//
// 判型按整词而非前缀：前缀匹配挡不住 `SELECTED` 这类假阳性，也挡不住
// 「首关键字不在白名单、但整句看起来像查询」的语句。
var readOnlyLeadingKeywords = map[string]bool{
	"SELECT":   true,
	"WITH":     true,
	"EXPLAIN":  true,
	"SHOW":     true,
	"DESCRIBE": true,
	"DESC":     true,
}

// assertReadOnly 是 MCP 侧只读红线（behavior.md §12.3）：**逐语句**判定。
//
// 旧实现是「前缀白名单」（HasPrefix "SELECT"），实测三条绕过：
//
//	SELECT 1; DROP TABLE t                                   —— 多语句，后半段被放行
//	WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d —— 数据修改 CTE
//	EXPLAIN ANALYZE DELETE FROM users                       —— PG 上 EXPLAIN ANALYZE 真执行
//
// 其中第一条已端到端证实：经 app.Execute 执行后目标表真的被删。
//
// 现在用方言感知的切分器把脚本拆成语句，逐条要求：
//   - 首关键字落在 readOnlyLeadingKeywords 里（挡住 DELETE/UPDATE/CALL 等）；
//   - 整条语句不含变更关键字（挡住 EXPLAIN ANALYZE DELETE、数据修改 CTE）。
//
// 两个条件缺一不可：只看首关键字会漏掉 EXPLAIN ANALYZE 与数据修改 CTE；
// 只看关键字扫描会漏掉首关键字不在白名单的语句。
func assertReadOnly(sql string, kind protocol.DatabaseKind) error {
	stmts := dbcore.SplitStatements(sql, kind)
	if len(stmts) == 0 {
		return readOnlyRejected()
	}
	for _, stmt := range stmts {
		if !readOnlyLeadingKeywords[dbcore.LeadingKeyword(stmt, kind)] {
			return readOnlyRejected()
		}
		if dbcore.MutatesData(stmt, kind) {
			return readOnlyRejected()
		}
	}
	return nil
}

func readOnlyRejected() error {
	return &protocol.PolyDBError{Code: protocol.ErrReadOnly, Message: "MCP 仅支持只读查询（SELECT/WITH/EXPLAIN/SHOW/DESCRIBE），写语句被拒绝"}
}
