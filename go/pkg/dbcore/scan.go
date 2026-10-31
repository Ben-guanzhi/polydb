package dbcore

import (
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// tokenKind 是词法单元的类别。
//
// 词法层只做一件事：把输入切成 token 并标出类别；「这是不是写语句」的判断
// 一律读 token 的类别，绝不在「清洗过的文本」上做子串搜索——后者每次都要
// 重新发明一遍词边界判断，且总有漏网的引号形式（db-dev 的教训）。
type tokenKind int

const (
	// tokenWord 是裸词：标识符或关键字。
	tokenWord tokenKind = iota
	// tokenQuoted 是引号标识符："x"（标准 SQL）、`x`（MySQL）、[x]（T-SQL）。
	tokenQuoted
	// tokenLiteral 是字符串字面量：'x'（含 PostgreSQL 的 dollar-quote 与 Oracle 的 q'…'）。
	tokenLiteral
	// tokenComment 是注释：-- 行、# 行（仅 MySQL/MariaDB）、/* */ 块。
	tokenComment
	// tokenSemicolon 是语句分隔符 ';'。
	tokenSemicolon
	// tokenOther 是其余记号（运算符、括号、逗号等）。
	tokenOther
)

// token 是一个词法单元。
type token struct {
	kind tokenKind
	// text 是 token 文本。tokenWord 是词本身；其余是原始片段。
	text string
	// start / end 是 token 在原文中的字节区间（end 不含）。
	start, end int
}

// lexProfile 描述一个方言的词法差异。
//
// 只读红线的正确性取决于「我们认为在哪结束一个字面量 / 注释」与「数据库
// 认为在哪结束」一致。两者不一致时，后面的写语句会被当成字面量吞掉——
// 这不是理论风险，而是已实测过的真实绕过：
//
//   - '#' 在 MySQL/MariaDB 是行注释，在 PostgreSQL 是位异或运算符。
//     一律当注释 → PG 上 `SELECT 1 # 2; DROP TABLE t` 的 DROP 被吞掉。
//     一律不当注释 → MySQL 上 `SELECT 1 # it's ok` 的撇号开启字面量，
//     把后面的写语句一起吞掉。
//   - 反斜杠转义只在 MySQL 恒成立；PostgreSQL 的普通字符串里反斜杠是普通
//     字符（standard_conforming_strings=on），只有 E'…' / U&'…' 才转义。
//   - '--' 在 MySQL 必须跟空白才是注释（`1--2` 是减法），其他方言无条件成立。
//   - PostgreSQL 的 $$…$$ 与 Oracle 的 q'…' 都是「字面量里可以藏引号」的
//     形式；不认识它们，后面的写语句会被前一个引号吞掉。
//   - MySQL 的 /*! … */ 内容会被服务端执行，当普通注释吞掉等于放行里面的写语句。
type lexProfile struct {
	// hashComment：'#' 是行注释（仅 MySQL/MariaDB）。
	hashComment bool
	// dashDashNeedsSpace：'--' 后必须跟空白/控制符才是注释（仅 MySQL）。
	dashDashNeedsSpace bool
	// backslashEscape：'…' 与 "…" 里的反斜杠是转义符（仅 MySQL）。
	backslashEscape bool
	// eString：E'…' / U&'…' 里反斜杠是转义符（仅 PostgreSQL）。
	eString bool
	// dollarQuote：$$…$$ / $tag$…$tag$ 是字符串字面量（仅 PostgreSQL）。
	dollarQuote bool
	// execComment：/*! … */ 与 /*M! … */ 的内容会被服务端执行（MySQL/MariaDB）。
	execComment bool
	// qQuote：q'…' 是备选引用（仅 Oracle），内容里可以含引号。
	qQuote bool
}

// mysqlProfile 是 MySQL / MariaDB 的词法剖面。
var mysqlProfile = lexProfile{
	hashComment:        true,
	dashDashNeedsSpace: true,
	backslashEscape:    true,
	execComment:        true,
}

// mysqlProfileNoEscape 是 MySQL 的第二变体：反斜杠**不是**转义符
// （服务端开了 NO_BACKSLASH_ESCAPES 时就是这个语义）。
var mysqlProfileNoEscape = lexProfile{
	hashComment:        true,
	dashDashNeedsSpace: true,
	execComment:        true,
}

// postgresProfile 是 PostgreSQL 的词法剖面（standard_conforming_strings=on：
// 普通字符串里反斜杠是普通字符，只有 E'…' / U&'…' 才转义）。
var postgresProfile = lexProfile{
	eString:     true,
	dollarQuote: true,
}

// postgresProfileEscape 是 PostgreSQL 的第二变体：
// standard_conforming_strings=off 时普通字符串也认反斜杠转义。
var postgresProfileEscape = lexProfile{
	eString:         true,
	dollarQuote:     true,
	backslashEscape: true,
}

// oracleProfile 是 Oracle 的词法剖面。
var oracleProfile = lexProfile{qQuote: true}

// standardProfile 是 SQLite / SQL Server 的词法剖面。
var standardProfile = lexProfile{}

// profileFor 返回该方言的词法剖面。第二个返回值报告方言是否已知：
// 未知时（空串 / Redis 等）调用方应改用「各剖面并集」的保守判定。
func profileFor(kind protocol.DatabaseKind) (lexProfile, bool) {
	switch kind {
	case protocol.DatabaseKindMySQL:
		return mysqlProfile, true
	case protocol.DatabaseKindPostgres:
		return postgresProfile, true
	case protocol.DatabaseKindOracle:
		return oracleProfile, true
	case protocol.DatabaseKindSQLite, protocol.DatabaseKindMSSQL:
		return standardProfile, true
	default:
		return standardProfile, false
	}
}

// allProfiles 列出全部已知剖面：方言未知时取它们的并集（任一解释命中即判写）。
var allProfiles = []lexProfile{
	mysqlProfile, mysqlProfileNoEscape,
	postgresProfile, postgresProfileEscape,
	oracleProfile, standardProfile,
}

// profilesFor 返回判定该方言时应当扫描的词法剖面集合。
//
// MySQL 与 PostgreSQL 各给两个变体，差别只在「反斜杠是不是转义符」。
// 这个语义由**服务端**设置决定（MySQL 的 NO_BACKSLASH_ESCAPES、
// PostgreSQL 的 standard_conforming_strings），而连接配置里看不到这些设置。
// 两种解释都扫一遍：认错的后果不是「多问一次」，而是把后面的写语句当成
// 字面量吞掉——直接丢数据。Oracle / SQLite / SQL Server 从不把反斜杠当
// 转义符，只有一个变体。
//
// 方言未知时取全部（并集 = fail-closed）。
func profilesFor(kind protocol.DatabaseKind) []lexProfile {
	switch kind {
	case protocol.DatabaseKindMySQL:
		return []lexProfile{mysqlProfile, mysqlProfileNoEscape}
	case protocol.DatabaseKindPostgres:
		return []lexProfile{postgresProfile, postgresProfileEscape}
	case protocol.DatabaseKindOracle:
		return []lexProfile{oracleProfile}
	case protocol.DatabaseKindSQLite, protocol.DatabaseKindMSSQL:
		return []lexProfile{standardProfile}
	default:
		return allProfiles
	}
}

// scanTokens 把 SQL 切成 token 序列（一次左到右扫描）。
func scanTokens(sql string, p lexProfile) []token {
	return scanRange(sql, 0, len(sql), p)
}

// scanRange 把 sql[from:to) 切成 token 序列。
// 嵌套扫描（MySQL 的 /*! … */ 内容）用它把子区间按同一剖面重新切一遍。
func scanRange(sql string, from, to int, p lexProfile) []token {
	var out []token
	i := from
	for i < to {
		c := sql[i]
		switch {
		case isSpaceByte(c):
			i++
			continue
		case c == '-' && i+1 < to && sql[i+1] == '-':
			// MySQL 的 '--' 必须跟空白才是注释；`1--2` 是减法。
			if p.dashDashNeedsSpace && i+2 < to && !isSpaceByte(sql[i+2]) {
				out = append(out, token{kind: tokenOther, text: "-", start: i, end: i + 1})
				i++
				continue
			}
			j := lineEnd(sql, i, to)
			out = append(out, token{kind: tokenComment, text: sql[i:j], start: i, end: j})
			i = j
			continue
		case c == '#' && p.hashComment:
			j := lineEnd(sql, i, to)
			out = append(out, token{kind: tokenComment, text: sql[i:j], start: i, end: j})
			i = j
			continue
		case c == '/' && i+1 < to && sql[i+1] == '*':
			j, _ := blockEnd(sql, i, to)
			// MySQL/MariaDB 的 /*! … */（与 /*M! … */）内容会被服务端执行：
			// 按注释吞掉等于放行里面的写语句，所以改成按代码再扫一遍。
			if p.execComment && j > i+2 {
				if inner, ok := execCommentBody(sql, i, j); ok {
					out = append(out, scanRange(sql, inner.start, inner.end, p)...)
					i = j
					continue
				}
			}
			out = append(out, token{kind: tokenComment, text: sql[i:j], start: i, end: j})
			i = j
			continue
		case c == '\'':
			// Oracle 的 q'…'：内容里可以含引号，别让前一个引号把后面吞掉。
			if p.qQuote && len(out) > 0 {
				prev := out[len(out)-1]
				if prev.kind == tokenWord && strings.EqualFold(prev.text, "q") && prev.end == i {
					j := skipQQuoted(sql, i, to)
					out = append(out, token{kind: tokenLiteral, text: sql[i:j], start: i, end: j})
					i = j
					continue
				}
			}
			j := skipQuoted(sql, i, '\'', p.backslashEscape || (p.eString && isEStringPrefix(sql, i)), to)
			out = append(out, token{kind: tokenLiteral, text: sql[i:j], start: i, end: j})
			i = j
			continue
		case c == '"' || c == '`' || c == '[':
			j := skipQuoted(sql, i, c, p.backslashEscape, to)
			out = append(out, token{kind: tokenQuoted, text: sql[i:j], start: i, end: j})
			i = j
			continue
		case c == '$' && p.dollarQuote:
			if j, ok := skipDollarQuoted(sql, i, to); ok {
				out = append(out, token{kind: tokenLiteral, text: sql[i:j], start: i, end: j})
				i = j
				continue
			}
			out = append(out, token{kind: tokenOther, text: "$", start: i, end: i + 1})
			i++
			continue
		case c == ';':
			out = append(out, token{kind: tokenSemicolon, text: ";", start: i, end: i + 1})
			i++
			continue
		case isWordByte(c):
			j := i
			for j < to && isWordByte(sql[j]) {
				j++
			}
			out = append(out, token{kind: tokenWord, text: sql[i:j], start: i, end: j})
			i = j
			continue
		default:
			// 其余记号：按整个 UTF-8 字符推进，保证 token 边界落在字符边界上
			// （Rust 侧按字节切片，边界落在字符中间会 panic）。
			j := advanceChar(sql, i)
			out = append(out, token{kind: tokenOther, text: sql[i:j], start: i, end: j})
			i = j
		}
	}
	return out
}

// byteSpan 是一个字节区间 [start, end)。
type byteSpan struct{ start, end int }

// execCommentBody 判定 sql[i:j] 是否是 MySQL 的可执行注释（/*! … */ 或 /*M! … */），
// 返回其中「会被服务端执行」的正文区间。
//
// MySQL 的注释体形如 `/*!50000 SELECT 1 */`：'!' 之后可跟 5 位版本号。
// 正文从版本号之后开始，到对应的 "*/" 之前结束。
func execCommentBody(sql string, i, j int) (byteSpan, bool) {
	inner := i + 2
	if inner >= j {
		return byteSpan{}, false
	}
	switch {
	case sql[inner] == '!':
		inner++
	case sql[inner] == 'M' && inner+1 < j && sql[inner+1] == '!':
		inner += 2
	default:
		return byteSpan{}, false
	}
	// 可选的 5 位版本号（MySQL 会剥掉它）。
	digits := 0
	for inner+digits < j && digits < 5 && sql[inner+digits] >= '0' && sql[inner+digits] <= '9' {
		digits++
	}
	inner += digits
	end := j
	if strings.HasSuffix(sql[i:j], "*/") {
		end = j - 2
	}
	if inner > end {
		return byteSpan{}, false
	}
	return byteSpan{start: inner, end: end}, true
}

// blockEnd 返回 sql[i:] 处块注释的结束位置（不含 "*/"）与是否闭合。
// 未闭合时返回 to（服务端会报语法错，整条不会执行）。
func blockEnd(sql string, i, to int) (int, bool) {
	if end := strings.Index(sql[i+2:to], "*/"); end >= 0 {
		return i + 2 + end + 2, true
	}
	return to, false
}

// lineEnd 返回从 start 起的行注释结束位置（不含换行符；到区间末则返回 limit）。
func lineEnd(sql string, start, limit int) int {
	if idx := strings.IndexByte(sql[start:limit], '\n'); idx >= 0 {
		return start + idx
	}
	return limit
}

// skipQuoted 越过从 sql[start] 开始的引号区域，返回结束引号之后的下标。
// 处理 ''（双写）；backslash 为真时还处理反斜杠转义。
// [] 标识符只按 ] 结束。
func skipQuoted(sql string, start int, quote byte, backslash bool, limit int) int {
	i := start + 1
	for i < limit {
		switch {
		case backslash && sql[i] == '\\':
			// 转义的是「反斜杠 + 一个字符」；按整个字符推进以保证边界对齐。
			i = advanceChar(sql, i+1)
		case sql[i] == quote && quote == '[':
			return i + 1
		case sql[i] == quote:
			if i+1 < limit && sql[i+1] == quote {
				i += 2
			} else {
				return i + 1
			}
		default:
			i = advanceChar(sql, i)
		}
	}
	return limit
}

// skipQQuoted 越过 Oracle 的 q'…' 备选引用（sql[start] 是开引号）。
//
// 形式是 q'<定界符>正文<定界符>'：定界符成对时（[ ] ( ) { } < >）按配对计数，
// 不成对时按「定界符后紧跟引号」结束。正文里可以含引号。
func skipQQuoted(sql string, start, limit int) int {
	if start+1 >= limit {
		return limit
	}
	d := sql[start+1]
	var closeCh byte
	switch d {
	case '[':
		closeCh = ']'
	case '(':
		closeCh = ')'
	case '{':
		closeCh = '}'
	case '<':
		closeCh = '>'
	default:
		closeCh = d
	}
	if closeCh == d {
		// 非配对定界符：找到「定界符 + '」才算结束。
		for j := start + 2; j < limit; j++ {
			if sql[j] == d && j+1 < limit && sql[j+1] == '\'' {
				return j + 2
			}
		}
		return limit
	}
	depth := 1
	for j := start + 2; j < limit; j++ {
		switch sql[j] {
		case d:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				if j+1 < limit && sql[j+1] == '\'' {
					return j + 2
				}
				return j + 1
			}
		}
	}
	return limit
}

// isEStringPrefix 报告 sql[start] 处的单引号是否属于 E'…' / U&'…' 前缀。
// PostgreSQL 只有这两种前缀的字面量才认反斜杠转义。
func isEStringPrefix(sql string, start int) bool {
	if start == 0 {
		return false
	}
	c := sql[start-1]
	if c == 'e' || c == 'E' {
		return start == 1 || !isWordByte(sql[start-2])
	}
	if c == '&' && start >= 2 {
		u := sql[start-2]
		return (u == 'u' || u == 'U') && (start == 2 || !isWordByte(sql[start-3]))
	}
	return false
}

// skipDollarQuoted 越过从 sql[start] 开始的 PostgreSQL dollar-quote，返回结束位置。
//
// 三个约束缺一不可：
//   - 定界符必须是 $$ 或 $tag$（标签首字符是字母或下划线）——$1 是占位符；
//   - '$' 前不能紧邻标识符字符——PG 规则：紧跟关键字/标识符的 $tag$ 会被
//     当成标识符的一部分（`a$b$c` 是一个标识符，不是 dollar-quote）；
//   - 内容可以含任意字符（含撇号），但必须由**同一个**定界符闭合。
//
// 未闭合时取到区间末（定界符缺失本身就是语法错误，不会多执行别的语句）。
func skipDollarQuoted(sql string, start, limit int) (int, bool) {
	if start > 0 && isIdentByte(sql[start-1]) {
		return 0, false
	}
	tagLen, ok := dollarTagLen(sql, start, limit)
	if !ok {
		return 0, false
	}
	bodyStart := start + tagLen
	idx := strings.Index(sql[bodyStart:limit], sql[start:start+tagLen])
	if idx < 0 {
		return limit, true
	}
	return bodyStart + idx + tagLen, true
}

// dollarTagLen 返回 sql[i:] 处的 dollar-quote 定界符长度（$$ 为 2，$tag$ 为
// len(tag)+2），不是定界符则返回 false。
//
// 标签首字符必须是字母或下划线：$1 这类占位符不算 dollar-quote
// （与 db-dev internal/db 的 dollarQuoteAt 同约定）。
func dollarTagLen(sql string, i, limit int) (int, bool) {
	if i >= limit || sql[i] != '$' {
		return 0, false
	}
	j := i + 1
	if j < limit && sql[j] == '$' {
		// 空标签定界符 $$：长度 2。这里必须提前返回——再往下走一次
		// 「必须以 $ 收尾」的检查会把 $$ 误判成非定界符。
		return 2, true
	}
	if j < limit && (sql[j] == '_' || isAlphaByte(sql[j])) {
		j++
		for j < limit && (sql[j] == '_' || isAlphaByte(sql[j]) || isDigitByte(sql[j])) {
			j++
		}
		if j < limit && sql[j] == '$' {
			return j + 1 - i, true
		}
	}
	return 0, false
}

// isWordByte 报告字节是否是裸词字符（字母、数字、下划线）。
func isWordByte(c byte) bool {
	return c == '_' || isAlphaByte(c) || isDigitByte(c)
}

// isIdentByte 报告字节是否是标识符字符（裸词字符或 '$'）。
// PG 的标识符可以含 '$'，所以 dollar-quote 的前置检查用它。
func isIdentByte(c byte) bool {
	return isWordByte(c) || c == '$'
}

func isAlphaByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isDigitByte(c byte) bool {
	return c >= '0' && c <= '9'
}

// isSpaceByte 报告字节是否是空白或控制符（MySQL 的 '--' 注释要求其后是这类字符）。
func isSpaceByte(c byte) bool {
	if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
		return true
	}
	return c < 0x20
}

// advanceChar 返回 sql[i] 处字符之后的下标（按 UTF-8 字符推进）。
//
// token 边界必须落在字符边界上：Rust 侧按字节切片，边界落在字符中间会 panic；
// Go 侧虽然不会 panic，但切出来的字符串会含半个字符。两侧统一按字符推进。
func advanceChar(sql string, i int) int {
	if i >= len(sql) {
		return i
	}
	if sql[i] < 0x80 {
		return i + 1
	}
	j := i + 1
	for j < len(sql) && sql[j]&0xC0 == 0x80 {
		j++
	}
	return j
}
