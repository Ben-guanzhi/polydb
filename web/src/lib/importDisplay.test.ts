import { describe, it, expect } from 'vitest';
import {
  classifyFailReason,
  failCategoryMeta,
  previewParams,
  sqlLiteral,
  boolLabel,
  fmtCacheAgeForAudit,
} from './importDisplay';

describe('classifyFailReason', () => {
  it('classifies each failure category by keyword', () => {
    expect(classifyFailReason('NOT NULL constraint failed').key).toBe('not-null');
    expect(classifyFailReason('foreign key constraint').key).toBe('fk');
    expect(classifyFailReason('duplicate key value').key).toBe('dup');
    expect(classifyFailReason('cannot be cast to type').key).toBe('type');
    expect(classifyFailReason('regex 校验未通过').key).toBe('validation');
    expect(classifyFailReason('affected_rows=0').key).toBe('no-affected');
    expect(classifyFailReason('something unknown').key).toBe('other');
  });
});

describe('failCategoryMeta', () => {
  it('maps every category key to display metadata', () => {
    for (const key of ['not-null', 'fk', 'dup', 'type', 'validation', 'no-affected']) {
      const meta = failCategoryMeta(key);
      expect(meta.icon).toBeTruthy();
      expect(meta.color).toMatch(/^#/);
      expect(meta.label).toBeTruthy();
    }
    expect(failCategoryMeta('nope').label).toBe('其他错误');
  });
});

describe('previewParams', () => {
  it('formats and truncates parameter previews', () => {
    expect(previewParams([null, 42, true])).toBe('NULL, 42, true');
    expect(previewParams(['a'.repeat(30)])).toBe(`${'a'.repeat(20)}…`);
  });
});

describe('sqlLiteral', () => {
  it('renders values as SQL literals', () => {
    expect(sqlLiteral(null)).toBe('NULL');
    expect(sqlLiteral(42)).toBe('42');
    expect(sqlLiteral(NaN)).toBe('NULL');
    expect(sqlLiteral(Infinity)).toBe('NULL');
    expect(sqlLiteral(true)).toBe('1');
    expect(sqlLiteral(false)).toBe('0');
    expect(sqlLiteral("it's")).toBe("'it''s'");
  });
});

describe('boolLabel', () => {
  it('labels booleans', () => {
    expect(boolLabel(true)).toBe('✅ 是');
    expect(boolLabel(false)).toBe('❌ 否');
  });
});

describe('fmtCacheAgeForAudit', () => {
  it('formats cache age buckets', () => {
    const now = Date.now();
    expect(fmtCacheAgeForAudit(now - 10_000)).toBe('刚刚');
    expect(fmtCacheAgeForAudit(now - 5 * 60 * 1000)).toBe('5 分钟前');
    expect(fmtCacheAgeForAudit(now - 3 * 60 * 60 * 1000)).toBe('3 小时前');
    expect(fmtCacheAgeForAudit(now - 2 * 24 * 60 * 60 * 1000)).toBe('2 天前');
  });
});
