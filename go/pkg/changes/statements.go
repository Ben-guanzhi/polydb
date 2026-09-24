package changes

import (
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// QuoteIdent 按方言引用标识符（表 / 列 / schema 名）。
func QuoteIdent(kind protocol.DatabaseKind, n string) string {
	switch kind {
	case protocol.DatabaseKindMySQL:
		return "`" + strings.ReplaceAll(n, "`", "``") + "`"
	case protocol.DatabaseKindMSSQL:
		return "[" + strings.ReplaceAll(n, "]", "]]") + "]"
	default:
		return "\"" + strings.ReplaceAll(n, "\"", "\"\"") + "\""
	}
}

// QuoteString 按方言转义字符串字面量（MySQL 额外转义反斜杠）。
func QuoteString(kind protocol.DatabaseKind, s string) string {
	if kind == protocol.DatabaseKindMySQL {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ValueToLiteral 把值渲染为内联 SQL 字面量（Review 预览用）。
func ValueToLiteral(kind protocol.DatabaseKind, v protocol.Value) string {
	switch p := parseValue(v); p.kind {
	case vNull:
		return "NULL"
	case vBool:
		if p.b {
			return "TRUE"
		}
		return "FALSE"
	case vNumber:
		return p.num.String()
	case vString:
		if p.s == "" {
			return "''"
		}
		return QuoteString(kind, p.s)
	}
	return "NULL"
}

func qualifiedTable(kind protocol.DatabaseKind, schema, table string) string {
	t := QuoteIdent(kind, table)
	if schema == "" {
		return t
	}
	return QuoteIdent(kind, schema) + "." + t
}

func pkWhere(kind protocol.DatabaseKind, pks []string, row Row) string {
	parts := make([]string, 0, len(pks))
	for _, p := range pks {
		parts = append(parts, QuoteIdent(kind, p)+" = "+ValueToLiteral(kind, rowGet(row, p)))
	}
	return strings.Join(parts, " AND ")
}

// Statement 一条待提交语句：预览 SQL + 参数化 SQL + 行数校验元数据。
type Statement struct {
	SQL            string // 占位符已内联为字面量（Review 预览用）
	ParamSQL       string // 提交用的参数化 SQL（? 占位）
	Params         []protocol.Value
	ExpectAffected uint64
	AllowZero      bool // keyed update：actual 仅在 > ExpectAffected 才拦（同值更新报 0 正常）
}

// BuildStatements 依据变更队列生成提交语句（顺序 = 用户操作顺序）。
func BuildStatements(chs []RowChange, schema, table string, pkColumns []string, kind protocol.DatabaseKind) []Statement {
	qt := qualifiedTable(kind, schema, table)
	out := make([]Statement, 0, len(chs))
	for _, ch := range chs {
		switch ch.Type {
		case Delete:
			where := pkWhere(kind, pkColumns, ch.OriginalRow)
			pwhere := ph_where(kind, pkColumns)
			out = append(out, Statement{
				SQL:            "DELETE FROM " + qt + " WHERE " + where,
				ParamSQL:       "DELETE FROM " + qt + " WHERE " + pwhere,
				Params:         pkParams(pkColumns, ch.OriginalRow),
				ExpectAffected: 1,
			})
		case Insert:
			row := ch.NewRow
			cols := make([]string, 0, len(row))
			for _, c := range row {
				cols = append(cols, c.Column)
			}
			isDefault := func(c string) bool {
				v := rowGet(row, c)
				blank := v.IsNull()
				if !blank {
					if p := parseValue(v); p.kind == vString && p.s == "" {
						blank = true
					}
				}
				if !blank {
					return false
				}
				for _, pk := range pkColumns {
					if pk == c {
						return true
					}
				}
				return false
			}
			colsQuoted := make([]string, 0, len(cols))
			literals := make([]string, 0, len(cols))
			placeholders := make([]string, 0, len(cols))
			var params []protocol.Value
			for _, c := range cols {
				colsQuoted = append(colsQuoted, QuoteIdent(kind, c))
				if isDefault(c) {
					literals = append(literals, "DEFAULT")
					placeholders = append(placeholders, "DEFAULT")
				} else {
					literals = append(literals, ValueToLiteral(kind, rowGet(row, c)))
					placeholders = append(placeholders, "?")
					params = append(params, rowGet(row, c))
				}
			}
			head := "INSERT INTO " + qt + " (" + strings.Join(colsQuoted, ", ") + ") VALUES ("
			out = append(out, Statement{
				SQL:            head + strings.Join(literals, ", ") + ")",
				ParamSQL:       head + strings.Join(placeholders, ", ") + ")",
				Params:         params,
				ExpectAffected: 1,
			})
		case Update:
			sets := make([]string, 0, len(ch.CellChanges))
			psets := make([]string, 0, len(ch.CellChanges))
			var params []protocol.Value
			for _, c := range ch.CellChanges {
				sets = append(sets, QuoteIdent(kind, c.Column)+" = "+ValueToLiteral(kind, c.NewValue))
				psets = append(psets, QuoteIdent(kind, c.Column)+" = ?")
				params = append(params, c.NewValue)
			}
			where := pkWhere(kind, pkColumns, ch.OriginalRow)
			pwhere := ph_where(kind, pkColumns)
			params = append(params, pkParams(pkColumns, ch.OriginalRow)...)
			out = append(out, Statement{
				SQL:            "UPDATE " + qt + " SET " + strings.Join(sets, ", ") + " WHERE " + where,
				ParamSQL:       "UPDATE " + qt + " SET " + strings.Join(psets, ", ") + " WHERE " + pwhere,
				Params:         params,
				ExpectAffected: 1,
				AllowZero:      true,
			})
		}
	}
	return out
}

func ph_where(kind protocol.DatabaseKind, pks []string) string {
	parts := make([]string, 0, len(pks))
	for _, p := range pks {
		parts = append(parts, QuoteIdent(kind, p)+" = ?")
	}
	return strings.Join(parts, " AND ")
}

func pkParams(pks []string, row Row) []protocol.Value {
	out := make([]protocol.Value, 0, len(pks))
	for _, p := range pks {
		out = append(out, rowGet(row, p))
	}
	return out
}

// RowsMismatch 判定实际影响行数是否不符：
// delete/insert 期望恰 1；keyed update 仅当 actual > ExpectAffected 才拦。
func RowsMismatch(s Statement, actual uint64) bool {
	if s.AllowZero {
		return actual > s.ExpectAffected
	}
	return actual != s.ExpectAffected
}
