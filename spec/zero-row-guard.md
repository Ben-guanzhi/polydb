# Zero-row guard — one policy, three languages

Status: contract (LENS-04, 2026-11-13). Implementations:
`go/pkg/changes/statements.go` (`NeedsRowProbe`, `ProbeAllowsCommit`),
`rust/crates/ui-gui/src/changes.rs` (`needs_row_probe`, `probe_allows_commit`),
`web/src/lib/changes.ts` (`needsRowProbe`, `probeAllowsCommit`).
Shared cases: `spec/vectors/zero-row-guard.json`, consumed by a test in every one
of those three packages.

## The question

A generated DML statement was executed inside a commit batch and reported
`actual` affected rows. Should the batch (a) treat it as applied, (b) probe, or
(c) roll back as a concurrent conflict?

The count alone cannot answer it: MySQL's changed-rows semantics report `0` for
an update that rewrote the value the row already held **and** for an update whose
row was deleted by someone else. Every engine reports `>1` only when the
statement's predicate matched more rows than the statement expects.

## The policy

Given `will_probe` = the statement carries a location predicate (a non-empty
table and where-clause; INSERT/DDL do not):

```
needs_probe(actual) = Mismatch(actual) OR (actual == 0 AND will_probe)

Mismatch(actual) = actual > expect   when allow_zero
                 = actual != expect  otherwise

probe_allows_commit(actual, row_present) =
        true                                    when NOT needs_probe(actual)
        NOT Mismatch(actual) AND row_present AND allow_zero   otherwise
```

`probe_allows_commit` is total: it answers "may the batch proceed" for every
case, so a caller can always ask it after running the probe when one was
needed. Callers must not pass `row_present = false` for a statement that
actually needed a probe — that would read as a conflict, which is the safe
direction.

`Mismatch` is `RowsMismatch`/`rows_mismatch`/the inline comparison the Web used
before this contract; `allow_zero` marks a keyed UPDATE (a statement whose
predicate names the primary key), `expect` is 1 for every generated statement.

## Why each clause exists

- **A mismatch is always a conflict**, even after a probe that finds the row:
  a predicate that matches more rows than expected means the batch is not
  addressing what the grid showed. (This clause was missing in one language:
  it let a `>expect` statement through when the row happened to still exist.)
- **`actual == 0` needs a probe only with a predicate.** Without one (INSERT)
  zero is a hard shortfall; with one, the probe tells a same-value no-op apart
  from a vanished row.
- **The probe must run inside the statement's own transaction.** It has to read
  the snapshot the statement wrote to; a second connection both reads a
  different state and (SQLite) fights the write lock.
- **`allow_zero` is a property of the generated statement**, not of the user's
  intent: only keyed updates get it, because only they can be a legal no-op.

## Divergence this contract closed

Before the contract the three copies disagreed:
the Rust GUI probed only on `Mismatch`, so a keyed UPDATE reporting 0 (the exact
case the probe exists for) was allowed with the row possibly gone; the Go TUI
and the Web allowed a `>expect` statement whenever the probe found the row.
Both are fixed by the policy above, and the shared vectors pin all three.
