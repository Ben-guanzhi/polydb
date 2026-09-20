package dbcore

import (
	"fmt"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// 表数据浏览的共享查询构造器（behavior.md §13）：条件值一律走绑定参数，
// 标识符由各方言的 Quote 函数引用（内部引号翻倍），绝不拼接字面量。
// 各 SQL 驱动的 BrowseRows / BrowseRowsCount 复用这里，方言差异只在
// SQLDialect 的三个字段里。

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
}

// DefaultSQLDialect：ANSI 双引号标识符 + LIMIT/OFFSET 分页（sqlite / postgres）。
func DefaultSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteDouble,
		LimitClause: limitOffsetClause,
	}
}

// MySQLSQLDialect：反引号标识符 + LIMIT/OFFSET。
func MySQLSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteBacktick,
		LimitClause: limitOffsetClause,
	}
}

// MSSQLSQLDialect：方括号标识符 + OFFSET/FETCH（要求 ORDER BY）。
func MSSQLSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:           quoteBracket,
		LimitClause:     offsetFetchClause,
		OrderByFallback: "(SELECT NULL)",
	}
}

// OracleSQLDialect：双引号标识符 + OFFSET/FETCH（12c+，无 ORDER BY 也可）。
func OracleSQLDialect() SQLDialect {
	return SQLDialect{
		Quote:       quoteDouble,
		LimitClause: offsetFetchClause,
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

// BrowseRowsLimits 应用 behavior.md §13.2 的分页钳制：limit 缺省 200，上限 10000。
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
			protocol.FilterOpGt, protocol.FilterOpGe, protocol.FilterOpLike, protocol.FilterOpNotLike:
			if cond.Value.IsNull() {
				return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("condition %d: op %s requires value", i, cond.Op)}
			}
		case protocol.FilterOpNull, protocol.FilterOpNotNull:
			// 无需值
		default:
			return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("unknown filter op: %s", cond.Op)}
		}
	}
	return nil
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
		default:
			opSQL, ok := map[protocol.FilterOperator]string{
				protocol.FilterOpEq:      "=",
				protocol.FilterOpNe:      "<>",
				protocol.FilterOpLt:      "<",
				protocol.FilterOpLe:      "<=",
				protocol.FilterOpGt:      ">",
				protocol.FilterOpGe:      ">=",
				protocol.FilterOpLike:    "LIKE",
				protocol.FilterOpNotLike: "NOT LIKE",
			}[cond.Op]
			if !ok {
				return "", nil, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "unknown filter op: " + string(cond.Op)}
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
