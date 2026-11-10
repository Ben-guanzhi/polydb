// Package changes 实现 TUI 变更跟踪编辑的纯逻辑：单元格编辑先在本地排队，
// Review 后在单个事务内提交、逐条校验影响行数。
//
// 语义与 Web `web/src/lib/changes.ts`、Rust `ui-gui/src/changes.rs` 对齐
// （三端各自独立实现，不 port 代码）。契约依据：spec/behavior.md §10（事务单次 finalize）。
//
// 本包无 IO、不依赖 driver，仅借用 protocol 的 Value / DatabaseKind。
package changes

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"sort"

	"github.com/polydb/polydb/pkg/protocol"
)

// ChangeType 行级变更类型。
type ChangeType int

const (
	Update ChangeType = iota
	Insert
	Delete
)

func (t ChangeType) asStr() string {
	switch t {
	case Update:
		return "update"
	case Insert:
		return "insert"
	case Delete:
		return "delete"
	}
	return "?"
}

// Cell 一个「列名 → 值」单元，行内保留列序（INSERT 的列序 load-bearing）。
type Cell struct {
	Column string
	Value  protocol.Value
}

// Row 整行快照。
type Row []Cell

func rowGet(row Row, col string) protocol.Value {
	for _, c := range row {
		if c.Column == col {
			return c.Value
		}
	}
	return protocol.NewNullValue()
}

func rowSet(row *Row, col string, v protocol.Value) {
	for i := range *row {
		if (*row)[i].Column == col {
			(*row)[i].Value = v
			return
		}
	}
	*row = append(*row, Cell{Column: col, Value: v})
}

// CellChange 单列的旧→新变更。
type CellChange struct {
	Column   string
	OldValue protocol.Value
	NewValue protocol.Value
}

// RowChange 一行的累积变更。
type RowChange struct {
	RowID       string
	Type        ChangeType
	CellChanges []CellChange
	OriginalRow Row
	NewRow      Row // Insert 使用；其余为 nil
	Sequence    int64
}

type undoOp struct {
	isRow  bool
	key    string
	column string // Cell 撤销时定位列
}

func keyOf(rowID string, t ChangeType) string {
	return rowID + "\x00" + t.asStr()
}

// ─── Value 解析（protocol.Value 只暴露 raw JSON）───────────

type valueKind int

const (
	vNull valueKind = iota
	vBool
	vNumber
	vString
	vOther // array / object：按 JSON 文本当字符串处理
)

type parsedValue struct {
	kind valueKind
	b    bool
	num  json.Number
	s    string
}

// parseValue 把 protocol.Value 解为其 JSON 语义。损坏 / 空按 null 处理。
func parseValue(v protocol.Value) parsedValue {
	raw, err := v.MarshalJSON()
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return parsedValue{kind: vNull}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var any0 any
	if err := dec.Decode(&any0); err != nil {
		return parsedValue{kind: vNull}
	}
	switch x := any0.(type) {
	case bool:
		return parsedValue{kind: vBool, b: x}
	case json.Number:
		return parsedValue{kind: vNumber, num: x}
	case string:
		return parsedValue{kind: vString, s: x}
	default:
		return parsedValue{kind: vString, s: string(raw)}
	}
}

// ValuesEqual 跨整型 / 浮点按数值比较，避免把 5（整数）判成与 5.0（浮点）不等。
//
// 精度守卫（与 Rust 端 int_float_equal、Web 端 bigintSafe 字符串承载三端对齐）：
// 64 位整数只有 53 位尾数，Float64() 会把两个 >2^53 的相邻大整数塌成同一个
// double 而误判相等——编辑被当 no-op 静默丢弃。所以先走**精确整数路径**
// （两侧都能精确解析成整数时直接比，不经 double）；一侧是整数、另一侧是
// 带小数点的文本时，只有"该文本是整数值（5.0）且落在 int64 范围内"才判等
// （Rust int_float_equal 同款），其余一律不等；两侧都不是精确整数才落
// float64 比较。
func ValuesEqual(a, b protocol.Value) bool {
	pa, pb := parseValue(a), parseValue(b)
	if pa.kind == vNumber && pb.kind == vNumber {
		ia, aInt := exactInt(pa.num)
		ib, bInt := exactInt(pb.num)
		if aInt && bInt {
			return ia.Cmp(ib) == 0 // 两个精确整数：直接比，不经 double
		}
		if aInt != bInt {
			// 一侧整数、一侧带小数点/指数。整数值的浮点（5.0）仍须与整数 5
			// 判等：把浮点侧的整数值也解析出来比；带真实小数部分或超出
			// int64 范围的不可能相等。
			intSide, floatSide := pa.num, pb.num
			if bInt {
				intSide, floatSide = pb.num, pa.num
			}
			return floatValueEqualsInt(floatSide, intSide)
		}
		fa, e1 := pa.num.Float64()
		fb, e2 := pb.num.Float64()
		if e1 == nil && e2 == nil {
			return fa == fb
		}
		return pa.num == pb.num
	}
	if pa.kind != pb.kind {
		return false
	}
	switch pa.kind {
	case vNull:
		return true
	case vBool:
		return pa.b == pb.b
	case vString:
		return pa.s == pb.s
	}
	return false
}

// exactInt 把 json.Number 解析为精确整数。返回 ok=false 表示这不是一个纯整数
// 文本——小数、科学计数或超范围数字。
func exactInt(n json.Number) (*big.Int, bool) {
	i, ok := new(big.Int).SetString(n.String(), 10)
	return i, ok
}

// floatValueEqualsInt 判"整数值的浮点文本"是否与整数相等（5.0 ↔ 5）。
// 只有文本是纯整数值且落在 int64 范围内才可能相等——这是 Rust 端
// int_float_equal 的语义：范围外的浮点不可能与 int64 精确相等。
func floatValueEqualsInt(floatSide, intSide json.Number) bool {
	f, err := floatSide.Float64()
	if err != nil || !isIntegralFloat(f) {
		return false
	}
	if f < mathMinInt64AsFloat || f >= mathMaxInt64AsFloat {
		return false
	}
	i, ok := exactInt(intSide)
	if !ok {
		return false
	}
	return new(big.Float).SetInt(i).Cmp(big.NewFloat(f)) == 0
}

// isIntegralFloat 报告浮点是否为整数值（无小数部分）。NaN/Inf 直接排除。
func isIntegralFloat(f float64) bool {
	return !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f)
}

// 浮点与 int64 边界比较用的常量：2^63 本身可被 double 精确表示，
// MaxInt64 (=2^63-1) 不行——落在 2^63 的 double 已超 int64，范围外。
const (
	mathMinInt64AsFloat = -9223372036854775808.0
	mathMaxInt64AsFloat = 9223372036854775808.0 // 2^63，double 能精确表示的最小越界点
)

// ─── 变更队列 ──────────────────────────────────────────────

// Queue 每表一个：同格重复编辑合并（保留最初旧值）、改回原值自动塌缩。
// index 维护 keyOf(rowID,type) → changes 下标，使 find 从 O(n) 降为 O(1)。
type Queue struct {
	changes   []RowChange
	index     map[string]int
	undoStack []undoOp
	seq       int64
}

// New 构造空队列。
func New() *Queue { return &Queue{index: make(map[string]int)} }

// Size 返回已排队行数。
func (q *Queue) Size() int { return len(q.changes) }

// All 按 Sequence 稳定排序返回（提交顺序 = 用户操作顺序）。
func (q *Queue) All() []RowChange {
	out := make([]RowChange, len(q.changes))
	copy(out, q.changes)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

func (q *Queue) find(rowID string, t ChangeType) int {
	k := keyOf(rowID, t)
	if i, ok := q.index[k]; ok {
		return i
	}
	return -1
}

// Get 取某行已存在的变更（update 优先，其次 delete）。
func (q *Queue) Get(rowID string) (RowChange, bool) {
	if i := q.find(rowID, Update); i >= 0 {
		return q.changes[i], true
	}
	if i := q.find(rowID, Delete); i >= 0 {
		return q.changes[i], true
	}
	return RowChange{}, false
}

// Has 报告某行是否有任何未提交变更。
func (q *Queue) Has(rowID string) bool {
	return q.find(rowID, Update) >= 0 ||
		q.find(rowID, Delete) >= 0 ||
		q.find(rowID, Insert) >= 0
}

// AddCellChange 记录一次单元格编辑：改回原值则移除该格改动（全空则移除整行变更）。
// 对 Insert 行直接写 NewRow，不进 CellChanges。
func (q *Queue) AddCellChange(rowID, column string, oldValue, newValue protocol.Value, originalRow Row) {
	if ValuesEqual(oldValue, newValue) {
		return
	}
	if i := q.find(rowID, Insert); i >= 0 {
		nr := q.changes[i].NewRow
		rowSet(&nr, column, newValue)
		q.changes[i].NewRow = nr
		q.undoStack = append(q.undoStack, undoOp{isRow: true, key: keyOf(rowID, Insert)})
		return
	}
	key := keyOf(rowID, Update)
	idx := q.find(rowID, Update)
	if idx < 0 {
		q.seq++
		q.changes = append(q.changes, RowChange{
			RowID:       rowID,
			Type:        Update,
			OriginalRow: originalRow,
			Sequence:    q.seq,
		})
		idx = len(q.changes) - 1
		q.index[key] = idx
	}
	ch := &q.changes[idx]
	orig := rowGet(ch.OriginalRow, column)
	// 新值回到原值 → 移除该格（塌缩）；否则合并 / 追加。
	if ValuesEqual(newValue, orig) {
		ch.CellChanges = retainExceptColumn(ch.CellChanges, column)
		q.undoStack = append(q.undoStack, undoOp{key: key, column: column})
		if len(ch.CellChanges) == 0 {
			q.removeByKey(key)
		}
		return
	}
	found := false
	for i := range ch.CellChanges {
		if ch.CellChanges[i].Column == column {
			ch.CellChanges[i].NewValue = newValue
			found = true
			break
		}
	}
	if !found {
		ch.CellChanges = append(ch.CellChanges, CellChange{Column: column, OldValue: oldValue, NewValue: newValue})
	}
	q.undoStack = append(q.undoStack, undoOp{key: key, column: column})
}

// AddInsert 追加一行插入。
func (q *Queue) AddInsert(rowID string, row Row) {
	q.seq++
	ch := RowChange{
		RowID:    rowID,
		Type:     Insert,
		NewRow:   row,
		Sequence: q.seq,
	}
	q.changes = append(q.changes, ch)
	q.index[keyOf(rowID, Insert)] = len(q.changes) - 1
	q.undoStack = append(q.undoStack, undoOp{isRow: true, key: keyOf(rowID, Insert)})
}

// AddDelete 追加一行删除。
func (q *Queue) AddDelete(rowID string, originalRow Row) {
	q.seq++
	ch := RowChange{
		RowID:       rowID,
		Type:        Delete,
		OriginalRow: originalRow,
		Sequence:    q.seq,
	}
	q.changes = append(q.changes, ch)
	q.index[keyOf(rowID, Delete)] = len(q.changes) - 1
	q.undoStack = append(q.undoStack, undoOp{isRow: true, key: keyOf(rowID, Delete)})
}

func (q *Queue) removeByKey(key string) {
	out := q.changes[:0]
	for _, c := range q.changes {
		if keyOf(c.RowID, c.Type) != key {
			out = append(out, c)
		}
	}
	q.changes = out
	// 重建索引（删除操作不频繁，重建代价可接受）
	q.index = make(map[string]int)
	for i, c := range q.changes {
		q.index[keyOf(c.RowID, c.Type)] = i
	}
}

// Undo 撤销最近一次操作：cell 移除该格改动（全空则整行）；row 移除整条插入 / 删除。
// 无可撤销或撤销无效返回 false。
func (q *Queue) Undo() bool {
	if len(q.undoStack) == 0 {
		return false
	}
	op := q.undoStack[len(q.undoStack)-1]
	q.undoStack = q.undoStack[:len(q.undoStack)-1]
	if op.isRow {
		before := len(q.changes)
		q.removeByKey(op.key)
		return len(q.changes) < before
	}
	i, ok := q.index[op.key]
	if !ok {
		return false
	}
	if q.changes[i].Type != Update {
		return false
	}
	q.changes[i].CellChanges = retainExceptColumn(q.changes[i].CellChanges, op.column)
	if len(q.changes[i].CellChanges) == 0 {
		q.changes = append(q.changes[:i], q.changes[i+1:]...)
		// 重建索引（删除操作不频繁）
		q.index = make(map[string]int)
		for j, c := range q.changes {
			q.index[keyOf(c.RowID, c.Type)] = j
		}
	}
	return true
}

// Clear 清空队列与撤销栈。
func (q *Queue) Clear() {
	q.changes = nil
	q.index = make(map[string]int)
	q.undoStack = nil
}

func retainExceptColumn(cs []CellChange, col string) []CellChange {
	out := cs[:0]
	for _, c := range cs {
		if c.Column != col {
			out = append(out, c)
		}
	}
	return out
}
