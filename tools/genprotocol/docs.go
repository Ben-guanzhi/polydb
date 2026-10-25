package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// POLYDB_* is the project's own naming convention for error codes and
// environment variables, so a doc token matching it is by construction meant
// to point at real code. Anything else in backticks — "SELECT 1", 127.0.0.1,
// a table name, a shell command — is prose, and guessing at it produces noise
// instead of signal.
var docTokenRe = regexp.MustCompile(`POLYDB_[A-Z0-9_]{2,}`)

// Restrict the search to backticked spans. Prose can mention an identifier
// without making a code reference, and chasing those down yields false
// positives rather than drift. The trade is one-way: a doc that forgets the
// backticks is not checked, which is a false negative instead of a false alarm.
var backtickSpanRe = regexp.MustCompile("`([^`\n]+)`")

// Extensions eligible to *define* an identifier. Markdown is the subject of
// this check and never part of the corpus — otherwise two docs naming the same
// dead token would pass each other.
var corpusExts = map[string]bool{
	".go": true, ".rs": true, ".ts": true, ".tsx": true,
	".json": true, ".sh": true, ".yml": true, ".yaml": true, ".toml": true,
}

// Pruned from both walks. .tools is a vendored tool checkout that is not even
// tracked, and node_modules is third-party: neither describes this codebase.
var skipDirs = map[string]bool{
	".git": true, ".tools": true, "target": true,
	"node_modules": true, "dist": true, "build": true, ".qoder": true,
}

// skipDir reports whether a directory is build output or a vendored dependency
// rather than project source. Cargo's output directory is named by
// CARGO_TARGET_DIR, and this repo points it at rust/target-gnu (gitignored,
// ~30GB after one local build); pruning only the bare name "target" let a
// local `genprotocol docs` walk 30GB of artifacts and never finish. CI never
// hit this because a fresh checkout has no build cache, which is exactly why
// the bug survived: the gate was green where it was enforced and unusable
// where it was needed. Match the prefix so any target dir name is covered.
func skipDir(name string) bool {
	return skipDirs[name] || strings.HasPrefix(name, "target")
}

// genprotocol already stops generated code from drifting off spec/. Docs drift
// differently: nobody regenerates them, so a renamed or dropped error code and
// env var keep living in prose forever and the reader follows a dead
// reference. checkDocTokens is that check.
func runDocs(repo string) {
	drift, refs, known, corpus, docs := checkDocTokens(repo)
	if len(drift) > 0 {
		fmt.Fprintf(os.Stderr, "documented identifiers missing from source (%d):\n\n", len(drift))
		fmt.Fprintln(os.Stderr, strings.Join(drift, "\n"))
		fmt.Fprintln(os.Stderr, "\nthe docs name something the code no longer has — rename the docs or restore the identifier")
		os.Exit(1)
	}
	fmt.Printf("doc check OK: %d backticked POLYDB_* references across %d docs, %d distinct identifiers in %d source files\n",
		refs, docs, known, corpus)
}

// Split out from runDocs so the finding logic can be tested without os.Exit.
func checkDocTokens(repo string) (drift []string, refs, known, corpus, docs int) {
	knownSet, corpus := sourceTokens(repo)
	known = len(knownSet)
	if known == 0 {
		fatal("found no POLYDB_* identifiers in the source tree; the corpus scan is broken")
	}

	walkFiles(repo, func(rel string, data []byte) {
		if filepath.Ext(rel) != ".md" || !isAuthoritativeDoc(rel) {
			return
		}
		docs++
		for i, line := range strings.Split(string(data), "\n") {
			for _, span := range backtickSpanRe.FindAllStringSubmatch(line, -1) {
				for _, tok := range docTokenRe.FindAllString(span[1], -1) {
					refs++
					if isTemplate(tok) || knownSet[tok] {
						continue
					}
					drift = append(drift, fmt.Sprintf(
						"%s:%d: error: `%s` is documented but defined nowhere in the source tree",
						rel, i+1, tok))
				}
			}
		}
	})

	sort.Strings(drift)
	return drift, refs, known, corpus, docs
}

// isTemplate distinguishes a real identifier from a placeholder. `POLYDB_ERR_`
// is a prefix and `POLYDB_ERR_XXX` is the stand-in from the error-shape
// example; neither refers to code, so demanding a definition for them is pure
// noise. Rules rather than an allowlist, so a new placeholder stays covered.
func isTemplate(tok string) bool {
	return strings.HasSuffix(tok, "_") || strings.Contains(tok, "XXX")
}

// Only the trees that describe this project are authoritative. Everything else
// is a vendored checkout or a dependency and cannot be held to it.
func isAuthoritativeDoc(rel string) bool {
	if rel == "README.md" || rel == "AGENTS.md" {
		return true
	}
	if !strings.HasPrefix(rel, "docs/") && !strings.HasPrefix(rel, "spec/") {
		return false
	}
	if filepath.Base(rel) == "CHANGELOG.md" {
		return false // historical record, deliberately names old things
	}
	return !strings.HasPrefix(filepath.Base(rel), "plan-") // proposals, not facts
}

// sourceTokens collects every POLYDB_* identifier defined outside of docs.
func sourceTokens(repo string) (map[string]bool, int) {
	tokens := make(map[string]bool)
	files := 0
	walkFiles(repo, func(rel string, data []byte) {
		if !corpusExts[filepath.Ext(rel)] {
			return
		}
		files++
		for _, tok := range docTokenRe.FindAllString(string(data), -1) {
			tokens[tok] = true
		}
	})
	return tokens, files
}

// walkFiles passes each readable file as a repo-relative slash path, pruning
// build, vendor and tooling trees that would otherwise dominate a full walk.
func walkFiles(repo string, fn func(rel string, data []byte)) {
	_ = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		fn(filepath.ToSlash(rel), data)
		return nil
	})
}
