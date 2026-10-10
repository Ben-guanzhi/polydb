package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ─── Schemadiff op-table generation ────────────────────────
//
// The schemadiff op registry (which ops exist + their execution phase,
// destructiveness, inverse op, and the DDL dialects the generator targets) is
// single-sourced in spec/schemas/schemadiff-ops.json. This command emits that
// table into BOTH the Go engine and the Rust engine so the two languages cannot
// drift apart. `ops --check` exits non-zero when a generated file is stale.

// OpRowSpec is one op entry in the registry file.
type OpRowSpec struct {
	ID          string `json:"id"`
	Phase       int    `json:"phase"`
	Destructive bool   `json:"destructive"`
	Inverse     string `json:"inverse"`
}

// OpsSpec is the whole registry document.
type OpsSpec struct {
	Title      string      `json:"title"`
	DDLs       []string    `json:"ddl_dialects"`
	Ops        []OpRowSpec `json:"ops"`
}

// LoadOpsSpec reads and validates the op-registry spec file.
func LoadOpsSpec(path string) (*OpsSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec OpsSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(spec.Ops) == 0 {
		return nil, fmt.Errorf("%s: no ops declared", path)
	}
	if len(spec.DDLs) == 0 {
		return nil, fmt.Errorf("%s: no ddl_dialects declared", path)
	}
	seen := map[string]bool{}
	for _, o := range spec.Ops {
		if o.ID == "" || o.Inverse == "" {
			return nil, fmt.Errorf("%s: op missing id/inverse: %+v", path, o)
		}
		if seen[o.ID] {
			return nil, fmt.Errorf("%s: duplicate op %q", path, o.ID)
		}
		seen[o.ID] = true
	}
	return &spec, nil
}

// pascal turns "add_foreign_key" into "AddForeignKey" (the Go constant suffix
// and the Rust enum variant, both of which the registry's ids follow).
func pascal(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// GenGoOps renders the op table as a Go source file (package schemadiff).
func GenGoOps(spec *OpsSpec) string {
	var b strings.Builder
	b.WriteString("// PolyDB schemadiff op table — GENERATED from spec/schemas/schemadiff-ops.json\n")
	b.WriteString("// by `go run polydb.dev/tools/genprotocol ops`. DO NOT EDIT; regenerate instead.\n")
	b.WriteString("//\n")
	b.WriteString("// The op *membership* is separately drift-checked by `genprotocol check`\n")
	b.WriteString("// against SchemaDiffOp in metadata.json; this file owns the per-op\n")
	b.WriteString("// properties (phase / destructive / inverse / ddl dialects).\n")
	b.WriteString("package schemadiff\n\n")

	b.WriteString("// OpRow is one row of the schemadiff op registry, single-sourced from the spec.\n")
	b.WriteString("type OpRow struct {\n")
	b.WriteString("\tOp          Op\n")
	b.WriteString("\tPhase       int\n")
	b.WriteString("\tDestructive bool\n")
	b.WriteString("\tInverse     Op\n")
	b.WriteString("}\n\n")

	b.WriteString("// OpRows is the op registry in the spec's declaration order.\n")
	b.WriteString("var OpRows = []OpRow{\n")
	for _, o := range spec.Ops {
		fmt.Fprintf(&b, "\t{Op: Op%s, Phase: %d, Destructive: %t, Inverse: Op%s},\n",
			pascal(o.ID), o.Phase, o.Destructive, pascal(o.Inverse))
	}
	b.WriteString("}\n\n")

	b.WriteString("// OpDDLs are the database-kind wire names the DDL generator targets\n")
	b.WriteString("// (v1: sqlite only). checkKind consults this instead of a hard-coded list.\n")
	b.WriteString("var OpDDLs = []string{\n")
	for _, d := range spec.DDLs {
		fmt.Fprintf(&b, "\t%q,\n", d)
	}
	b.WriteString("}\n\n")

	b.WriteString("// lookupOpRow returns the registry row for `op`, ok=false when `op` is not\n")
	b.WriteString("// a known schemadiff op.\n")
	b.WriteString("func lookupOpRow(op Op) (OpRow, bool) {\n")
	b.WriteString("\tfor _, r := range OpRows {\n")
	b.WriteString("\t\tif r.Op == op {\n")
	b.WriteString("\t\t\treturn r, true\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn OpRow{}, false\n")
	b.WriteString("}\n\n")

	b.WriteString("// OpDDLsContain reports whether the dialect `wire` is one the DDL generator targets.\n")
	b.WriteString("func OpDDLsContain(wire string) bool {\n")
	b.WriteString("\tfor _, d := range OpDDLs {\n")
	b.WriteString("\t\tif d == wire {\n")
	b.WriteString("\t\t\treturn true\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn false\n")
	b.WriteString("}\n")
	return b.String()
}

// GenRustOps renders the op table as a Rust source file (the schema-diff crate).
func GenRustOps(spec *OpsSpec) string {
	var b strings.Builder
	b.WriteString("// PolyDB schemadiff op table — GENERATED from spec/schemas/schemadiff-ops.json\n")
	b.WriteString("// by `go run polydb.dev/tools/genprotocol ops`. DO NOT EDIT; regenerate instead.\n")
	b.WriteString("//\n")
	b.WriteString("// The op *membership* is separately drift-checked by `genprotocol check`\n")
	b.WriteString("// against SchemaDiffOp in metadata.json; this module owns the per-op\n")
	b.WriteString("// properties (phase / destructive / inverse / ddl dialects).\n")
	b.WriteString("use crate::Op;\n\n")
	b.WriteString("/// One row of the schemadiff op registry, single-sourced from the spec.\n")
	b.WriteString("#[derive(Debug, Clone, Copy, PartialEq, Eq)]\n")
	b.WriteString("pub struct OpRow {\n")
	b.WriteString("    pub op: Op,\n")
	b.WriteString("    pub phase: u8,\n")
	b.WriteString("    pub destructive: bool,\n")
	b.WriteString("    pub inverse: Op,\n")
	b.WriteString("}\n\n")
	b.WriteString("/// The op registry in the spec's declaration order.\n")
	b.WriteString("pub const OP_ROWS: &[OpRow] = &[\n")
	for _, o := range spec.Ops {
		// 逐字段展开与 rustfmt 的多行形态逐字节一致：`ops --check` 是
		// 字节级比对，若产出紧凑形态则与 `cargo fmt --check` 互相打架。
		fmt.Fprintf(&b, "    OpRow {\n        op: Op::%s,\n        phase: %d,\n        destructive: %t,\n        inverse: Op::%s,\n    },\n",
			pascal(o.ID), o.Phase, o.Destructive, pascal(o.Inverse))
	}
	b.WriteString("];\n\n")
	b.WriteString("/// Database-kind wire names the DDL generator targets (v1: sqlite only).\n")
	b.WriteString("/// `check_kind` consults this instead of a hard-coded kind.\n")
	// 单行放得下（≤100 列 = rustfmt max_width）就单行，否则多行——两种
	// 形态都取 rustfmt 的规范形态。
	dl := "pub const OP_DDL_DIALECTS: &[&str] = &["
	for i, d := range spec.DDLs {
		if i > 0 {
			dl += ", "
		}
		dl += fmt.Sprintf("%q", d)
	}
	dl += "];"
	if len(dl) <= 100 {
		b.WriteString(dl + "\n\n")
	} else {
		b.WriteString("pub const OP_DDL_DIALECTS: &[&str] = &[\n")
		for _, d := range spec.DDLs {
			fmt.Fprintf(&b, "    %q,\n", d)
		}
		b.WriteString("];\n\n")
	}
	b.WriteString("/// The registry row for `op`, or `None` when `op` is not a known schemadiff op.\n")
	b.WriteString("pub fn lookup_op_row(op: Op) -> Option<&'static OpRow> {\n")
	b.WriteString("    OP_ROWS.iter().find(|r| r.op == op)\n")
	b.WriteString("}\n\n")
	b.WriteString("/// True when `wire` names a dialect the DDL generator targets.\n")
	b.WriteString("pub fn ddl_supported(wire: &str) -> bool {\n")
	b.WriteString("    OP_DDL_DIALECTS.contains(&wire)\n")
	b.WriteString("}\n")
	return b.String()
}

func runOps(repo, specDir string, check bool) {
	specPath := filepath.Join(specDir, "schemas", "schemadiff-ops.json")
	spec, err := LoadOpsSpec(specPath)
	if err != nil {
		fatal("load ops spec: %v", err)
	}

	out := []struct {
		path string
		body string
	}{
		{filepath.Join(repo, "go", "pkg", "schemadiff", "ops_gen.go"), GenGoOps(spec)},
		{filepath.Join(repo, "rust", "crates", "schema-diff", "src", "ops_gen.rs"), GenRustOps(spec)},
	}
	for _, o := range out {
		if check {
			cur, err := os.ReadFile(o.path)
			if err != nil {
				fatal("read %s: %v (run `make gen-ops` first)", o.path, err)
			}
			if string(cur) != o.body {
				fmt.Fprintf(os.Stderr, "%s is stale — run `make gen-ops` after spec changes\n", o.path)
				os.Exit(1)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
			fatal(err.Error())
		}
		if err := os.WriteFile(o.path, []byte(o.body), 0o644); err != nil {
			fatal("write %s: %v", o.path, err)
		}
		fmt.Printf("generated %s\n", o.path)
	}
	if check {
		fmt.Printf("ops table up to date: %d op(s) vs go + rust\n", len(spec.Ops))
	}
}
