// Package explain 把各数据库 EXPLAIN 输出解析为计划树（纯函数，无 IO）。
//
// 语义与 Web 端 lib/explain.ts 对齐：SQLite 用 id/parent 行树，PostgreSQL /
// MySQL 解析 JSON 计划，其余（含解析失败）退化为「列 | 行」表格文本。
// 本包为独立实现，JSON 键名按各数据库服务端实际输出（PG 用带空格键名，
// MySQL 用 snake_case）并同时接受无空格变体以防驱动差异。
package explain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// maxDepth 计划树递归上限：防御畸形/自引用输出把栈打爆。
const maxDepth = 64

// Node 一个计划节点。
type Node struct {
	Name     string // 算子 / 表名（含访问方式）
	Detail   string // 附加说明（SQLite detail、filter、key 等）
	Cost     string // 已格式化的估算代价
	Rows     string // 已格式化的估算行数
	Children []Node
}

// Render 解析结果：能成树则用 Nodes，否则用 Text 行。
type Render struct {
	Nodes []Node
	Text  []string
}

// Tree 报告是否为树模式。
func (r Render) Tree() bool { return len(r.Nodes) > 0 }

// Lines 返回渲染文本：树模式展平为带缩进连接线的行，文本模式原样返回。
func (r Render) Lines() []string {
	if !r.Tree() {
		if len(r.Text) == 0 {
			return []string{"(空结果)"}
		}
		return r.Text
	}
	var out []string
	for i, n := range r.Nodes {
		out = appendNode(out, n, "", i == len(r.Nodes)-1)
	}
	return out
}

// appendNode 前序展平：prefix + 连接线 + 节点标签，子节点缩进一级。
func appendNode(dst []string, n Node, prefix string, last bool) []string {
	connector := "├─ "
	childPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		childPrefix = prefix + "   "
	}
	dst = append(dst, prefix+connector+nodeLabel(n))
	for i, c := range n.Children {
		dst = appendNode(dst, c, childPrefix, i == len(n.Children)-1)
	}
	return dst
}

// nodeLabel 拼出一行节点文本：名称 + [cost · rows] + 说明。
func nodeLabel(n Node) string {
	var meta []string
	if n.Cost != "" {
		meta = append(meta, n.Cost)
	}
	if n.Rows != "" {
		meta = append(meta, n.Rows)
	}
	s := n.Name
	if len(meta) > 0 {
		s += " [" + strings.Join(meta, " · ") + "]"
	}
	if n.Detail != "" {
		s += "  " + n.Detail
	}
	return s
}

// BuildSQL 按方言把编辑器里的 SQL 包成 EXPLAIN 语句（注释与尾分号先剥）。
// 空输入返回空串；已经以 EXPLAIN 开头的原样返回。
func BuildSQL(raw string, kind protocol.DatabaseKind) string {
	clean := strings.TrimRight(stripComments(raw), " \t\r\n;")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return ""
	}
	if startsWithExplain(clean) {
		return clean
	}
	switch kind {
	case protocol.DatabaseKindSQLite:
		return "EXPLAIN QUERY PLAN " + clean
	case protocol.DatabaseKindPostgres:
		return "EXPLAIN (FORMAT JSON) " + clean
	case protocol.DatabaseKindMySQL:
		return "EXPLAIN FORMAT=JSON " + clean
	case protocol.DatabaseKindOracle:
		return "EXPLAIN PLAN FOR " + clean
	default:
		return "EXPLAIN " + clean
	}
}

func startsWithExplain(s string) bool {
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "explain") {
		return false
	}
	rest := low[len("explain"):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r'
}

// stripComments 去掉 -- 行注释与 /* */ 块注释，字符串字面量内的内容保持原样。
func stripComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			j := endOfQuoted(s, i)
			b.WriteString(s[i:j])
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				return b.String()
			}
			i += nl // 换行本身保留，避免把两行语句粘成一行
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// endOfQuoted 返回闭合引号之后的下标（未闭合则到串尾）。
func endOfQuoted(s string, start int) int {
	q := s[start]
	for i := start + 1; i < len(s); i++ {
		if s[i] == '\\' && q == '\'' { // MySQL 反斜杠转义
			i++
			continue
		}
		if s[i] == q {
			if i+1 < len(s) && s[i+1] == q { // SQL 双写引号转义
				i++
				continue
			}
			return i + 1
		}
	}
	return len(s)
}

// RenderResult 把一次 EXPLAIN 查询的结果解析为计划树或表格文本。
func RenderResult(res *protocol.QueryResult, kind protocol.DatabaseKind) Render {
	if res == nil || len(res.Rows) == 0 {
		return Render{Text: []string{"(空结果)"}}
	}
	switch kind {
	case protocol.DatabaseKindSQLite:
		if nodes, ok := sqliteTree(res); ok {
			return Render{Nodes: nodes}
		}
	case protocol.DatabaseKindPostgres:
		if nodes, ok := pgTree(firstCell(res)); ok {
			return Render{Nodes: nodes}
		}
	case protocol.DatabaseKindMySQL:
		if nodes, ok := mysqlTree(firstCell(res)); ok {
			return Render{Nodes: nodes}
		}
	}
	return tableText(res)
}

// ─── SQLite：id / parent / detail 行树 ───────────────────────
//
// SQLite 的 EXPLAIN QUERY PLAN 还有第 4 列 notused，实测（3.53）在多条计划间取值
// 漂移（216 / 56），是其内部标志位而非计划信息，Web 侧 explain.ts 会把它显示出来，
// 这里有意省略。

type sqliteRow struct {
	id     int
	parent int
	node   Node
}

func sqliteTree(res *protocol.QueryResult) ([]Node, bool) {
	idx := columnIndexes(res)
	iID, okID := idx["id"]
	iPar, okPar := idx["parent"]
	iDet, okDet := idx["detail"]
	if !okID || !okPar || !okDet {
		return nil, false
	}
	rows := make([]sqliteRow, 0, len(res.Rows))
	known := make(map[int]bool, len(res.Rows))
	for _, r := range res.Rows {
		id := cellInt(r, iID)
		known[id] = true
		detail := cellText(r, iDet)
		rows = append(rows, sqliteRow{
			id:     id,
			parent: cellInt(r, iPar),
			node:   Node{Name: fmt.Sprintf("Step %d", id), Detail: detail},
		})
	}
	kids := make(map[int][]int, len(rows))
	var rootIDs []int
	for _, e := range rows {
		if e.parent == e.id || !known[e.parent] {
			rootIDs = append(rootIDs, e.id)
			continue
		}
		kids[e.parent] = append(kids[e.parent], e.id)
	}
	byID := make(map[int]Node, len(rows))
	for _, e := range rows {
		byID[e.id] = e.node
	}
	roots := make([]Node, 0, len(rootIDs))
	emitted := make(map[int]bool, len(rows))
	for _, id := range rootIDs {
		roots = append(roots, buildSQLite(id, byID, kids, 0, map[int]bool{}, emitted))
	}
	// 环（parent 互指）中的节点没有任何根可达：补为顶层，避免计划行凭空消失。
	for _, e := range rows {
		if !emitted[e.id] {
			roots = append(roots, buildSQLite(e.id, byID, kids, 0, map[int]bool{}, emitted))
		}
	}
	return roots, len(roots) > 0
}

func buildSQLite(id int, byID map[int]Node, kids map[int][]int, depth int, onPath map[int]bool, emitted map[int]bool) Node {
	n := byID[id]
	emitted[id] = true
	if depth >= maxDepth || onPath[id] {
		n.Detail += " …"
		return n
	}
	onPath[id] = true
	for _, c := range kids[id] {
		n.Children = append(n.Children, buildSQLite(c, byID, kids, depth+1, onPath, emitted))
	}
	delete(onPath, id)
	return n
}

// ─── PostgreSQL：EXPLAIN (FORMAT JSON) ───────────────────────

func pgTree(raw string) ([]Node, bool) {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		var one map[string]any
		if err2 := json.Unmarshal([]byte(raw), &one); err2 != nil {
			return nil, false
		}
		arr = []map[string]any{one}
	}
	nodes := make([]Node, 0, len(arr))
	for _, item := range arr {
		plan := item
		if p, ok := item["Plan"].(map[string]any); ok {
			plan = p
		}
		n, ok := pgNode(plan, 0)
		if !ok {
			return nil, false
		}
		nodes = append(nodes, n)
	}
	return nodes, len(nodes) > 0
}

func pgNode(plan map[string]any, depth int) (Node, bool) {
	if depth >= maxDepth || len(plan) == 0 {
		return Node{}, false
	}
	name := stringOf(plan, "Node Type", "NodeType", "Operation")
	if name == "" {
		name = stringOf(plan, "Relation Name", "RelationName", "Index Name", "Function Name")
	}
	if name == "" {
		return Node{}, false
	}
	n := Node{Name: name}
	if rel := stringOf(plan, "Relation Name", "RelationName"); rel != "" && rel != name {
		n.Detail = "on " + rel
	}
	if idxName := stringOf(plan, "Index Name", "IndexName"); idxName != "" {
		n.Detail = strings.TrimSpace(n.Detail + " using " + idxName)
	}
	if filter := stringOf(plan, "Filter", "Join Cond"); filter != "" {
		n.Detail = strings.TrimSpace(n.Detail + " " + filter)
	}
	if cost, ok := floatOf(plan, "Total Cost", "TotalCost"); ok {
		n.Cost = fmt.Sprintf("cost=%.2f", cost)
	}
	if rows, ok := floatOf(plan, "Plan Rows", "PlanRows"); ok {
		n.Rows = fmt.Sprintf("rows≈%d", int64(rows))
	}
	for _, sub := range mapList(plan, "Plans") {
		if c, ok := pgNode(sub, depth+1); ok {
			n.Children = append(n.Children, c)
		}
	}
	return n, true
}

// ─── MySQL：EXPLAIN FORMAT=JSON ──────────────────────────────

func mysqlTree(raw string) ([]Node, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, false
	}
	qb, ok := doc["query_block"].(map[string]any)
	if !ok {
		return nil, false
	}
	n, ok := mysqlBlock(qb, 0)
	if !ok {
		return nil, false
	}
	return []Node{n}, true
}

// mysqlBlock 解析 query_block / 各类操作包装块。
func mysqlBlock(b map[string]any, depth int) (Node, bool) {
	if depth >= maxDepth || len(b) == 0 {
		return Node{}, false
	}
	if t, ok := b["table"].(map[string]any); ok {
		n, ok := mysqlTable(t, depth)
		if !ok {
			return Node{}, false
		}
		attachCost(&n, b)
		return n, true
	}
	if steps := mapList(b, "nested_loop"); len(steps) > 0 {
		n := Node{Name: "Nested loop"}
		for _, s := range steps {
			c, ok := mysqlBlock(s, depth+1)
			if !ok {
				continue
			}
			n.Children = append(n.Children, c)
		}
		if len(n.Children) == 0 {
			return Node{}, false
		}
		attachCost(&n, b)
		return n, true
	}
	for _, wrapper := range []struct {
		key  string
		name string
	}{
		{"ordering_operation", "Sort"},
		{"grouping_operation", "Group"},
		{"windowing", "Window"},
		{"dml_table", "DML"},
	} {
		inner, ok := b[wrapper.key].(map[string]any)
		if !ok {
			continue
		}
		n, ok := mysqlBlock(inner, depth+1)
		if !ok {
			return Node{}, false
		}
		if n.Name == wrapper.name {
			return n, true
		}
		out := Node{Name: wrapper.name, Children: []Node{n}}
		if filesort, ok := inner["using_filesort"].(bool); ok && filesort {
			out.Detail = "filesort"
		}
		attachCost(&out, b)
		return out, true
	}
	if n, ok := mysqlSubquery(b, depth); ok {
		return n, true
	}
	return Node{}, false
}

func mysqlTable(t map[string]any, depth int) (Node, bool) {
	name := stringOf(t, "table_name", "subquery_name", "reference")
	if name == "" {
		name = stringOf(t, "message")
	}
	if name == "" {
		return Node{}, false
	}
	n := Node{Name: name}
	if acc := stringOf(t, "access_type", "type"); acc != "" {
		n.Detail = acc
	}
	if key := stringOf(t, "key"); key != "" {
		n.Detail = strings.TrimSpace(n.Detail + " key=" + key)
	}
	if cond := stringOf(t, "attached_condition"); cond != "" {
		n.Detail = strings.TrimSpace(n.Detail + " " + cond)
	}
	if rows, ok := floatOf(t, "rows_examined_per_scan", "rows"); ok {
		n.Rows = fmt.Sprintf("rows≈%d", int64(rows))
	}
	if sub, ok := t["subqueries"]; ok {
		for _, s := range anyList(sub) {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := mysqlBlock(m, depth+1); ok {
				n.Children = append(n.Children, c)
			}
		}
	}
	attachCost(&n, t)
	return n, true
}

func mysqlSubquery(b map[string]any, depth int) (Node, bool) {
	for _, key := range []string{"select", "subquery", "materialized_subquery"} {
		inner, ok := b[key].(map[string]any)
		if !ok {
			continue
		}
		if n, ok := mysqlBlock(inner, depth+1); ok {
			return n, true
		}
	}
	return Node{}, false
}

// attachCost 从同层 cost_info.query_cost 取代价。
func attachCost(n *Node, b map[string]any) {
	if n.Cost != "" {
		return
	}
	ci, ok := b["cost_info"].(map[string]any)
	if !ok {
		return
	}
	if c := stringOf(ci, "query_cost", "total_cost"); c != "" {
		n.Cost = "cost=" + c
	}
}

// ─── 通用兜底：列 | 行 表格文本 ──────────────────────────────

func tableText(res *protocol.QueryResult) Render {
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	lines := []string{strings.Join(names, " | ")}
	for _, r := range res.Rows {
		cells := make([]string, len(r))
		for i := range r {
			cells[i] = cellText(r, i)
		}
		lines = append(lines, strings.Join(cells, " | "))
	}
	return Render{Text: lines}
}

// ─── 协议值 / JSON 取值辅助 ─────────────────────────────────

func columnIndexes(res *protocol.QueryResult) map[string]int {
	m := make(map[string]int, len(res.Columns))
	for i, c := range res.Columns {
		if _, dup := m[strings.ToLower(c.Name)]; !dup {
			m[strings.ToLower(c.Name)] = i
		}
	}
	return m
}

func firstCell(res *protocol.QueryResult) string {
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return ""
	}
	return cellText(res.Rows[0], 0)
}

func cellText(row []protocol.Value, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strOf(row[i])
}

// strOf 把协议值转为其文本形态（字符串去引号，其余保留 JSON 原文）。
func strOf(v protocol.Value) string {
	if v.IsNull() {
		return ""
	}
	b, err := v.MarshalJSON()
	if err != nil {
		return ""
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func cellInt(row []protocol.Value, i int) int {
	n, _ := strconv.Atoi(strings.TrimSpace(cellText(row, i)))
	return n
}

// stringOf 返回首个非空字符串字段。
func stringOf(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// floatOf 返回首个数值字段（JSON 数字或数字字符串）。
func floatOf(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v, true
		case int64:
			return float64(v), true
		case json.Number:
			f, err := v.Float64()
			return f, err == nil
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// mapList 取 []map[string]any 字段（JSON 对象数组）。
func mapList(m map[string]any, key string) []map[string]any {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	if single, ok := raw.(map[string]any); ok {
		return []map[string]any{single}
	}
	out := []map[string]any{}
	for _, item := range anyList(raw) {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func anyList(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	if single, ok := v.(map[string]any); ok {
		return []any{single}
	}
	return nil
}
