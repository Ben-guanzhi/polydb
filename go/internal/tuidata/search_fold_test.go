package tuidata

// 产品码 SearchExactColsFold 的契约测试：与改前逐字同形的参考实现差分等值、
// "缓存必须命中"这条性质、键的每个维度都能失效、越界列与大帧回退。
// 差分只证明"改后＝改前"，证明不了"优化真发生了"——所以 builds 断言和
// 逐维度失效断言是这里的主角，不是附加项。

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// refSearchExactCols 是 HEAD 版 SearchExactCols 的函数体，原样抄在这里当差分参考。
func refSearchExactCols(f *Frame, cols []int, needle string) []int {
	lower := strings.ToLower(needle)
	rows := f.NumRows()
	out := make([]int, 0, rows)
	for r := 0; r < rows; r++ {
		for _, c := range cols {
			if c < len(f.Columns) && strings.Contains(strings.ToLower(f.CellString(r, c)), lower) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// foldTestFrame 造一帧：cols 个字符串列，第 c 列第 r 格取 shapes[(r+c)%n]。
func foldTestFrame(cols int, shapes []string) *Frame {
	f := &Frame{Columns: make([]Column, cols)}
	for c := 0; c < cols; c++ {
		f.Columns[c] = Column{Name: fmt.Sprintf("c%d", c), Type: TypeString, cells: make([]any, len(shapes))}
		for r := range shapes {
			f.Columns[c].cells[r] = shapes[r]
		}
	}
	return f
}

var foldTestShapes = []string{
	"customer name here",
	"CUSTOMER NAME HERE",
	"Customer_Name 42",
	"客户名称测试数据一二三",
	"客户Name测试数据",
	"İstanbul",
	string(rune(0x212A)) + "elvin",
	"Straße",
	"",
	"12345678901234",
	strings.Repeat("a", 300) + "B",
}

var foldTestNeedles = []string{
	"", "a", "A", "zz", "name", "NAME", "客户", "名称测试",
	"K", "k", string(rune(0x212A)), string(rune(0x0130)), "ß", "ẞ",
	"abc", "AbC", strings.Repeat("a", 300), "b",
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFoldPathEqualsReference 跨按键地比对三条路：参考实现、cache=nil、带 cache 的第二键之后。
// cols 覆盖全列、子集、单列、含越界列、空集合。
func TestFoldPathEqualsReference(t *testing.T) {
	colSets := [][]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		{0, 2, 4},
		{3},
		{0, 99},   // 含越界列
		{7, 7, 7}, // 重复列
		{},        // 空集合：改前返回空结果，改后必须还是空结果
	}
	for ci, cols := range colSets {
		f := foldTestFrame(6, foldTestShapes)
		var cache FoldCache
		for ki, needle := range foldTestNeedles {
			want := refSearchExactCols(f, cols, needle)
			gotNoCache := SearchExactColsFold(f, cols, needle, nil)
			if !sameIntSlice(want, gotNoCache) {
				t.Fatalf("colset#%d=%v 无缓存路与参考分叉 needle=%q 参考=%v 无缓存=%v", ci, cols, needle, want, gotNoCache)
			}
			gotCached := SearchExactColsFold(f, cols, needle, &cache)
			if !sameIntSlice(want, gotCached) {
				t.Fatalf("colset#%d=%v 缓存路第%d键分叉 needle=%q 参考=%v 缓存=%v", ci, cols, ki, needle, want, gotCached)
			}
			// 公开入口也必须同值（它就是 wrapper）。
			if !sameIntSlice(want, SearchExactCols(f, cols, needle)) {
				t.Fatalf("colset#%d=%v SearchExactCols 与参考分叉 needle=%q", ci, cols, needle)
			}
		}
	}
}

// TestFoldCachesAcrossKeystrokes 钉住本轮的目的本身：同一帧同一列集合连打 5 键，只该建一次。
// 没有这条断言，"build 其实从没被复用"的实现的差分测试照样全绿。
func TestFoldCachesAcrossKeystrokes(t *testing.T) {
	f := foldTestFrame(4, foldTestShapes)
	cols := []int{0, 1, 2, 3}
	var cache FoldCache
	for _, needle := range foldTestNeedles[:foldKeystrokes] {
		SearchExactColsFold(f, cols, needle, &cache)
	}
	if cache.builds != 1 {
		t.Fatalf("连打 %d 键建了 %d 次缓存，缓存没跨按键生效（应为 1）", foldKeystrokes, cache.builds)
	}
	if cache.f != f || cache.rows != f.NumRows() || cache.ncols != len(f.Columns) || !sameCols(cache.cols, cols) {
		t.Fatalf("缓存键没锁住当前身份 f=%p rows=%d ncols=%d cols=%v", cache.f, cache.rows, cache.ncols, cache.cols)
	}
	// needle 变了不该重建。
	SearchExactColsFold(f, cols, "zzzzzz", &cache)
	if cache.builds != 1 {
		t.Fatalf("换 needle 触发重建：%d 次", cache.builds)
	}
}

// TestFoldInvalidatesOnEveryKeyDim 逐维度改身份，每个维度都必须逼出一次重建。
func TestFoldInvalidatesOnEveryKeyDim(t *testing.T) {
	cols := []int{0, 1, 2}

	t.Run("换帧指针", func(t *testing.T) {
		f1, f2 := foldTestFrame(4, foldTestShapes), foldTestFrame(4, foldTestShapes)
		var c FoldCache
		SearchExactColsFold(f1, cols, "a", &c)
		SearchExactColsFold(f2, cols, "a", &c)
		if c.builds != 2 {
			t.Fatalf("同列集合换帧只建了 %d 次", c.builds)
		}
		if c.f != f2 {
			t.Fatalf("缓存身份没跟上新帧")
		}
	})

	t.Run("行数变了", func(t *testing.T) {
		f := foldTestFrame(4, foldTestShapes)
		var c FoldCache
		SearchExactColsFold(f, cols, "a", &c)
		f.AppendRow([]any{"new row", "NEW ROW", "客户", "x", "y"})
		got := SearchExactColsFold(f, cols, "new", &c)
		if c.builds != 2 {
			t.Fatalf("AppendRow 后只建了 %d 次，行数没进键", c.builds)
		}
		if want := refSearchExactCols(f, cols, "new"); !sameIntSlice(want, got) {
			t.Fatalf("追加行后结果分叉 参考=%v 缓存=%v", want, got)
		}
	})

	t.Run("列数变了", func(t *testing.T) {
		f := foldTestFrame(4, foldTestShapes)
		var c FoldCache
		SearchExactColsFold(f, cols, "a", &c)
		f.Columns = append(f.Columns, Column{Name: "extra", Type: TypeString, cells: make([]any, f.NumRows())})
		SearchExactColsFold(f, cols, "a", &c)
		if c.builds != 2 {
			t.Fatalf("加列后只建了 %d 次，列数没进键", c.builds)
		}
	})

	t.Run("列集合变了", func(t *testing.T) {
		f := foldTestFrame(4, foldTestShapes)
		var c FoldCache
		SearchExactColsFold(f, []int{0, 1, 2}, "客户", &c)
		got := SearchExactColsFold(f, []int{3, 4, 5}, "客户", &c)
		if c.builds != 2 {
			t.Fatalf("换列集合只建了 %d 次，cols 没进键", c.builds)
		}
		if want := refSearchExactCols(f, []int{3, 4, 5}, "客户"); !sameIntSlice(want, got) {
			t.Fatalf("换列集合后结果分叉 参考=%v 缓存=%v", want, got)
		}
	})

	// 调用方原地改 cols（app.go 的 p.TableCols 就是可变切片）必须也被键抓住，
	// 这条性质来自 build 里对 cols 的拷贝。
	t.Run("调用方原地改cols", func(t *testing.T) {
		f := foldTestFrame(4, foldTestShapes)
		live := []int{0, 1, 2}
		var c FoldCache
		SearchExactColsFold(f, live, "客户", &c)
		live[1] = 3
		live[2] = 5
		got := SearchExactColsFold(f, live, "客户", &c)
		if c.builds != 2 {
			t.Fatalf("调用方原地改 cols 后只建了 %d 次（缓存里的 cols 必须是拷贝）", c.builds)
		}
		if want := refSearchExactCols(f, live, "客户"); !sameIntSlice(want, got) {
			t.Fatalf("原地改 cols 后结果分叉 参考=%v 缓存=%v", want, got)
		}
	})

	// WithCell 返回新帧：跨按键换帧必须重算（改格的最常见来源）。
	t.Run("WithCell换帧", func(t *testing.T) {
		f := foldTestFrame(4, foldTestShapes)
		var c FoldCache
		SearchExactColsFold(f, cols, "客户", &c)
		f2 := f.WithCell(0, 0, "客户客户客户")
		got := SearchExactColsFold(f2, cols, "客户", &c)
		if c.builds != 2 {
			t.Fatalf("WithCell 换帧后只建了 %d 次", c.builds)
		}
		if want := refSearchExactCols(f2, cols, "客户"); !sameIntSlice(want, got) {
			t.Fatalf("换帧后结果分叉 参考=%v 缓存=%v", want, got)
		}
	})
}

// TestFoldKeyClearedOnMismatch 键不符时旧料要放掉：缓存不能长期滞留一帧的大字符串。
func TestFoldKeyClearedOnMismatch(t *testing.T) {
	f1 := foldTestFrame(4, foldTestShapes)
	f2 := foldTestFrame(4, foldTestShapes)
	var c FoldCache
	SearchExactColsFold(f1, cols4(), "a", &c)
	if c.vals == nil {
		t.Fatal("首键就该建好缓存")
	}
	SearchExactColsFold(f2, cols4(), "zzz", &c)
	if c.vals == nil {
		t.Fatal("换帧后应重建，vals 不该为空")
	}
	// 第三次用回 f1：身份已不在键里，必须重建而不是读到被放掉的旧料。
	got := SearchExactColsFold(f1, cols4(), "a", &c)
	if c.builds != 3 {
		t.Fatalf("放掉旧料后没重建：%d 次", c.builds)
	}
	if want := refSearchExactCols(f1, cols4(), "a"); !sameIntSlice(want, got) {
		t.Fatal("放掉旧料后结果分叉")
	}
}

func cols4() []int { return []int{0, 1, 2, 3} }

// TestFoldOverCapSkipsCache 超过 foldCacheMaxCells 的大帧：结果仍要等于参考，但不建缓存。
func TestFoldOverCapSkipsCache(t *testing.T) {
	rows := foldCacheMaxCells/2 + 1
	cell := "客户名称测试数据一二三"
	f := foldTestFrame(2, []string{cell})
	for c := 0; c < 2; c++ {
		f.Columns[c].cells = make([]any, rows)
		for r := range f.Columns[c].cells {
			f.Columns[c].cells[r] = cell
		}
	}
	cols := []int{0, 1}
	var c FoldCache
	needle := "zz"
	got := SearchExactColsFold(f, cols, needle, &c)
	if c.builds != 0 || c.vals != nil {
		t.Fatalf("超限帧不该建缓存：builds=%d vals==nil=%v", c.builds, c.vals == nil)
	}
	want := refSearchExactCols(f, cols, needle)
	if !sameIntSlice(want, got) {
		t.Fatalf("超限帧结果分叉 参考=%d 行 缓存=%d 行", len(want), len(got))
	}
	// 恰好在上限内的小帧必须建（负对照：把上限写死成 0 时这里要 FAIL）。
	f2 := foldTestFrame(2, []string{cell})
	var c2 FoldCache
	SearchExactColsFold(f2, cols, needle, &c2)
	if c2.builds != 1 {
		t.Fatalf("上限内的小帧没建缓存")
	}
}

// TestFoldCacheNilIsNoOp cache 传 nil 时必须完全等价于改前路径、且不改任何状态。
func TestFoldCacheNilIsNoOp(t *testing.T) {
	f := foldTestFrame(3, foldTestShapes)
	cols := []int{0, 1, 2}
	for _, needle := range foldTestNeedles {
		if got, want := SearchExactColsFold(f, cols, needle, nil), refSearchExactCols(f, cols, needle); !sameIntSlice(want, got) {
			t.Fatalf("nil cache 与参考分叉 needle=%q", needle)
		}
	}
	var c *FoldCache
	SearchExactColsFold(f, cols, "a", c) // 不该 panic
	if f2 := SearchExactColsFold(f, cols, "a", nil); len(f2) == 0 {
		t.Fatal("合成料失效：没有任何行命中")
	}
}

// TestFoldAcrossColumnTypes 覆盖帧的合同里全部六种表示（str/i64/f64/bool/date/datetime）：
// 缓存的是 CellString＝FormatValue 之后的串，所以"折叠后的显示形"必须逐类型都对得上，
// 而且 FormatValue 必须是纯函数（frame.go:174-198 只看值，不读时钟/区域）。
func TestFoldAcrossColumnTypes(t *testing.T) {
	f := New("s", "i", "f", "b", "d", "dt", "mix")
	types := []DType{TypeString, TypeInt, TypeFloat, TypeBool, TypeDate, TypeDatetime, TypeString}
	for i, ty := range types {
		f.Columns[i].Type = ty
	}
	f.AppendRow([]any{"Alice", int64(-42), 3.5, true, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "客户Name"})
	f.AppendRow([]any{nil, int64(0), 0.0, false, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "客户"})
	f.AppendRow([]any{"TRUE", int64(1234567), 2.0, nil, time.Time{}, time.Time{}, "NAME 客户"})
	cols := []int{0, 1, 2, 3, 4, 5, 6}
	// 硬编码的期望行：钉住"缓存里存的必须是 FormatValue 之后、再折叠的显示形"。
	// 若哪天改成缓存原始值（fmt.Sprintf("%v", v)），time.Time 会带上时区和秒，
	// "2026-01-02" 那一档的命中集合就变；若折叠被丢掉（缓存原始显示形做大小写敏感匹配），
	// "TRUE" 那一档只余第 3 行。差分参考永远跟着 CellString 走，所以这几档的另一层用处是：
	// 显示形本身被改动时它红，而不是两侧一起漂（§8.4 TestCallCountModel 同型）。
	for _, exp := range []struct {
		needle string
		want   []int
	}{
		{"-42", []int{0}},
		{"03:04:05", []int{1}},
		{"2026-01-02", []int{0, 1}},
		{"客户", []int{0, 1, 2}},
		{"TRUE", []int{0, 2}}, // 折叠后 "true"：第 1 行的 bool true 与第 3 行的字符串 "TRUE" 都该命中
		{"3.5", []int{0}},
	} {
		gotAll := SearchExactColsFold(f, cols, exp.needle, &FoldCache{})
		if !sameIntSlice(exp.want, gotAll) {
			t.Fatalf("needle=%q 期望行 %v，实得 %v（显示形合同被破）", exp.needle, exp.want, gotAll)
		}
	}
	var c FoldCache
	for _, needle := range []string{"", "a", "42", "true", "false", "0", "2026-01-02", "15:04:05", "客户", "name", "NAME", "0001"} {
		want := refSearchExactCols(f, cols, needle)
		got := SearchExactColsFold(f, cols, needle, &c)
		if !sameIntSlice(want, got) {
			t.Fatalf("六种表示的帧在 needle=%q 分叉 参考=%v 缓存=%v", needle, want, got)
		}
		if got0 := SearchExactColsFold(f, cols, needle, nil); !sameIntSlice(want, got0) {
			t.Fatalf("needle=%q 无缓存路与参考分叉", needle)
		}
	}
	if c.builds != 1 {
		t.Fatalf("同一帧连打 %d 键建了 %d 次缓存", 12, c.builds)
	}
	// 同一帧换显示形（WithCell 造新帧）必须重算。
	f2 := f.WithCell(0, 1, int64(999999))
	if got := SearchExactColsFold(f2, cols, "999999", &c); !sameIntSlice(refSearchExactCols(f2, cols, "999999"), got) {
		t.Fatalf("改值换帧后分叉 got=%v", got)
	}
	if c.builds != 2 {
		t.Fatalf("WithCell 之后没重建：%d", c.builds)
	}
}
