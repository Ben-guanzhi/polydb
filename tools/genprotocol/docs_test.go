package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDocumentedTokenPresentInSourcePasses(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md":           "Timeouts surface as `POLYDB_ERR_TIMEOUT`.",
	})

	drift, refs, known, corpus, docs := checkDocTokens(repo)

	if len(drift) != 0 {
		t.Fatalf("unexpected drift: %v", drift)
	}
	if refs != 1 || known != 1 || corpus != 1 || docs != 1 {
		t.Errorf("counts = refs %d, known %d, corpus %d, docs %d; want 1 each", refs, known, corpus, docs)
	}
}

func TestDocumentedTokenAbsentFromSourceIsReported(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md": "# Errors\n\n" +
			"Line one is filler so the offset is non-trivial.\n" +
			"Timeouts used to surface as `POLYDB_ERR_DEADLINE_EXCEEDED`.",
	})

	drift, _, _, _, _ := checkDocTokens(repo)

	if len(drift) != 1 {
		t.Fatalf("drift = %v, want exactly one finding", drift)
	}
	got := drift[0]
	wantToken := "`POLYDB_ERR_DEADLINE_EXCEEDED`"
	if !strings.Contains(got, "docs/errors.md:4:") {
		t.Errorf("finding %q must name the file and line", got)
	}
	if !strings.Contains(got, wantToken) {
		t.Errorf("finding %q must name the dead identifier", got)
	}
	// gate.sh publishes the lines it recognises as annotations, so every
	// finding has to carry the "error:" keyword or it would never surface.
	if !strings.Contains(got, "error:") {
		t.Errorf("finding %q must contain \"error:\" for gate.sh to annotate it", got)
	}
}

func TestMarkdownIsNotPartOfTheCorpus(t *testing.T) {
	// Two docs naming the same undefined token must not pass each other; only
	// code counts as a definition.
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md":           "See `POLYDB_ERR_GHOST`.",
		"docs/other.md":            "Also see `POLYDB_ERR_GHOST`.",
	})

	drift, refs, _, _, _ := checkDocTokens(repo)

	if len(drift) != 2 {
		t.Fatalf("drift = %v, want both docs reported", drift)
	}
	if refs != 2 {
		t.Errorf("refs = %d, want 2", refs)
	}
}

func TestPlaceholderTokensAreNotRequired(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md":           "Error codes follow `POLYDB_ERR_` and look like `POLYDB_ERR_XXX`.",
	})

	drift, refs, _, _, _ := checkDocTokens(repo)

	if len(drift) != 0 {
		t.Fatalf("drift = %v, want none: both tokens are templates, not identifiers", drift)
	}
	if refs != 2 {
		t.Errorf("refs = %d, want 2 (templates are counted but not required)", refs)
	}
}

func TestProseOutsideBackticksIsIgnored(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md":           "POLYDB_ERR_UNDOCUMENTED_SHOULD_BE_IGNORED appears only in prose.",
	})

	drift, refs, _, _, _ := checkDocTokens(repo)

	if len(drift) != 0 {
		t.Fatalf("drift = %v, want none: unquoted prose is not a code reference", drift)
	}
	if refs != 0 {
		t.Errorf("refs = %d, want 0", refs)
	}
}

func TestNonAuthoritativeDocsAreIgnored(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go":       "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"CHANGELOG.md":                   "Removed `POLYDB_ERR_LEGACY` in v0.9.",
		"docs/plan-next.md":              "Will add `POLYDB_ERR_NOT_YET`.",
		"docs/CHANGELOG.md":              "Renamed `POLYDB_ERR_OLD`.",
		".tools/docs/reference.md":       "`POLYDB_ERR_VENDORED`",
		"web/node_modules/pkg/README.md": "`POLYDB_ERR_DEPENDENCY`",
	})

	drift, refs, _, _, docs := checkDocTokens(repo)

	if len(drift) != 0 {
		t.Fatalf("drift = %v, want none: historical and vendored docs are not authoritative", drift)
	}
	if refs != 0 {
		t.Errorf("refs = %d, want 0", refs)
	}
	if docs != 0 {
		t.Errorf("docs = %d, want 0 authoritative docs in this fixture", docs)
	}
}

func TestSpansAreExtractedFromSurroundingProse(t *testing.T) {
	const quoted = "`POLYDB_ERR_TIMEOUT`"

	repo := writeRepo(t, map[string]string{
		"go/pkg/protocol/error.go": "package protocol\nconst Timeout = \"POLYDB_ERR_TIMEOUT\"\n",
		"docs/errors.md":           "Unquoted POLYDB_ERR_TIMEOUT is prose, then " + quoted + " is a reference.",
	})

	drift, refs, _, _, _ := checkDocTokens(repo)

	if len(drift) != 0 {
		t.Fatalf("drift = %v, want none", drift)
	}
	if refs != 1 {
		t.Errorf("refs = %d, want 1: only the backticked mention counts", refs)
	}
}
