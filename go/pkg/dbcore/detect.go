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
