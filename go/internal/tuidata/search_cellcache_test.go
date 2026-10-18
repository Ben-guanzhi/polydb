package tuidata

import (
	"reflect"
	"strings"
	"testing"
)

// searchExactBefore is the pre-cache implementation kept verbatim as the
// oracle: the cached arm must answer the same rows for every shape,
// case-sensitivity included.
func searchExactBefore(f *Frame, needle string) []int {
	rows := f.NumRows()
	out := make([]int, 0, rows)
	for r := 0; r < rows; r++ {
		for c := range f.Columns {
			if strings.Contains(f.CellString(r, c), needle) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// TestSearchExactCachedMatches: for every shape, cached == SearchExact.
func TestSearchExactCachedMatches(t *testing.T) {
	needles := []string{"", "x", "X", "苹果", "apple pie", "zzz", "42", "200", "2", "10", "-"}
	shapeFrames := []*Frame{
		frameOf(Column{Name: "name", Type: TypeString, cells: []any{"apple pie", "Banana", "cherry", nil}}),
		foldFrame(200, 6, "Customer_Name 42"),
		typedFrame(50, 4),
		&Frame{}, // zero columns
	}
	for _, f := range shapeFrames {
		for _, n := range needles {
			var c CellCache
			if got, want := SearchExactCached(f, n, &c), searchExactBefore(f, n); !reflect.DeepEqual(got, want) {
				t.Fatalf("frame=%p needle=%q: cached=%v before=%v", f, n, got, want)
			}
		}
	}
}

// 缓存跨按键生效：连打 5 个键只该建一次。
func TestSearchExactCachesAcrossKeystrokes(t *testing.T) {
	f := foldFrame(200, 6, "Customer_Name 42")
	var c CellCache
	for _, n := range []string{"c", "cu", "cus", "cust", " Custo"} {
		SearchExactCached(f, n, &c)
	}
	if c.builds != 1 {
		t.Fatalf("连打 5 键重扫了 %d 次（应为 1）", c.builds)
	}
}

// 每一维变了都要作废：换帧、改行数、改列数。
func TestSearchExactCachedInvalidatesOnEveryKeyDim(t *testing.T) {
	f := foldFrame(100, 3, "Customer_Name 42")
	var c CellCache
	for i := 0; i < foldKeystrokes; i++ {
		SearchExactCached(f, "cus", &c)
	}
	if c.builds != 1 {
		t.Fatalf("同帧连打建了 %d 次（应为 1）", c.builds)
	}
	SearchExactCached(foldFrame(100, 4, "Customer_Name 42"), "cus", &c)
	if c.builds != 2 {
		t.Fatalf("列数变后建了 %d 次（应为 2）", c.builds)
	}
	SearchExactCached(foldFrame(101, 4, "Customer_Name 42"), "cus", &c)
	if c.builds != 3 {
		t.Fatalf("行数变后建了 %d 次（应为 3）", c.builds)
	}
	SearchExactCached(&Frame{Columns: f.Columns}, "cus", &c)
	if c.builds != 4 {
		t.Fatalf("换帧指针后建了 %d 次（应为 4）", c.builds)
	}
}

// 超限的大帧不缓存，但仍要给出正确答案（＝改前行为）。
func TestSearchExactCachedOverCapSkipsCache(t *testing.T) {
	rows := foldCacheMaxCells/7 + 2
	f := foldFrame(rows, 7, "Customer_Name 42")
	if rows*7 <= foldCacheMaxCells {
		t.Fatalf("测试料没有超限：rows=%d", rows)
	}
	var c CellCache
	if got, want := SearchExactCached(f, "cus", &c), searchExactBefore(f, "cus"); !reflect.DeepEqual(got, want) {
		t.Fatalf("超限帧：cached=%v before=%v", got, want)
	}
	if c.builds != 0 {
		t.Fatalf("超限帧不该建缓存（建了 %d 次）", c.builds)
	}
}

// nil cache 是 no-op：每条键都现算（＝ SearchExact 本身）。
func TestSearchExactCachedNilCacheIsNoOp(t *testing.T) {
	f := foldFrame(50, 3, "Customer_Name 42")
	for _, n := range []string{"", "cus", "CUST", "no"} {
		if got, want := SearchExactCached(f, n, nil), searchExactBefore(f, n); !reflect.DeepEqual(got, want) {
			t.Fatalf("nil cache needle=%q: %v vs %v", n, got, want)
		}
	}
}
