import { describe, it, expect } from 'vitest';
import {
  backupPreviewFiltered,
  backupPreviewGroupOrderMap,
  backupPreviewSorted,
  diffCountOf,
  riskScoreOf,
} from './importBackupPreview';
import type { BackupPreviewItem, PresetDiff } from './importPresets';

const ctx = { connId: 'conn1', audit: null as Record<string, never> | null };

const diff = (n: number, name = 'a'): PresetDiff => ({
  total: n,
  aligned: n === 0,
  scalarDiffs: Array.from({ length: n }, (_, i) => `${name}${i}`),
  removedTargets: [],
  addedTargets: [],
  removedCols: {},
  addedCols: {},
});

const item = (over: Partial<BackupPreviewItem>): BackupPreviewItem => ({
  key: `conn1::${over.schema ?? 'public'}::${over.table ?? 'users'}`,
  kind: 'preset',
  action: 'add',
  schema: 'public',
  table: 'users',
  snapshotCount: 0,
  ...over,
});

describe('diffCountOf', () => {
  it('counts scalar/target/col changes; add items are 0', () => {
    expect(diffCountOf(item({}))).toBe(0);
    expect(diffCountOf(item({ action: 'overwrite', diff: diff(2) }))).toBe(2);
    const d = diff(0);
    d.removedTargets = ['-t1'];
    d.addedTargets = ['+t1', '+t2'];
    d.removedCols = { cfg: ['x', 'y'] };
    expect(diffCountOf(item({ action: 'overwrite', diff: d }))).toBe(4);
  });
});

describe('riskScoreOf', () => {
  it('weights dead audit > cross-conn > diff > overwrite', () => {
    expect(riskScoreOf(item({}), ctx)).toBe(0);
    expect(riskScoreOf(item({ action: 'overwrite' }), ctx)).toBe(5);
    expect(riskScoreOf(item({ key: 'conn2::public::users' }), ctx)).toBe(5000);
    expect(
      riskScoreOf(item({ key: 'conn2::public::users', action: 'overwrite', diff: diff(4) }), ctx),
    ).toBe(5000 + 40 + 5);
    expect(
      riskScoreOf(item({}), { connId: 'conn1', audit: { 'conn1::public::users': 'dead' } }),
    ).toBe(10000);
  });
});

describe('backupPreviewSorted', () => {
  const rows = [
    item({ schema: 'b', table: 't2' }),
    item({ schema: 'a', table: 't1', action: 'overwrite', diff: diff(3) }),
    item({ schema: 'a', table: 't9' }),
  ];

  it('sorts by schema.table name ascending (schema mode, stable tiebreaker)', () => {
    const out = backupPreviewSorted(rows, 'schema', ctx);
    expect(out.map((r) => `${r.schema}.${r.table}`)).toEqual(['a.t1', 'a.t9', 'b.t2']);
  });

  it('sorts by diff count desc with overwrite-first tiebreaker', () => {
    const out = backupPreviewSorted(rows, 'diff', ctx);
    expect(out[0].table).toBe('t1'); // 唯一有 diff 的项前置
    // 其余并列 0 diff：overwrite 无、按名字典序
    expect(out.slice(1).map((r) => `${r.schema}.${r.table}`)).toEqual(['a.t9', 'b.t2']);
  });

  it('sorts by risk score desc with name tiebreaker', () => {
    const out = backupPreviewSorted(rows, 'risk', ctx);
    expect(out[0].table).toBe('t1'); // diff=3 → 30+5=35
    expect(out.slice(1).map((r) => `${r.schema}.${r.table}`)).toEqual(['a.t9', 'b.t2']);
  });

  it('does not mutate the input array', () => {
    const before = [...rows];
    backupPreviewSorted(rows, 'diff', ctx);
    expect(rows).toEqual(before);
  });
});

describe('backupPreviewGroupOrderMap', () => {
  const rows = [
    item({ schema: 'public', table: 'a' }),
    item({ schema: 'public', table: 'b', action: 'overwrite', diff: diff(2) }),
    item({ schema: 'public', table: 'b', action: 'overwrite', diff: diff(1, 'b') }),
  ];

  it('returns empty map when grouping disabled', () => {
    expect(backupPreviewGroupOrderMap(rows, { enabled: false, groupSort: 'diff', ...ctx })).toEqual({});
  });

  it('orders groups by diff total desc', () => {
    const map = backupPreviewGroupOrderMap(rows, { enabled: true, groupSort: 'diff', ...ctx });
    // public::b 组合计 diff=3 > public::a 组 0
    expect(map['public::b']).toBe(0);
    expect(map['public::a']).toBe(1);
  });

  it('orders groups alphabetically in name mode', () => {
    const map = backupPreviewGroupOrderMap(rows, { enabled: true, groupSort: 'name', ...ctx });
    expect(map['public::a']).toBe(0);
    expect(map['public::b']).toBe(1);
  });
});

describe('backupPreviewFiltered', () => {
  const rows = [
    item({ schema: 'public', table: 'users', action: 'overwrite', diff: diff(5) }),
    item({ schema: 'public', table: 'orders' }),
    item({ schema: 'billing', table: 'invoices', kind: 'snapshot' }),
  ];
  const all = {
    search: '',
    actionFilter: 'all',
    kindFilter: 'all',
    diffBinFilter: 'all',
    groupBy: false,
    collapsedGroups: new Set<string>(),
    riskLevelFilter: 'all',
    riskThreshold: 0,
  } as const;

  it('passes everything through with default filters', () => {
    expect(backupPreviewFiltered(rows, all, ctx, {})).toHaveLength(3);
  });

  it('filters by action, kind and search substring', () => {
    expect(backupPreviewFiltered(rows, { ...all, actionFilter: 'overwrite' }, ctx, {})).toHaveLength(1);
    expect(backupPreviewFiltered(rows, { ...all, kindFilter: 'snapshot' }, ctx, {})).toHaveLength(1);
    expect(backupPreviewFiltered(rows, { ...all, search: 'BILL' }, ctx, {})).toHaveLength(1);
    expect(backupPreviewFiltered(rows, { ...all, search: 'user' }, ctx, {})).toHaveLength(1);
  });

  it('drops items in collapsed groups when grouped and orders by group map', () => {
    const out = backupPreviewFiltered(
      rows,
      { ...all, groupBy: true, collapsedGroups: new Set(['public::users']) },
      ctx,
      { 'public::orders': 0, 'billing::invoices': 1 },
    );
    expect(out.map((r) => r.table)).toEqual(['orders', 'invoices']);
  });

  it('applies risk threshold on the risk score', () => {
    const out = backupPreviewFiltered(rows, { ...all, riskThreshold: 20 }, ctx, {});
    // 只有 overwrite+diff=5 的项达到 15+10=25 分
    expect(out.map((r) => r.table)).toEqual(['users']);
  });

  it('applies diff-bin filter to overwrite presets only', () => {
    // 分档 2：diffCount 4-10 → 只有 users（diff=5）
    expect(backupPreviewFiltered(rows, { ...all, diffBinFilter: '2' }, ctx, {}).map((r) => r.table)).toEqual(['users']);
    // 分档 0：无 diff 的 overwrite 才算；orders/invoices 非 overwrite preset → 不保留
    expect(backupPreviewFiltered(rows, { ...all, diffBinFilter: '0' }, ctx, {})).toHaveLength(0);
  });

  it('reorders by group order map when grouped', () => {
    const groupOrderMap = { 'billing::invoices': 0, 'public::users': 1, 'public::orders': 2 };
    const out = backupPreviewFiltered(rows, { ...all, groupBy: true }, ctx, groupOrderMap);
    expect(out.map((r) => r.table)).toEqual(['invoices', 'users', 'orders']);
  });
});
