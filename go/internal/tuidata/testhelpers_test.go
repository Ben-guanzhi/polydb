package tuidata

import "fmt"

// frameOf 由 concat/search 测试共用的帧构造助手（原定义于已弃用的 infer_test.go）。
func frameOf(cols ...Column) *Frame { return &Frame{Columns: cols} }

// foldFrame 构造 rows×cols 的全同值字符串帧（原定义于已弃用的 bench 文件，
// search_fold_test.go 依赖它）。
func foldFrame(rows, cols int, cell string) *Frame {
	f := &Frame{Columns: make([]Column, cols)}
	for c := 0; c < cols; c++ {
		f.Columns[c] = Column{Name: fmt.Sprintf("c%d", c), Type: TypeString, cells: make([]any, rows)}
		for r := 0; r < rows; r++ {
			f.Columns[c].cells[r] = cell
		}
	}
	return f
}

// typedFrame 构造 rows×cols 的 int64 帧（原定义于已弃用的 bench 文件，
// search_cellcache_test.go 依赖它）。
func typedFrame(rows, cols int) *Frame {
	f := &Frame{Columns: make([]Column, cols)}
	for c := 0; c < cols; c++ {
		f.Columns[c] = Column{Name: fmt.Sprintf("c%d", c), Type: TypeInt, cells: make([]any, rows)}
		for r := 0; r < rows; r++ {
			f.Columns[c].cells[r] = int64(r*1000 + c)
		}
	}
	return f
}

// foldKeystrokes 是折叠搜索测试的按键轮数（原定义于已弃用的 bench 文件）。
const foldKeystrokes = 5
