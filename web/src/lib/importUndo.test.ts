import { describe, it, expect } from 'vitest';
import {
  applySingleItemUndo,
  canRevertSingleItem,
  extractColIdx,
  isDisplayEmptyStr,
  revertSingleItem,
} from './importUndo';
import { DEFAULT_IMPORT_OPTIONS, type ImportOptions, type Mapping } from './importData';

const opts = (over: Partial<ImportOptions>): ImportOptions => ({
  ...DEFAULT_IMPORT_OPTIONS,
  ...over,
});

const mappings: Mapping[] = [
  { csvIndex: 0, csvName: 'name', targetColumn: 'name' },
  { csvIndex: 1, csvName: 'email', targetColumn: 'email' },
];

describe('isDisplayEmptyStr', () => {
  it('treats empty string and diff placeholders as empty', () => {
    expect(isDisplayEmptyStr('')).toBe(true);
    expect(isDisplayEmptyStr('∅')).toBe(true);
    expect(isDisplayEmptyStr('空')).toBe(true);
    expect(isDisplayEmptyStr('null（默认）')).toBe(true);
    expect(isDisplayEmptyStr('trim')).toBe(false);
  });
});

describe('extractColIdx', () => {
  it('prefers the #N suffix then falls back to csv name lookup', () => {
    expect(extractColIdx('name#2 其余', mappings)).toBe(2);
    expect(extractColIdx('email → email', mappings)).toBe(1);
    expect(extractColIdx('unknown → x', mappings)).toBe(null);
  });
});

describe('canRevertSingleItem', () => {
  const target = opts({ mode: 'upsert' });

  it('accepts scalar fields and rejects unknown heads', () => {
    expect(canRevertSingleItem({ field: '模式', before: 'insert', after: 'upsert' }, target, mappings)).toBe(true);
    expect(canRevertSingleItem({ field: '严格校验', before: '是', after: '否' }, target, mappings)).toBe(true);
    // 「空串视为 null」标签自带空格 → head 解析为「空串视为」→ 落 default（与原实现一致的既有行为）
    expect(canRevertSingleItem({ field: '空串视为 null', before: '是', after: '否' }, target, mappings)).toBe(false);
    expect(canRevertSingleItem({ field: '行过滤', before: '∅', after: 'name = x' }, target, mappings)).toBe(true);
    expect(canRevertSingleItem({ field: '未知字段', before: 'a', after: 'b' }, target, mappings)).toBe(false);
  });

  it('rejects non-finite batch size and unresolvable columns', () => {
    expect(canRevertSingleItem({ field: '批大小', before: '500', after: '100' }, target, mappings)).toBe(true);
    expect(canRevertSingleItem({ field: '批大小', before: '500', after: '100' }, opts({ batchSize: NaN }), mappings)).toBe(false);
    expect(canRevertSingleItem({ field: '转换 nonexistent → x', before: 'trim', after: '∅' }, target, mappings)).toBe(false);
  });

  it('requires a non-empty before when reverting to empty display value', () => {
    expect(canRevertSingleItem({ field: '转换 name#0', before: 'trim', after: '∅' }, target, mappings)).toBe(true);
    expect(canRevertSingleItem({ field: '转换 name#0', before: '', after: '∅' }, target, mappings)).toBe(false);
  });
});

describe('revertSingleItem', () => {
  it('restores mode and does not mutate current opts', () => {
    const cur = opts({ mode: 'insert' });
    const next = revertSingleItem({ field: '模式', before: 'upsert', after: 'insert' }, opts({ mode: 'upsert' }), cur, mappings);
    expect(next?.mode).toBe('upsert');
    expect(cur.mode).toBe('insert');
  });

  it('restores batch size; NaN target is impossible', () => {
    const next = revertSingleItem({ field: '批大小', before: '500', after: '100' }, opts({ batchSize: 100 }), opts({ batchSize: 500 }), mappings);
    expect(next?.batchSize).toBe(100);
    expect(revertSingleItem({ field: '批大小', before: '500', after: 'NaN' }, opts({ batchSize: NaN }), opts({}), mappings)).toBe(null);
  });

  it('deletes a transform when target is the empty mark, writes it otherwise', () => {
    const del = revertSingleItem(
      { field: '转换 name#0', before: 'trim', after: '∅' },
      opts({}),
      opts({ transforms: { 0: 'trim' } }),
      mappings,
    );
    expect(del?.transforms).toEqual({});
    const set = revertSingleItem(
      { field: '转换 name#0', before: '∅', after: 'upper' },
      opts({ transforms: { 0: 'upper' } }),
      opts({}),
      mappings,
    );
    expect(set?.transforms).toEqual({ 0: 'upper' });
  });

  it('restores validations from the target snapshot', () => {
    const tv = { required: true, regex: '^a', minValue: '', maxValue: '', enum: '' };
    const next = revertSingleItem(
      { field: '校验 name#0', before: '∅', after: '必填' },
      opts({ validations: { 0: tv } }),
      opts({}),
      mappings,
    );
    expect(next?.validations?.[0]).toEqual(tv);
    // 目标为空标记且当前也空 → 无变化（返回空 validations 的 clone）
    const none = revertSingleItem(
      { field: '校验 name#0', before: '', after: '∅' },
      opts({}),
      opts({}),
      mappings,
    );
    expect(none).not.toBe(null);
  });

  it('falls back to null for unknown heads', () => {
    expect(revertSingleItem({ field: '未知字段', before: 'a', after: 'b' }, opts({}), opts({}), mappings)).toBe(null);
  });

  it('reverts row filter to none when target has no filter', () => {
    const next = revertSingleItem(
      { field: '行过滤', before: 'name eq x', after: '∅' },
      opts({ filterColumn: null }),
      opts({ filterColumn: 0, filterOp: 'eq', filterValue: 'x' }),
      mappings,
    );
    expect(next?.filterColumn).toBe(null);
    expect(next?.filterValue).toBe('');
  });
});

describe('applySingleItemUndo', () => {
  it('classifies impossible / already / applied outcomes', () => {
    const cur = opts({ mode: 'insert' });
    expect(applySingleItemUndo({ field: '未知字段', before: 'a', after: 'b' }, opts({}), cur, mappings).status).toBe('impossible');
    expect(applySingleItemUndo({ field: '模式', before: 'insert', after: 'insert' }, opts({ mode: 'insert' }), cur, mappings).status).toBe('already');
    const r = applySingleItemUndo({ field: '模式', before: 'upsert', after: 'insert' }, opts({ mode: 'upsert' }), cur, mappings);
    expect(r.status).toBe('applied');
    if (r.status === 'applied') expect(r.next.mode).toBe('upsert');
  });
});
