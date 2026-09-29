import { describe, it, expect } from 'vitest';
import { computeItemRiskScore } from './importRisk';
import type { BackupPreviewItem, PresetDiff } from './importPresets';

const diff = (total: number): PresetDiff => ({
  total,
  aligned: total === 0,
  scalarDiffs: [],
  removedTargets: [],
  addedTargets: [],
  removedCols: {},
  addedCols: {},
});

const item = (over: Partial<BackupPreviewItem>): BackupPreviewItem => ({
  key: 'conn1::public::users',
  kind: 'preset',
  action: 'add',
  schema: 'public',
  table: 'users',
  snapshotCount: 0,
  ...over,
});

describe('computeItemRiskScore', () => {
  it('scores a fresh same-conn preset as low risk with no parts', () => {
    const r = computeItemRiskScore(item({}), 'conn1', null);
    expect(r.score).toBe(0);
    expect(r.level).toBe('low');
    expect(r.icon).toBe('🟢');
    expect(r.label).toBe('低风险');
    expect(r.parts).toEqual([]);
  });

  it('adds diff buckets by size and overwrite bonus', () => {
    // 小 diff(15) + 覆盖(10) = 25 → mid
    const small = computeItemRiskScore(item({ action: 'overwrite', diff: diff(2) }), 'conn1', null);
    expect(small.parts).toEqual(['diff=2(小)', '覆盖']);
    expect(small.level).toBe('mid');
    // 中 diff(30) → 30 → mid
    const mid = computeItemRiskScore(item({ action: 'add', diff: diff(6) }), 'conn1', null);
    expect(mid.parts).toContain('diff=6(中)');
    expect(mid.level).toBe('mid');
    // 大 diff(50) → high
    const large = computeItemRiskScore(item({ action: 'add', diff: diff(12) }), 'conn1', null);
    expect(large.parts).toContain('diff=12(大)');
    expect(large.level).toBe('high');
  });

  it('detects cross-conn items by key prefix', () => {
    const r = computeItemRiskScore(item({ key: 'conn2::public::users' }), 'conn1', null);
    expect(r.parts).toContain('跨连接');
    expect(r.score).toBe(25);
  });

  it('uses audit status: dead adds 60, ok is informational only', () => {
    const dead = computeItemRiskScore(item({}), 'conn1', { 'conn1::public::users': 'dead' });
    expect(dead.parts).toContain('审计失效');
    expect(dead.level).toBe('high');
    const ok = computeItemRiskScore(item({}), 'conn1', { 'conn1::public::users': 'ok' });
    expect(ok.parts).toContain('存活');
    expect(ok.score).toBe(0);
  });

  it('caps the score at 100', () => {
    const r = computeItemRiskScore(
      item({ action: 'overwrite', diff: diff(12) }),
      'conn2',
      { 'conn1::public::users': 'dead' },
    );
    // 大 diff 50 + 跨连接 25 + 覆盖 10 + 审计失效 60 = 145 → 封顶 100
    expect(r.score).toBe(100);
    expect(r.level).toBe('high');
  });

  it('adds snapshot bonus only for many snapshots', () => {
    const many = computeItemRiskScore(item({ kind: 'snapshot', snapshotCount: 12 }), 'conn1', null);
    expect(many.parts).toContain('快照=12(多)');
    expect(many.score).toBe(10);
    const few = computeItemRiskScore(item({ kind: 'snapshot', snapshotCount: 3 }), 'conn1', null);
    expect(few.parts).toEqual([]);
    expect(few.score).toBe(0);
  });
});
