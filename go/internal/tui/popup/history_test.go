package popup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHistoryRecordRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	qxHistoryPath = p
	defer func() { qxHistoryPath = "" }()

	// Gate: a nil client records nothing (mirrors the test stub).
	qxRecordHistory(&cmpTestCtx{client: nil}, "SELECT 1")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected no history file written when client is nil")
	}

	// A real client records successfully.
	qxRecordHistory(&cmpTestCtx{client: cmpLocal(t)}, "SELECT 2")
	qxRecordHistory(&cmpTestCtx{client: cmpLocal(t)}, "")
	qxRecordHistory(&cmpTestCtx{client: cmpLocal(t)}, "  INSERT INTO t VALUES (1)  ")

	lines, err := qxReadHistory(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 entries (blank skipped), got %d: %v", len(lines), lines)
	}
	// Trimmed; write order is preserved (newest-first sort happens at load).
	if !strings.Contains(lines[0], "SELECT 2") || !strings.Contains(lines[1], "INSERT INTO t VALUES (1)") {
		t.Fatalf("unexpected written lines: %v", lines)
	}
}

func TestHistoryOverlayLoadsAndReplays(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	qxHistoryPath = p
	defer func() { qxHistoryPath = "" }()
	qxRecordHistory(&cmpTestCtx{client: cmpLocal(t)}, "SELECT 42")

	o := &qxHistory{ctx: &cmpTestCtx{client: cmpLocal(t)}, sel: 0, offset: 0}
	ov, _ := o.Update(o.Init()())
	o = ov.(*qxHistory)
	if len(o.entries) != 1 {
		t.Fatalf("expected 1 loaded entry, got %d", len(o.entries))
	}
	// Enter on the entry replays it: the run cmd should be non-nil (a
	// CloseOverlay+run sequence) so the overlay closes and the query runs.
	_, cmd := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected a replay command on enter")
	}
	// Esc emits the CloseOverlay command (non-nil) and returns the same overlay.
	_, esc := o.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if esc == nil {
		t.Fatalf("expected a close command on esc")
	}
}
