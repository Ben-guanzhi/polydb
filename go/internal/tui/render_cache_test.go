package tui

// 锁住 renderTableDecoratedCached 的两条前提：
//  T-A 逐字节行为等价（含脏格装饰、参差行、空页、光标在/不在窗口内）；
//  T-B 失效条件齐全（换结果集、行数/列数变化 ⇒ 必须 miss；跨 maxWidth 复用 ⇒ 缓存本体
//      必须仍是「未缩放」的那份，否则第二次窄屏渲染会污染第三次宽屏渲染）。

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

type fakeRes struct{ tag string }

func cacheCases() []struct {
	name             string
	rows, cols       int
	maxWidth, window int
	start            int
	curRow, curCol   int
	ragged           bool
	dirty            bool
} {
	return []struct {
		name             string
		rows, cols       int
		maxWidth, window int
		start            int
		curRow, curCol   int
		ragged           bool
		dirty            bool
	}{
		{"首页", 60, 5, 120, 20, 0, -1, -1, false, false},
		{"翻页到中段", 60, 5, 120, 20, 25, 30, 2, false, false},
		{"翻页到底", 60, 5, 120, 20, 45, 59, 4, false, false},
		{"光标在窗口外", 60, 5, 120, 20, 10, 3, 0, false, false},
		{"窗口大于页", 7, 5, 120, 30, 0, 3, 1, false, false},
		{"空页", 0, 5, 120, 20, 0, -1, -1, false, false},
		{"窄屏强制缩放", 60, 8, 40, 15, 3, 5, 5, false, false},
		{"参差行", 40, 6, 100, 12, 7, 9, 3, true, false},
		{"脏格装饰", 40, 6, 100, 12, 7, 9, 3, false, true},
		{"参差+脏格+窄屏", 40, 9, 44, 12, 11, 12, 8, true, true},
		{"整页窗口", 200, 12, 200, 200, 0, 77, 3, false, false},
	}
}

func cacheRows(n, cols int, ragged bool) [][]protocol.Value {
	rows := make([][]protocol.Value, n)
	for r := range rows {
		c := cols
		if ragged {
			switch r % 4 {
			case 0:
				c = cols - 2 // 短行：缺尾列
			case 1:
				c = cols + 3 // 长行：越界列（测量遍按 i>=len(widths) 截断）
			}
			if c < 0 {
				c = 0
			}
		}
		row := make([]protocol.Value, c)
		for j := range row {
			switch (r + j) % 5 {
			case 0:
				row[j] = protocol.NewStringValue(strings.Repeat("abcdefghij", 6))
			case 1:
				row[j] = protocol.NewIntValue(int64(r*100 + j))
			case 2:
				row[j] = protocol.NewNullValue()
			case 3:
				row[j] = protocol.NewStringValue("中文宽字符——测列用")
			default:
				row[j] = protocol.NewStringValue(`{"k":1,"v":"` + strings.Repeat("x", j+1) + `"}`)
			}
		}
		rows[r] = row
	}
	return rows
}

// dirtyAt 覆盖两个格的显示文本（其中一个是超长文本，用来确认脏值不参与列宽）。
func dirtyAt() cellDecorator {
	long := strings.Repeat("Z", 200)
	return func(rowIdx, colIdx int) (string, bool) {
		if rowIdx == 8 && colIdx == 3 {
			return long, true
		}
		if rowIdx == 9 && colIdx == 0 {
			return "已暂存", true
		}
		return "", false
	}
}

// T-A1：同一结果集上，缓存路径与无缓存路径逐字节相同；且第二/三帧（命中路径）同样相同。
func TestCachedRenderByteIdenticalToUncached(t *testing.T) {
	for _, tc := range cacheCases() {
		t.Run(tc.name, func(t *testing.T) {
			cols := benchColumns(tc.cols)
			rows := cacheRows(tc.rows, tc.cols, tc.ragged)
			res := &fakeRes{tag: tc.name}
			var decor cellDecorator
			if tc.dirty {
				decor = dirtyAt()
			}
			want := renderTableDecorated(cols, rows, tc.maxWidth, tc.window, tc.start, tc.curRow, tc.curCol, decor)
			var cache widthCache
			for frame := 0; frame < 3; frame++ {
				got := renderTableDecoratedCached(&cache, res, cols, rows, tc.maxWidth, tc.window, tc.start, tc.curRow, tc.curCol, decor)
				if got != want {
					t.Fatalf("第 %d 帧与无缓存输出不同\n--- uncached ---\n%s\n--- cached ---\n%s", frame+1, want, got)
				}
			}
			// nil 缓存 / nil 身份都必须退化成全量重算，输出同样不变。
			if got := renderTableDecoratedCached(nil, res, cols, rows, tc.maxWidth, tc.window, tc.start, tc.curRow, tc.curCol, decor); got != want {
				t.Fatalf("cache=nil 退化路径输出不同:\n%s\nvs\n%s", want, got)
			}
			var nilIDCache widthCache
			for frame := 0; frame < 3; frame++ {
				if got := renderTableDecoratedCached(&nilIDCache, nil, cols, rows, tc.maxWidth, tc.window, tc.start, tc.curRow, tc.curCol, decor); got != want {
					t.Fatalf("res=nil 退化路径第 %d 帧输出不同:\n%s\nvs\n%s", frame+1, want, got)
				}
			}
			// res=nil 不该被存进缓存（否则三次 nil 帧里后两次是"无身份命中"）
			if _, ok := nilIDCache.lookup(nil, len(rows), len(cols)); ok {
				t.Fatalf("res=nil 的结果被缓存了——无身份命中")
			}
		})
	}
}

// T-A2：装饰脏格不参与列宽（当前语义）。缓存不需要随脏格失效，这条就是那个前提的证词。
func TestDirtyCellsDoNotAffectWidths(t *testing.T) {
	cols := benchColumns(6)
	rows := cacheRows(40, 6, false)
	plain := renderTableDecorated(cols, rows, 200, 12, 0, -1, -1, nil)
	withDirty := renderTableDecorated(cols, rows, 200, 12, 0, -1, -1, dirtyAt())
	w := func(s string) string { return strings.Split(s, "\n")[1] } // 分隔行＝各列宽度指纹
	if len(w(plain)) != len(w(withDirty)) {
		t.Fatalf("脏格改变了列宽：%d vs %d", len(w(plain)), len(w(withDirty)))
	}
	// 且缓存版本在「先无脏、后有脏」的帧序下仍逐字节等于无缓存的带脏输出。
	var cache widthCache
	res := &fakeRes{"dirty-order"}
	if got := renderTableDecoratedCached(&cache, res, cols, rows, 200, 12, 0, -1, -1, nil); got != plain {
		t.Fatalf("无脏帧不等价")
	}
	if got := renderTableDecoratedCached(&cache, res, cols, rows, 200, 12, 0, -1, -1, dirtyAt()); got != withDirty {
		t.Fatalf("命中缓存后的带脏帧不等价:\n%s\nvs\n%s", withDirty, got)
	}
}

// T-B1：跨 maxWidth 复用同一份缓存不得互相污染——缓存里必须是「未缩放」的列宽。
// 这是这个缓存唯一能造成的真实回归：第二次窄屏渲染就地缩放的是**共享切片**的话，
// 第三次宽屏渲染就会拿到被压扁的宽度。
func TestCacheIsNotPollutedByScaling(t *testing.T) {
	cols := benchColumns(10)
	rows := cacheRows(80, 10, false)
	res := &fakeRes{"pollution"}
	var cache widthCache
	wantWide := renderTableDecorated(cols, rows, 240, 20, 0, -1, -1, nil)
	narrow := renderTableDecorated(cols, rows, 30, 20, 0, -1, -1, nil)

	if got := renderTableDecoratedCached(&cache, res, cols, rows, 240, 20, 0, -1, -1, nil); got != wantWide {
		t.Fatalf("第 1 帧（宽屏）不等价")
	}
	if got := renderTableDecoratedCached(&cache, res, cols, rows, 30, 20, 0, -1, -1, nil); got != narrow {
		t.Fatalf("第 2 帧（窄屏）不等价")
	}
	if got := renderTableDecoratedCached(&cache, res, cols, rows, 240, 20, 0, -1, -1, nil); got != wantWide {
		t.Fatalf("第 3 帧（回到宽屏）被窄屏污染:\n%s\nvs\n%s", wantWide, got)
	}
	// 缓存本体必须等于独立的测量结果（未钳制前 max 是 cellCap，未被缩放）。
	if got := measureCellWidths(cols, rows); len(cache.width) != len(got) {
		t.Fatalf("缓存宽度长度 %d ≠ 测量长度 %d", len(cache.width), len(got))
	} else {
		for i := range got {
			if cache.width[i] != got[i] {
				t.Fatalf("缓存本体被就地缩放污染：列 %d 缓存 %d，重测 %d", i, cache.width[i], got[i])
			}
		}
	}
}

// T-B2：失效条件。换指针 / 行数变 / 列数变，都必须重新测量并给出与无缓存一致的输出。
func TestCacheInvalidation(t *testing.T) {
	cols := benchColumns(6)
	rows := cacheRows(50, 6, false)
	res := &fakeRes{"page-1"}
	var cache widthCache
	one := renderTableDecoratedCached(&cache, res, cols, rows, 200, 20, 0, -1, -1, nil)
	if one != renderTableDecorated(cols, rows, 200, 20, 0, -1, -1, nil) {
		t.Fatalf("首帧不等价")
	}
	if hit, ok := cache.lookup(res, len(rows), len(cols)); !ok || hit == nil {
		t.Fatalf("同键应当命中")
	}

	// ① 换结果集（下一页）
	other := &fakeRes{"page-2"}
	otherRows := cacheRows(50, 6, true)
	got := renderTableDecoratedCached(&cache, other, cols, otherRows, 200, 20, 0, -1, -1, nil)
	if want := renderTableDecorated(cols, otherRows, 200, 20, 0, -1, -1, nil); got != want {
		t.Fatalf("换指针后未重测:\n%s\nvs\n%s", want, got)
	}

	// ② 同一指针、原地追加行（本仓库现在没有这种写法；这条锁住的是键为什么要带行数）。
	// 起点必须是**窄值**：若各列早已被 cellCap=24 钳住，再 append 一个 40 字符的行不会改变
	// 任何列宽，这条用例就没有牙齿（负对照实测：把行数从键里删掉，全绿）。
	res2 := &fakeRes{"grown"}
	rows2 := make([][]protocol.Value, 50)
	for i := range rows2 {
		row := make([]protocol.Value, 6)
		for j := range row {
			row[j] = protocol.NewStringValue("x")
		}
		rows2[i] = row
	}
	first := renderTableDecoratedCached(&cache, res2, cols, rows2, 200, 20, 0, -1, -1, nil)
	if want := renderTableDecorated(cols, rows2, 200, 20, 0, -1, -1, nil); first != want {
		t.Fatalf("窄页首帧不等价")
	}
	wide := make([]protocol.Value, 6)
	for j := range wide {
		wide[j] = protocol.NewStringValue(strings.Repeat("W", 40))
	}
	rows2 = append(rows2, wide)
	got = renderTableDecoratedCached(&cache, res2, cols, rows2, 200, 20, 0, -1, -1, nil)
	want2 := renderTableDecorated(cols, rows2, 200, 20, 0, -1, -1, nil)
	if got != want2 {
		t.Fatalf("行数变化未失效（键里少了行数）:\n%s\nvs\n%s", want2, got)
	}
	if strings.Split(want2, "\n")[1] == strings.Split(first, "\n")[1] {
		t.Fatalf("这条用例没有牙齿：追加宽行前后分隔行一样长，列宽根本没变")
	}

	// ③ 同一指针、列数变化
	cols3 := benchColumns(3)
	rows3 := cacheRows(10, 3, false)
	res3 := &fakeRes{"cols"}
	renderTableDecoratedCached(&cache, res3, cols3, rows3, 200, 5, 0, -1, -1, nil)
	got = renderTableDecoratedCached(&cache, res3, append(cols3, protocol.ResultColumn{Name: "extra", DataType: "TEXT"}), rows3, 200, 5, 0, -1, -1, nil)
	if want := renderTableDecorated(append(cols3, protocol.ResultColumn{Name: "extra", DataType: "TEXT"}), rows3, 200, 5, 0, -1, -1, nil); got != want {
		t.Fatalf("列数变化未失效（键里少了列数）")
	}
}

// T-B3：lookup 返回的是副本——调用方就地缩放的必须是副本。
// 判据锚在**独立重测的宽度**上，而不是 `cache.width` 本体：负对照（让 lookup 直接返回
// 共享切片）证明"跟 cache.width 比"是同义反复、测不出任何东西（两边一起被改脏，比较照样相等）。
func TestLookupReturnsCopy(t *testing.T) {
	cols := benchColumns(4)
	rows := cacheRows(10, 4, false)
	res := &fakeRes{"copy"}
	var cache widthCache
	renderTableDecoratedCached(&cache, res, cols, rows, 200, 5, 0, -1, -1, nil)
	want := measureCellWidths(cols, rows)
	a, ok := cache.lookup(res, len(rows), len(cols))
	if !ok {
		t.Fatalf("同键未命中，无从谈副本语义")
	}
	for i := range a {
		a[i] = 1 // 就地"缩放"，模拟渲染里那两步
	}
	b, _ := cache.lookup(res, len(rows), len(cols))
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("lookup 返回了共享切片：改过一份之后第 %d 列读到 %d，独立重测为 %d", i, b[i], want[i])
		}
	}
}
