package main

import (
	"os"
	"path/filepath"
	"testing"
)

const opsSpecJSON = `{
  "ddl_dialects": ["sqlite"],
  "ops": [
    {"id":"create_table","phase":50,"destructive":false,"inverse":"drop_table"},
    {"id":"drop_table","phase":40,"destructive":true,"inverse":"create_table"}
  ]
}`

func writeOpsSpec(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "schemadiff-ops.json")
	if err := os.WriteFile(p, []byte(opsSpecJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOpsSpecRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "schemadiff-ops.json")
	_ = os.WriteFile(p, []byte(`{"ddl_dialects":["sqlite"],"ops":[]}`), 0o644)
	if _, err := LoadOpsSpec(p); err == nil {
		t.Fatal("expected error for empty ops")
	}
}

func TestLoadOpsSpecRejectsDuplicateOp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "schemadiff-ops.json")
	_ = os.WriteFile(p, []byte(`{"ddl_dialects":["sqlite"],"ops":`+
		`[{"id":"a","phase":1,"destructive":false,"inverse":"b"},`+
		`{"id":"a","phase":2,"destructive":true,"inverse":"a"}]}`), 0o644)
	if _, err := LoadOpsSpec(p); err == nil {
		t.Fatal("expected error for duplicate op id")
	}
}

func TestGenGoOpsEmitsRegistry(t *testing.T) {
	spec, err := LoadOpsSpec(writeOpsSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	out := GenGoOps(spec)
	for _, want := range []string{"package schemadiff", "OpCreateTable", "OpDropTable", `OpDDLs`, `"sqlite"`} {
		if !contains(out, want) {
			t.Errorf("GenGoOps missing %q", want)
		}
	}
}

func TestGenRustOpsUsesBoolVerb(t *testing.T) {
	spec, err := LoadOpsSpec(writeOpsSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	out := GenRustOps(spec)
	// A bool mis-formatted with %s renders as "%!s(bool=..." — that must never appear.
	if contains(out, "%!s(bool=") {
		t.Fatalf("GenRustOps produced a malformed bool: %s", out)
	}
	for _, want := range []string{"use crate::Op;", "pub const OP_ROWS", "destructive: false", "destructive: true"} {
		if !contains(out, want) {
			t.Errorf("GenRustOps missing %q", want)
		}
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	spec, err := LoadOpsSpec(writeOpsSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if GenGoOps(spec) != GenGoOps(spec) {
		t.Fatal("GenGoOps is not deterministic")
	}
	if GenRustOps(spec) != GenRustOps(spec) {
		t.Fatal("GenRustOps is not deterministic")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
