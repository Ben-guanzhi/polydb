package changes

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func sv(x string) protocol.Value { return protocol.NewStringValue(x) }
func iv(x int64) protocol.Value  { return protocol.NewIntValue(x) }
func fv(x float64) protocol.Value {
	return protocol.NewFloatValue(x)
}

func mkRow(pairs ...Cell) Row { return Row(pairs) }

func cell(c string, v protocol.Value) Cell { return Cell{Column: c, Value: v} }

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
	if !(all[1].Sequence > all[0].Sequence) {
		t.Fatalf("sequence not increasing: %d,%d", all[0].Sequence, all[1].Sequence)
	}
}
