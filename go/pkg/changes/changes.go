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
func ValuesEqual(a, b protocol.Value) bool {
	pa, pb := parseValue(a), parseValue(b)
	if pa.kind == vNumber && pb.kind == vNumber {
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

// ─── 变更队列 ──────────────────────────────────────────────

// Queue 每表一个：同格重复编辑合并（保留最初旧值）、改回原值自动塌缩。
type Queue struct {
	changes   []RowChange
	undoStack []undoOp
	seq       int64
}

// New 构造空队列。
func New() *Queue { return &Queue{} }

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
	for i := range q.changes {
		if keyOf(q.changes[i].RowID, q.changes[i].Type) == k {
			return i
		}
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
	q.upsert(RowChange{
		RowID:    rowID,
		Type:     Insert,
		NewRow:   row,
		Sequence: q.seq,
	})
	q.undoStack = append(q.undoStack, undoOp{isRow: true, key: keyOf(rowID, Insert)})
}

// AddDelete 追加一行删除。
func (q *Queue) AddDelete(rowID string, originalRow Row) {
	q.seq++
	q.upsert(RowChange{
		RowID:       rowID,
		Type:        Delete,
		OriginalRow: originalRow,
		Sequence:    q.seq,
	})
	q.undoStack = append(q.undoStack, undoOp{isRow: true, key: keyOf(rowID, Delete)})
}

func (q *Queue) upsert(ch RowChange) {
	k := keyOf(ch.RowID, ch.Type)
	for i := range q.changes {
		if keyOf(q.changes[i].RowID, q.changes[i].Type) == k {
			q.changes[i] = ch
			return
		}
	}
	q.changes = append(q.changes, ch)
}

func (q *Queue) removeByKey(key string) {
	out := q.changes[:0]
	for _, c := range q.changes {
		if keyOf(c.RowID, c.Type) != key {
			out = append(out, c)
		}
	}
	q.changes = out
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
	i := -1
	for j := range q.changes {
		if keyOf(q.changes[j].RowID, q.changes[j].Type) == op.key {
			i = j
			break
		}
	}
	if i < 0 || q.changes[i].Type != Update {
		return false
	}
	q.changes[i].CellChanges = retainExceptColumn(q.changes[i].CellChanges, op.column)
	if len(q.changes[i].CellChanges) == 0 {
		q.changes = append(q.changes[:i], q.changes[i+1:]...)
	}
	return true
}

// Clear 清空队列与撤销栈。
func (q *Queue) Clear() {
	q.changes = nil
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
