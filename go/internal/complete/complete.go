package complete

import (
	"fmt"
	"sort"
	"strings"
)

// Schema is everything the completer knows about the queryable world:
// the catalog of tables (with their columns, which may be nil while a live
// connection is still being interrogated) and the columns of the frame
// currently under view.
type Schema struct {
	Tables  map[string][]string // table name -> column names (nil = unknown yet)
	Current []string            // columns of the current frame
	Engine  string              // database engine kind (e.g., "sqlserver", "oracle")
}

// Suggestion is one completion candidate. Text is the full replacement for
// the token being completed (functions carry a trailing "("), Kind is one of
// "column", "table", "function" or "keyword", and Detail is extra context
// such as the owning table of a column.
type Suggestion struct {
	Text   string
	Kind   string
	Detail string
}

// Suggestion kinds.
const (
	KindColumn   = "column"
	KindTable    = "table"
	KindFunction = "function"
	KindKeyword  = "keyword"
)

// sqlKeywords is the fixed set of non-function SQL words offered by the
// completer, stored uppercase and pre-sorted alphabetically.
var sqlKeywords = []string{
	"ALL", "AND", "AS", "ASC", "BETWEEN", "BY", "CASE", "CROSS", "DELETE",
	"DESC", "DISTINCT", "ELSE", "END", "EXCEPT", "EXISTS", "FROM", "FULL",
	"GLOB", "GROUP", "HAVING", "IN", "INNER", "INSERT", "INTERSECT", "INTO",
	"IS", "JOIN", "LEFT", "LIKE", "LIMIT", "NOT", "NULL", "OFFSET", "ON",
	"OR", "ORDER", "OUTER", "RIGHT", "SELECT", "SET", "TABLE", "THEN",
	"UNION", "UPDATE", "USING", "VALUES", "WHEN", "WHERE", "WITH",
}

// engineKeywords are additional keywords specific to each database engine.
var engineKeywords = map[string][]string{
	"sqlserver": {
		"TOP", "IDENTITY", "NVARCHAR", "VARCHAR", "INT", "BIGINT", "BIT",
		"DATETIME", "DECIMAL", "FLOAT", "MONEY", "REAL", "SMALLINT", "TINYINT",
		"CREATE", "ALTER", "DROP", "TRUNCATE", "EXEC", "EXECUTE", "PROCEDURE",
		"FUNCTION", "VIEW", "INDEX", "TRIGGER", "CONSTRAINT", "PRIMARY", "FOREIGN",
		"KEY", "REFERENCES", "UNIQUE", "CHECK", "DEFAULT", "IF", "ELSE", "BEGIN",
		"END", "DECLARE", "SET", "PRINT", "RETURN", "WHILE", "BREAK", "CONTINUE",
		"WAITFOR", "DELAY", "RETRY", "TRY", "CATCH", "THROW", "RAISERROR",
		"MERGE", "PIVOT", "UNPIVOT", "APPLY", "OUTER", "CROSS", "FULL", "INNER",
		"LEFT", "RIGHT", "TOP", "PERCENT", "WITH", "TIES", "OFFSET", "FETCH",
		"NEXT", "ROWS", "ONLY", "OPTION", "RECOMPILE", "KEEP", "FIXEDPLAN",
		"MAXDOP", "MAXRECURSION", "FORCE", "HINT",
	},
	// Oracle words come from Oracle's own dictionary: the [PLSQL] and [SQL]
	// sections of PL/SQL Developer's default.kwf, merged into
	// keywords_oracle_gen.go. Both sections are needed: MINUS, MERGE and
	// CONNECT are plain SQL words that live only in [PLSQL]. This replaces
	// the earlier hand-picked ~60 words, which also repeated MATCHED, NOT,
	// UNION, ALL, ROWNUM and ROWID.
	"oracle": dedupSorted(oracleSQLKeywords, oracleSQLExtra),
}

// oracleSQLExtra are Oracle words that default.kwf omits but Oracle accepts:
// the CONNECT BY predicates and the SYS_CONNECT_BY_PATH path function. They
// are added by hand because default.kwf is an external artifact we cannot
// patch.
var oracleSQLExtra = []string{
	"CONNECT_BY_ISCYCLE", "CONNECT_BY_ISLEAF", "CONNECT_BY_ROOT",
	"SYS_CONNECT_BY_PATH",
}

// sqlFunctions is the set of built-in functions offered by the completer
// (suggested with a trailing "("), stored uppercase and pre-sorted.
var sqlFunctions = []string{
	"ABS", "AVG", "CAST", "COALESCE", "COUNT", "DATE", "DATETIME",
	"GROUP_CONCAT", "HEX", "IFNULL", "INSTR", "JULIANDAY", "LENGTH", "LOWER",
	"LTRIM", "MAX", "MIN", "NULLIF", "PRINTF", "QUOTE", "REPLACE", "ROUND",
	"RTRIM", "STRFTIME", "SUBSTR", "SUM", "TIME", "TOTAL", "TRIM", "TYPEOF",
	"UPPER",
}

// engineFunctions are additional functions specific to each database engine.
var engineFunctions = map[string][]string{
	"sqlserver": {
		"ASCII", "CHAR", "CHARINDEX", "CONCAT", "CONCAT_WS", "DIFFERENCE",
		"FORMAT", "LEFT", "LEN", "LOWER", "LTRIM", "NCHAR", "PATINDEX",
		"QUOTENAME", "REPLICATE", "REVERSE", "RIGHT", "RTRIM", "SOUNDEX",
		"SPACE", "STR", "STRING_AGG", "STRING_ESCAPE", "STRING_SPLIT",
		"STUFF", "SUBSTRING", "TRANSLATE", "TRIM", "UNICODE", "UPPER",
		"CAST", "CONVERT", "TRY_CAST", "TRY_CONVERT", "TRY_PARSE",
		"ABS", "ACOS", "ASIN", "ATAN", "ATN2", "CEILING", "COS", "COT",
		"DEGREES", "EXP", "FLOOR", "LOG", "LOG10", "PI", "POWER", "RADIANS",
		"RAND", "ROUND", "SIGN", "SIN", "SQRT", "TAN", "CHECKSUM", "CHECKSUM_AGG",
		"COUNT", "COUNT_BIG", "GROUPING", "GROUPING_ID", "MAX", "MIN", "SUM", "AVG",
		"CURRENT_TIMESTAMP", "DATEADD", "DATEDIFF", "DATEFROMPARTS", "DATENAME",
		"DATEPART", "DAY", "DAYOFYEAR", "EOMONTH", "GETDATE", "GETUTCDATE",
		"ISDATE", "MONTH", "SYSDATETIME", "SYSDATETIMEOFFSET", "SYSUTCDATETIME",
		"TODATETIMEOFFSET", "YEAR", "JSON_VALUE", "JSON_QUERY", "JSON_MODIFY",
		"ISJSON", "OPENJSON",
	},
	"oracle": {
		"ASCII", "CHR", "CONCAT", "INITCAP", "INSTR", "INSTRB", "LENGTH",
		"LENGTHB", "LOWER", "LPAD", "LTRIM", "NLS_INITCAP", "NLS_LOWER",
		"NLS_UPPER", "NLSSORT", "REPLACE", "RPAD", "RTRIM", "SOUNDEX",
		"SUBSTR", "SUBSTRB", "TRANSLATE", "TRIM", "UPPER", "ABSORB",
		"ACOS", "ASIN", "ATAN", "ATAN2", "CEIL", "COS", "COSH", "EXP",
		"FLOOR", "LN", "LOG", "MOD", "POWER", "ROUND", "SIGN", "SIN",
		"SINH", "SQRT", "TAN", "TANH", "TRUNC", "WIDTH_BUCKET",
		"AVG", "COUNT", "GROUPING", "MAX", "MIN", "STDDEV", "STDDEV_POP",
		"STDDEV_SAMP", "SUM", "VAR_POP", "VAR_SAMP", "VARIANCE",
		"ADD_MONTHS", "LAST_DAY", "MONTHS_BETWEEN", "NEXT_DAY", "ROUND",
		"SYSDATE", "TO_CHAR", "TO_DATE", "TO_NUMBER", "TRUNC",
		"CAST", "COALESCE", "DECODE", "GREATEST", "LEAST", "NVL", "NVL2",
		"NULLIF", "REMAINDER", "LNNVL", "NULLS", "FIRST", "LAST",
	},
}

// reservedWords is the union of keywords and function names, used to decide
// that an identifier in a FROM clause cannot be a table alias.
var reservedWords = func() map[string]bool {
	m := make(map[string]bool, len(sqlKeywords)+len(sqlFunctions))
	for _, k := range sqlKeywords {
		m[k] = true
	}
	for _, f := range sqlFunctions {
		m[f] = true
	}
	return m
}()

// dedupSorted concatenates parts, dropping empty entries and duplicates
// (compared case-insensitively - a word is either in the dictionary or
// it is not), and returns the result sorted ascending.
func dedupSorted(parts ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range parts {
		for _, w := range p {
			if w == "" || seen[strings.ToLower(w)] {
				continue
			}
			seen[strings.ToLower(w)] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// Keywords returns every SQL word known to the completer (keywords and
// function names, without the "(" suffix), sorted.
func Keywords() []string {
	out := make([]string, 0, len(sqlKeywords)+len(sqlFunctions))
	out = append(out, sqlKeywords...)
	out = append(out, sqlFunctions...)
	sort.Strings(out)
	return out
}

// EngineKeywords returns the union of base SQL keywords, engine-specific
// keywords, and function names for a given engine kind. If engine is empty,
// only base keywords and functions are returned.
func EngineKeywords(engine string) []string {
	var parts [][]string
	parts = append(parts, sqlKeywords, sqlFunctions)
	if engineKw, ok := engineKeywords[engine]; ok {
		parts = append(parts, engineKw)
	}
	if engineFn, ok := engineFunctions[engine]; ok {
		parts = append(parts, engineFn)
	}
	return dedupSorted(parts...)
}

// tableCtxWords introduce a table-name position (context a).
var tableCtxWords = map[string]bool{
	"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true,
}

// columnCtxWords introduce a column position (context c).
var columnCtxWords = map[string]bool{
	"SELECT": true, "WHERE": true, "ON": true, "AND": true, "OR": true,
	"BY": true, "HAVING": true, "SET": true,
}

// ctxKind classifies the token position under the cursor.
type ctxKind int

const (
	ctxOther  ctxKind = iota // context d: anywhere else
	ctxTable                 // context a: right after FROM/JOIN/INTO/UPDATE/TABLE
	ctxColumn                // context c: after SELECT/WHERE/ON/AND/OR/BY/HAVING/SET/","/"("
)

// Suggest returns completion candidates for the identifier token ending at
// cursor (a byte offset) in input, ranked by how relevant each candidate
// kind is to the token's syntactic position:
//
//   - right after FROM/JOIN/INTO/UPDATE/TABLE: table names (also with an
//     empty token, so "from " pops the catalog);
//   - "name." dot prefix: the columns of that table, resolving FROM/JOIN
//     aliases ("tbl [AS] alias"), also with an empty token;
//   - after SELECT/WHERE/ON/AND/OR/BY/HAVING/SET or "," or "(": columns
//     first, then functions, then keywords (empty token: columns only);
//   - anywhere else: columns, then tables, then functions, then keywords;
//     an empty token yields nothing.
//
// Matching is case-insensitive on the token prefix. Keywords and functions
// follow the typed case (all-lowercase prefix gets lowercase words, any
// uppercase letter gets uppercase); identifiers are always verbatim.
func Suggest(input string, cursor int, sc Schema) []Suggestion {
	token, start := tokenAt(input, cursor)
	sg := &suggester{token: token, seen: make(map[string]bool), engine: sc.Engine}

	// Dot completion: qualifier "." token.
	if start > 0 && input[start-1] == '.' {
		tbl, cols, ok := resolveDot(input, start-1, sc)
		if !ok {
			return nil
		}
		for _, c := range sortedMatches(cols, token) {
			sg.add(c, KindColumn, tbl)
		}
		return sg.out
	}

	switch prevContext(input, start) {
	case ctxTable:
		sg.tables(sc)
	case ctxColumn:
		sg.columns(sc)
		if token != "" {
			sg.functions()
			sg.keywords()
		}
	default:
		if token == "" {
			return nil
		}
		sg.columns(sc)
		sg.tables(sc)
		sg.functions()
		sg.keywords()
	}
	return sg.out
}

// DotTable resolves the table a "qualifier." token ending at cursor refers
// to: the qualifier itself, or the table it aliases in a FROM/JOIN clause.
// It reports ok only when the cursor sits in a dot completion and the table
// exists in sc.Tables (returning the schema's canonical spelling), so
// callers can use it to decide which table's columns to fetch.
func DotTable(input string, cursor int, sc Schema) (string, bool) {
	_, start := tokenAt(input, cursor)
	if start == 0 || input[start-1] != '.' {
		return "", false
	}
	tbl, _, ok := resolveDot(input, start-1, sc)
	if !ok {
		return "", false
	}
	return tbl, true
}

// tokenAt returns the identifier token ending at cursor and its start
// offset, clamping cursor into range.
func tokenAt(input string, cursor int) (string, int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && isIdentByte(input[start-1]) {
		start--
	}
	return input[start:cursor], start
}

// prevContext classifies the position at start by the significant token
// right before it.
func prevContext(input string, start int) ctxKind {
	i := start
	for i > 0 && isSpaceByte(input[i-1]) {
		i--
	}
	if i == 0 {
		return ctxOther
	}
	switch input[i-1] {
	case ',', '(':
		return ctxColumn
	}
	if !isIdentByte(input[i-1]) {
		return ctxOther
	}
	j := i
	for j > 0 && isIdentByte(input[j-1]) {
		j--
	}
	switch w := strings.ToUpper(input[j:i]); {
	case tableCtxWords[w]:
		return ctxTable
	case columnCtxWords[w]:
		return ctxColumn
	}
	return ctxOther
}

// resolveDot resolves the qualifier of the dot at offset dot: first through
// the statement's FROM/JOIN aliases, then directly against the schema
// (case-insensitively). It reports ok when the resolved table exists in
// sc.Tables; cols may still be nil for a live table whose columns have not
// been fetched yet.
func resolveDot(input string, dot int, sc Schema) (table string, cols []string, ok bool) {
	qend := dot
	qstart := qend
	for qstart > 0 && isIdentByte(input[qstart-1]) {
		qstart--
	}
	qual := input[qstart:qend]
	if qual == "" {
		return "", nil, false
	}
	name := qual
	if t, found := scanAliases(input)[strings.ToLower(qual)]; found {
		name = t
	}
	if cols, found := sc.Tables[name]; found {
		return name, cols, true
	}
	for k, v := range sc.Tables {
		if strings.EqualFold(k, name) {
			return k, v, true
		}
	}
	return "", nil, false
}

// scanAliases collects "tbl [AS] alias" pairs from every FROM and JOIN
// clause of input, keyed by lowercased alias. Namespace qualifications
// ("ns.tbl alias") resolve to the last path segment.
func scanAliases(input string) map[string]string {
	toks := lexTokens(input)
	aliases := make(map[string]string)
	n := len(toks)
	i := 0
	for i < n {
		if !toks[i].ident || (!strings.EqualFold(toks[i].text, "FROM") && !strings.EqualFold(toks[i].text, "JOIN")) {
			i++
			continue
		}
		i++
		// Parse a comma-separated list of table references.
		for i < n {
			if !toks[i].ident || reservedWords[strings.ToUpper(toks[i].text)] {
				break
			}
			tbl := toks[i].text
			i++
			for i+1 < n && toks[i].text == "." && toks[i+1].ident {
				tbl = toks[i+1].text
				i += 2
			}
			alias := ""
			if i < n && toks[i].ident && strings.EqualFold(toks[i].text, "AS") {
				i++
				if i < n && toks[i].ident && !reservedWords[strings.ToUpper(toks[i].text)] {
					alias = toks[i].text
					i++
				}
			} else if i < n && toks[i].ident && !reservedWords[strings.ToUpper(toks[i].text)] {
				alias = toks[i].text
				i++
			}
			if alias != "" {
				aliases[strings.ToLower(alias)] = tbl
			}
			if i < n && toks[i].text == "," {
				i++
				continue
			}
			break
		}
	}
	return aliases
}

// qtok is one lexed token: an identifier (bare or quoted) or a single
// punctuation byte.
type qtok struct {
	text  string
	ident bool
}

// lexTokens splits input into identifier and punctuation tokens, skipping
// whitespace. Double-quoted and backticked names come out as identifiers
// (without the quotes); single-quoted string literals collapse to one
// opaque punctuation token.
func lexTokens(input string) []qtok {
	var toks []qtok
	i := 0
	for i < len(input) {
		b := input[i]
		switch {
		case isSpaceByte(b):
			i++
		case isIdentByte(b):
			j := i
			for j < len(input) && isIdentByte(input[j]) {
				j++
			}
			toks = append(toks, qtok{text: input[i:j], ident: true})
			i = j
		case b == '"' || b == '`':
			j := i + 1
			for j < len(input) && input[j] != b {
				j++
			}
			toks = append(toks, qtok{text: input[i+1 : j], ident: true})
			if j < len(input) {
				j++
			}
			i = j
		case b == '\'':
			j := i + 1
			for j < len(input) && input[j] != '\'' {
				j++
			}
			toks = append(toks, qtok{text: "'"})
			if j < len(input) {
				j++
			}
			i = j
		default:
			toks = append(toks, qtok{text: string(b)})
			i++
		}
	}
	return toks
}

// --- candidate assembly --------------------------------------------------------

// suggester accumulates suggestions, deduplicating by replacement text.
type suggester struct {
	token  string
	seen   map[string]bool
	out    []Suggestion
	engine string
	// keys memoizes sortedKeys(sc.Tables): columns and tables both walk it
	// in the default context, and sorting the same T names twice per
	// keystroke is pure waste (CLRS ch3: sort once, reuse).
	keys     []string
	keysDone bool
	// buf is scratch for columns(): one per-table match slice reused across
	// every table instead of allocated per table. add() copies the strings
	// out, so clobbering the buffer afterwards is safe.
	buf []string
}

func (s *suggester) tableKeys(sc Schema) []string {
	if !s.keysDone {
		s.keys = sortedKeys(sc.Tables)
		s.keysDone = true
	}
	return s.keys
}

func (s *suggester) add(text, kind, detail string) {
	if s.seen[text] {
		return
	}
	s.seen[text] = true
	s.out = append(s.out, Suggestion{Text: text, Kind: kind, Detail: detail})
}

// columns adds matching columns: the current frame's first, then every
// schema table's (grouped by table, both levels sorted).
func (s *suggester) columns(sc Schema) {
	for _, c := range sortedMatches(sc.Current, s.token) {
		s.add(c, KindColumn, "")
	}
	for _, t := range s.tableKeys(sc) {
		start := len(s.buf)
		for _, c := range sc.Tables[t] {
			if hasFoldPrefix(c, s.token) {
				s.buf = append(s.buf, c)
			}
		}
		m := s.buf[start:]
		sort.Strings(m)
		for _, c := range m {
			s.add(c, KindColumn, t)
		}
		s.buf = s.buf[:start]
	}
}

// tables adds matching table names, sorted, with a column count when known.
func (s *suggester) tables(sc Schema) {
	for _, t := range s.tableKeys(sc) {
		if !hasFoldPrefix(t, s.token) {
			continue
		}
		detail := ""
		if n := len(sc.Tables[t]); n > 0 {
			detail = fmt.Sprintf("%d cols", n)
		}
		s.add(t, KindTable, detail)
	}
}

func (s *suggester) functions() {
	// Start with base functions
	for _, f := range sqlFunctions {
		if hasFoldPrefix(f, s.token) {
			s.add(followCase(s.token, f)+"(", KindFunction, "")
		}
	}
	// Add engine-specific functions
	if s.engine != "" {
		if engineFn, ok := engineFunctions[s.engine]; ok {
			for _, f := range engineFn {
				if hasFoldPrefix(f, s.token) {
					s.add(followCase(s.token, f)+"(", KindFunction, "")
				}
			}
		}
	}
}

func (s *suggester) keywords() {
	// Start with base keywords
	for _, k := range sqlKeywords {
		if hasFoldPrefix(k, s.token) {
			s.add(followCase(s.token, k), KindKeyword, "")
		}
	}
	// Add engine-specific keywords
	if s.engine != "" {
		if engineKw, ok := engineKeywords[s.engine]; ok {
			for _, k := range engineKw {
				if hasFoldPrefix(k, s.token) {
					s.add(followCase(s.token, k), KindKeyword, "")
				}
			}
		}
	}
}

// followCase renders word (stored uppercase) in the case the user is
// typing: any uppercase letter in the token keeps it uppercase, otherwise
// it is lowered. Identifiers never go through this.
func followCase(token, word string) string {
	if strings.ToLower(token) != token {
		return strings.ToUpper(word)
	}
	return strings.ToLower(word)
}

// hasFoldPrefix reports whether s starts with prefix, case-insensitively.
func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// sortedMatches filters cands by the token prefix and sorts the result.
// For pre-sorted candidate lists (sqlKeywords/sqlFunctions), use
// sortedMatchesSorted which binary-searches the prefix range (CLRS ch3).
func sortedMatches(cands []string, token string) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if hasFoldPrefix(c, token) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isIdentByte reports whether b can appear in an identifier token.
func isIdentByte(b byte) bool {
	return b == '_' ||
		('a' <= b && b <= 'z') ||
		('A' <= b && b <= 'Z') ||
		('0' <= b && b <= '9')
}

// isSpaceByte reports whether b is insignificant whitespace.
func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
