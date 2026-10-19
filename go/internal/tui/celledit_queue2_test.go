package tui

// 编辑入队用到的两个纯函数。整条入队链路要连服务端（要主键），这里只钉住
// 行标识与原始行的构造 —— 它们决定提交时能不能定位到正确的行。

import (
	"testing"

	"github.com/polydb/polydb/internal/tuidata"
)

func twoRowFrame() *tuidata.Frame {
	return &tuidata.Frame{Columns: []tuidata.Column{
		tuidata.NewColumn("id", tuidata.TypeString, []any{"1", "2"}),
		tuidata.NewColumn("name", tuidata.TypeString, []any{"alice", "bob"}),
	}}
}

func TestPkRowIDSingle(t *testing.T) {
	f := twoRowFrame()
	if got := pkRowID(f, 1, []string{"id"}); got != "2" {
		t.Fatalf("pkRowID = %q, want %q", got, "2")
	}
}

// TestPkRowIDComposite pins that composite keys are joined with a separator
// that cannot appear in ordinary values (US), so "a,b" + "c" is not confused
// with "a" + "b,c".
func TestPkRowIDComposite(t *testing.T) {
	f := twoRowFrame()
	got := pkRowID(f, 0, []string{"id", "name"})
	if got != "1\x1falice" {
		t.Fatalf("复合主键 = %q", got)
	}
}

func TestPkRowIDMissingColumn(t *testing.T) {
	f := twoRowFrame()
	if got := pkRowID(f, 0, []string{"nope"}); got != "" {
		t.Fatalf("主键列不存在应返回空串, got %q", got)
	}
	if got := pkRowID(nil, 0, []string{"id"}); got != "" {
		t.Fatalf("nil 帧应返回空串, got %q", got)
	}
}

func TestChangesRow(t *testing.T) {
	f := twoRowFrame()
	row := changesRow(f, 1)
	if len(row) != 2 {
		t.Fatalf("原始行列数 = %d, want 2", len(row))
	}
	if row[0].Column != "id" || row[1].Column != "name" {
		t.Fatalf("列名 = %q, %q", row[0].Column, row[1].Column)
	}
}

func TestChangesRowOutOfRange(t *testing.T) {
	f := twoRowFrame()
	if got := changesRow(f, 99); got != nil {
		t.Fatalf("越界行应返回 nil, got %v", got)
	}
	if got := changesRow(nil, 0); got != nil {
		t.Fatalf("nil 帧应返回 nil, got %v", got)
	}
}
