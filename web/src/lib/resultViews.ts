// W1（阶段 C）结果区视图模式的纯逻辑：Inspector 行详情字段构建与 Output(消息) 行生成。
// 纯函数、无 IO、无 React，供 QueryWorkspace 与单测复用。
// 许可红线：仅参考 TablePro(AGPLv3) 的交互语义，实现全部自研，未移植其代码。
import type { QueryResult, ResultColumn, StatementType } from '../api';

export type ViewMode = 'table' | 'json' | 'chart' | 'output';

/** Inspector 单元格值的显示上限，超出裁断并提示走完整值面板。 */
export const INSPECTOR_VALUE_MAX = 400;

export type CellClass = 'null' | 'num' | 'bool' | 'text' | 'blob';

/** 单元格值的语义分类（网格与 Inspector 共用一套颜色语义）。 */
export function cellClass(v: unknown, col?: ResultColumn): CellClass {
  if (v === null || v === undefined) return 'null';
  const t = col?.type?.toLowerCase() ?? '';
  if (typeof v === 'number') return 'num';
  if (typeof v === 'boolean') return 'bool';
  if (t.includes('int') || t.includes('smallint') || t.includes('bigint') || t.includes('decimal') || t.includes('numeric') || t.includes('float') || t.includes('double') || t.includes('real')) {
    const s = String(v);
    if (/^-?\d+(\.\d+)?$/.test(s)) return 'num';
  }
  return 'text';
}

/** 单元格值转显示文本：NULL 大写、数组递归、对象走 JSON。 */
export function fmt(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'object') {
    if (Array.isArray(v)) return `[${v.map(fmt).join(', ')}]`;
    return JSON.stringify(v);
  }
  return String(v);
}

export interface InspectorField {
  /** 原始列下标（编辑与完整值面板按它定位，不受隐藏列影响） */
  col: number;
  name: string;
  type: string;
  display: string;
  isNull: boolean;
  truncated: boolean;
}

/**
 * 把一行拆成 Inspector 的逐字段条目。
 * hidden 为隐藏列下标集合：与网格保持一致，隐藏列不进尾板。
 */
export function buildInspectorFields(
  columns: ResultColumn[],
  row: unknown[] | undefined,
  hidden?: Set<number>,
  maxLen = INSPECTOR_VALUE_MAX,
): InspectorField[] {
  if (!row) return [];
  const out: InspectorField[] = [];
  for (let col = 0; col < columns.length; col++) {
    if (hidden?.has(col)) continue;
    const v = row[col];
    const isNull = v === null || v === undefined;
    const display = fmt(v);
    out.push({
      col,
      name: columns[col].name,
      type: columns[col].type,
      display,
      isNull,
      truncated: !isNull && display.length > maxLen,
    });
  }
  return out;
}

export type MessageStatus = 'ok' | 'err' | 'cancelled' | 'skipped' | 'running' | 'pending';

export interface StatementMessage {
  status: MessageStatus;
  /** 形如 “SELECT · 第 2/3 条”，单语句时省略序号 */
  title: string;
  lines: string[];
  sql: string;
  /** 多语句时用于高亮当前查看的那一条 */
  active: boolean;
}

export interface StatementInput {
  sql: string;
  result: QueryResult | null;
  error: string | null;
  elapsed: number | null;
  status: MessageStatus;
}

const fmtMs = (ms: number): string => (ms < 1 ? ms.toFixed(3) : ms.toFixed(2));

function resultLines(r: QueryResult, elapsed: number | null): string[] {
  const lines: string[] = [];
  const type: StatementType | string = r.statement_type ?? 'other';
  if (r.columns.length > 0) {
    lines.push(`✓ ${type} 返回 ${r.rows.length} 行 · ${r.columns.length} 列`);
  } else {
    lines.push(`✓ ${type} 执行成功，无结果集`);
  }
  if (type !== 'select' && r.affected_rows > 0) lines.push(`影响 ${r.affected_rows} 行`);
  lines.push(`服务端 ${fmtMs(r.execution_time_ms)} ms${elapsed != null ? ` · 往返 ${fmtMs(elapsed)} ms` : ''}`);
  if (r.truncated) lines.push('⚠ 服务端已截断结果（超过 max_rows）');
  if (r.has_more && r.total_rows != null) lines.push(`ℹ 仍有更多行：已取 ${r.rows.length} / 共 ${r.total_rows}`);
  return lines;
}

function statusLines(s: StatementInput): string[] {
  switch (s.status) {
    case 'err':
      return [`✕ ${(s.error ?? '执行失败').trim()}`];
    case 'cancelled':
      return ['⊘ 已取消（当前语句执行完毕后中断）'];
    case 'skipped':
      return ['· 未执行（前面的执行被取消）'];
    case 'running':
      return ['… 执行中'];
    case 'pending':
      return ['· 排队待执行'];
    case 'ok':
      if (s.result) return resultLines(s.result, s.elapsed);
      if (s.error) return [`✕ ${s.error.trim()}`];
      return ['✓ 执行成功'];
  }
  return [];
}

/**
 * 生成 Output(消息) 页签的逐语句条目。
 * multi 非空表示一次多语句执行（逐条列出）；否则按 single 生成一条。
 */
export function outputMessages(input: {
  multi: StatementInput[] | null;
  activeIdx: number;
  single: { sql: string | null; result: QueryResult | null; error: string | null; elapsed: number | null; status: MessageStatus } | null;
}): StatementMessage[] {
  const { multi, activeIdx, single } = input;
  if (multi && multi.length > 0) {
    return multi.map((s, i) => ({
      status: s.status,
      title: `${(s.sql.trimStart().split(/\s+/)[0] ?? '?').toUpperCase()} · 第 ${i + 1}/${multi.length} 条`,
      lines: statusLines(s),
      sql: s.sql,
      active: i === activeIdx,
    }));
  }
  if (!single) return [];
  const s = single;
  const kw = (s.sql ?? '').trimStart().split(/\s+/)[0] ?? '';
  const type = s.result?.statement_type;
  return [{
    status: s.status,
    title: kw ? `${kw.toUpperCase()}${type && type !== kw.toLowerCase() ? ` · ${type}` : ''}` : (type ?? '语句'),
    lines: statusLines({ sql: s.sql ?? '', result: s.result, error: s.error, elapsed: s.elapsed, status: s.status }),
    sql: s.sql ?? '',
    active: true,
  }];
}
