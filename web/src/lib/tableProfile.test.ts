import { describe, expect, it } from 'vitest';
import { buildProfilePlan, isLobType, isNumericType, parseProfile } from './tableProfile';

const q = (s: string) => `"${s}"`;

describe('type heuristics', () => {
  it('numeric vs lob', () => {
    expect(isNumericType('integer')).toBe(true);
    expect(isNumericType('NUMBER(10,2)')).toBe(true);
    expect(isNumericType('double precision')).toBe(true);
    expect(isNumericType('varchar')).toBe(false);
    expect(isLobType('text')).toBe(true);
    expect(isLobType('jsonb')).toBe(true);
    expect(isLobType('integer')).toBe(false);
  });
});

describe('buildProfilePlan', () => {
  it('numeric col gets nulls/distinct/min/max/avg, lob col only nulls+distinct? no—lob skips distinct', () => {
    const plan = buildProfilePlan(q, '"main"."emp"', [
      { name: 'salary', data_type: 'real' },
      { name: 'bio', data_type: 'text' },
    ]);
    expect(plan.countIdx).toBe(0);
    expect(plan.sql).toContain('COUNT(*)');
    expect(plan.sql).toContain('MIN("salary")');
    expect(plan.sql).toContain('AVG("salary")');
    expect(plan.sql).toContain('COUNT(DISTINCT "salary")');
    expect(plan.sql).not.toContain('COUNT(DISTINCT "bio")');
    expect(plan.sql).not.toContain('MIN("bio")');
    // salary: nulls=1 distinct=2 min=3 max=4 avg=5; bio: nulls=6
    expect(plan.metrics).toEqual([
      { idx: 1, column: 'salary', kind: 'nulls' },
      { idx: 2, column: 'salary', kind: 'distinct' },
      { idx: 3, column: 'salary', kind: 'min' },
      { idx: 4, column: 'salary', kind: 'max' },
      { idx: 5, column: 'salary', kind: 'avg' },
      { idx: 6, column: 'bio', kind: 'nulls' },
    ]);
  });

  it('plain text col still gets distinct', () => {
    const plan = buildProfilePlan(q, '"main"."t"', [{ name: 'name', data_type: 'varchar' }]);
    expect(plan.metrics.map((m) => m.kind)).toEqual(['nulls', 'distinct']);
  });
});

describe('parseProfile', () => {
  it('restores rowCount and per-column metrics by index', () => {
    const plan = buildProfilePlan(q, '"main"."emp"', [{ name: 'salary', data_type: 'real' }]);
    const row = [100, 3, 97, 1000.5, 9000, 4321.5];
    const r = parseProfile(row, plan);
    expect(r.rowCount).toBe(100);
    expect(r.byColumn.get('salary')).toEqual({
      nulls: 3,
      distinct: 97,
      min: 1000.5,
      max: 9000,
      avg: 4321.5,
    });
  });

  it('missing row yields empty result', () => {
    const plan = buildProfilePlan(q, '"main"."t"', [{ name: 'x', data_type: 'int' }]);
    const r = parseProfile(undefined, plan);
    expect(r.rowCount).toBeNull();
    expect(r.byColumn.size).toBe(0);
  });

  it('string-encoded numbers are coerced', () => {
    const plan = buildProfilePlan(q, '"main"."t"', [{ name: 'x', data_type: 'int' }]);
    const row = ['42', '1', '4', 'a', 'z', '2.5'];
    const r = parseProfile(row, plan);
    expect(r.rowCount).toBe(42);
    const p = r.byColumn.get('x');
    expect(p?.nulls).toBe(1);
    expect(p?.min).toBe('a');
    expect(p?.avg).toBe(2.5);
  });
});
