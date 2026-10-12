package dbcore

import (
	"strings"
	"unicode"

	"github.com/polydb/polydb/pkg/protocol"
)

// StripLeadingComments 剥离 SQL 开头的前导注释（-- 行注释与 /* */ 块注释）与空白。
// 各驱动统一走这里，避免带注释的 SELECT 被误判为 OTHER 而走 Exec 分支（双后端一致性红线）。
func StripLeadingComments(sql string) string {
	s := sql
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
			} else {
				return ""
			}
		default:
			return s
		}
	}
}

// DetectStatementType 判定语句类型（剥离前导注释后），所有 SQL 驱动共用。
func DetectStatementType(sql string) protocol.StatementType {
	t := strings.ToUpper(strings.TrimSpace(StripLeadingComments(sql)))
	switch {
	case strings.HasPrefix(t, "SELECT"), strings.HasPrefix(t, "WITH"):
		return protocol.StatementTypeSelect
	case strings.HasPrefix(t, "INSERT"):
		return protocol.StatementTypeInsert
	case strings.HasPrefix(t, "UPDATE"):
		return protocol.StatementTypeUpdate
	case strings.HasPrefix(t, "DELETE"):
		return protocol.StatementTypeDelete
	case strings.HasPrefix(t, "CREATE"), strings.HasPrefix(t, "ALTER"), strings.HasPrefix(t, "DROP"):
		return protocol.StatementTypeDDL
	default:
		return protocol.StatementTypeOther
	}
}

// mutatingKeywords 会变更数据或结构的 SQL 关键字（整词、大小写不敏感）。
// 只读连接据此拦截，不再复用 DetectStatementType 的语句分类——后者对
// `WITH d AS (DELETE …) SELECT …`（数据修改 CTE）与 TRUNCATE/GRANT 等
// 归入 select/other 的写型语句一律放行，会击穿只读红线。
var mutatingKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true,
	"REPLACE": true, "TRUNCATE": true, "GRANT": true, "REVOKE": true,
	"CREATE": true, "ALTER": true, "DROP": true, "CALL": true,
	"EXEC": true, "EXECUTE": true, "COPY": true, "VACUUM": true,
	"REINDEX": true, "CLUSTER": true, "LOCK": true, "COMMENT": true,
	// 这些写语句的首关键字不在上面，§11 判型会把它们归入 select/other 而放行。
	"SETVAL": true, "REFRESH": true, "INTO": true, "LOAD": true, "DO": true, "OPTIMIZE": true,
}

// MutatesData 判断语句是否包含变更关键字（behavior.md §12.3 只读拦截的判定基础）。
// 扫描整条语句（含 WITH 子树与多语句），先剥离 -- / /* */ 注释与 '…'、"…"、
// `…`、[…] 字面量再整词匹配：字符串里的 "delete"、名为 inserted_at 的列都不会误判。
func MutatesData(sql string) bool {
	stripped := stripCommentsAndLiterals(sql)
	for start := 0; start < len(stripped); {
		for start < len(stripped) && !isWordByte(stripped[start]) {
			start++
		}
		end := start
		for end < len(stripped) && isWordByte(stripped[end]) {
			end++
		}
		if start < end && mutatingKeywords[strings.ToUpper(stripped[start:end])] {
			return true
		}
		start = end
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// stripCommentsAndLiterals 把注释与引号字面量替换为空格后返回文本。
func stripCommentsAndLiterals(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			i = skipQuoted(sql, i, c)
			b.WriteByte(' ')
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && sql[i+1] == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i = i + 2 + end + 2
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipQuoted 越过从 sql[start] 开始的引号字面量，返回结束引号之后的下标。
// 处理 ”（双写）与 MySQL 反斜杠转义；[] 标识符只按 ] 结束。
func skipQuoted(sql string, start int, quote byte) int {
	i := start + 1
	n := len(sql)
	for i < n {
		switch {
		case sql[i] == '\\' && quote == '\'':
			i += 2
		case sql[i] == quote && quote == '[':
			return i + 1
		case sql[i] == quote:
			if i+1 < n && sql[i+1] == quote {
				i += 2
			} else {
				return i + 1
			}
		default:
			i++
		}
	}
	return n
}
