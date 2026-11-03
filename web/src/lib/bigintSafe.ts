// 大整数（|v| > 2^53）在 UI 里的安全承载。
//
// spec/encoding.md §4 规定 JSON 通道的大整数以**字符串**承载以免精度丢失；
// TableProGo 的 jsonSafeInt/jsonSafeUint 是同一约定的既成实现（超出安全范围的整数
// 以精确十进制文本过线，网格按数值排序）。这里把该约定搬到 web 的 msgpack 解码边界。
//
// 为什么不是「开 useBigInt64 再让 UI 全面适配 BigInt」：useBigInt64 会把**每一个**
// int64 都变成 BigInt，而 Value DTO 是 number|string|... ——那需要先改 spec 扩契约
// （走 genprotocol 四门禁），再把约 20 处渲染/算术/排序站点逐个换成 BigInt 安全路径。
// 这里反过来：解码时开 useBigInt64 取精确值，**立刻**归一化成 number|string，BigInt
// 不外泄。安全范围内的整数仍收敛为 number（UI 现有算术/渲染/排序一行不改）；超出范
// 围的收敛为精确十进制字符串（渲染天然正确，排序与编辑按字符串精确处理）。
//
// 归一化是 O(n) 的原地遍历，与 msgpack 解码本身同阶；结果集里绝大多数 cell 是字符串
// /小整数，逐节点的 typeof 判断开销可以忽略。

import { decode } from '@msgpack/msgpack';

/** IEEE754 能精确表示的整数上界（2^53 - 1）。 */
export const MAX_SAFE_INT = Number.MAX_SAFE_INTEGER; // 9007199254740991

/** 精确整数字符串：可选负号 + 十进制数字（不接受前导 +、空白、小数点、科学计数）。 */
const EXACT_INT_RE = /^-?\d+$/;

/**
 * 判断字符串是否为「超出安全范围的精确整数」。
 *
 * 归一化之后这是大整数的**唯一**形态：安全范围内的整数已经收敛成 number，所以一个
 * 看起来像整数的 string 只有在超出安全范围时才可能是大整数。字面量生成（PK WHERE、
 * INSERT/UPDATE VALUES）据此发裸数字而不是字符串字面量。
 */
export function isBigIntString(s: string): boolean {
  if (!EXACT_INT_RE.test(s)) return false;
  // 必须用 BigInt 比较：Number(s) 会先把超出范围的精度丢掉，正是本模块要防的事。
  const n = BigInt(s);
  return n > MAX_SAFE_INT || n < -MAX_SAFE_INT;
}

/** 单个 BigInt → UI 承载形态：安全范围内 → number，超出 → 精确十进制字符串。 */
export function bigIntToCellValue(v: bigint): number | string {
  if (v > MAX_SAFE_INT || v < -MAX_SAFE_INT) return v.toString();
  return Number(v);
}

/**
 * 递归归一化解码树里的 BigInt：数组与对象都原地改写，避免整树拷贝。
 * 非 BigInt 的叶子原样返回，容器返回同一引用（调用方据此跳过无谓赋值）。
 */
export function normalizeBigInts(value: unknown): unknown {
  if (typeof value === 'bigint') return bigIntToCellValue(value);
  if (Array.isArray(value)) {
    for (let i = 0; i < value.length; i++) {
      const next = normalizeBigInts(value[i]);
      if (next !== value[i]) value[i] = next;
    }
    return value;
  }
  if (value !== null && typeof value === 'object') {
    const rec = value as Record<string, unknown>;
    for (const key of Object.keys(rec)) {
      const next = normalizeBigInts(rec[key]);
      if (next !== rec[key]) rec[key] = next;
    }
    return value;
  }
  return value;
}

/**
 * msgpack 解码 + BigInt 归一化（数据面与控制面共用）。
 *
 * useBigInt64: true 让 int64/uint64 解成精确 BigInt——不开它的话解码器内部就把它
 * 转成 number，精度在解码器里就已经丢了，事后无法挽回。紧接着归一化成 number|string，
 * BigInt 不外泄到 UI。
 */
export function decodeMsgpack(raw: ArrayBuffer | Uint8Array): unknown {
  return normalizeBigInts(decode(raw, { useBigInt64: true }));
}

/**
 * 把 cell 解析为精确 BigInt；不是精确整数则返回 null。
 * 用于排序比较——BigInt 比较不会像 Number() 那样把 >2^53 的值压成同一个 double。
 */
export function toBigInt(v: unknown): bigint | null {
  if (typeof v === 'bigint') return v;
  if (typeof v === 'number') return Number.isSafeInteger(v) ? BigInt(v) : null;
  if (typeof v === 'string') {
    const t = v.trim();
    return EXACT_INT_RE.test(t) ? BigInt(t) : null;
  }
  return null;
}

/**
 * 数值列的两个 cell 比较：都是精确整数时用 BigInt 精确比（覆盖 >2^53 的归一化字符串），
 * 否则用 Number 比；至少一个不可比较时返回 null，交回调用方走字典序。
 */
export function compareNumeric(x: unknown, y: unknown): number | null {
  // null/undefined 交给调用方的空值分支处理（Number(null) === 0 会把空值当成 0）。
  if (x === null || x === undefined || y === null || y === undefined) return null;
  const bx = toBigInt(x);
  const by = toBigInt(y);
  if (bx !== null && by !== null) return bx < by ? -1 : bx > by ? 1 : 0;
  const nx = Number(x);
  const ny = Number(y);
  if (Number.isFinite(nx) && Number.isFinite(ny)) return nx < ny ? -1 : nx > ny ? 1 : 0;
  return null;
}
