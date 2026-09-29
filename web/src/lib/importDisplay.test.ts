import { describe, it, expect } from 'vitest';
import { ApiError } from './api';
import {
  classifyFailReason,
  failCategoryMeta,
  previewParams,
  sqlLiteral,
  boolLabel,
  fmtCacheAgeForAudit,
  colDtypeColor,
  formatTime,
  formatValue,
  historyStatusBadge,
  qualityScoreColor,
  sampleForKind,
  toMsg,
  typeBadgeColor,
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

// ─── 自 components/ImportModalParts.tsx 下沉的展示/格式化纯函数 ───

describe('formatValue', () => {
  it('formats scalar and container values', () => {
    expect(formatValue(null)).toBe('NULL');
    expect(formatValue("a'b")).toBe("'a''b'");
    expect(formatValue(true)).toBe('true');
    expect(formatValue(42)).toBe('42');
    expect(formatValue([1, null, 'x'])).toBe("[1, NULL, 'x']");
    expect(formatValue({ a: 1 })).toBe('{"a":1}');
  });
});

describe('typeBadgeColor', () => {
  it('maps every badge type to a color', () => {
    const types = ['string', 'integer', 'float', 'boolean', 'date', 'null'] as const;
    for (const t of types) expect(typeBadgeColor(t)).toBeTruthy();
    expect(typeBadgeColor('integer')).toBe('#3b82f6');
    expect(typeBadgeColor('date')).toBe('#eab308');
    expect(typeBadgeColor('string')).toBe('var(--muted)');
  });
});

describe('qualityScoreColor', () => {
  it('thresholds at 80/60/40', () => {
    expect(qualityScoreColor(95)).toBe('var(--ok, #10b981)');
    expect(qualityScoreColor(70)).toBe('#eab308');
    expect(qualityScoreColor(50)).toBe('var(--warn, #d97706)');
    expect(qualityScoreColor(10)).toBe('var(--danger, #dc2626)');
  });
});

describe('colDtypeColor', () => {
  it('classifies dtype keywords into color buckets', () => {
    expect(colDtypeColor('INTEGER')).toBe('#3b82f6');
    expect(colDtypeColor('double precision')).toBe('#06b6d4');
    expect(colDtypeColor('boolean')).toBe('#10b981');
    expect(colDtypeColor('timestamp')).toBe('#eab308');
    expect(colDtypeColor('varchar(64)')).toBe('var(--accent, #3b82f6)');
    expect(colDtypeColor('bytea')).toBe('var(--muted)');
    expect(colDtypeColor('mystery_type')).toBe('var(--muted)');
  });
});

describe('toMsg', () => {
  it('formats ApiError with code, Error with message, others with String', () => {
    expect(toMsg(new ApiError(404, 'not-found', 'missing'))).toBe('not-found: missing');
    expect(toMsg(new TypeError('boom'))).toBe('boom');
    expect(toMsg('plain')).toBe('plain');
    expect(toMsg(42)).toBe('42');
  });
});

describe('formatTime', () => {
  it('formats as YYYY-MM-DD HH:mm with zero padding', () => {
    expect(formatTime(new Date(2025, 0, 2, 3, 4).getTime())).toBe('2025-01-02 03:04');
    expect(formatTime(new Date(2025, 10, 20, 13, 45).getTime())).toBe('2025-11-20 13:45');
  });
});

describe('historyStatusBadge', () => {
  it('maps every status to a badge label', () => {
    expect(historyStatusBadge('success')).toBe('✓ 成功');
    expect(historyStatusBadge('partial')).toBe('◐ 部分');
    expect(historyStatusBadge('failed')).toBe('✕ 失败');
    expect(historyStatusBadge('cancelled')).toBe('⏹ 取消');
  });
});

describe('sampleForKind', () => {
  it('falls back to the sqlite sample for unknown kind and splits csv/jsonl text', () => {
    expect(sampleForKind(null, 'csv').title).toBe('SQLite 示例');
    expect(sampleForKind(null, 'csv').text).toContain('id,name,email');
    const mysqlJson = sampleForKind('mysql', 'jsonl');
    expect(mysqlJson.title).toBe('MySQL 示例');
    expect(mysqlJson.text).toContain('"created_at"');
    expect(sampleForKind('mysql', 'csv').text).not.toContain('{');
    expect(sampleForKind('postgres', 'csv').title).toBe('PostgreSQL 示例');
  });
});
