// D1.5（阶段 C 协议补齐）过滤操作符元数据：18 op + case_sensitive 三态 + 方言能力矩阵。
// 契约真相源在 spec/behavior.md §13.3/§13.5/§13.6，本文件只做前端侧的映射与置灰判定。
// 纯函数、无 IO、无 React，供 TableDataView 与单测复用。
// 许可红线：仅参考 TablePro(AGPLv3) 的交互语义，实现全部自研，未移植其代码。
import type { DatabaseKind, FilterCondition, FilterOperator, Value } from '../api';

export interface FilterOpMeta {
  value: FilterOperator;
  label: string;
  needsValue: boolean;
  needsValues: boolean;
  needsSecond: boolean;
  /** 文本匹配类：唯一接受 case_sensitive 的 op 家族（behavior.md §13.5） */
  textMatch: boolean;
}

export const FILTER_OPS: FilterOpMeta[] = [
  { value: 'eq', label: '= 等于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'ne', label: '≠ 不等于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'lt', label: '< 小于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'le', label: '≤ 小于等于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'gt', label: '> 大于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'ge', label: '≥ 大于等于', needsValue: true, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'like', label: 'LIKE 模式', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'not_like', label: 'NOT LIKE 模式', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'contains', label: '包含子串', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'starts_with', label: '前缀为', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'ends_with', label: '后缀为', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'regex', label: '正则匹配', needsValue: true, needsValues: false, needsSecond: false, textMatch: true },
  { value: 'in', label: 'IN 列表', needsValue: false, needsValues: true, needsSecond: false, textMatch: false },
  { value: 'not_in', label: 'NOT IN 列表', needsValue: false, needsValues: true, needsSecond: false, textMatch: false },
  { value: 'between', label: 'BETWEEN 区间', needsValue: true, needsValues: false, needsSecond: true, textMatch: false },
  { value: 'null', label: 'IS NULL', needsValue: false, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'not_null', label: 'IS NOT NULL', needsValue: false, needsValues: false, needsSecond: false, textMatch: false },
  { value: 'empty', label: '为空（NULL 或空串）', needsValue: false, needsValues: false, needsSecond: false, textMatch: false },
];

export function opMeta(op: FilterOperator): FilterOpMeta | undefined {
  return FILTER_OPS.find((o) => o.value === op);
}

/** case_sensitive 三态：缺席=方言默认（旧客户端零影响），true/false=强制。 */
export type CaseMode = 'default' | 'sensitive' | 'insensitive';

export const CASE_MODES: { value: CaseMode; label: string }[] = [
  { value: 'default', label: '默认' },
  { value: 'sensitive', label: '区分大小写' },
  { value: 'insensitive', label: '忽略大小写' },
];

export function caseModeToWire(mode: CaseMode): boolean | undefined {
  if (mode === 'sensitive') return true;
  if (mode === 'insensitive') return false;
  return undefined;
}

export function wireToCaseMode(v: boolean | undefined): CaseMode {
  if (v === true) return 'sensitive';
  if (v === false) return 'insensitive';
  return 'default';
}

/** 某方言是否支持该 op（behavior.md §13.6 能力矩阵：MSSQL 无原生正则）。 */
export function opSupported(kind: DatabaseKind, op: FilterOperator): boolean {
  if (op === 'regex' && kind === 'mssql') return false;
  return true;
}

/** 不支持时的置灰提示文案；可用则返回 undefined。 */
export function opUnsupportedHint(kind: DatabaseKind, op: FilterOperator): string | undefined {
  if (opSupported(kind, op)) return undefined;
  if (op === 'regex' && kind === 'mssql') return 'SQL Server 无原生正则，后端会返回 POLYDB_ERR_NOT_SUPPORTED';
  return '当前数据库不支持该操作符';
}

export interface CondRow {
  column: string;
  op: FilterOperator;
  value: string;
  second: string;
  values: string;
  caseMode: CaseMode;
}

export function emptyCondRow(op: FilterOperator = 'eq'): CondRow {
  return { column: '', op, value: '', second: '', values: '', caseMode: 'default' };
}

/** 数字外观的字符串转 number，其余原样传（与后端绑定参数的宽松类型一致）。 */
export function numish(s: string): Value {
  return /^-?\d+(\.\d+)?$/.test(s.trim()) ? Number(s) : s;
}

/**
 * UI 行 → 线格式条件。列名为空、op 未知、IN 列表为空、当前方言不支持该 op → null（跳过该条件）。
 * case_sensitive 仅对文本匹配类 op 下发，其余 op 后端本就忽略，前端不发以免污染日志/对拍。
 */
export function condToWire(r: CondRow, kind?: DatabaseKind): FilterCondition | null {
  if (!r.column) return null;
  const meta = opMeta(r.op);
  if (!meta) return null;
  if (kind && !opSupported(kind, r.op)) return null;
  const wire: FilterCondition = { column: r.column, op: r.op };
  if (meta.needsValue) wire.value = numish(r.value);
  if (meta.needsSecond) wire.second_value = numish(r.second);
  if (meta.needsValues) {
    wire.values = r.values.split(',').map((s) => numish(s.trim())).filter((v) => v !== '');
    if (wire.values.length === 0) return null;
  }
  if (meta.textMatch) {
    const cs = caseModeToWire(r.caseMode);
    if (cs !== undefined) wire.case_sensitive = cs;
  }
  return wire;
}

/** 线格式条件 → UI 行（预设/历史回填用）。数组值以逗号串回显。 */
export function wireToCond(c: FilterCondition): CondRow {
  return {
    column: c.column,
    op: c.op,
    value: c.value === undefined || c.value === null ? '' : String(c.value),
    second: c.second_value === undefined || c.second_value === null ? '' : String(c.second_value),
    values: Array.isArray(c.values) ? c.values.join(',') : '',
    caseMode: wireToCaseMode(c.case_sensitive),
  };
}
