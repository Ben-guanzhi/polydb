package tui

// 第九轮 ① 段：tableColsSubFrame 那一支的**分配账探针**。只测不改——
// 本文件不碰产品码；tableview.go 的指纹必须在跑前跑后逐字相同
// （926 行 / 29,473 B / md5 ba750de06c862fc46cdd2f557cd21eac）。
//
// 形状取自 §12 的 E 档：父帧 50000×20、cols＝{0,1} ⇒ 子帧 50000×2。
// 三条腿分开量：
//
//	(a) live 数据本体   bodyA   每列一次 make([]any,rows) ＋ 下标直写
//	(b) append 再生长   growAB  Cells 从 nil 起 AppendRow，行切片循环外建一次
//	(c) 每行临时切片    prod − growAB（＝0 则说明它被逃逸分析留在栈上）
//	                   rowHeapC 是 (c) 的反事实：强制逃逸要付多少
//
// 候选形状各一臂：preallocC（C＝只给 Cells 预分配 cap）、winD（D＝只物化行窗口）。
// 每条**语义零变化**的臂都拿 reflect.DeepEqual 与真 prod 差分钉住，
// 防止量出一个产品里不存在的形状。

import (
	"reflect"
	"testing"

	"github.com/polydb/polydb/internal/tuidata"
)

const (
	p13Rows       = 50000   // §12 E 档行数
	p13ParentCols = 20      // 父帧列数
	p13WinRows    = 200     // sampleRows（tableview.go:20）＝候选 D 的行跨度
	p13Kind       = "ascii" // 值全部来自包级常量池 ⇒ 拷格子不产生新分配
)

var p13Cols = []int{0, 1}

// keep13 防止编译器把整臂删掉；sinkRow13 只用来**逼**行切片逃逸（rowHeapC 臂）。
var (
	keep13    any
	sinkRow13 []any
)

// pfFixed13 ＝ (a) 之前的固定层：names + tuidata.New + 类型，一行不填。
func pfFixed13(parent *tuidata.Frame, cols []int) *tuidata.Frame {
	names := make([]string, len(cols))
	for i, c := range cols {
		if c < parent.NumCols() {
			names[i] = parent.Columns[c].Name
		}
	}
	out := tuidata.New(names...)
	for i, c := range cols {
		if c < parent.NumCols() {
			out.Columns[i].Type = parent.Columns[c].Type
		}
	}
	return out
}

// pfBody13 ＝ (a)：每列一次 make([]any, rows)，下标直写；无 append、无行切片。
// cells 在调用方手里写满，经 NewColumn 移交所有权（data 包外唯一的直写通道）。
func pfBody13(parent *tuidata.Frame, cols []int) *tuidata.Frame {
	out := pfFixed13(parent, cols)
	rows := parent.NumRows()
	for i, c := range cols {
		cells := make([]any, rows)
		if c < parent.NumCols() {
			for r := 0; r < rows; r++ {
				cells[r] = parent.Cell(r, c)
			}
			out.Columns[i] = tuidata.NewColumn(parent.Columns[c].Name, parent.Columns[c].Type, cells)
		} else {
			out.Columns[i] = tuidata.NewColumn(out.Columns[i].Name, out.Columns[i].Type, cells)
		}
	}
	return out
}

// pfGrow13 ＝ (a)+(b)：Cells 从 nil 起 AppendRow；行切片**在循环外**建一次 ⇒ (c) 摘掉。
func pfGrow13(parent *tuidata.Frame, cols []int) *tuidata.Frame {
	out := pfFixed13(parent, cols)
	row := make([]any, len(cols))
	for r := 0; r < parent.NumRows(); r++ {
		for i, c := range cols {
			if c < parent.NumCols() {
				row[i] = parent.Cell(r, c)
			}
		}
		out.AppendRow(row)
	}
	return out
}

// pfPrealloc13 ＝ 候选 C：Cells 预分配 cap=rows，其余逐字同 prod。
// reuse 为真时行切片只建一次（(c)＝0 的旁证臂）。
func pfPrealloc13(parent *tuidata.Frame, cols []int, reuse bool) *tuidata.Frame {
	out := pfFixed13(parent, cols)
	rows := parent.NumRows()
	out.PreallocRows(rows)
	shared := make([]any, len(cols))
	for r := 0; r < rows; r++ {
		row := shared
		if !reuse {
			row = make([]any, len(cols))
		}
		for i, c := range cols {
			if c < parent.NumCols() {
				row[i] = parent.Cell(r, c)
			}
		}
		out.AppendRow(row)
	}
	return out
}

// pfRowHeap13 ＝ (c) 的反事实：Cells 已预分配，行切片每行 make 且**强制逃逸**。
func pfRowHeap13(parent *tuidata.Frame, cols []int) *tuidata.Frame {
	out := pfFixed13(parent, cols)
	rows := parent.NumRows()
	out.PreallocRows(rows)
	for r := 0; r < rows; r++ {
		row := make([]any, len(cols))
		for i, c := range cols {
			if c < parent.NumCols() {
				row[i] = parent.Cell(r, c)
			}
		}
		out.AppendRow(row)
		sinkRow13 = row // 包级写 ⇒ row 必逃逸
	}
	return out
}

// pfWin13 ＝ 候选 D：只物化前 win 行（渲染路径的行读点全在 [rowOff, rowOff+max(body,sampleRows)) 内，
// 见 tableview.go:291 与 :401-403 的行循环）。cap 预分配，与 C 同形。
func pfWin13(parent *tuidata.Frame, cols []int, win int) *tuidata.Frame {
	out := pfFixed13(parent, cols)
	rows := min(parent.NumRows(), win)
	out.PreallocRows(rows)
	row := make([]any, len(cols))
	for r := 0; r < rows; r++ {
		for i, c := range cols {
			if c < parent.NumCols() {
				row[i] = parent.Cell(r, c)
			}
		}
		out.AppendRow(row)
	}
	return out
}

func BenchmarkSubFramePrice13(b *testing.B) {
	f := benchFrame(p13Rows, p13ParentCols, p13Kind)
	if f.NumRows() != p13Rows || f.NumCols() != p13ParentCols {
		b.Fatalf("父帧 %d×%d ≠ %d×%d", f.NumRows(), f.NumCols(), p13Rows, p13ParentCols)
	}

	// 差分钉：语义零变化必须由真 prod 证明，不许"我以为等价"。
	prod := tableColsSubFrame(f, p13Cols)
	if prod.NumRows() != p13Rows || prod.NumCols() != len(p13Cols) {
		b.Fatalf("prod 子帧 %d×%d 不对", prod.NumRows(), prod.NumCols())
	}
	for _, eq := range []struct {
		name string
		got  *tuidata.Frame
	}{
		{"bodyA", pfBody13(f, p13Cols)},
		{"growAB", pfGrow13(f, p13Cols)},
		{"preallocC", pfPrealloc13(f, p13Cols, false)},
		{"preallocReuse", pfPrealloc13(f, p13Cols, true)},
		{"rowHeapC", pfRowHeap13(f, p13Cols)},
	} {
		if !reflect.DeepEqual(eq.got, prod) {
			b.Fatalf("%s 与 prod 不等价 ⇒ 这一臂量的不是产品里那个形状", eq.name)
		}
	}
	// winD 只许是 prod 的前缀（下标 0..win-1 逐格相等，行数＝win）。
	if w := pfWin13(f, p13Cols, p13WinRows); w.NumRows() != p13WinRows {
		b.Fatalf("winD 行数 %d ≠ %d", w.NumRows(), p13WinRows)
	} else {
		for i := range w.Columns {
			for r := 0; r < p13WinRows; r++ {
				if w.Cell(r, i) != prod.Cell(r, i) {
					b.Fatalf("winD 第 %d 列第 %d 行与 prod 前缀不符", i, r)
				}
			}
		}
	}

	arms := []struct {
		name string
		fn   func(*tuidata.Frame) *tuidata.Frame
		rows int
	}{
		{"prod", func(x *tuidata.Frame) *tuidata.Frame { return tableColsSubFrame(x, p13Cols) }, p13Rows},
		{"fixed", func(x *tuidata.Frame) *tuidata.Frame { return pfFixed13(x, p13Cols) }, 0},
		{"bodyA", func(x *tuidata.Frame) *tuidata.Frame { return pfBody13(x, p13Cols) }, p13Rows},
		{"growAB", func(x *tuidata.Frame) *tuidata.Frame { return pfGrow13(x, p13Cols) }, p13Rows},
		{"preallocC", func(x *tuidata.Frame) *tuidata.Frame { return pfPrealloc13(x, p13Cols, false) }, p13Rows},
		{"preallocReuse", func(x *tuidata.Frame) *tuidata.Frame { return pfPrealloc13(x, p13Cols, true) }, p13Rows},
		{"rowHeapC", func(x *tuidata.Frame) *tuidata.Frame { return pfRowHeap13(x, p13Cols) }, p13Rows},
		{"winD", func(x *tuidata.Frame) *tuidata.Frame { return pfWin13(x, p13Cols, p13WinRows) }, p13WinRows},
	}
	for _, ar := range arms {
		ar := ar
		b.Run(ar.name, func(b *testing.B) {
			b.SetBytes(int64(ar.rows * len(p13Cols) * 16))
			for i := 0; i < b.N; i++ {
				got := ar.fn(f)
				if got.NumRows() != ar.rows || got.NumCols() != len(p13Cols) {
					b.Fatalf("%s 子帧 %d×%d ≠ %d×%d", ar.name,
						got.NumRows(), got.NumCols(), ar.rows, len(p13Cols))
				}
				if got.NumRows() > 0 {
					keep13 = got.Cell(got.NumRows()-1, got.NumCols()-1)
				} else {
					keep13 = got.Columns[got.NumCols()-1].Name
				}
			}
		})
	}
}

// TestSubFrameProbe13Shapes 把上面那些臂的**形状**独立钉一次（不依赖 benchmark 跑过）：
// 四条改写臂与 prod 逐字相等；winD 是 prod 的行前缀；fixed 是 0 行。
func TestSubFrameProbe13Shapes(t *testing.T) {
	f := benchFrame(p13Rows, p13ParentCols, p13Kind)
	prod := tableColsSubFrame(f, p13Cols)
	for _, eq := range []struct {
		name string
		got  *tuidata.Frame
	}{
		{"bodyA", pfBody13(f, p13Cols)},
		{"growAB", pfGrow13(f, p13Cols)},
		{"preallocC", pfPrealloc13(f, p13Cols, false)},
		{"preallocReuse", pfPrealloc13(f, p13Cols, true)},
		{"rowHeapC", pfRowHeap13(f, p13Cols)},
	} {
		if !reflect.DeepEqual(eq.got, prod) {
			t.Errorf("%s 与 prod 不等价", eq.name)
		}
	}
	if fx := pfFixed13(f, p13Cols); fx.NumRows() != 0 || fx.NumCols() != len(p13Cols) {
		t.Errorf("fixed 应是 0 行 %d 列，实得 %d×%d", len(p13Cols), fx.NumRows(), fx.NumCols())
	}
	w := pfWin13(f, p13Cols, p13WinRows)
	if w.NumRows() != p13WinRows {
		t.Errorf("winD 行数 %d ≠ %d", w.NumRows(), p13WinRows)
	}
	for i := range w.Columns {
		if w.Columns[i].Name != prod.Columns[i].Name || w.Columns[i].Type != prod.Columns[i].Type {
			t.Errorf("winD 第 %d 列的名字/类型与 prod 不符", i)
		}
		for r := 0; r < w.NumRows(); r++ {
			if w.Cell(r, i) != prod.Cell(r, i) {
				t.Fatalf("winD (%d,%d) 与 prod 前缀不符", i, r)
			}
		}
	}
}
