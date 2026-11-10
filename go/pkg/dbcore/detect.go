package dbcore

import (
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

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
	// 这些写语句的首关键字不在上面，§11 判型会把它们归入 select/other。
	"SETVAL": true, "REFRESH": true, "INTO": true, "LOAD": true, "DO": true, "OPTIMIZE": true,
}

// keywordThatIsAlsoAFunction 列出「既是写关键字又是 SQL 内建函数名」的歧义词。
// `REPLACE(name,'a','b')` 在 MySQL/SQLite/PG 里是纯读的字符串函数，但 REPLACE
// 同时是 MySQL 的写语句动词——旧判定把 `SELECT REPLACE(name,'a','b') FROM t`
// 判为写并在只读连接上拒绝。函数调用形式（词后紧跟 `(`）豁免：SQL 语句动词
// 后从不直接跟 `(`，而函数调用总是。
var keywordThatIsAlsoAFunction = map[string]bool{
	"REPLACE": true,
}

// MutatesData 判断语句是否包含变更关键字（behavior.md §12.3 只读拦截的判定基础）。
//
// 扫描整条语句（含 WITH 子树与多语句），**按 token 类别**判定：只认裸词
// token，关键字藏在 '…'、"…"、`…`、[…]、$$…$$、q'…' 这些字面量里、或藏在
// -- / # / /* */ 注释里，都不算命中——字符串里的 "delete"、名为 inserted_at
// 的列都不会误判。
//
// kind 是连接的方言：词法差异（'#' 行注释、反斜杠转义、PG 的 dollar-quote、
// Oracle 的 q'…'、MySQL 的 /*! … */）决定「字面量在哪结束」，认错方言会把
// 后面的写语句当成字面量吞掉（fail-open）。方言未知时取各剖面并集：任一剖面
// 认为写了就算写了——宁可多问，放过去就是丢数据。
func MutatesData(sql string, kind protocol.DatabaseKind) bool {
	for _, p := range profilesFor(kind) {
		toks := scanTokens(sql, p)
		for i, t := range toks {
			if t.kind != tokenWord {
				continue
			}
			word := strings.ToUpper(t.text)
			if !mutatingKeywords[word] {
				continue
			}
			// A keyword that is also a SQL function name, called as a function
			// (word immediately followed by `(`), is a read: MySQL's
			// `SELECT REPLACE(name,'a','b') FROM t` never writes. A statement
			// verb is never followed directly by an open paren. scanTokens
			// skips whitespace without emitting tokens, so the gap between the
			// word's end and the next token's start is whitespace.
			if keywordThatIsAlsoAFunction[word] && i+1 < len(toks) {
				next := toks[i+1]
				if next.kind == tokenOther && next.start >= t.end &&
					strings.HasPrefix(strings.TrimSpace(sql[t.end:next.start+1]), "(") {
					continue
				}
			}
			return true
		}
	}
	return false
}

// SplitStatements 按方言感知的分隔符切分 SQL：字面量与注释里的 ';' 不是分隔符。
//
// 一个方言可能有多个词法变体（反斜杠转义开/关），取切得**最细**的那一份：
// 多切出来的片段只会让只读闸门更严，不会把写语句藏进只读片段里。
// 方言未知时同样取各变体里最细的一份。
func SplitStatements(sql string, kind protocol.DatabaseKind) []string {
	best := []string{}
	for _, p := range profilesFor(kind) {
		if s := splitUnder(sql, p); len(s) > len(best) {
			best = s
		}
	}
	return best
}

// splitUnder 按给定词法剖面把 SQL 切成语句（跳过空片段）。
func splitUnder(sql string, p lexProfile) []string {
	var out []string
	start := 0
	for _, t := range scanTokens(sql, p) {
		if t.kind != tokenSemicolon {
			continue
		}
		if s := strings.TrimSpace(sql[start:t.start]); s != "" {
			out = append(out, s)
		}
		start = t.end
	}
	if s := strings.TrimSpace(sql[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// LeadingKeyword 返回语句的首个关键字（大写）；没有关键字时返回空串。
//
// 方言已知时按该方言判定；方言未知时要求各剖面结论一致，否则返回空串
// （判不出关键字 → 只读闸门按「不认识」处理，更严）。
func LeadingKeyword(sql string, kind protocol.DatabaseKind) string {
	profiles := profilesFor(kind)
	kw := ""
	for i, p := range profiles {
		k := leadingKeywordUnder(sql, p)
		if i == 0 {
			kw = k
			continue
		}
		if k != kw {
			return ""
		}
	}
	return kw
}

// leadingKeywordUnder 按给定词法剖面取首个关键字。
func leadingKeywordUnder(sql string, p lexProfile) string {
	for _, t := range scanTokens(sql, p) {
		if t.kind == tokenComment {
			continue
		}
		if t.kind != tokenWord {
			return ""
		}
		return strings.ToUpper(t.text)
	}
	return ""
}

// DetectStatementType 判定语句类型（按首个关键字整词判定），所有 SQL 驱动共用。
//
// 与 Rust 侧 db-core detect.rs 逐字一致（spec/behavior.md §11 双后端一致性红线）。
func DetectStatementType(sql string, kind protocol.DatabaseKind) protocol.StatementType {
	switch LeadingKeyword(sql, kind) {
	case "SELECT", "WITH":
		return protocol.StatementTypeSelect
	case "INSERT":
		return protocol.StatementTypeInsert
	case "UPDATE":
		return protocol.StatementTypeUpdate
	case "DELETE":
		return protocol.StatementTypeDelete
	case "CREATE", "ALTER", "DROP":
		return protocol.StatementTypeDDL
	default:
		return protocol.StatementTypeOther
	}
}

// StripLeadingComments 剥离 SQL 开头的前导注释（-- 行、# 行（MySQL）、/* */ 块）
// 与空白。各驱动统一走这里，避免带注释的 SELECT 被误判为 OTHER 而走 Exec 分支
// （双后端一致性红线）。
func StripLeadingComments(sql string, kind protocol.DatabaseKind) string {
	for _, t := range scanTokens(sql, profileForKind(kind)) {
		if t.kind == tokenComment {
			continue
		}
		return sql[t.start:]
	}
	return ""
}

// StripComments 去掉 SQL 里所有注释（-- 行、# 行（MySQL）、/* */ 块），保留其余
// 字符（含空白与字面量）。EXPLAIN 包装与任何需要「清洗掉所有注释」的地方统一走这里，
// 避免方言盲区（MySQL 的 '#' 行注释、PG 的 $$…$$ 字面量）造成误判。
//
// 重建方式：只跳过 tokenComment 的区间，其余字节原样保留。扫描器会把空白跳过
// （不 emit 成 token），空白因此落在 token 之间的间隔里，一并保留。MySQL 的
// /*! … */ 在扫描时已被当作代码重扫（见 scan.go），不会成为注释 token，内容原样
// 保留——这与 MySQL 服务端真的执行它一致。
func StripComments(sql string, kind protocol.DatabaseKind) string {
	p := profileForKind(kind)
	var b strings.Builder
	last := 0
	for _, t := range scanTokens(sql, p) {
		if t.kind == tokenComment {
			b.WriteString(sql[last:t.start])
			last = t.end
		}
	}
	b.WriteString(sql[last:])
	return b.String()
}

// profileForKind 返回该方言的词法剖面；方言未知时退回标准剖面
// （只影响「剥前导注释」这类非安全判定；安全判定走 profilesFor 的并集）。
func profileForKind(kind protocol.DatabaseKind) lexProfile {
	if p, ok := profileFor(kind); ok {
		return p
	}
	return standardProfile
}
