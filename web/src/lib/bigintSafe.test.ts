import { describe, expect, it } from 'vitest';
import { decode, encode } from '@msgpack/msgpack';
import {
  bigIntToCellValue,
  compareNumeric,
  decodeMsgpack,
  isBigIntString,
  MAX_SAFE_INT,
  normalizeBigInts,
  toBigInt,
} from './bigintSafe';

describe('MAX_SAFE_INT', () => {
  it('是 IEEE754 精确整数上界（2^53 - 1）', () => {
    expect(MAX_SAFE_INT).toBe(9007199254740991);
  });
});

describe('bigIntToCellValue', () => {
  it('安全范围内收敛为 number，超出收敛为精确十进制字符串', () => {
    expect(bigIntToCellValue(0n)).toBe(0);
    expect(bigIntToCellValue(42n)).toBe(42);
    expect(bigIntToCellValue(-42n)).toBe(-42);
    expect(bigIntToCellValue(9007199254740991n)).toBe(9007199254740991);
    expect(bigIntToCellValue(-9007199254740991n)).toBe(-9007199254740991);
    expect(bigIntToCellValue(9007199254740992n)).toBe('9007199254740992');
    expect(bigIntToCellValue(9007199254740993n)).toBe('9007199254740993');
    expect(bigIntToCellValue(-9007199254740995n)).toBe('-9007199254740995');
  });
});

describe('isBigIntString', () => {
  it('只认超出安全范围的精确整数字符串', () => {
    expect(isBigIntString('9007199254740993')).toBe(true);
    expect(isBigIntString('-9007199254740995')).toBe(true);
    expect(isBigIntString('99999999999999999999999')).toBe(true);
    // 2^53：超出安全整数上界（2^53 - 1），必须走字符串分支
    expect(isBigIntString('9007199254740992')).toBe(true);
    expect(isBigIntString('42')).toBe(false);
    expect(isBigIntString('-42')).toBe(false);
    expect(isBigIntString('9007199254740991')).toBe(false);
    expect(isBigIntString('1.5')).toBe(false);
    expect(isBigIntString('1e5')).toBe(false);
    expect(isBigIntString('')).toBe(false);
    expect(isBigIntString(' 42')).toBe(false);
    expect(isBigIntString('+5')).toBe(false);
    expect(isBigIntString('007')).toBe(false);
    expect(isBigIntString('abc')).toBe(false);
  });
});

describe('normalizeBigInts', () => {
  it('无 BigInt 的树原样返回同一引用', () => {
    const obj = { a: 1, b: 'x', c: [1, 2], d: null };
    expect(normalizeBigInts(obj)).toBe(obj);
    expect(normalizeBigInts(42)).toBe(42);
    expect(normalizeBigInts('x')).toBe('x');
    expect(normalizeBigInts(null)).toBeNull();
  });

  it('递归归一化嵌套数组与对象里的 BigInt', () => {
    const out = normalizeBigInts({
      id: 9007199254740993n,
      small: 42n,
      rows: [{ big: -9007199254740995n }, [1n, 2n]],
    }) as Record<string, unknown>;
    expect(out.id).toBe('9007199254740993');
    expect(out.small).toBe(42);
    const rows = out.rows as unknown[];
    expect(rows[0]).toEqual({ big: '-9007199254740995' });
    expect(rows[1]).toEqual([1, 2]);
  });

  it('归一化后树里不留任何 BigInt', () => {
    const seen: unknown[] = [];
    const walk = (v: unknown): void => {
      if (typeof v === 'bigint') throw new Error('仍有 BigInt 外泄到 UI');
      if (Array.isArray(v)) {
        v.forEach(walk);
        return;
      }
      if (v !== null && typeof v === 'object') {
        Object.values(v as Record<string, unknown>).forEach(walk);
        return;
      }
      seen.push(v);
    };
    walk(normalizeBigInts({ a: 9007199254740993n, b: [1n, { c: -1n }] }));
    expect(seen).toEqual(['9007199254740993', 1, -1]);
  });
});

describe('decodeMsgpack', () => {
  it('大整数过线后仍是精确数字，安全范围内仍是 number', () => {
    const payload = encode(
      { id: 9007199254740993n, small: 42n, name: 'x', nested: { big: -9007199254740995n } },
      { useBigInt64: true },
    );
    const out = decodeMsgpack(payload) as Record<string, unknown>;
    expect(out.id).toBe('9007199254740993');
    expect(out.small).toBe(42);
    expect(out.name).toBe('x');
    expect((out.nested as Record<string, unknown>).big).toBe('-9007199254740995');
  });

  it('回归证明：不开 useBigInt64 会把 2^53+1 压成 2^53', () => {
    const payload = encode(9007199254740993n, { useBigInt64: true });
    // 旧行为（decode 默认 useBigInt64:false）：解码器内部就转成 number，精度已丢。
    expect(decode(payload)).toBe(9007199254740992);
    // 新行为：精确 BigInt → 精确十进制字符串。
    expect(decodeMsgpack(payload)).toBe('9007199254740993');
  });
});

describe('toBigInt / compareNumeric', () => {
  it('toBigInt 只接受精确整数', () => {
    expect(toBigInt(5)).toBe(5n);
    expect(toBigInt(-5)).toBe(-5n);
    expect(toBigInt(1.5)).toBeNull();
    expect(toBigInt('42')).toBe(42n);
    expect(toBigInt('-42')).toBe(-42n);
    expect(toBigInt('9007199254740993')).toBe(9007199254740993n);
    expect(toBigInt('42.5')).toBeNull();
    expect(toBigInt('')).toBeNull();
    expect(toBigInt(null)).toBeNull();
    expect(toBigInt(undefined)).toBeNull();
    expect(toBigInt(true)).toBeNull();
  });

  it('compareNumeric 对 >2^53 的大整数字符串精确排序', () => {
    expect(compareNumeric('9007199254740993', '9007199254740992')).toBe(1);
    expect(compareNumeric('9007199254740992', '9007199254740993')).toBe(-1);
    expect(compareNumeric('9007199254740993', '9007199254740993')).toBe(0);
    expect(compareNumeric('-9007199254740995', '9007199254740993')).toBe(-1);
    // 混合 number 与大整数字符串
    expect(compareNumeric(1, '9007199254740993')).toBe(-1);
    expect(compareNumeric('9007199254740993', 1)).toBe(1);
  });

  it('compareNumeric 小整数与浮点仍按数值比，非数值/空值返回 null', () => {
    expect(compareNumeric(2, 10)).toBe(-1);
    expect(compareNumeric(10, 2)).toBe(1);
    expect(compareNumeric(1.5, 2.5)).toBe(-1);
    expect(compareNumeric('abc', 'def')).toBeNull();
    expect(compareNumeric(1, null)).toBeNull();
    expect(compareNumeric(null, 1)).toBeNull();
  });
});
