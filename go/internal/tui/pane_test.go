package tui

import (
	"reflect"
	"testing"
)

func TestPaneStack(t *testing.T) {
	base := testFrame(10)
	p := NewPane("people", base)

	if p.Current() != base {
		t.Fatal("Current should be the base frame")
	}
	if p.Pop() {
		t.Fatal("Pop on base stack must report false")
	}

	f1 := testFrame(5)
	f2 := testFrame(2)
	p.Push(f1, "filter")
	p.Push(f2, "sort")

	if p.Current() != f2 || p.Depth() != 3 {
		t.Fatalf("stack top wrong: depth=%d", p.Depth())
	}
	if got := p.Crumbs(); !reflect.DeepEqual(got, []string{"people", "filter", "sort"}) {
		t.Fatalf("crumbs = %v", got)
	}

	if !p.Pop() || p.Current() != f1 {
		t.Fatal("Pop should return to f1")
	}
	if !p.Pop() || p.Current() != base {
		t.Fatal("Pop should return to base")
	}
	if p.Pop() {
		t.Fatal("Pop past base must report false")
	}
}

func TestPanePushResetsView(t *testing.T) {
	base := testFrame(100)
	p := NewPane("t", base)
	p.Table.JumpTo(50, 100)
	p.Mode = ModeSheet
	p.SheetOff = 7

	p.Push(testFrame(3), "filter")
	if p.Table.Sel() != 0 {
		t.Fatalf("selection not reset on push: %d", p.Table.Sel())
	}
	if p.Mode != ModeTable || p.SheetOff != 0 {
		t.Fatal("push should return to table mode with sheet scroll reset")
	}
}

func TestPanePopClampsSelection(t *testing.T) {
	base := testFrame(3)
	p := NewPane("t", base)
	p.Push(testFrame(100), "query")
	p.Table.JumpTo(80, 100)

	p.Pop()
	if p.Table.Sel() > 2 {
		t.Fatalf("selection %d out of range after pop to 3-row frame", p.Table.Sel())
	}
}

func TestPaneReset(t *testing.T) {
	base := testFrame(10)
	p := NewPane("t", base)
	p.Push(testFrame(5), "a")
	p.Push(testFrame(2), "b")
	p.Reset()
	if p.Depth() != 1 || p.Current() != base {
		t.Fatal("Reset should drop back to the base frame")
	}
	if got := p.Crumbs(); !reflect.DeepEqual(got, []string{"t"}) {
		t.Fatalf("crumbs after reset = %v", got)
	}
}

func TestPaneResetReleasesDroppedFrames(t *testing.T) {
	base := testFrame(10)
	p := NewPane("t", base)
	for i := 0; i < 8; i++ {
		p.Push(testFrame(4), "derived")
	}
	if cap(p.stack) <= p.Depth() {
		t.Fatalf("cap %d <= len %d: no tail left to check", cap(p.stack), p.Depth())
	}

	p.Reset()

	if p.Depth() != 1 || p.Current() != base {
		t.Fatalf("reset to depth=%d, want the base at depth 1", p.Depth())
	}
	tail := p.stack[len(p.stack):cap(p.stack)]
	for i, e := range tail {
		if e.frame != nil || e.crumb != "" {
			t.Fatalf("slot %d = %q/%p: derived frame still pinned after Reset",
				len(p.stack)+i, e.crumb, e.frame)
		}
	}
	// 容量区被清过之后，下一次 Push 仍要能原地写回去。
	p.Push(testFrame(2), "again")
	if p.Depth() != 2 || p.stack[1].frame == nil || p.stack[1].crumb != "again" {
		t.Fatalf("push after reset did not reuse the cleared tail")
	}
}

func TestPanePopReleasesDroppedFrame(t *testing.T) {
	p := NewPane("t", testFrame(10))
	for i := 0; i < 8; i++ {
		p.Push(testFrame(4), "derived")
	}
	if cap(p.stack) <= p.Depth() {
		t.Fatalf("cap %d <= len %d: no tail left to check", cap(p.stack), p.Depth())
	}

	if !p.Pop() {
		t.Fatal("Pop above the base must report true")
	}
	if p.Depth() != 8 {
		t.Fatalf("depth = %d, want 8", p.Depth())
	}
	for i := 0; i < len(p.stack); i++ {
		if p.stack[i].frame == nil {
			t.Fatalf("kept slot %d lost its frame", i)
		}
	}
	tail := p.stack[len(p.stack):cap(p.stack)]
	for i, e := range tail {
		if e.frame != nil || e.crumb != "" {
			t.Fatalf("slot %d = %q/%p: popped frame still pinned after Pop",
				len(p.stack)+i, e.crumb, e.frame)
		}
	}
}
