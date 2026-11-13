package changes

// The zero-row guard policy is a cross-language contract
// (spec/zero-row-guard.md) with one shared case file. This test is the Go half:
// it reads ../spec/vectors/zero-row-guard.json so the three implementations are
// pinned to the same answers instead of drifting (LENS-04).

import (
	"encoding/json"
	"os"
	"testing"
)

type zeroRowCase struct {
	Name           string `json:"name"`
	AllowZero      bool   `json:"allow_zero"`
	ExpectAffected uint64 `json:"expect_affected"`
	HasPredicate   bool   `json:"has_predicate"`
	Actual         uint64 `json:"actual"`
	Present        bool   `json:"present"`
	NeedsProbe     bool   `json:"needs_probe"`
	AllowsCommit   bool   `json:"allows_commit"`
}

func TestZeroRowGuardVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/vectors/zero-row-guard.json")
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var doc struct {
		Cases []zeroRowCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse shared vectors: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("shared vectors are empty")
	}
	for _, c := range doc.Cases {
		s := Statement{AllowZero: c.AllowZero, ExpectAffected: c.ExpectAffected}
		if c.HasPredicate {
			s.Table = "t"
			s.Where = "id = ?"
		}
		if got := NeedsRowProbe(s, c.Actual); got != c.NeedsProbe {
			t.Errorf("%s: NeedsRowProbe = %v, want %v", c.Name, got, c.NeedsProbe)
		}
		if got := ProbeAllowsCommit(s, c.Actual, c.Present); got != c.AllowsCommit {
			t.Errorf("%s: ProbeAllowsCommit = %v, want %v", c.Name, got, c.AllowsCommit)
		}
	}
}
