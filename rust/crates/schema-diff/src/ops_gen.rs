// PolyDB schemadiff op table — GENERATED from spec/schemas/schemadiff-ops.json
// by `go run polydb.dev/tools/genprotocol ops`. DO NOT EDIT; regenerate instead.
//
// The op *membership* is separately drift-checked by `genprotocol check`
// against SchemaDiffOp in metadata.json; this module owns the per-op
// properties (phase / destructive / inverse / ddl dialects).
use crate::Op;

/// One row of the schemadiff op registry, single-sourced from the spec.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct OpRow {
    pub op: Op,
    pub phase: u8,
    pub destructive: bool,
    pub inverse: Op,
}

/// The op registry in the spec's declaration order.
pub const OP_ROWS: &[OpRow] = &[
    OpRow {
        op: Op::CreateTable,
        phase: 50,
        destructive: false,
        inverse: Op::DropTable,
    },
    OpRow {
        op: Op::DropTable,
        phase: 40,
        destructive: true,
        inverse: Op::CreateTable,
    },
    OpRow {
        op: Op::AddColumn,
        phase: 60,
        destructive: false,
        inverse: Op::DropColumn,
    },
    OpRow {
        op: Op::DropColumn,
        phase: 30,
        destructive: true,
        inverse: Op::AddColumn,
    },
    OpRow {
        op: Op::AlterColumn,
        phase: 70,
        destructive: false,
        inverse: Op::AlterColumn,
    },
    OpRow {
        op: Op::AddIndex,
        phase: 80,
        destructive: false,
        inverse: Op::DropIndex,
    },
    OpRow {
        op: Op::DropIndex,
        phase: 20,
        destructive: true,
        inverse: Op::AddIndex,
    },
    OpRow {
        op: Op::AddForeignKey,
        phase: 90,
        destructive: false,
        inverse: Op::DropForeignKey,
    },
    OpRow {
        op: Op::DropForeignKey,
        phase: 10,
        destructive: true,
        inverse: Op::AddForeignKey,
    },
];

/// Database-kind wire names the DDL generator targets (v1: sqlite only).
/// `check_kind` consults this instead of a hard-coded kind.
pub const OP_DDL_DIALECTS: &[&str] = &["sqlite"];

/// The registry row for `op`, or `None` when `op` is not a known schemadiff op.
pub fn lookup_op_row(op: Op) -> Option<&'static OpRow> {
    OP_ROWS.iter().find(|r| r.op == op)
}

/// True when `wire` names a dialect the DDL generator targets.
pub fn ddl_supported(wire: &str) -> bool {
    OP_DDL_DIALECTS.contains(&wire)
}
