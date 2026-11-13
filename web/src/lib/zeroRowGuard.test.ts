// The zero-row guard is a cross-language contract (spec/zero-row-guard.md) with
// one shared case file; this test is the Web half, so the Go, Rust and Web
// implementations cannot drift (LENS-04).

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { needsRowProbe, probeAllowsCommit, type GuardStatement } from './changes';

interface VectorCase {
  name: string;
  allow_zero: boolean;
  expect_affected: number;
  has_predicate: boolean;
  actual: number;
  present: boolean;
  needs_probe: boolean;
  allows_commit: boolean;
}

const here = dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(
  readFileSync(resolve(here, '../../../spec/vectors/zero-row-guard.json'), 'utf8'),
) as { cases: VectorCase[] };

describe('zero-row guard contract', () => {
  it('matches the shared vectors', () => {
    expect(vectors.cases.length).toBeGreaterThan(0);
    for (const c of vectors.cases) {
      const stmt: GuardStatement = {
        allowZero: c.allow_zero,
        expectAffected: c.expect_affected,
        table: c.has_predicate ? 't' : '',
        whereClause: c.has_predicate ? 'id = ?' : '',
      };
      expect(needsRowProbe(stmt, c.actual), `${c.name}: needsRowProbe`).toBe(c.needs_probe);
      expect(probeAllowsCommit(stmt, c.actual, c.present), `${c.name}: probeAllowsCommit`).toBe(
        c.allows_commit,
      );
    }
  });
});
