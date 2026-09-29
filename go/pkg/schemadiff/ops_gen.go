// PolyDB schemadiff op table — GENERATED from spec/schemas/schemadiff-ops.json
// by `go run polydb.dev/tools/genprotocol ops`. DO NOT EDIT; regenerate instead.
//
// The op *membership* is separately drift-checked by `genprotocol check`
// against SchemaDiffOp in metadata.json; this file owns the per-op
// properties (phase / destructive / inverse / ddl dialects).
package schemadiff

// OpRow is one row of the schemadiff op registry, single-sourced from the spec.
type OpRow struct {
	Op          Op
	Phase       int
	Destructive bool
	Inverse     Op
}

// OpRows is the op registry in the spec's declaration order.
var OpRows = []OpRow{
	{Op: OpCreateTable, Phase: 50, Destructive: false, Inverse: OpDropTable},
	{Op: OpDropTable, Phase: 40, Destructive: true, Inverse: OpCreateTable},
	{Op: OpAddColumn, Phase: 60, Destructive: false, Inverse: OpDropColumn},
	{Op: OpDropColumn, Phase: 30, Destructive: true, Inverse: OpAddColumn},
	{Op: OpAlterColumn, Phase: 70, Destructive: false, Inverse: OpAlterColumn},
	{Op: OpAddIndex, Phase: 80, Destructive: false, Inverse: OpDropIndex},
	{Op: OpDropIndex, Phase: 20, Destructive: true, Inverse: OpAddIndex},
	{Op: OpAddForeignKey, Phase: 90, Destructive: false, Inverse: OpDropForeignKey},
	{Op: OpDropForeignKey, Phase: 10, Destructive: true, Inverse: OpAddForeignKey},
}

// OpDDLs are the database-kind wire names the DDL generator targets
// (v1: sqlite only). checkKind consults this instead of a hard-coded list.
var OpDDLs = []string{
	"sqlite",
}

// lookupOpRow returns the registry row for `op`, ok=false when `op` is not
// a known schemadiff op.
func lookupOpRow(op Op) (OpRow, bool) {
	for _, r := range OpRows {
		if r.Op == op {
			return r, true
		}
	}
	return OpRow{}, false
}

// allOps lists every registered op in the spec's order.
func allOps() []Op {
	out := make([]Op, 0, len(OpRows))
	for _, r := range OpRows {
		out = append(out, r.Op)
	}
	return out
}

// OpDDLsContain reports whether the dialect `wire` is one the DDL generator targets.
func OpDDLsContain(wire string) bool {
	for _, d := range OpDDLs {
		if d == wire {
			return true
		}
	}
	return false
}
