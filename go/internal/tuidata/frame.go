// Package tuidata provides the in-memory columnar table model of the TUI:
// database backends produce Frames and the UI renders them.
package tuidata

import (
	"fmt"
	"strconv"
	"time"
)

// DType identifies the logical type of a column.
type DType uint8

const (
	TypeString DType = iota
	TypeInt
	TypeFloat
	TypeBool
	TypeDate
	TypeDatetime
)

func (t DType) String() string {
	switch t {
	case TypeInt:
		return "i64"
	case TypeFloat:
		return "f64"
	case TypeBool:
		return "bool"
	case TypeDate:
		return "date"
	case TypeDatetime:
		return "datetime"
	default:
		return "str"
	}
}

// Cell values are one of: nil (null), string, int64, float64, bool, time.Time.
// Readers must normalize to exactly these representations.
//
// The cell slice is unexported: a published frame's cell values cannot be
// changed or appended to from outside this package. That is the structural
// half of the immutability contract (the other half is AppendRow's build-time
// rule below); caches that key on frame identity (data.FoldCache, the UI
// width cache) may trust that a frame's cells never change in place.
type Column struct {
	Name  string
	Type  DType
	cells []any
}

// NewColumn builds a column from name, type and cell values. cells is taken
// by ownership (no copy): the caller must not mutate it afterwards. nil
// means an empty column.
func NewColumn(name string, t DType, cells []any) Column {
	return Column{Name: name, Type: t, cells: cells}
}

// Len returns the number of cells (rows) in the column.
func (c Column) Len() int {
	return len(c.cells)
}

// Cell returns the raw value at row i; nil means null.
func (c Column) Cell(i int) any {
	return c.cells[i]
}

// Values returns a copy of all cell values. The copy keeps the immutability
// contract: mutating the result cannot affect the column.
func (c Column) Values() []any {
	return append([]any(nil), c.cells...)
}

// Frame is an immutable table once published. Operations that transform a
// frame (filter, sort, query, cast) produce a new Frame. The contract is
// enforced structurally: cell storage lives behind Column's unexported
// field, so only this package can write it. The one exported mutator,
// AppendRow, is a build-time tool: readers call it while assembling a
// frame, never on a frame already visible to the UI (caches key on
// NumRows, so even a stray append would be detected, but the rule keeps
// "published frames don't change" true by construction).
type Frame struct {
	Columns []Column
}

// New creates an empty frame with the given column names, all string-typed.
func New(names ...string) *Frame {
	cols := make([]Column, len(names))
	for i, n := range names {
		cols[i] = Column{Name: n, Type: TypeString}
	}
	return &Frame{Columns: cols}
}

func (f *Frame) NumCols() int {
	return len(f.Columns)
}

func (f *Frame) NumRows() int {
	if len(f.Columns) == 0 {
		return 0
	}
	return len(f.Columns[0].cells)
}

func (f *Frame) ColumnNames() []string {
	names := make([]string, len(f.Columns))
	for i, c := range f.Columns {
		names[i] = c.Name
	}
	return names
}

// ColumnIndex returns the index of the named column, or -1.
func (f *Frame) ColumnIndex(name string) int {
	for i, c := range f.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Cell returns the raw value at (row, col); nil means null.
func (f *Frame) Cell(row, col int) any {
	return f.Columns[col].cells[row]
}

// CellString renders the value at (row, col) for display.
func (f *Frame) CellString(row, col int) string {
	return FormatValue(f.Columns[col].cells[row])
}

// Row returns the raw values of one row.
func (f *Frame) Row(row int) []any {
	vals := make([]any, len(f.Columns))
	for i := range f.Columns {
		vals[i] = f.Columns[i].cells[row]
	}
	return vals
}

// AppendRow appends one row; vals must have NumCols entries. Build-time
// only: do not call on a frame already visible to the UI.
func (f *Frame) AppendRow(vals []any) {
	for i := range f.Columns {
		f.Columns[i].cells = append(f.Columns[i].cells, vals[i])
	}
}

// PreallocRows reserves capacity for n more rows so subsequent AppendRow
// calls don't reallocate. Build-time only.
func (f *Frame) PreallocRows(n int) {
	for i := range f.Columns {
		c := make([]any, 0, n+len(f.Columns[i].cells))
		c = append(c, f.Columns[i].cells...)
		f.Columns[i].cells = c
	}
}

// Select returns a new frame containing the given rows (in order).
func (f *Frame) Select(rows []int) *Frame {
	out := &Frame{Columns: make([]Column, len(f.Columns))}
	for i, c := range f.Columns {
		cells := make([]any, len(rows))
		for j, r := range rows {
			cells[j] = c.cells[r]
		}
		out.Columns[i] = Column{Name: c.Name, Type: c.Type, cells: cells}
	}
	return out
}

// WithCell returns a copy of f with the cell at (row, col) replaced by
// val. The column type is preserved.
func (f *Frame) WithCell(row, col int, val any) *Frame {
	out := &Frame{Columns: make([]Column, len(f.Columns))}
	for i, c := range f.Columns {
		cells := make([]any, len(c.cells))
		copy(cells, c.cells)
		if i == col {
			cells[row] = val
		}
		out.Columns[i] = Column{Name: c.Name, Type: c.Type, cells: cells}
	}
	return out
}

// WithoutRows returns a copy of f with the rows at the given indices removed.
// Indices are 0-based; out-of-range indices are ignored. Row order of the
// surviving rows is preserved.
func (f *Frame) WithoutRows(rows []int) *Frame {
	if len(f.Columns) == 0 {
		return &Frame{Columns: make([]Column, 0)}
	}
	n := f.NumRows()
	drop := make(map[int]bool, len(rows))
	for _, r := range rows {
		if r >= 0 && r < n {
			drop[r] = true
		}
	}
	out := &Frame{Columns: make([]Column, len(f.Columns))}
	for i, c := range f.Columns {
		cells := make([]any, 0, len(c.cells)-len(drop))
		for r, v := range c.cells {
			if !drop[r] {
				cells = append(cells, v)
			}
		}
		out.Columns[i] = Column{Name: c.Name, Type: c.Type, cells: cells}
	}
	return out
}

// FormatValue renders a cell value for display.
func FormatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprint(x)
	}
}
