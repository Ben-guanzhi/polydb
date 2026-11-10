package changes

import (
	"math/big"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func sv(x string) protocol.Value { return protocol.NewStringValue(x) }
func iv(x int64) protocol.Value  { return protocol.NewIntValue(x) }
func fv(x float64) protocol.Value {
	return protocol.NewFloatValue(x)
}

// nv 从精确十进制文本构造数值 Value（big 大整数的唯一保真形态——
// protocol.Value 的 raw 就是 JSON 数字原文，直接写文本）。
func nv(lit string) protocol.Value {
	return protocol.NewRawNumberValue(lit)
}

func mkRow(pairs ...Cell) Row { return Row(pairs) }

func cell(c string, v protocol.Value) Cell { return Cell{Column: c, Value: v} }

// 大整数编辑的 no-op 判定：两个 >2^53 的相邻整数经 Float64() 会塌成同一个
// double 而误判相等，编辑被静默吞掉。ValuesEqual 必须走精确整数路径。
// （Rust 端同名守卫：int_float_equality_is_exact_above_two_pow_53。）
func TestValuesEqualBigintAboveTwoPow53(t *testing.T) {
	even := "9007199254740992" // 2^53，double 可精确表示
	odd := "9007199254740993"  // 2^53+1，塌成与 2^53 同一 double
	if ValuesEqual(nv(even), nv(odd)) {
		t.Fatal("adjacent integers above 2^53 must stay distinct (old float64 path collapsed them)")
	}
	if !ValuesEqual(nv(even), nv(even)) {
		t.Fatal("identical big integers must be equal")
	}
	max := "18446744073709551615" // MaxUint64
	if ValuesEqual(nv(max), nv("18446744073709551614")) {
		t.Fatal("MaxUint64 vs MaxUint64-1 must stay distinct")
	}
	if !ValuesEqual(nv(max), nv(max)) {
		t.Fatal("identical max uint64 must be equal")
	}
	// 小整数路径不受影响。
	if !ValuesEqual(iv(5), iv(5)) || ValuesEqual(iv(5), iv(6)) {
		t.Fatal("small integer equality broken")
	}
}

// int/float 相等语义保持：5（整数）与 5.0（浮点）判等（与 Rust/Web 端一致）；
// 带真实小数部分的不等；范围外浮点不可能与 int64 精确相等。
func TestValuesEqualIntFloatBoundary(t *testing.T) {
	if !ValuesEqual(iv(5), fv(5.0)) || !ValuesEqual(fv(5.0), iv(5)) {
		t.Fatal("int 5 vs float 5.0 must be equal")
	}
	if ValuesEqual(iv(5), fv(5.5)) {
		t.Fatal("int 5 vs float 5.5 must differ")
	}
	// 2^53 与 2^53+1 的浮点塌缩：float(2^53+1) 数学上不存在（塌成 2^53），
	// 这里钉的是"整数值浮点与大整数"经 big.Float 比较仍精确。
	if ValuesEqual(nv("9007199254740993"), fv(9007199254740992.0)) {
		t.Fatal("2^53+1 (exact integer) vs float 2^53 must differ — the double cannot represent 2^53+1")
	}
	if !ValuesEqual(nv("9007199254740992"), fv(9007199254740992.0)) {
		t.Fatal("2^53 vs float 2^53 must be equal")
	}
	if _, ok := new(big.Int).SetString("9007199254740993", 10); !ok {
		t.Fatal("sanity: big.Int parse")
	}
}

func TestMergesRepeatedEditsKeepingOriginalOldValue(t *testing.T) {
	q := New()
	orig := mkRow(cell("a", sv("x")), cell("b", iv(1)))
	q.AddCellChange("r1", "a", sv("x"), sv("y"), orig)
	q.AddCellChange("r1", "a", sv("y"), sv("z"), orig)
	c, ok := q.Get("r1")
	if !ok {
		t.Fatal("r1 not found")
	}
	if len(c.CellChanges) != 1 {
		t.Fatalf("cell changes = %d, want 1", len(c.CellChanges))
	}
	if c.CellChanges[0].Column != "a" || !ValuesEqual(c.CellChanges[0].OldValue, sv("x")) || !ValuesEqual(c.CellChanges[0].NewValue, sv("z")) {
		t.Fatalf("merged change wrong: %+v", c.CellChanges[0])
	}
	if q.Size() != 1 {
		t.Fatalf("size = %d, want 1", q.Size())
	}
}

func TestCollapsesWhenEditedBackToOriginal(t *testing.T) {
	q := New()
	orig := mkRow(cell("a", sv("x")))
	q.AddCellChange("r1", "a", sv("x"), sv("y"), orig)
	if q.Size() != 1 {
		t.Fatalf("size = %d, want 1", q.Size())
	}
	q.AddCellChange("r1", "a", sv("y"), sv("x"), orig)
	if q.Size() != 0 {
		t.Fatalf("size = %d, want 0 after collapse", q.Size())
	}
}

func TestIgnoresNoopAndTreatsIntFloatAsEqual(t *testing.T) {
	q := New()
	q.AddCellChange("r1", "a", sv("x"), sv("x"), mkRow(cell("a", sv("x"))))
	if q.Size() != 0 {
		t.Fatalf("noop recorded: size = %d", q.Size())
	}
	q.AddCellChange("r2", "n", iv(5), fv(5.0), mkRow(cell("n", iv(5))))
	if q.Size() != 0 {
		t.Fatalf("int/float 5 vs 5.0 should be equal: size = %d", q.Size())
	}
}

func TestUndoesRowLevelOps(t *testing.T) {
	q := New()
	q.AddDelete("r1", mkRow(cell("id", iv(1)), cell("a", sv("x"))))
	q.AddInsert("r2", mkRow(cell("id", iv(2)), cell("a", sv("y"))))
	if q.Size() != 2 {
		t.Fatalf("size = %d, want 2", q.Size())
	}
	if !q.Undo() {
		t.Fatal("first undo should succeed")
	}
	if q.Has("r2") {
		t.Fatal("r2 (insert) should be undone")
	}
	if !q.Undo() {
		t.Fatal("second undo should succeed")
	}
	if q.Size() != 0 {
		t.Fatalf("size = %d, want 0", q.Size())
	}
}

func TestOrdersStatementsByUserSequence(t *testing.T) {
	q := New()
	q.AddDelete("r1", mkRow(cell("id", iv(5)), cell("a", sv("x"))))
	q.AddInsert("r1", mkRow(cell("id", iv(5)), cell("a", sv("y"))))
	all := q.All()
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}
	if all[0].Type != Delete || all[1].Type != Insert {
		t.Fatalf("order wrong: %v then %v", all[0].Type, all[1].Type)
	}
	if all[1].Sequence <= all[0].Sequence {
		t.Fatalf("sequence not increasing: %d,%d", all[0].Sequence, all[1].Sequence)
	}
}
