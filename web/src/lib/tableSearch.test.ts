import { describe, expect, it } from 'vitest';
import { fuzzyScore, rankTables, type SearchableTable } from './tableSearch';

const T = (schema: string, table: string): SearchableTable => ({ schema, table });

describe('fuzzyScore', () => {
  it('空 query 恒匹配', () => {
    expect(fuzzyScore('', 'anything')).toBe(1);
  });
  it('子序列命中与未命中', () => {
    expect(fuzzyScore('ua', 'user_addresses')).toBeGreaterThanOrEqual(0);
    expect(fuzzyScore('zz', 'user_addresses')).toBe(-1);
  });
  it('分隔符边界（含 . _）加分，前缀优于中部命中', () => {
    const prefix = fuzzyScore('pub', 'public.orders');
    const middle = fuzzyScore('pub', 'republish_log');
    expect(prefix).toBeGreaterThan(middle);
  });
});

describe('rankTables', () => {
  const items = [T('public', 'users'), T('public', 'user_addresses'), T('sales', 'orders'), T('sales', 'order_items')];
  it('usadd 命中 user_addresses（验收用例）', () => {
    const r = rankTables('usadd', items);
    expect(r[0].table).toBe('user_addresses');
  });
  it('空 query 原序截断', () => {
    expect(rankTables('  ', items, 2)).toHaveLength(2);
  });
  it('so 命中 sales.orders（schema 边界）优于 order_items', () => {
    const r = rankTables('so', items);
    expect(r[0]).toEqual(T('sales', 'orders'));
  });
  it('limit 生效', () => {
    expect(rankTables('o', items, 1)).toHaveLength(1);
  });
});
