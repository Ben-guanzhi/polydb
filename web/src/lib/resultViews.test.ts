import { describe, expect, it } from 'vitest';
import type { QueryResult, ResultColumn } from '../api';
import { buildInspectorFields, cellClass, fmt, outputMessages } from './resultViews';

const col = (name: string, type = 'text'): ResultColumn => ({ name, type });

const res = (over: Partial<QueryResult> = {}): QueryResult => ({
  columns: [col('id', 'integer'), col('name')],
  rows: [[1, 'alice'], [2, null]],
  affected_rows: 0,
  execution_time_ms: 3.456,
  statement_type: 'select',
  ...over,
});

describe('fmt / cellClass', () => {
  it('NULL 大写、数组递归、对象走 JSON', () => {
    expect(fmt(null)).toBe('NULL');
    expect(fmt(undefined)).toBe('NULL');
    expect(fmt([1, 'a', null])).toBe('[1, a, NULL]');
    expect(fmt({ a: 1 })).toBe('{"a":1}');
  });

  it('按值与列类型分类', () => {
    expect(cellClass(1, col('id', 'integer'))).toBe('num');
    expect(cellClass(true)).toBe('bool');
    expect(cellClass('42', col('n', 'bigint'))).toBe('num');
    expect(cellClass('42', col('s', 'varchar'))).toBe('text');
    expect(cellClass(null)).toBe('null');
  });

  it('$blob 内联与旁路引用统一占位标签（encoding.md §5.1）', () => {
    expect(fmt({ $blob: { len: 3, base64: 'AAEC' } })).toBe('<blob 3 bytes>');
    expect(fmt({ $blob: { len: 2097153, ref: '$polydb_blob:00000000-0000-0000-0000-000000000000' } }))
      .toBe('<blob 2097153 bytes>');
    expect(cellClass({ $blob: { len: 3, base64: 'AAEC' } })).toBe('blob');
    expect(cellClass({ $blob: { len: 2097153, ref: '$polydb_blob:x' } })).toBe('blob');
    // 非 $blob 对象仍走 JSON
    expect(fmt({ other: 1 })).toBe('{"other":1}');
  });
});

describe('buildInspectorFields', () => {
  it('逐字段产出，跳过隐藏列并保留原始列下标', () => {
    const f = buildInspectorFields([col('id', 'integer'), col('a'), col('b')], [7, 'x', null], new Set([1]));
    expect(f.map((x) => x.col)).toEqual([0, 2]);
    expect(f[0]).toMatchObject({ name: 'id', type: 'integer', display: '7', isNull: false, truncated: false });
    expect(f[1]).toMatchObject({ name: 'b', display: 'NULL', isNull: true });
  });

  it('超过上限标 truncated，NULL 不算超长', () => {
    const long = 'z'.repeat(401);
    const f = buildInspectorFields([col('big'), col('n')], [long, null]);
    expect(f[0].truncated).toBe(true);
    expect(f[1].truncated).toBe(false);
  });

  it('行缺失时为空', () => {
    expect(buildInspectorFields([col('id')], undefined)).toEqual([]);
  });
});

describe('outputMessages', () => {
  it('单条 SELECT：行列数 + 服务端/往返耗时', () => {
    const m = outputMessages({
      multi: null,
      activeIdx: 0,
      single: { sql: 'select * from t', result: res(), error: null, elapsed: 12.5, status: 'ok' },
    });
    expect(m).toHaveLength(1);
    expect(m[0].title).toBe('SELECT');
    expect(m[0].lines[0]).toBe('✓ select 返回 2 行 · 2 列');
    expect(m[0].lines[1]).toBe('服务端 3.46 ms · 往返 12.50 ms');
  });

  it('编辑器关键词与 result.statement_type 不同时一起标出', () => {
    const m = outputMessages({
      multi: null,
      activeIdx: 0,
      single: { sql: 'with x as (select 1) select * from x', result: res({ statement_type: 'select' }), error: null, elapsed: null, status: 'ok' },
    });
    expect(m[0].title).toBe('WITH · select');
    expect(m[0].lines).toEqual(['✓ select 返回 2 行 · 2 列', '服务端 3.46 ms']);
  });

  it('DML 报受影响行数，截断与分页提示各一条', () => {
    const m = outputMessages({
      multi: null,
      activeIdx: 0,
      single: {
        sql: 'update t set name = 1',
        result: res({ columns: [], rows: [], affected_rows: 5, statement_type: 'update', truncated: true, has_more: true, total_rows: 120 }),
        error: null,
        elapsed: null,
        status: 'ok',
      },
    });
    expect(m[0].lines).toEqual([
      '✓ update 执行成功，无结果集',
      '影响 5 行',
      '服务端 3.46 ms',
      '⚠ 服务端已截断结果（超过 max_rows）',
      'ℹ 仍有更多行：已取 0 / 共 120',
    ]);
  });

  it('错误条目只出错误行，不带耗时', () => {
    const m = outputMessages({
      multi: null,
      activeIdx: 0,
      single: { sql: 'select * from nope', result: null, error: 'no such table: nope', elapsed: 4, status: 'err' },
    });
    expect(m[0].lines).toEqual(['✕ no such table: nope']);
  });

  it('多语句逐条列出并标出当前条与各态文案', () => {
    const m = outputMessages({
      activeIdx: 1,
      single: null,
      multi: [
        { sql: 'create table t(id int)', result: res({ columns: [], rows: [], statement_type: 'ddl' }), error: null, elapsed: null, status: 'ok' },
        { sql: 'insert into t values (1)', result: null, error: null, elapsed: null, status: 'running' },
        { sql: 'select * from t', result: null, error: null, elapsed: null, status: 'pending' },
        { sql: 'drop table t', result: null, error: null, elapsed: null, status: 'cancelled' },
        { sql: 'select 1', result: null, error: null, elapsed: null, status: 'skipped' },
      ],
    });
    expect(m.map((x) => x.title)).toEqual([
      'CREATE · 第 1/5 条',
      'INSERT · 第 2/5 条',
      'SELECT · 第 3/5 条',
      'DROP · 第 4/5 条',
      'SELECT · 第 5/5 条',
    ]);
    expect(m.map((x) => x.active)).toEqual([false, true, false, false, false]);
    expect(m[0].lines[0]).toBe('✓ ddl 执行成功，无结果集');
    expect(m[1].lines).toEqual(['… 执行中']);
    expect(m[2].lines).toEqual(['· 排队待执行']);
    expect(m[3].lines).toEqual(['⊘ 已取消（当前语句执行完毕后中断）']);
    expect(m[4].lines).toEqual(['· 未执行（前面的执行被取消）']);
  });

  it('无执行记录时为空', () => {
    expect(outputMessages({ multi: null, activeIdx: 0, single: null })).toEqual([]);
  });
});
