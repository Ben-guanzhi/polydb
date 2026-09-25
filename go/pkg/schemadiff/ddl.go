// DDL 语句生成（Compare & Sync E3）：SQLite 方言的脚本 + 回滚脚本。
//
// 与 Rust 侧 rust/crates/schema-diff/src/ddl.rs 逐字等价，由 test/schema-ddl-vectors/*.json 锁定。
// 输入 = 两侧快照 + 已勾选的变更条目（Diff 的 Entry），输出 = 有序语句脚本 + 回滚脚本。
// 零 IO、零 UI：语句只是文本，执行由调用方走既有的 /query 或 /api/transactions。
//
// 规则全部来自 docs/plan-compare-sync.md 附录 A.11 的实测取证（不是文档推测）：
//
//  1. 直改：create_table / drop_table / add_column / drop_column / add_index / drop_index。
//  2. 恒走重建：alter_column / add_foreign_key / drop_foreign_key
//     （SQLite 无 ALTER COLUMN、无 ADD/DROP CONSTRAINT）。
//  3. drop_column 若该列是主键、或出现在任一索引里 ⇒ 转重建
//     （实测 cannot drop PRIMARY KEY column、error in index … after drop column）。
//  4. 名字以 sqlite_ 开头的索引是 PRIMARY KEY / UNIQUE 约束的自动索引，
//     既不能 CREATE INDEX 也不能 DROP INDEX（实测 object name reserved for internal use、
//     index associated with UNIQUE or PRIMARY KEY constraint cannot be dropped）
//     ⇒ 涉及它的 add_index / drop_index 转重建，重建时以表级 UNIQUE (…) 还原。
//  5. 脚本含重建时首尾加 PRAGMA foreign_keys=OFF/ON，并置 pragmas_outside_tx = true
//     （实测：FK 开着时重建被引用的父表会级联清空子表数据；而 PRAGMA foreign_keys
//     在事务内是 no-op）。脚本自身不含 BEGIN/COMMIT。
//  6. 不可逆判定：drop_table / drop_column 恒不可逆；alter_column 当 flags 含 type
//     或 nullability 时不可逆（仅 default 可逆）；其余可逆。
//  7. 回滚承接正向的重建：正向重建过的表在回滚方向**强制**再重建。直改语句补不回重建时
//     丢弃的对象（例：删一个被索引覆盖的列 ⇒ 整表重建 ⇒ 回滚只 ADD COLUMN 会永久留下没有索引的表）。
//  8. 勾选校验（SelectEntries）：id 必须属于当前计划；进入重建的表必须**整组**勾选，
//     否则该表上未勾选的变更会被重建语句一并落地。
//
// 其余方言显式 NOT_SUPPORTED（定案见 §8-1 与 §8-7：四方言的 on_update/on_delete、
// generic_type 尚未落地，diff 比不出差异，凭文档盲写语法等于「数字上已实现、实际未验证」）。
package schemadiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// 固定警告文案：逐字进 spec/behavior.md §15，并由共享向量语料锁定。
const (
	WTriggers          = "重建表会丢失该表上的触发器（快照不含触发器）"
	WGeneratedColumns  = "生成列会退化为普通列（快照不标注生成列）"
	WCheckConstraints  = "CHECK 约束会丢失（快照不含 CHECK 约束）"
	WAutoincrement     = "主键整数列的 AUTOINCREMENT 关键字无法从快照还原"
	WCreateTable       = "新建表只按快照里的列定义生成，不含 CHECK 约束、生成列与触发器"
	WDropColumnCheck   = "该列若被 CHECK 约束引用，删除后约束会一并消失（快照不可见）"
	WAddColumnLimits   = "SQLite 的 ADD COLUMN 只接受常量默认值，非空表上加 NOT NULL 且无默认值的列也会报错"
	WRebuildNewNotNull = "重建只复制两侧同名列，新增的 NOT NULL 列若无默认值会让数据搬运 INSERT 报错"
)

const (
	pragmaFKOff = "PRAGMA foreign_keys=OFF;"
	pragmaFKOn  = "PRAGMA foreign_keys=ON;"
	// PRAGMA 包裹条的相位：首条在最前、末条在最后。它们不属于任何变更条目，EntryIDs 为空。
	phasePragmaFirst = 0
	phasePragmaLast  = 255
)

// Statement 是一条可执行语句。
//
// EntryIDs 是这条语句实现的变更条目 id：直改动作恰好 1 个；重建表的一组语句承载该表
// 被吸收的**全部**条目（重建以目标快照为基准，同表其余变更自然一并落进新定义里）；
// PRAGMA 包裹条为空。
type Statement struct {
	EntryIDs []string `json:"entry_ids"`
	SQL      string   `json:"sql"`
	Phase    int      `json:"phase"`
	// Table 是语句作用的表名（原样大小写）；PRAGMA 包裹条为空串。
	Table string `json:"table"`
	// Warnings 为空时必须保持 nil 或空切片，线路层按 omitempty 省略。
	Warnings []string `json:"warnings,omitempty"`
	// NonReversible：该语句承载的条目里只要有一条不可逆，整条语句就标为不可逆。
	NonReversible bool `json:"non_reversible"`
}

// Script 是语句脚本 + 其反向脚本。
type Script struct {
	Statements []Statement `json:"statements"`
	Rollback   []Statement `json:"rollback"`
	// PragmasOutsideTx：任一脚本含首尾 PRAGMA 包裹条时为 true，那两条必须留在事务外（取证 G6）。
	PragmasOutsideTx bool `json:"pragmas_outside_tx"`
	// DdlInTx：SQLite 的 DDL 可以在事务里（取证 G7，实测值）。
	DdlInTx bool `json:"ddl_in_tx"`
}

func invalidParam(msg string) error {
	return &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: msg}
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func joinIdents(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = quoteIdent(c)
	}
	return strings.Join(parts, ",")
}

func tableOrErr(t *TableSnapshot, name string) (*TableSnapshot, error) {
	if t == nil {
		return nil, invalidParam(fmt.Sprintf("快照中缺少表 %s，无法生成 DDL", name))
	}
	return t, nil
}

// posName 是「列名 + 位置」对，用于按位置还原索引列顺序。
type posName struct {
	pos  int32
	name string
}

// orderedColumns 按 OrdinalPosition 稳定排序（缺省 0 时保持快照原序）。
func orderedColumns(t *TableSnapshot) []*protocol.ColumnInfo {
	v := make([]*protocol.ColumnInfo, len(t.Columns))
	for i := range t.Columns {
		v[i] = &t.Columns[i]
	}
	sort.SliceStable(v, func(i, j int) bool { return v[i].OrdinalPosition < v[j].OrdinalPosition })
	return v
}

func sortedByPosition(cols []posName) []string {
	v := make([]posName, len(cols))
	copy(v, cols)
	sort.SliceStable(v, func(i, j int) bool { return v[i].pos < v[j].pos })
	out := make([]string, len(v))
	for i, c := range v {
		out[i] = c.name
	}
	return out
}

func indexColsByPosition(idx *protocol.IndexInfo) []string {
	cols := make([]posName, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = posName{c.Position, c.Name}
	}
	return sortedByPosition(cols)
}

// pkColumns 优先取快照里标记为主键的索引（带列序），否则退回列上的 IsPrimaryKey。
func pkColumns(t *TableSnapshot) []string {
	for i := range t.Indexes {
		p := &t.Indexes[i]
		if !p.Primary {
			continue
		}
		if cols := indexColsByPosition(p); len(cols) > 0 {
			return cols
		}
		break
	}
	var out []string
	for _, c := range orderedColumns(t) {
		if c.IsPrimaryKey {
			out = append(out, c.Name)
		}
	}
	return out
}

// sqlAction 把线路上的外键动作转成 SQL 关键字；空串表示快照里没填，不生成子句。
func sqlAction(a protocol.ForeignKeyAction) string {
	switch a {
	case protocol.FKActionCascade:
		return "CASCADE"
	case protocol.FKActionSetNull:
		return "SET NULL"
	case protocol.FKActionSetDefault:
		return "SET DEFAULT"
	case protocol.FKActionRestrict:
		return "RESTRICT"
	case protocol.FKActionNoAction:
		return "NO ACTION"
	default:
		return ""
	}
}

// literalDefault 保持快照原样，只折叠空白；空串表示「无默认值」。
//
// 不走 NormalizeDefault——(current_timestamp) 这类表达式默认必须带括号，
// 归一化会把括号剥掉而生成非法 SQL。NULL 视为无默认值。
func literalDefault(raw *string) string {
	d := foldSpaces(strings.TrimSpace(derefOrEmpty(raw)))
	if d == "" || lowerASCII(d) == "null" {
		return ""
	}
	return d
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// isConstantDefault 判断默认值是否为 SQLite 认可的常量字面量（数字 / 单引号串 / blob / NULL）。
//
// 用于给 add_column 挂警告：非常量默认值在 ADD COLUMN 上直接报错（取证 G11）。
func isConstantDefault(raw *string) bool {
	d := literalDefault(raw)
	if d == "" {
		return true // 无默认值即 NULL，本身是常量
	}
	b := []byte(d)
	if b[0] == '\'' {
		// 单引号串：内部只允许 '' 转义，且必须收尾在最后一个字符
		for i := 1; i < len(b); {
			if b[i] != '\'' {
				i++
				continue
			}
			if i+1 < len(b) && b[i+1] == '\'' {
				i += 2
				continue
			}
			return i+1 == len(b)
		}
		return false
	}
	if (b[0] == 'x' || b[0] == 'X') && len(b) > 1 && b[1] == '\'' {
		if b[len(b)-1] != '\'' || len(b) < 2 {
			return false
		}
		for _, c := range []byte(d[2 : len(d)-1]) {
			if !isHexDigit(c) {
				return false
			}
		}
		return true
	}
	return isNumber(d)
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isNumber(s string) bool {
	body := s
	if len(body) > 0 && (body[0] == '-' || body[0] == '+') {
		body = body[1:]
	}
	digits, dot, exp := false, false, false
	b := []byte(body)
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && digits && !exp:
			exp = true
			if i+1 < len(b) && (b[i+1] == '+' || b[i+1] == '-') {
				i++
			}
		default:
			return false
		}
	}
	return digits
}

// columnDef 是快照里的列定义文本："a" integer NOT NULL DEFAULT 'x'。
//
// 类型走 NormalizeType，与 diff 条目的 from/to 用同一字面量。
// 不读 IsAutoIncrement：SQLite 驱动的快照里该字段恒为 false（取证 G3 同源），
// AUTOINCREMENT 只存在于 sqlite_master.sql 文本中，由警告覆盖。
func columnDef(kind protocol.DatabaseKind, c *protocol.ColumnInfo) string {
	var sb strings.Builder
	sb.WriteString(quoteIdent(c.Name))
	if ty := NormalizeType(kind, c.DataType); ty != "" {
		sb.WriteString(" ")
		sb.WriteString(ty)
	}
	if !c.Nullable {
		sb.WriteString(" NOT NULL")
	}
	if d := literalDefault(c.DefaultValue); d != "" {
		sb.WriteString(" DEFAULT " + d)
	}
	return sb.String()
}

// isAutoIndex：名字以 sqlite_ 开头（SQLite 为 PRIMARY KEY / UNIQUE 约束自建）。
func isAutoIndex(name string) bool {
	return strings.HasPrefix(lowerASCII(strings.TrimSpace(name)), "sqlite_")
}

// tableConstraints 是表级约束：主键、由自动索引还原的 UNIQUE、外键。
func tableConstraints(t *TableSnapshot) []string {
	var out []string
	if pk := pkColumns(t); len(pk) > 0 {
		out = append(out, "PRIMARY KEY ("+joinIdents(pk)+")")
	}
	var uniq []kv
	for i := range t.Indexes {
		idx := &t.Indexes[i]
		if idx.Primary || !isAutoIndex(idx.Name) {
			continue
		}
		uniq = append(uniq, kv{lowerASCII(idx.Name), "UNIQUE (" + joinIdents(indexColsByPosition(idx)) + ")"})
	}
	sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].k < uniq[j].k })
	for _, u := range uniq {
		out = append(out, u.v)
	}
	var fks []kv
	for i := range t.ForeignKeys {
		f := &t.ForeignKeys[i]
		cols := joinIdents(f.Columns)
		refs := joinIdents(f.ReferencedColumns)
		s := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", cols, quoteIdent(f.ReferencedTable), refs)
		if a := sqlAction(f.OnDelete); a != "" {
			s += " ON DELETE " + a
		}
		if a := sqlAction(f.OnUpdate); a != "" {
			s += " ON UPDATE " + a
		}
		fks = append(fks, kv{cols + ">" + refs, s})
	}
	sort.SliceStable(fks, func(i, j int) bool { return fks[i].k < fks[j].k })
	for _, f := range fks {
		out = append(out, f.v)
	}
	return out
}

type kv struct {
	k string
	v string
}

// createTableBody 是建表主体：列定义 + 表级约束，单行以 , 连接（向量语料按字面锁定）。
func createTableBody(kind protocol.DatabaseKind, t *TableSnapshot) string {
	parts := make([]string, 0, len(t.Columns)+2)
	for _, c := range orderedColumns(t) {
		parts = append(parts, columnDef(kind, c))
	}
	parts = append(parts, tableConstraints(t)...)
	return strings.Join(parts, ",")
}

// indexExecName 是索引的可执行名字：无名索引合成 polydb_idx_<表>_<列>（两端同一规则）。
func indexExecName(t *TableSnapshot, idx *protocol.IndexInfo) string {
	if strings.TrimSpace(idx.Name) != "" {
		return idx.Name
	}
	return fmt.Sprintf("polydb_idx_%s_%s", t.Name, strings.ReplaceAll(indexSig(idx), ",", "_"))
}

func createIndexSQL(t *TableSnapshot, idx *protocol.IndexInfo) string {
	uniq := ""
	if idx.Unique {
		uniq = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s(%s);",
		uniq,
		quoteIdent(indexExecName(t, idx)),
		quoteIdent(t.Name),
		joinIdents(indexColsByPosition(idx)))
}

func mkStmt(entryIDs []string, sql string, phase int, table string, warnings []string, nonReversible bool) Statement {
	ids := make([]string, len(entryIDs))
	copy(ids, entryIDs)
	return Statement{
		EntryIDs:      ids,
		SQL:           sql,
		Phase:         phase,
		Table:         table,
		Warnings:      warnings,
		NonReversible: nonReversible,
	}
}

func pragmaStmt(sql string, phase int) Statement {
	return mkStmt(nil, sql, phase, "", nil, false)
}

// entryNonReversible 是条目的不可逆判定（规则 6）。
func entryNonReversible(e *Entry) bool {
	switch e.Op {
	case OpDropTable, OpDropColumn:
		return true
	case OpAlterColumn:
		for _, f := range e.Flags {
			if f == FlagType || f == FlagNullability {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// findIndex 按条目 object 找快照里的索引（E1 对无名索引给的是合成名 idx(a,b)）。
func findIndex(t *TableSnapshot, object string) *protocol.IndexInfo {
	want := lowerASCII(strings.TrimSpace(object))
	for i := range t.Indexes {
		if lowerASCII(indexObject(&t.Indexes[i])) == want {
			return &t.Indexes[i]
		}
	}
	return nil
}

func columnOf(t *TableSnapshot, object string) *protocol.ColumnInfo {
	return columnMap(t)[lowerASCII(strings.TrimSpace(object))]
}

// needsRebuild 判定条目能否直改（规则 2/3/4）。
func needsRebuild(e *Entry, src, tgt *TableSnapshot) bool {
	switch e.Op {
	case OpAlterColumn, OpAddForeignKey, OpDropForeignKey:
		return true
	case OpDropColumn:
		if src == nil {
			return false
		}
		col := lowerASCII(strings.TrimSpace(e.Object))
		if c := columnOf(src, col); c != nil && c.IsPrimaryKey {
			return true
		}
		for _, i := range src.Indexes {
			for _, c := range i.Columns {
				if lowerASCII(c.Name) == col {
					return true
				}
			}
		}
		return false
	case OpAddIndex:
		return indexNeedsRebuild(tgt, e.Object)
	case OpDropIndex:
		return indexNeedsRebuild(src, e.Object)
	default:
		return false
	}
}

func indexNeedsRebuild(t *TableSnapshot, object string) bool {
	if t != nil {
		if i := findIndex(t, object); i != nil {
			return i.Primary || isAutoIndex(i.Name)
		}
	}
	return isAutoIndex(object)
}

// hasIntegerPK：单列 INTEGER 主键在 SQLite 里是 rowid 别名，而 AUTOINCREMENT 不在快照里。
func hasIntegerPK(kind protocol.DatabaseKind, t *TableSnapshot) bool {
	pk := pkColumns(t)
	if len(pk) != 1 {
		return false
	}
	c := columnOf(t, pk[0])
	return c != nil && NormalizeType(kind, c.DataType) == "integer"
}

func createWarnings(kind protocol.DatabaseKind, t *TableSnapshot) []string {
	w := []string{WCreateTable}
	if hasIntegerPK(kind, t) {
		w = append(w, WAutoincrement)
	}
	return w
}

func rebuildWarnings(kind protocol.DatabaseKind, tgt, src *TableSnapshot) []string {
	w := []string{WTriggers, WGeneratedColumns, WCheckConstraints}
	if hasIntegerPK(kind, tgt) {
		w = append(w, WAutoincrement)
	}
	sc := columnMap(src)
	for _, c := range orderedColumns(tgt) {
		d := literalDefault(c.DefaultValue)
		_, known := sc[lowerASCII(c.Name)]
		if !c.Nullable && d == "" && !known {
			w = append(w, WRebuildNewNotNull)
			break
		}
	}
	return w
}

// tmpName 是重建用的临时表名：与任一侧快照里的表重名时追加序号。
func tmpName(base string, taken map[string]bool) string {
	cand := "polydb_tmp_" + base
	for n := 2; taken[lowerASCII(cand)]; n++ {
		cand = fmt.Sprintf("polydb_tmp_%s_%d", base, n)
	}
	return cand
}

// rebuildStatements 重建一张表：建新表 → 搬同名列 → 删旧表 → 改名 → 补用户索引。
//
// 整组语句的 Phase 取该表在计划里**第一个**条目的相位（组的输出位置就在那里），
// EntryIDs 是该表被吸收的全部条目。
func rebuildStatements(kind protocol.DatabaseKind, tgt, src *TableSnapshot, taken map[string]bool, entryIDs []string, phase int, nonReversible bool) []Statement {
	tmp := tmpName(tgt.Name, taken)
	sc := columnMap(src)
	var common []string
	for _, c := range orderedColumns(tgt) {
		if _, ok := sc[lowerASCII(c.Name)]; ok {
			common = append(common, c.Name)
		}
	}
	list := joinIdents(common)
	out := []Statement{
		mkStmt(entryIDs, fmt.Sprintf("CREATE TABLE %s(%s);", quoteIdent(tmp), createTableBody(kind, tgt)),
			phase, tgt.Name, rebuildWarnings(kind, tgt, src), nonReversible),
		mkStmt(entryIDs, fmt.Sprintf("INSERT INTO %s(%s) SELECT %s FROM %s;", quoteIdent(tmp), list, list, quoteIdent(src.Name)),
			phase, tgt.Name, nil, nonReversible),
		mkStmt(entryIDs, fmt.Sprintf("DROP TABLE %s;", quoteIdent(src.Name)),
			phase, tgt.Name, nil, nonReversible),
		mkStmt(entryIDs, fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", quoteIdent(tmp), quoteIdent(tgt.Name)),
			phase, tgt.Name, nil, nonReversible),
	}
	var user []kv
	for i := range tgt.Indexes {
		idx := &tgt.Indexes[i]
		if idx.Primary || isAutoIndex(idx.Name) {
			continue
		}
		user = append(user, kv{lowerASCII(indexExecName(tgt, idx)), createIndexSQL(tgt, idx)})
	}
	sort.SliceStable(user, func(i, j int) bool { return user[i].k < user[j].k })
	for _, u := range user {
		out = append(out, mkStmt(entryIDs, u.v, phase, tgt.Name, nil, nonReversible))
	}
	return out
}

// directStatement 把直改动作翻成单条语句（规则 1）。
func directStatement(kind protocol.DatabaseKind, e *Entry, src, tgt *TableSnapshot) (Statement, error) {
	one := []string{e.ID}
	nr := entryNonReversible(e)
	switch e.Op {
	case OpCreateTable:
		t, err := tableOrErr(tgt, e.Table)
		if err != nil {
			return Statement{}, err
		}
		sql := fmt.Sprintf("CREATE TABLE %s(%s);", quoteIdent(t.Name), createTableBody(kind, t))
		return mkStmt(one, sql, e.Phase, e.Table, createWarnings(kind, t), nr), nil
	case OpDropTable:
		return mkStmt(one, fmt.Sprintf("DROP TABLE %s;", quoteIdent(e.Table)), e.Phase, e.Table, nil, nr), nil
	case OpAddColumn:
		t, err := tableOrErr(tgt, e.Table)
		if err != nil {
			return Statement{}, err
		}
		c := columnOf(t, e.Object)
		if c == nil {
			return Statement{}, invalidParam(fmt.Sprintf("快照中缺少列 %s.%s，无法生成 DDL", e.Table, e.Object))
		}
		var warnings []string
		d := literalDefault(c.DefaultValue)
		if !isConstantDefault(c.DefaultValue) || (!c.Nullable && d == "") {
			warnings = []string{WAddColumnLimits}
		}
		sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quoteIdent(e.Table), columnDef(kind, c))
		return mkStmt(one, sql, e.Phase, e.Table, warnings, nr), nil
	case OpDropColumn:
		sql := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", quoteIdent(e.Table), quoteIdent(strings.TrimSpace(e.Object)))
		return mkStmt(one, sql, e.Phase, e.Table, []string{WDropColumnCheck}, nr), nil
	case OpAddIndex:
		t, err := tableOrErr(tgt, e.Table)
		if err != nil {
			return Statement{}, err
		}
		idx := findIndex(t, e.Object)
		if idx == nil {
			return Statement{}, invalidParam(fmt.Sprintf("快照中缺少索引 %s.%s，无法生成 DDL", e.Table, e.Object))
		}
		return mkStmt(one, createIndexSQL(t, idx), e.Phase, e.Table, nil, nr), nil
	case OpDropIndex:
		// 名字以快照为准；快照里无名的索引（手写快照的未命名 UNIQUE）合成可执行名，
		// 与 CREATE INDEX 用同一套规则，回滚才不会 DROP 一个不存在的对象。
		name := strings.TrimSpace(e.Object)
		if src != nil {
			if i := findIndex(src, e.Object); i != nil {
				name = indexExecName(src, i)
			}
		}
		return mkStmt(one, fmt.Sprintf("DROP INDEX %s;", quoteIdent(name)), e.Phase, e.Table, nil, nr), nil
	default:
		return Statement{}, invalidParam(fmt.Sprintf("%s 动作必须走重建路径，不该单独成语句", string(e.Op)))
	}
}

// emitScript 生成一个方向的脚本，返回 (语句列表, 本方向实际重建的表键)。
//
// forced 是**必须**整表重建的表键集合：回滚方向用它承接正向的重建（见 Generate）——
// 正向一旦重建过某表，该表的原始定义就已不在库里，直改语句补不回重建时丢弃的对象。
func emitScript(kind protocol.DatabaseKind, source, target *Snapshot, entries []Entry, forced map[string]bool) ([]Statement, map[string]bool, error) {
	src := tableMap(source)
	tgt := tableMap(target)
	taken := map[string]bool{}
	for k := range src {
		taken[k] = true
	}
	for k := range tgt {
		taken[k] = true
	}

	// 1. 哪些表必须整体重建（条目自身触发，或来自 forced——回滚方向承接正向的重建）
	rebuild := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		k := lowerASCII(e.Table)
		if forced[k] || needsRebuild(e, src[k], tgt[k]) {
			rebuild[k] = true
		}
	}

	// 2. 重建表吸收同表全部条目：ids 按条目顺序、phase 取第一个、不可逆取或
	type absorbed struct {
		ids   []string
		phase int
		nr    bool
	}
	groups := map[string]*absorbed{}
	for i := range entries {
		e := &entries[i]
		k := lowerASCII(e.Table)
		if !rebuild[k] {
			continue
		}
		g := groups[k]
		if g == nil {
			g = &absorbed{phase: e.Phase}
			groups[k] = g
		}
		g.ids = append(g.ids, e.ID)
		g.nr = g.nr || entryNonReversible(e)
	}

	// 3. 按条目顺序输出（E1 的计划顺序已含建表拓扑，这里不再重排）
	out := []Statement{}
	done := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		k := lowerASCII(e.Table)
		if g, ok := groups[k]; ok {
			if done[k] {
				continue
			}
			done[k] = true
			t, err := tableOrErr(tgt[k], e.Table)
			if err != nil {
				return nil, nil, err
			}
			s, err := tableOrErr(src[k], e.Table)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, rebuildStatements(kind, t, s, taken, g.ids, g.phase, g.nr)...)
			continue
		}
		st, err := directStatement(kind, e, src[k], tgt[k])
		if err != nil {
			return nil, nil, err
		}
		out = append(out, st)
	}

	// 4. 含重建 ⇒ 首尾 PRAGMA 包裹（必须留在事务外，取证 G5 + G6）
	if len(rebuild) == 0 {
		return out, rebuild, nil
	}
	wrapped := make([]Statement, 0, len(out)+2)
	wrapped = append(wrapped, pragmaStmt(pragmaFKOff, phasePragmaFirst))
	wrapped = append(wrapped, out...)
	wrapped = append(wrapped, pragmaStmt(pragmaFKOn, phasePragmaLast))
	return wrapped, rebuild, nil
}

func inverseOp(op Op) Op {
	switch op {
	case OpCreateTable:
		return OpDropTable
	case OpDropTable:
		return OpCreateTable
	case OpAddColumn:
		return OpDropColumn
	case OpDropColumn:
		return OpAddColumn
	case OpAlterColumn:
		return OpAlterColumn
	case OpAddIndex:
		return OpDropIndex
	case OpDropIndex:
		return OpAddIndex
	case OpAddForeignKey:
		return OpDropForeignKey
	case OpDropForeignKey:
		return OpAddForeignKey
	default:
		return op
	}
}

// mirrorID 是正向条目 id → 反向条目 id：只替换最后一段 op。
//
// 对象名在镜像比对里保持不变——E1 的 add_* 取目标侧对象、drop_* 取源侧对象，
// 互换两侧后取到的正是同一个对象，所以 id 的表段与对象段都能对上。
func mirrorID(id string) string {
	i := strings.LastIndex(id, "/")
	if i < 0 {
		return id
	}
	head, tail := id[:i], id[i+1:]
	if !isKnownOp(tail) {
		return id
	}
	return head + "/" + string(inverseOp(Op(tail)))
}

func isKnownOp(s string) bool {
	switch Op(s) {
	case OpCreateTable, OpDropTable, OpAddColumn, OpDropColumn, OpAlterColumn,
		OpAddIndex, OpDropIndex, OpAddForeignKey, OpDropForeignKey:
		return true
	default:
		return false
	}
}

func checkKind(source, target *Snapshot) (protocol.DatabaseKind, error) {
	for _, snap := range []*Snapshot{source, target} {
		if snap.Kind == protocol.DatabaseKindRedis {
			return "", notSupported("Redis 无模式概念，不参与结构比对")
		}
	}
	if source.Kind != target.Kind {
		return "", notSupported(fmt.Sprintf("跨种类比对未支持：%s vs %s（v1 只比同一种数据库）",
			source.Kind, target.Kind))
	}
	if source.Kind != protocol.DatabaseKindSQLite {
		return "", notSupported(fmt.Sprintf("%s 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）", source.Kind))
	}
	return source.Kind, nil
}

// Generate 由（两侧快照, 已勾选条目）生成 SQLite 的语句脚本与回滚脚本。
//
// entries 是已勾选的变更条目，正常由 SelectEntries 从 Diff(source, target, opts) 里筛出
// （它同时锁定「重建表必须整组勾选」）；opts 只用于反向镜像重算，使回滚与正向严格对称。
//
// 回滚 = source/target 互换后按同一套规则再生成（定案 §8-2），
// 因此不可逆标记也按同一规则打在回滚语句上：例如正向 create_table 的回滚是
// drop_table，回滚脚本自身也就成了不可逆动作。
func Generate(source, target *Snapshot, entries []Entry, opts *Options) (*Script, error) {
	kind, err := checkKind(source, target)
	if err != nil {
		return nil, err
	}
	statements, rebuilt, err := emitScript(kind, source, target, entries, nil)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, e := range entries {
		want[mirrorID(e.ID)] = true
	}
	rev, err := Diff(target, source, opts)
	if err != nil {
		return nil, err
	}
	var reverse []Entry
	for _, e := range rev {
		if want[e.ID] {
			reverse = append(reverse, e)
		}
	}
	// 正向重建过的表，回滚也必须重建：原表定义已不在库里，直改语句补不回索引与约束。
	rollback, rebuiltRev, err := emitScript(kind, target, source, reverse, rebuilt)
	if err != nil {
		return nil, err
	}
	return &Script{
		Statements:       statements,
		Rollback:         rollback,
		PragmasOutsideTx: len(rebuilt) > 0 || len(rebuiltRev) > 0,
		DdlInTx:          true,
	}, nil
}

// SelectEntries 按勾选 id 从**全量计划**里取出子集，并锁定两条选择规则。
//
//  1. 每个 id 必须真实出现在 plan 里，否则计划已过期（两侧结构在勾选后又变了），
//     调用方必须重新比对——绝不能拿旧 id 去解释新计划。
//  2. 进入整表重建的表，其**全部**条目必须一起勾选：重建以目标快照为准生成新表定义，
//     同表被漏掉的变更会被悄悄带进新表里（用户以为只勾了两条，实则是三条）。
//
// plan 必须是 Diff(source, target, opts) 的原始输出（顺序即相位序）。
func SelectEntries(source, target *Snapshot, plan []Entry, selected []string) ([]Entry, error) {
	want := map[string]bool{}
	for _, id := range selected {
		want[id] = true
	}
	inPlan := map[string]bool{}
	for _, e := range plan {
		inPlan[e.ID] = true
	}
	var stale []string
	seenStale := map[string]bool{}
	for _, id := range selected {
		if !inPlan[id] && !seenStale[id] {
			stale = append(stale, id)
			seenStale[id] = true
		}
	}
	if len(stale) > 0 {
		return nil, invalidParam("勾选的条目不在当前比对计划里：" + strings.Join(stale, "、") + "（请重新比对后再执行）")
	}
	src := tableMap(source)
	tgt := tableMap(target)
	var picked []Entry
	for i := range plan {
		if want[plan[i].ID] {
			picked = append(picked, plan[i])
		}
	}
	rebuild := map[string]bool{}
	for i := range picked {
		e := &picked[i]
		k := lowerASCII(e.Table)
		if needsRebuild(e, src[k], tgt[k]) {
			rebuild[k] = true
		}
	}
	// 按计划的表出现顺序报错，保证双端逐字一致
	seen := map[string]bool{}
	for i := range picked {
		e := &picked[i]
		k := lowerASCII(e.Table)
		if !rebuild[k] || seen[k] {
			continue
		}
		seen[k] = true
		var missing []string
		for j := range plan {
			if lowerASCII(plan[j].Table) == k && !want[plan[j].ID] {
				missing = append(missing, plan[j].ID)
			}
		}
		if len(missing) > 0 {
			return nil, invalidParam("表 " + e.Table + " 需要整表重建，同表变更必须整组勾选：缺少 " +
				strings.Join(missing, "、"))
		}
	}
	return picked, nil
}
