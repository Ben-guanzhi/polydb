package tui

// 第三轮候选（round2.md §4 P1「每格重算」）的**量尺**，不改产品代码。
//
// 要量的机制：renderTableDecorated 一次渲染里，值→文本（cellString，内含
// MarshalJSON）被调用几次、每次多贵。按源码读出的调用数：
//   - 列宽测量遍 :127-137  = len(rows) × len(cols)   次（扫**整页**所有行）
//   - 显示遍    :205        = 窗口内行数 × len(cols)  次（同一批格**再算一遍**）
// ⇒ 一次渲染 = (len(rows) + 窗口行数) × 列数 次。翻页/移光标每帧重跑。
//
// 这里用两件事把它钉住：①`BenchmarkCellString` 单独给单价；②`BenchmarkRenderWindowSweep`
// 固定 len(rows) 只扫窗口行数，把窗口从 1 行拉到整页——若调用数真是上面那个式子，
// ns/op 应随窗口行数**线性**上升，斜率≈ 列数 × 单价。斜率与单价对得上，就证明
// "窗口内每格算了两遍"是真实成本而不是猜测。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func benchColumns(n int) []protocol.ResultColumn {
	cols := make([]protocol.ResultColumn, n)
	for i := range cols {
		cols[i] = protocol.ResultColumn{Name: fmt.Sprintf("col_%02d", i), DataType: "TEXT"}
	}
	return cols
}

// benchRows 造 rows 行 cols 列的值，混合 TEXT / INTEGER / NULL / JSON 文本，
// 贴近浏览网格的真实形状（长文本会被 cellCap=24 截断，但 MarshalJSON 仍付全价）。
func benchRows(rows, cols int) [][]protocol.Value {
	out := make([][]protocol.Value, rows)
	long := strings.Repeat("abcdefghij", 6) // 60 字节
	for r := range out {
		row := make([]protocol.Value, cols)
		for c := range row {
			switch (r + c) % 4 {
			case 0:
				row[c] = protocol.NewStringValue(long)
			case 1:
				row[c] = protocol.NewIntValue(int64(r*1000 + c))
			case 2:
				row[c] = protocol.NewNullValue()
			default:
				row[c] = protocol.NewStringValue(`{"a":1,"b":"` + long[:20] + `"}`)
			}
		}
		out[r] = row
	}
	return out
}

func BenchmarkCellString(b *testing.B) {
	cases := []struct {
		name string
		v    protocol.Value
	}{
		{"text60", protocol.NewStringValue(strings.Repeat("abcdefghij", 6))},
		{"int", protocol.NewIntValue(1234567)},
		{"null", protocol.NewNullValue()},
		{"json_text", protocol.NewStringValue(`{"a":1,"b":"abcdefghijklmnopqrst"}`)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for range b.N {
				_ = cellString(c.v)
			}
		})
	}
}

// BenchmarkRenderWindowSweep 固定整页 500 行 × 12 列，只变窗口行数。
func BenchmarkRenderWindowSweep(b *testing.B) {
	cols := benchColumns(12)
	rows := benchRows(500, 12)
	for _, win := range []int{1, 30, 125, 250, 500} {
		b.Run(fmt.Sprintf("window=%d", win), func(b *testing.B) {
			b.ReportMetric(float64(len(rows)*len(cols)+win*len(cols)), "cellString/call")
			for range b.N {
				_ = renderTableDecorated(cols, rows, 200, win, 0, -1, -1, nil)
			}
		})
	}
}

// BenchmarkRenderSameDataRepeated 模拟"同一份数据连续渲染 N 帧"（移光标/翻页），
// 给跨帧记忆化的上界定价：现在每帧都是全价。
func BenchmarkRenderSameDataRepeated(b *testing.B) {
	cols := benchColumns(12)
	rows := benchRows(500, 12)
	const frames = 20
	b.ReportMetric(float64(frames*(500*12+30*12)), "cellString/op")
	b.ResetTimer()
	for range b.N {
		for f := range frames {
			_ = renderTableDecorated(cols, rows, 200, 30, (f*30)%470, -1, -1, nil)
		}
	}
}

// BenchmarkRenderCachedSameData 与上条同料、同帧序，只是带上 widthCache：
// 差值就是本轮机制的价格（第一帧仍全量测量，其后 19 帧跳过全页扫描）。
func BenchmarkRenderCachedSameData(b *testing.B) {
	cols := benchColumns(12)
	rows := benchRows(500, 12)
	const frames = 20
	res := &struct{ tag string }{"bench"}
	b.ResetTimer()
	for range b.N {
		var cache widthCache
		for f := range frames {
			_ = renderTableDecoratedCached(&cache, res, cols, rows, 200, 30, (f*30)%470, -1, -1, nil)
		}
	}
}

// BenchmarkCachedPageSizes 用浏览网格真实的三档页大小（50/200/1000 行）定价：
// 无缓存 = 每帧 Θ(页×列)，有缓存 = 首帧 Θ(页×列) + 其余帧 Θ(窗口×列)。
func BenchmarkCachedPageSizes(b *testing.B) {
	for _, page := range []int{50, 200, 1000} {
		for _, mode := range []string{"uncached", "cached"} {
			b.Run(fmt.Sprintf("page=%d/%s", page, mode), func(b *testing.B) {
				cols := benchColumns(12)
				rows := benchRows(page, 12)
				res := &struct{ tag string }{mode}
				var cache widthCache
				b.ResetTimer()
				for range b.N {
					for f := range 20 {
						start := (f * 7) % max(1, page-30)
						if mode == "cached" {
							_ = renderTableDecoratedCached(&cache, res, cols, rows, 200, 30, start, -1, -1, nil)
						} else {
							_ = renderTableDecoratedCached(nil, nil, cols, rows, 200, 30, start, -1, -1, nil)
						}
					}
				}
			})
		}
	}
}

// TestCallCountModel 把"式子"钉成可回归的形状：测量遍扫整页、显示遍扫窗口。
// 若将来有人把测量收窄到窗口，列宽会随页码变——这条测试先记录当前语义。
func TestCallCountModel(t *testing.T) {
	cols := benchColumns(3)
	rows := benchRows(200, 3)
	// 整页测量 ⇒ 列宽与窗口无关：同一份数据、两个不同窗口起点，宽度必须相同。
	a := renderTableDecorated(cols, rows, 200, 30, 0, -1, -1, nil)
	b := renderTableDecorated(cols, rows, 200, 30, 100, -1, -1, nil)
	widthOf := func(s string) []int {
		l := strings.Split(s, "\n")[0]
		return []int{len(strings.TrimSpace(strings.Split(l, "│")[0]))}
	}
	if widthOf(a)[0] != widthOf(b)[0] {
		t.Fatalf("列宽随窗口起点改变（%d vs %d）——测量遍已经不再扫整页", widthOf(a)[0], widthOf(b)[0])
	}
	if !strings.Contains(a, "col_00 (TEXT)") || !strings.Contains(b, "col_00 (TEXT)") {
		t.Fatalf("表头不见了：\n%.200s", a)
	}
}
