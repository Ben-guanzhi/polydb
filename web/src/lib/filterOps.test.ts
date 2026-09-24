import { describe, expect, it } from 'vitest';
import type { FilterCondition, FilterOperator } from '../api';
import {
  CASE_MODES, FILTER_OPS, caseModeToWire, condToWire, emptyCondRow,
  numish, opMeta, opSupported, opUnsupportedHint, wireToCaseMode, wireToCond,
  type CondRow,
} from './filterOps';

const row = (over: Partial<CondRow> = {}): CondRow => ({ ...emptyCondRow(), column: 'name', ...over });

describe('FILTER_OPS 元数据', () => {
  it('共 18 个 op，与 spec 枚举一一对应且不重复', () => {
    expect(FILTER_OPS).toHaveLength(18);
    expect(new Set(FILTER_OPS.map((o) => o.value)).size).toBe(18);
    const spec = ['between', 'contains', 'empty', 'ends_with', 'eq', 'ge', 'gt', 'in', 'le',
      'like', 'lt', 'ne', 'not_in', 'not_like', 'not_null', 'null', 'regex', 'starts_with'];
    expect(FILTER_OPS.map((o) => o.value).sort()).toEqual(spec);
  });

  it('取值形态：needsValues 与其余互斥；needsSecond 只随 between 出现', () => {
    for (const o of FILTER_OPS) {
      if (o.needsValues) expect([o.needsValue, o.needsSecond]).toEqual([false, false]);
      if (o.needsSecond) expect([o.value, o.needsValue]).toEqual(['between', true]);
    }
    for (const op of ['null', 'not_null', 'empty'] as FilterOperator[]) {
      const m = opMeta(op)!;
      expect([m.needsValue, m.needsValues, m.needsSecond]).toEqual([false, false, false]);
    }
    // between 是唯一的三取值 op
    expect(opMeta('between')!.needsSecond).toBe(true);
  });

  it('textMatch 恰好是 behavior.md §13.5 的六个 op', () => {
    expect(FILTER_OPS.filter((o) => o.textMatch).map((o) => o.value).sort()).toEqual(
      ['contains', 'ends_with', 'like', 'not_like', 'regex', 'starts_with'],
    );
  });
});

describe('case_sensitive 三态', () => {
  it('default 省略字段（旧客户端零影响）', () => {
    expect(caseModeToWire('default')).toBeUndefined();
    expect(caseModeToWire('sensitive')).toBe(true);
    expect(caseModeToWire('insensitive')).toBe(false);
    expect(wireToCaseMode(undefined)).toBe('default');
    expect(wireToCond({ column: 'a', op: 'like', value: 'x' }).caseMode).toBe('default');
  });

  it('三态控件标签齐备且回读往返一致', () => {
    expect(CASE_MODES.map((m) => m.value)).toEqual(['default', 'sensitive', 'insensitive']);
    for (const mode of ['default', 'sensitive', 'insensitive'] as const) {
      expect(wireToCaseMode(caseModeToWire(mode))).toBe(mode);
    }
  });

  it('文本匹配 op 才下发字段；比较类 op 即使设了也省略', () => {
    expect(condToWire(row({ op: 'contains', value: 'a', caseMode: 'sensitive' }))).toEqual(
      { column: 'name', op: 'contains', value: 'a', case_sensitive: true },
    );
    expect(condToWire(row({ op: 'eq', value: '1', caseMode: 'sensitive' }))).toEqual(
      { column: 'name', op: 'eq', value: 1 },
    );
  });
});

describe('opSupported / 能力置灰', () => {
  it('mssql 无原生正则，其余五库可用（behavior.md §13.6）', () => {
    for (const kind of ['sqlite', 'mysql', 'postgres', 'oracle'] as const) {
      expect(opSupported(kind, 'regex')).toBe(true);
      expect(opUnsupportedHint(kind, 'regex')).toBeUndefined();
    }
    expect(opSupported('mssql', 'regex')).toBe(false);
    expect(opUnsupportedHint('mssql', 'regex')).toContain('NOT_SUPPORTED');
  });

  it('包裹类与 empty 在全部方言可用', () => {
    for (const kind of ['sqlite', 'mysql', 'postgres', 'mssql', 'oracle'] as const) {
      for (const op of ['contains', 'starts_with', 'ends_with', 'empty'] as FilterOperator[]) {
        expect(opSupported(kind, op)).toBe(true);
      }
    }
  });

  it('已知不支持的 op 整条条件不下发（不触发后端报错）', () => {
    expect(condToWire(row({ op: 'regex', value: '^a' }), 'mssql')).toBeNull();
    expect(condToWire(row({ op: 'regex', value: '^a' }), 'sqlite')).toEqual(
      { column: 'name', op: 'regex', value: '^a' },
    );
  });
});

describe('condToWire', () => {
  it('列名为空 / op 未知 → null', () => {
    expect(condToWire(row({ column: '' }))).toBeNull();
    expect(condToWire(row({ op: 'nope' as FilterOperator }))).toBeNull();
  });

  it('numish：纯数字转 number，其余原样', () => {
    expect(numish('42')).toBe(42);
    expect(numish(' -3.5 ')).toBe(-3.5);
    expect(numish('0x1f')).toBe('0x1f');
    expect(numish('1e5')).toBe('1e5');
    expect(numish('')).toBe('');
  });

  it('needsValue 类：空串仍作为值下发（like/contains 的空模式合法）', () => {
    expect(condToWire(row({ op: 'contains', value: '' }))).toEqual(
      { column: 'name', op: 'contains', value: '' },
    );
  });

  it('between 用 value + second_value', () => {
    expect(condToWire(row({ op: 'between', value: '1', second: '10' }))).toEqual(
      { column: 'name', op: 'between', value: 1, second_value: 10 },
    );
  });

  it('in/not_in 逗号切分并跳过空项；全空 → null', () => {
    expect(condToWire(row({ column: 'id', op: 'in', values: '1, 2 ,x,' }))).toEqual(
      { column: 'id', op: 'in', values: [1, 2, 'x'] },
    );
    expect(condToWire(row({ column: 'id', op: 'in', values: ' , ' }))).toBeNull();
  });

  it('null/not_null/empty 不带值', () => {
    for (const op of ['null', 'not_null', 'empty'] as FilterOperator[]) {
      expect(condToWire(row({ op, value: 'ignored' }))).toEqual({ column: 'name', op });
    }
  });

  it('wireToCond：数组值以逗号串回显，缺省 caseMode 为 default', () => {
    const c: FilterCondition = { column: 'id', op: 'in', values: [1, 2], case_sensitive: false };
    expect(wireToCond(c)).toEqual(
      { column: 'id', op: 'in', value: '', second: '', values: '1,2', caseMode: 'insensitive' },
    );
  });
});
