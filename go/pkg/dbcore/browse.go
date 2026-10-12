package dbcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// 表数据浏览的共享查询构造器（behavior.md §13）：条件值一律走绑定参数，
// 标识符由各方言的 Quote 函数引用（内部引号翻倍），绝不拼接字面量。
// 各 SQL 驱动的 BrowseRows / BrowseRowsCount 复用这里，方言差异只在
// SQLDialect 的四个字段里（引号 / 分页 / ORDER BY 兜底 / 文本匹配形态）。

// BrowseFlavor 声明文本匹配类 op 的方言形态差异（behavior.md §13.5），
// 与引号/分页无关，所以单独建模。
type BrowseFlavor string

const (
	// FlavorPg：ANSI，LIKE 默认区分大小写，正则用 `~` / `~*`。
	FlavorPg BrowseFlavor = "pg"
	// FlavorMySQL：反引号 + `REGEXP`，LIKE 默认按排序规则不区分。
	FlavorMySQL BrowseFlavor = "mysql"
	// FlavorMSSQL：方括号 + `LIKE … COLLATE …_CS_AS`，**无原生正则**。
	FlavorMSSQL BrowseFlavor = "mssql"
	// FlavorOracle：`REGEXP_LIKE`，LIKE 默认区分大小写。
	FlavorOracle BrowseFlavor = "oracle"
	// FlavorSQLite：强制区分走 `GLOB`，正则走驱动注册的 `REGEXP` UDF。
	FlavorSQLite BrowseFlavor = "sqlite"
)

// SupportsRegex 报告该方言能否渲染 `regex` op（能力矩阵见 behavior.md §13.6）。
func (f BrowseFlavor) SupportsRegex() bool { return f != FlavorMSSQL }

// SQLDialect 声明浏览查询需要的方言差异。
type SQLDialect struct {
	// Quote 把标识符（表名/列名）包上引号。实现必须转义内部引号。
	Quote func(name string) string
	// LimitClause 产出分页子句（含前导空格），如 " LIMIT 200 OFFSET 0"
	// 或 " OFFSET 0 ROWS FETCH NEXT 200 ROWS ONLY"。
	LimitClause func(offset, limit uint64) string
	// OrderByFallback 在「分页子句要求必须有 ORDER BY 且请求未带排序」时使用，
	// 如 MSSQL 的 "(SELECT NULL)"；空串表示无需兜底。
	OrderByFallback string
	// Flavor 决定文本匹配与正则 op 的 SQL 形态；空值按 ANSI 默认（plain LIKE）处理。
	Flavor BrowseFlavor
}

// DefaultSQLDialect：ANSI 双引号标识符 + LIMIT/OFFSET 分页（postgres）。
func DefaultSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteDouble,
		LimitClause: limitOffsetClause,
		Flavor:      FlavorPg,
	}
}

// SQLiteSQLDialect：引号/分页同 ANSI，但大小写与正则形态不同。
func SQLiteSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteDouble,
		LimitClause: limitOffsetClause,
		Flavor:      FlavorSQLite,
	}
}

// MySQLSQLDialect：反引号标识符 + LIMIT/OFFSET。
func MySQLSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteBacktick,
		LimitClause: limitOffsetClause,
		Flavor:      FlavorMySQL,
	}
}

// MSSQLSQLDialect：方括号标识符 + OFFSET/FETCH（要求 ORDER BY）。
func MSSQLSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:           quoteBracket,
		LimitClause:     offsetFetchClause,
		OrderByFallback: "(SELECT NULL)",
		Flavor:          FlavorMSSQL,
	}
}

// OracleSQLDialect：双引号标识符 + OFFSET/FETCH（12c+，无 ORDER BY 也可）。
func OracleSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteDouble,
		LimitClause: offsetFetchClause,
		Flavor:      FlavorOracle,
	}
}

func quoteDouble(name string) string   { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func quoteBacktick(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
func quoteBracket(name string) string  { return "[" + strings.ReplaceAll(name, "]", "]]") + "]" }

func limitOffsetClause(offset, limit uint64) string {
	return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
}

func offsetFetchClause(offset, limit uint64) string {
	return fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
}

// BrowseRowsLimits 应用 behavior.md §13.2 的分页钳制（上限 10000）。缺省值当前
// 取 200，而 spec 声明 100——偏差已登记在 AGENTS.md §16 第 9 条。
func BrowseRowsLimits(limit uint32) uint64 {
	if limit == 0 {
		return 200
	}
	if limit > 10000 {
		return 10000
	}
	return uint64(limit)
}

// BrowseRowsValidate 校验请求的公共部分（列名/条件/op），驱动实现应先调用。
func BrowseRowsValidate(req *protocol.TableRowsRequest) error {
	for _, c := range req.Columns {
		if strings.TrimSpace(c) == "" {
			return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "empty column name"}
		}
	}
	for i, cond := range req.Conditions {
		if strings.TrimSpace(cond.Column) == "" {
			return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("empty column name in condition %d", i)}
		}
		switch cond.Op {
		case protocol.FilterOpIn, protocol.FilterOpNotIn:
			if len(cond.Values) == 0 {
				return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("condition %d: op %s requires non-empty values", i, cond.Op)}
			}
		case protocol.FilterOpBetween:
			if cond.Value.IsNull() || cond.SecondValue.IsNull() {
				return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("condition %d: op between requires value and second_value", i)}
			}
		case protocol.FilterOpEq, protocol.FilterOpNe, protocol.FilterOpLt, protocol.FilterOpLe,
			protocol.FilterOpGt, protocol.FilterOpGe, protocol.FilterOpLike, protocol.FilterOpNotLike,
			protocol.FilterOpContains, protocol.FilterOpStartsWith, protocol.FilterOpEndsWith,
			protocol.FilterOpRegex:
			if cond.Value.IsNull() {
				return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("condition %d: op %s requires value", i, cond.Op)}
			}
		case protocol.FilterOpNull, protocol.FilterOpNotNull, protocol.FilterOpEmpty:
			// 无需值
		default:
			return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("unknown filter op: %s", cond.Op)}
		}
	}
	return nil
}

// likeEscapeChar 是文本匹配类 op 的转义符（behavior.md §13.3）：不用 `\`，
// MySQL 字符串字面量里它自身还要再转义，`!` 无歧义。
const likeEscapeChar = '!'

func invalidParam(msg string) error {
	return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: msg}
}

func notSupported(msg string) error {
	return &protocol.PolyDBError{Code: protocol.ErrNotSupported, Message: msg}
}

// valueToText 把标量值转成文本模式；Null/数组/对象不是合法匹配值 → false。
func valueToText(v protocol.Value) (string, bool) {
	if v.IsNull() {
		return "", false
	}
	raw, err := v.MarshalJSON()
	if err != nil {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return "", false
	}
	switch t := decoded.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case json.Number:
		return t.String(), true
	default:
		return "", false
	}
}

var likeTextReplacer = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

// EscapeLikeText 转义用户文本里的 LIKE 通配符，供包裹类 op 加 `%` 后使用。
func EscapeLikeText(s string) string { return likeTextReplacer.Replace(s) }

// LikePatternToGlob 把 SQL LIKE 模式翻译为 SQLite GLOB 模式（GLOB 区分大小写，
// 元字符用 `\` 转义）。只用于**用户自填的 LIKE 模式**，故 `%`/`_` 视为通配符。
func LikePatternToGlob(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for _, ch := range s {
		switch ch {
		case '%':
			sb.WriteByte('*')
		case '_':
			sb.WriteByte('?')
		case '\\', '*', '?', '[':
			sb.WriteByte('\\')
			sb.WriteRune(ch)
		default:
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// GlobQuoteText 把字面文本转成 GLOB 片段：只转义 GLOB 元字符，
// `%`/`_` 在此是普通字符，不当通配符解释。
func GlobQuoteText(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for _, ch := range s {
		switch ch {
		case '\\', '*', '?', '[':
			sb.WriteByte('\\')
			sb.WriteRune(ch)
		default:
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// textCond 是 LIKE 家族的同一份值的两种模式形态 + 是否取反 + 是否带 ESCAPE 子句。
// SQLite 强制区分大小写时走 glob，其余方言走 like——两套转义符互不通用。
type textCond struct {
	like    string
	glob    string
	neg     bool
	escaped bool
}

// newTextCond 由 op 与值构造 textCond。like/not_like 原样透传（既有语义不变），
// 包裹类 op 先各自转义字面值再加通配符。
func newTextCond(op protocol.FilterOperator, v protocol.Value) (textCond, error) {
	raw, ok := valueToText(v)
	if !ok {
		return textCond{}, invalidParam("op " + string(op) + " requires a scalar value")
	}
	e := EscapeLikeText(raw)
	g := GlobQuoteText(raw)
	switch op {
	case protocol.FilterOpLike:
		return textCond{like: raw, glob: LikePatternToGlob(raw)}, nil
	case protocol.FilterOpNotLike:
		return textCond{like: raw, glob: LikePatternToGlob(raw), neg: true}, nil
	case protocol.FilterOpContains:
		return textCond{like: "%" + e + "%", glob: "*" + g + "*", escaped: true}, nil
	case protocol.FilterOpStartsWith:
		return textCond{like: e + "%", glob: g + "*", escaped: true}, nil
	case protocol.FilterOpEndsWith:
		return textCond{like: "%" + e, glob: "*" + g, escaped: true}, nil
	default:
		return textCond{}, invalidParam("not a like-family op")
	}
}

// renderLike 渲染 LIKE 家族条件（behavior.md §13.5）：返回 SQL 片段与绑定值。
func renderLike(d SQLDialect, col string, tc textCond, cs *bool) (string, protocol.Value) {
	neg := ""
	if tc.neg {
		neg = "NOT "
	}
	if d.Flavor == FlavorSQLite && cs != nil && *cs {
		return col + " " + neg + "GLOB ?", protocol.NewStringValue(tc.glob)
	}
	escClause := ""
	if tc.escaped {
		escClause = fmt.Sprintf(" ESCAPE '%c'", likeEscapeChar)
	}
	var sql string
	switch {
	case d.Flavor == FlavorPg && cs != nil && !*cs:
		op := "ILIKE"
		if tc.neg {
			op = "NOT ILIKE"
		}
		sql = col + " " + op + " ?" + escClause
	// SQLite 的 LIKE 对 ASCII 本就不区分，无需再套 LOWER。
	case d.Flavor == FlavorSQLite && cs != nil && !*cs:
		sql = col + " " + neg + "LIKE ?" + escClause
	case cs != nil && !*cs:
		sql = "LOWER(" + col + ") " + neg + "LIKE LOWER(?)" + escClause
	case d.Flavor == FlavorMySQL && cs != nil && *cs:
		sql = "CAST(" + col + " AS BINARY) " + neg + "LIKE CAST(? AS BINARY)" + escClause
	case d.Flavor == FlavorMSSQL && cs != nil && *cs:
		sql = col + " COLLATE Latin1_General_CS_AS " + neg + "LIKE ?" + escClause
	default:
		sql = col + " " + neg + "LIKE ?" + escClause
	}
	return sql, protocol.NewStringValue(tc.like)
}

// renderRegex 渲染 `regex` 条件；无原生正则的方言显式 NOT_SUPPORTED，绝不静默降级。
func renderRegex(d SQLDialect, col string, v protocol.Value, cs *bool) (string, protocol.Value, error) {
	raw, ok := valueToText(v)
	if !ok {
		return "", protocol.Value{}, invalidParam("op regex requires a scalar value")
	}
	val := protocol.NewStringValue(raw)
	toCS := cs != nil && *cs
	toCI := cs != nil && !*cs
	switch d.Flavor {
	case FlavorMSSQL:
		return "", protocol.Value{}, notSupported("op regex is not supported: MSSQL has no native regular expressions")
	case FlavorPg:
		if toCI {
			return col + " ~* ?", val, nil
		}
		return col + " ~ ?", val, nil
	case FlavorMySQL:
		switch {
		case toCS:
			return "CAST(" + col + " AS BINARY) REGEXP CAST(? AS BINARY)", val, nil
		case toCI:
			return "LOWER(" + col + ") REGEXP LOWER(?)", val, nil
		default:
			return col + " REGEXP ?", val, nil
		}
	case FlavorOracle:
		if toCI {
			return "REGEXP_LIKE(" + col + ", ?, 'i')", val, nil
		}
		return "REGEXP_LIKE(" + col + ", ?)", val, nil
	case FlavorSQLite:
		if toCI {
			return col + " REGEXP '(?i)' || ?", val, nil
		}
		return col + " REGEXP ?", val, nil
	default:
		return "", protocol.Value{}, notSupported("op regex is not supported by this dialect")
	}
}

// BuildRowsWhere 渲染 WHERE 子句（不含 "WHERE" 前缀；无条件返回空串）与绑定参数。
func BuildRowsWhere(d SQLDialect, req *protocol.TableRowsRequest) (string, []protocol.Value, error) {
	if len(req.Conditions) == 0 {
		return "", nil, nil
	}
	var sb strings.Builder
	var args []protocol.Value
	joiner := " AND "
	if req.Logic == protocol.FilterLogicOr {
		joiner = " OR "
	}
	for i, cond := range req.Conditions {
		if i > 0 {
			sb.WriteString(joiner)
		}
		col := d.Quote(cond.Column)
		switch cond.Op {
		case protocol.FilterOpNull:
			sb.WriteString(col + " IS NULL")
		case protocol.FilterOpNotNull:
			sb.WriteString(col + " IS NOT NULL")
		case protocol.FilterOpEmpty:
			sb.WriteString("(" + col + " IS NULL OR " + col + " = '')")
		case protocol.FilterOpIn, protocol.FilterOpNotIn:
			op := "IN"
			if cond.Op == protocol.FilterOpNotIn {
				op = "NOT IN"
			}
			sb.WriteString(col + " " + op + " (")
			for j := range cond.Values {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString("?")
				args = append(args, cond.Values[j])
			}
			sb.WriteString(")")
		case protocol.FilterOpBetween:
			sb.WriteString(col + " BETWEEN ? AND ?")
			args = append(args, cond.Value, cond.SecondValue)
		case protocol.FilterOpRegex:
			s, v, err := renderRegex(d, col, cond.Value, cond.CaseSensitive)
			if err != nil {
				return "", nil, err
			}
			sb.WriteString(s)
			args = append(args, v)
		case protocol.FilterOpLike, protocol.FilterOpNotLike, protocol.FilterOpContains,
			protocol.FilterOpStartsWith, protocol.FilterOpEndsWith:
			tc, err := newTextCond(cond.Op, cond.Value)
			if err != nil {
				return "", nil, err
			}
			s, v := renderLike(d, col, tc, cond.CaseSensitive)
			sb.WriteString(s)
			args = append(args, v)
		default:
			opSQL, ok := map[protocol.FilterOperator]string{
				protocol.FilterOpEq: "=",
				protocol.FilterOpNe: "<>",
				protocol.FilterOpLt: "<",
				protocol.FilterOpLe: "<=",
				protocol.FilterOpGt: ">",
				protocol.FilterOpGe: ">=",
			}[cond.Op]
			if !ok {
				return "", nil, invalidParam("unknown filter op: " + string(cond.Op))
			}
			sb.WriteString(col + " " + opSQL + " ?")
			args = append(args, cond.Value)
		}
	}
	return sb.String(), args, nil
}

// BuildRowsQuery 构造浏览查询（含 limit+1 供 has_more 判定）与绑定参数。
func BuildRowsQuery(d SQLDialect, schema, table string, req *protocol.TableRowsRequest) (string, []protocol.Value, error) {
	if err := BrowseRowsValidate(req); err != nil {
		return "", nil, err
	}
	limit := BrowseRowsLimits(req.Limit)
	var sb strings.Builder
	sb.WriteString("SELECT ")
	if len(req.Columns) == 0 {
		sb.WriteString("*")
	} else {
		cols := make([]string, len(req.Columns))
		for i, c := range req.Columns {
			cols[i] = d.Quote(c)
		}
		sb.WriteString(strings.Join(cols, ", "))
	}
	sb.WriteString(" FROM " + qualifyTable(d, schema, table))
	where, args, err := BuildRowsWhere(d, req)
	if err != nil {
		return "", nil, err
	}
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	orderBy := buildOrderBy(d, req)
	if orderBy == "" && d.OrderByFallback != "" {
		orderBy = " ORDER BY " + d.OrderByFallback
	}
	sb.WriteString(orderBy)
	sb.WriteString(d.LimitClause(req.Offset, limit+1))
	return sb.String(), args, nil
}

// BuildRowsCountQuery 构造同条件的 COUNT(*) 查询与绑定参数。
func BuildRowsCountQuery(d SQLDialect, schema, table string, req *protocol.TableRowsRequest) (string, []protocol.Value, error) {
	if err := BrowseRowsValidate(req); err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	sb.WriteString("SELECT COUNT(*) FROM " + qualifyTable(d, schema, table))
	where, args, err := BuildRowsWhere(d, req)
	if err != nil {
		return "", nil, err
	}
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	return sb.String(), args, nil
}

func buildOrderBy(d SQLDialect, req *protocol.TableRowsRequest) string {
	if len(req.OrderBy) == 0 {
		return ""
	}
	parts := make([]string, len(req.OrderBy))
	for i, o := range req.OrderBy {
		dir := string(protocol.SortAsc)
		if o.Dir == protocol.SortDesc {
			dir = string(protocol.SortDesc)
		}
		parts[i] = d.Quote(o.Column) + " " + dir
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

func qualifyTable(d SQLDialect, schema, table string) string {
	if schema == "" {
		return d.Quote(table)
	}
	return d.Quote(schema) + "." + d.Quote(table)
}

// RowsResultToBrowsePage 把「按 limit+1 取回」的执行结果映射为浏览页：
// 超出 limit 时截断并置 has_more（behavior.md §13.2）。
func RowsResultToBrowsePage(res *protocol.QueryResult, offset, limit uint64) *protocol.TableRowsResult {
	rows := res.Rows
	hasMore := uint64(len(rows)) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return &protocol.TableRowsResult{
		Columns:         res.Columns,
		Rows:            rows,
		Offset:          offset,
		HasMore:         hasMore,
		ExecutionTimeMs: res.ExecutionTimeMs,
	}
}

// CountResultToUint64 从单行单列结果提取 COUNT(*)。
func CountResultToUint64(res *protocol.QueryResult) (uint64, error) {
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "count query returned no rows"}
	}
	v := res.Rows[0][0]
	if i, ok := protocol.TryGetInt(v); ok {
		if i < 0 {
			return 0, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "negative count"}
		}
		return uint64(i), nil
	}
	if s, ok := protocol.TryGetString(v); ok {
		var n uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil {
			return n, nil
		}
	}
	return 0, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "count query returned non-numeric value"}
}
