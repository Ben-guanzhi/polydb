// Diff：输入两侧元数据快照，输出结构化变更计划（零 IO、零 UI、不生成 DDL）。
//
// 与 Rust 侧 polydb-schema-diff 逐字等价，由 test/schema-diff-vectors/*.json 锁定。
//
// v1 有意**不比较**：字符集/排序规则、列顺序、注释、视图与物化视图、
// 触发器/存储过程/序列/权限。跨种类比对（如 SQLite ↔ PG）直接报错而非猜测。
// 视图一侧整体跳过：若源侧是视图而目标侧是同名表，仍按「缺失表」产出
// create_table（不猜语义，执行期由数据库报错兜底）。
package schemadiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
)

// Op 是一个原子动作，线路名与 Rust 的 Op::as_str 逐字一致。
type Op string

const (
	OpCreateTable    Op = "create_table"
	OpDropTable      Op = "drop_table"
	OpAddColumn      Op = "add_column"
	OpDropColumn     Op = "drop_column"
	OpAlterColumn    Op = "alter_column"
	OpAddIndex       Op = "add_index"
	OpDropIndex      Op = "drop_index"
	OpAddForeignKey  Op = "add_foreign_key"
	OpDropForeignKey Op = "drop_foreign_key"
)

// Phase 是执行相位：数值小的先执行，保证「先解除依赖再删、先建表再加约束」。
// 取值单一来源自 spec/schemas/schemadiff-ops.json（见 ops_gen.go 的 OpRows）。
func (o Op) Phase() int {
	if r, ok := lookupOpRow(o); ok {
		return r.Phase
	}
	return 0
}

// Destructive 报告该动作是否破坏数据（删表/删列/删索引/删外键）。
func (o Op) Destructive() bool {
	r, ok := lookupOpRow(o)
	return ok && r.Destructive
}

// ColumnFlag 是 alter_column 的改变维度（供 UI 分行展示与选择性勾选）。
type ColumnFlag string

const (
	FlagType          ColumnFlag = "type"
	FlagNullability   ColumnFlag = "nullability"
	FlagDefault       ColumnFlag = "default"
	FlagAutoIncrement ColumnFlag = "auto_increment"
)

// Entry 是计划里的一条变更。字段名与 Rust 的 serde 输出逐字一致。
type Entry struct {
	// ID 是稳定标识：{表名小写}/{对象名小写}/{op}，供 UI 记忆勾选状态。
	ID string `json:"id"`
	// Op 是动作类型。
	Op Op `json:"op"`
	// Phase 是执行相位（Op.Phase），排序主键。
	Phase int `json:"phase"`
	// Table 取目标侧原样大小写；删表时取源侧。
	Table string `json:"table"`
	// Object 是列名/索引名/外键名；表级动作为空。
	Object string `json:"object,omitempty"`
	// From 是变更前取值，语义随 Op 而定。
	From *string `json:"from,omitempty"`
	// To 是变更后取值。
	To *string `json:"to,omitempty"`
	// Flags 仅 alter_column 使用；无改变时必须为 nil（不得是空切片）。
	Flags []ColumnFlag `json:"flags,omitempty"`
	// Destructive 标记破坏性动作。
	Destructive bool `json:"destructive"`
	// Selected 是默认勾选状态：破坏性动作默认 false。
	Selected bool `json:"selected"`
	// Reason 是人读原因（中文，双端逐字一致）。
	Reason string `json:"reason"`
}

// Options 是比对选项。
type Options struct {
	// IgnoreDefaults 忽略默认值差异（跨环境序列/时间戳默认常属噪声）。
	IgnoreDefaults bool `json:"ignore_defaults,omitempty"`
	// SelectDestructive 让破坏性动作默认勾选（须用户显式确认时才置 true）。
	SelectDestructive bool `json:"select_destructive,omitempty"`
}

// TableSnapshot 是一张表的元数据快照（字段名与 protocol DTO 对齐）。
type TableSnapshot struct {
	Name        string                    `json:"name"`
	Type        protocol.TableType        `json:"type,omitempty"`
	Columns     []protocol.ColumnInfo     `json:"columns,omitempty"`
	Indexes     []protocol.IndexInfo      `json:"indexes,omitempty"`
	ForeignKeys []protocol.ForeignKeyInfo `json:"foreign_keys,omitempty"`
}

// Snapshot 是一个（种类, schema）的结构快照。
type Snapshot struct {
	Kind   protocol.DatabaseKind `json:"kind"`
	Schema string                `json:"schema,omitempty"`
	Tables []TableSnapshot       `json:"tables,omitempty"`
}

func notSupported(msg string) error {
	return &protocol.PolyDBError{Code: protocol.ErrNotSupported, Message: msg}
}

func nullWord(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func yesWord(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func defaultWord(d *string) string {
	if d == nil {
		return "无"
	}
	return *d
}

func actionWord(a protocol.ForeignKeyAction) string {
	if strings.TrimSpace(string(a)) == "" {
		return "无"
	}
	return string(a)
}

func joinLower(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = lowerASCII(n)
	}
	return strings.Join(out, ",")
}

// indexSig 是索引列签名：按 Position 升序取列名小写、逗号连接（忽略 ASC/DESC 与前缀长度）。
func indexSig(idx *protocol.IndexInfo) string {
	cols := make([]protocol.IndexColumn, len(idx.Columns))
	copy(cols, idx.Columns)
	sort.SliceStable(cols, func(i, j int) bool {
		if cols[i].Position != cols[j].Position {
			return cols[i].Position < cols[j].Position
		}
		return lowerASCII(cols[i].Name) < lowerASCII(cols[j].Name)
	})
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = lowerASCII(c.Name)
	}
	return strings.Join(names, ",")
}

func referencedTable(fk *protocol.ForeignKeyInfo) string {
	t := lowerASCII(fk.ReferencedTable)
	if strings.TrimSpace(fk.ReferencedSchema) == "" {
		return t
	}
	return lowerASCII(fk.ReferencedSchema) + "/" + t
}

func fkKey(fk *protocol.ForeignKeyInfo) string {
	if strings.TrimSpace(fk.Name) != "" {
		return "n:" + lowerASCII(fk.Name)
	}
	return "c:" + joinLower(fk.Columns) + "->" + lowerASCII(fk.ReferencedTable) +
		"(" + joinLower(fk.ReferencedColumns) + ")"
}

func fkObject(fk *protocol.ForeignKeyInfo) string {
	if strings.TrimSpace(fk.Name) == "" {
		return "fk(" + joinLower(fk.Columns) + ")"
	}
	return fk.Name
}

func indexObject(idx *protocol.IndexInfo) string {
	if strings.TrimSpace(idx.Name) == "" {
		return "idx(" + indexSig(idx) + ")"
	}
	return idx.Name
}

func isView(t *TableSnapshot) bool {
	return t.Type == protocol.TableTypeView || t.Type == protocol.TableTypeMaterializedView
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tableMap(snap *Snapshot) map[string]*TableSnapshot {
	m := make(map[string]*TableSnapshot, len(snap.Tables))
	for i := range snap.Tables {
		t := &snap.Tables[i]
		if isView(t) {
			continue
		}
		k := lowerASCII(t.Name)
		if _, ok := m[k]; !ok {
			m[k] = t
		}
	}
	return m
}

// viewNames 是快照里的视图名（按小写键升序，同名只留首个出现的原样大小写）。
//
// 视图**不进**比对（tableMap 已把它们跳掉，见 behavior.md §14.1），但生成器必须知道这些名字：
// 重建用的临时表名不能撞上它们（A.15 G17），删表/重建对视图的影响也要能警告出来（A.15 G13/G16）。
// SQLite 没有依赖目录 ⇒ 这里只有名字，判定不了「哪个视图引用了哪张表」。
func viewNames(snap *Snapshot) []string {
	first := map[string]string{}
	for i := range snap.Tables {
		t := &snap.Tables[i]
		if !isView(t) {
			continue
		}
		k := lowerASCII(t.Name)
		if _, ok := first[k]; !ok {
			first[k] = t.Name
		}
	}
	out := make([]string, 0, len(first))
	for _, k := range sortedKeys(first) {
		out = append(out, first[k])
	}
	return out
}

func columnMap(t *TableSnapshot) map[string]*protocol.ColumnInfo {
	m := make(map[string]*protocol.ColumnInfo, len(t.Columns))
	for i := range t.Columns {
		c := &t.Columns[i]
		k := lowerASCII(c.Name)
		if _, ok := m[k]; !ok {
			m[k] = c
		}
	}
	return m
}

func indexMap(items *[]protocol.IndexInfo, primary bool) map[string]*protocol.IndexInfo {
	m := map[string]*protocol.IndexInfo{}
	for i := range *items {
		idx := &(*items)[i]
		if idx.Primary != primary {
			continue
		}
		k := lowerASCII(indexObject(idx))
		if _, ok := m[k]; !ok {
			m[k] = idx
		}
	}
	return m
}

func fkMap(items *[]protocol.ForeignKeyInfo) map[string]*protocol.ForeignKeyInfo {
	m := map[string]*protocol.ForeignKeyInfo{}
	for i := range *items {
		f := &(*items)[i]
		k := fkKey(f)
		if _, ok := m[k]; !ok {
			m[k] = f
		}
	}
	return m
}

func firstPrimary(items []protocol.IndexInfo) *protocol.IndexInfo {
	for i := range items {
		if items[i].Primary {
			return &items[i]
		}
	}
	return nil
}

func mk(op Op, table, object, reason string, opts *Options) Entry {
	destructive := op.Destructive()
	return Entry{
		ID:          lowerASCII(table) + "/" + lowerASCII(object) + "/" + string(op),
		Op:          op,
		Phase:       op.Phase(),
		Table:       table,
		Object:      object,
		Flags:       nil,
		Destructive: destructive,
		Selected:    !destructive || opts.SelectDestructive,
		Reason:      reason,
	}
}

func strPtr(s string) *string { return &s }

func diffColumns(kind protocol.DatabaseKind, table string, src, tgt *TableSnapshot, opts *Options) []Entry {
	var out []Entry
	sc := columnMap(src)
	tc := columnMap(tgt)
	for _, k := range sortedKeys(tc) {
		c := tc[k]
		s, ok := sc[k]
		if !ok {
			e := mk(OpAddColumn, table, c.Name, "列缺失，需新增", opts)
			e.To = strPtr(NormalizeType(kind, c.DataType))
			out = append(out, e)
			continue
		}
		st := NormalizeType(kind, s.DataType)
		tt := NormalizeType(kind, c.DataType)
		var flags []ColumnFlag
		var reasons []string
		if st != tt {
			flags = append(flags, FlagType)
			reasons = append(reasons, "类型 "+st+" → "+tt)
		}
		if s.Nullable != c.Nullable {
			flags = append(flags, FlagNullability)
			reasons = append(reasons, "可空性 "+nullWord(s.Nullable)+" → "+nullWord(c.Nullable))
		}
		if !opts.IgnoreDefaults {
			sd := NormalizeDefault(s.DefaultValue)
			td := NormalizeDefault(c.DefaultValue)
			if !sameOptStr(sd, td) {
				flags = append(flags, FlagDefault)
				reasons = append(reasons, "默认值 "+defaultWord(sd)+" → "+defaultWord(td))
			}
		}
		if s.IsAutoIncrement != c.IsAutoIncrement {
			flags = append(flags, FlagAutoIncrement)
			reasons = append(reasons, "自增 "+yesWord(s.IsAutoIncrement)+" → "+yesWord(c.IsAutoIncrement))
		}
		if len(flags) == 0 {
			continue
		}
		e := mk(OpAlterColumn, table, c.Name, strings.Join(reasons, "；"), opts)
		e.From = strPtr(st)
		e.To = strPtr(tt)
		e.Flags = flags
		out = append(out, e)
	}
	for _, k := range sortedKeys(sc) {
		if _, ok := tc[k]; ok {
			continue
		}
		c := sc[k]
		e := mk(OpDropColumn, table, c.Name, "列多余，需删除", opts)
		e.From = strPtr(NormalizeType(kind, c.DataType))
		out = append(out, e)
	}
	return out
}

func sameOptStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func diffIndexes(table string, src, tgt *TableSnapshot, opts *Options) []Entry {
	var out []Entry
	sPri := firstPrimary(src.Indexes)
	tPri := firstPrimary(tgt.Indexes)
	switch {
	case sPri != nil && tPri != nil:
		if indexSig(sPri) != indexSig(tPri) {
			out = append(out,
				mk(OpDropIndex, table, indexObject(sPri), "主键列变化，需重建", opts),
				mk(OpAddIndex, table, indexObject(tPri), "主键列变化，需重建", opts))
		}
	case sPri != nil:
		out = append(out, mk(OpDropIndex, table, indexObject(sPri), "索引多余，需删除", opts))
	case tPri != nil:
		out = append(out, mk(OpAddIndex, table, indexObject(tPri), "索引缺失，需新增", opts))
	}
	si := indexMap(&src.Indexes, false)
	ti := indexMap(&tgt.Indexes, false)
	for _, k := range sortedKeys(ti) {
		t := ti[k]
		s, ok := si[k]
		if !ok {
			out = append(out, mk(OpAddIndex, table, indexObject(t), "索引缺失，需新增", opts))
			continue
		}
		a := indexSig(s)
		b := indexSig(t)
		var reasons []string
		if a != b {
			reasons = append(reasons, "索引定义变化："+a+" → "+b)
		}
		if s.Unique != t.Unique {
			reasons = append(reasons, "唯一性 "+yesWord(s.Unique)+" → "+yesWord(t.Unique))
		}
		if len(reasons) == 0 {
			continue
		}
		r := strings.Join(reasons, "；")
		out = append(out,
			mk(OpDropIndex, table, indexObject(s), r, opts),
			mk(OpAddIndex, table, indexObject(t), r, opts))
	}
	for _, k := range sortedKeys(si) {
		if _, ok := ti[k]; ok {
			continue
		}
		s := si[k]
		out = append(out, mk(OpDropIndex, table, indexObject(s), "索引多余，需删除", opts))
	}
	return out
}

func diffForeignKeys(table string, src, tgt *TableSnapshot, opts *Options) []Entry {
	var out []Entry
	sf := fkMap(&src.ForeignKeys)
	tf := fkMap(&tgt.ForeignKeys)
	for _, k := range sortedKeys(tf) {
		t := tf[k]
		s, ok := sf[k]
		if !ok {
			out = append(out, mk(OpAddForeignKey, table, fkObject(t), "外键缺失，需新增", opts))
			continue
		}
		var reasons []string
		sc := joinLower(s.Columns)
		tc := joinLower(t.Columns)
		if sc != tc {
			reasons = append(reasons, "列 "+sc+" → "+tc)
		}
		sr := referencedTable(s)
		tr := referencedTable(t)
		if sr != tr {
			reasons = append(reasons, "引用表 "+sr+" → "+tr)
		}
		src2 := joinLower(s.ReferencedColumns)
		trc := joinLower(t.ReferencedColumns)
		if src2 != trc {
			reasons = append(reasons, "引用列 "+src2+" → "+trc)
		}
		if s.OnDelete != t.OnDelete {
			reasons = append(reasons, "ON DELETE "+actionWord(s.OnDelete)+" → "+actionWord(t.OnDelete))
		}
		if s.OnUpdate != t.OnUpdate {
			reasons = append(reasons, "ON UPDATE "+actionWord(s.OnUpdate)+" → "+actionWord(t.OnUpdate))
		}
		if len(reasons) == 0 {
			continue
		}
		r := strings.Join(reasons, "；")
		out = append(out,
			mk(OpDropForeignKey, table, fkObject(s), r, opts),
			mk(OpAddForeignKey, table, fkObject(t), r, opts))
	}
	for _, k := range sortedKeys(sf) {
		if _, ok := tf[k]; ok {
			continue
		}
		s := sf[k]
		out = append(out, mk(OpDropForeignKey, table, fkObject(s), "外键多余，需删除", opts))
	}
	return out
}

func diffTable(kind protocol.DatabaseKind, src, tgt *TableSnapshot, opts *Options) []Entry {
	out := diffColumns(kind, tgt.Name, src, tgt, opts)
	out = append(out, diffIndexes(tgt.Name, src, tgt, opts)...)
	out = append(out, diffForeignKeys(tgt.Name, src, tgt, opts)...)
	return out
}

func sortKey(e *Entry) string {
	return fmt.Sprintf("%03d\x00%s\x00%s\x00%s", e.Phase, lowerASCII(e.Table), lowerASCII(e.Object), e.ID)
}

// topoCreateTables 在新建表之间按外键引用排依赖序（父表在前）；
// 同名候选按名字序，环按 DFS 到达序（两端一致的确定性兜底）。
func topoCreateTables(sorted []Entry, tgt map[string]*TableSnapshot) []Entry {
	byKey := map[string]Entry{}
	for _, e := range sorted {
		if e.Op == OpCreateTable {
			byKey[lowerASCII(e.Table)] = e
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	deps := map[string][]string{}
	for _, k := range sortedKeys(byKey) {
		t, ok := tgt[k]
		if !ok {
			continue
		}
		var d []string
		for i := range t.ForeignKeys {
			r := lowerASCII(t.ForeignKeys[i].ReferencedTable)
			if _, ok := byKey[r]; ok && r != k {
				d = append(d, r)
			}
		}
		sort.Strings(d)
		deps[k] = dedupSorted(d)
	}
	visited := map[string]bool{}
	order := make([]Entry, 0, len(byKey))
	var visit func(k string)
	visit = func(k string) {
		if visited[k] {
			return
		}
		visited[k] = true
		for _, r := range deps[k] {
			visit(r)
		}
		if e, ok := byKey[k]; ok {
			order = append(order, e)
		}
	}
	for _, k := range sortedKeys(byKey) {
		visit(k)
	}
	return order
}

func dedupSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Diff 比对两个快照，产出按相位排好序的结构化计划。
//
// 错误：任一侧为 Redis（无模式概念），或两侧种类不同。
func Diff(source, target *Snapshot, opts *Options) ([]Entry, error) {
	if opts == nil {
		opts = &Options{}
	}
	for _, snap := range []*Snapshot{source, target} {
		if snap.Kind == protocol.DatabaseKindRedis {
			return nil, notSupported("Redis 无模式概念，不参与结构比对")
		}
	}
	if source.Kind != target.Kind {
		return nil, notSupported(fmt.Sprintf("跨种类比对未支持：%s vs %s（v1 只比同一种数据库）",
			source.Kind, target.Kind))
	}
	src := tableMap(source)
	tgt := tableMap(target)
	var out []Entry
	for _, k := range sortedKeys(tgt) {
		t := tgt[k]
		if s, ok := src[k]; ok {
			out = append(out, diffTable(source.Kind, s, t, opts)...)
			continue
		}
		out = append(out, mk(OpCreateTable, t.Name, "", "表中缺失，需新建", opts))
	}
	for _, k := range sortedKeys(src) {
		if _, ok := tgt[k]; ok {
			continue
		}
		s := src[k]
		out = append(out, mk(OpDropTable, s.Name, "", "表中多余，需删除", opts))
	}
	sort.SliceStable(out, func(i, j int) bool { return sortKey(&out[i]) < sortKey(&out[j]) })
	create := topoCreateTables(out, tgt)
	res := make([]Entry, 0, len(out))
	ci := 0
	for _, e := range out {
		if e.Op == OpCreateTable {
			res = append(res, create[ci])
			ci++
			continue
		}
		res = append(res, e)
	}
	return res, nil
}
