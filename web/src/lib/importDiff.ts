// 撤销/diff 面板纯工具（M30.x，原 ImportModal 模块级，上移至 lib 供 ImportModal/Step4Preview 复用）
// 每组分色：按语义分配到色卡（未识别分组 fallback 到 muted）
export const DIFF_GROUP_COLOR: Record<string, string> = {
  '转换':     '#f59e0b',
  '空值策略': '#8b5cf6',
  '校验':     '#ef4444',
  '模式':     '#3b82f6',
  '严格校验': '#f97316',
  '空串视为 null': '#06b6d4',
  '批大小':   '#10b981',
  '行过滤':   '#ec4899',
};

// diff 项语义分类：识别 buildUndoDiff 输出里的"新增/删除/变化"三态
// added = before 是空标记（∅ / 空 / null（默认））→ 撤销后新增
// removed = after 是空标记 → 撤销后删除
// changed = 其他（值替换）
export type DiffItemKind = 'added' | 'removed' | 'changed';
export const DIFF_EMPTY_MARKS = new Set(['∅', '空', 'null（默认）']);
export const FIX_STACK_MAX_BYTES = 512 * 1024;
export const classifyDiffItem = (it: { before: string; after: string }): DiffItemKind => {
  const isBeforeEmpty = it.before === '' || DIFF_EMPTY_MARKS.has(it.before);
  const isAfterEmpty = it.after === '' || DIFF_EMPTY_MARKS.has(it.after);
  if (isBeforeEmpty && !isAfterEmpty) return 'added';
  if (!isBeforeEmpty && isAfterEmpty) return 'removed';
  return 'changed';
};
export const diffItemColor = (kind: DiffItemKind) =>
  kind === 'added' ? '#10b981' : kind === 'removed' ? '#dc2626' : '#f59e0b';
export const diffItemIcon = (kind: DiffItemKind) => (kind === 'added' ? '＋' : kind === 'removed' ? '－' : '↔');
export const diffItemLabel = (kind: DiffItemKind) =>
  kind === 'added' ? '新增' : kind === 'removed' ? '删除' : '变化';

// diff 面板搜索匹配的富文本分段：hit=true 的段是搜索关键字命中，需高亮渲染
// kwIdx（M30.35）：多关键字模式下该段命中的关键字索引，供分色高亮
export type DiffSearchPart = { text: string; hit: boolean; kwIdx?: number };
// 关键字分色（M30.35）：HSL 色相按 kwIdx 均匀分布，避开 danger/accent 常见色（红/蓝）
// 返回 {bg, fg} 供 <mark> 使用
export const diffKeywordColor = (kwIdx: number): { bg: string; fg: string } => {
  const hues = [28, 160, 300, 55, 200, 340, 100, 250];
  const h = hues[kwIdx % hues.length];
  return {
    bg: `hsla(${h}, 75%, 55%, 0.32)`,
    fg: `hsl(${h}, 90%, 78%)`,
  };
};

// 搜索字符串切分为多个关键字（M30.32 多关键字 OR 匹配）
// 空白和 `|` 都是分隔符；空 token 过滤；不做 lowercase 由调用方处理
export const splitDiffSearchKeywords = (qRaw: string): string[] => {
  if (!qRaw) return [];
  return qRaw.split(/\s+|\|/).map((s) => s).filter((s) => s.length > 0);
};

export const splitDiffSearchParts = (
  text: string,
  query: string,
  caseSensitive: boolean,
): DiffSearchPart[] => {
  const keywords = splitDiffSearchKeywords(query);
  if (keywords.length === 0) return [{ text, hit: false }];
  const s = caseSensitive ? text : text.toLowerCase();
  const kws = keywords.map((k) => caseSensitive ? k : k.toLowerCase());
  // 收集所有命中区间 [start, end)，同时记录来源关键字索引（M30.35 分色）
  const intervals: Array<[number, number, number]> = [];
  for (let ki = 0; ki < kws.length; ki++) {
    const kw = kws[ki];
    if (!kw) continue;
    let i = 0;
    while (i <= s.length - kw.length) {
      const idx = s.indexOf(kw, i);
      if (idx < 0) break;
      intervals.push([idx, idx + kw.length, ki]);
      i = idx + 1; // 允许重叠匹配下一个关键字
    }
  }
  if (intervals.length === 0) return [{ text, hit: false }];
  // 按起点排序，起点相同保留最长（其 kwIdx 胜出）
  intervals.sort((a, b) => a[0] - b[0] || (b[1] - b[0]) - (a[1] - a[0]));
  const merged: Array<[number, number, number]> = [];
  for (const iv of intervals) {
    const last = merged[merged.length - 1];
    if (last && iv[0] < last[1]) {
      if (iv[1] > last[1]) last[1] = iv[1];
    } else {
      merged.push([iv[0], iv[1], iv[2]]);
    }
  }
  const parts: DiffSearchPart[] = [];
  let last = 0;
  for (const [a, b, ki] of merged) {
    if (a > last) parts.push({ text: text.slice(last, a), hit: false });
    parts.push({ text: text.slice(a, b), hit: true, kwIdx: ki });
    last = b;
  }
  if (last < text.length) parts.push({ text: text.slice(last), hit: false });
  return parts;
};

// 分组颜色映射（原 ImportModal 组件体内 diffGroupColor，随 DIFF_GROUP_COLOR 上移）
export const diffGroupColor = (g: string): string => DIFF_GROUP_COLOR[g] ?? '#6b7280';

// 撤销/diff 面板纯工具（续）：undo diff 构建/分组/过滤/复制（原 ImportModal 组件体内纯函数，上移至 lib）
import { boolLabel } from './importDisplay';
import { type ImportOptions, type ColumnValidation } from './importData';
import { publishEditorStatus } from './statusBus';

export type DiffItem = { field: string; before: string; after: string };

export function recDiffItems(
    label: string,
    aMap: Record<number, unknown>,
    bMap: Record<number, unknown>,
    fmt: (v: unknown) => string,
    colLabelFn: (idx: number) => string,
  ): DiffItem[] {
    const out: DiffItem[] = [];
    const keys = new Set<number>([...Object.keys(aMap).map(Number), ...Object.keys(bMap).map(Number)]);
    for (const k of Array.from(keys).sort((x, y) => x - y)) {
      const av = aMap[k] === undefined ? null : aMap[k];
      const bv = bMap[k] === undefined ? null : bMap[k];
      if (JSON.stringify(av) !== JSON.stringify(bv)) out.push({ field: `${label} ${colLabelFn(k)}`, before: fmt(av), after: fmt(bv) });
    }
    return out;
  };

export function buildUndoDiff(a: ImportOptions, b: ImportOptions, colLabelFn: (idx: number) => string): DiffItem[] {
    const items: DiffItem[] = [];
    if (a.mode !== b.mode) items.push({ field: '模式 mode', before: a.mode, after: b.mode });
    if (a.strictValidation !== b.strictValidation) items.push({ field: '严格校验', before: boolLabel(a.strictValidation), after: boolLabel(b.strictValidation) });
    if (a.emptyAsNull !== b.emptyAsNull) items.push({ field: '空串视为 null', before: boolLabel(a.emptyAsNull), after: boolLabel(b.emptyAsNull) });
    if (a.batchSize !== b.batchSize) items.push({ field: '批大小', before: String(a.batchSize), after: String(b.batchSize) });
    items.push(...recDiffItems('转换 transform', a.transforms ?? {}, b.transforms ?? {}, (v) => (v === null ? '∅' : String(v)), colLabelFn));
    items.push(...recDiffItems('空值策略 nullPolicy', a.nullPolicies ?? {}, b.nullPolicies ?? {}, (v) => (v === null ? 'null（默认）' : String(v)), colLabelFn));
    items.push(...recDiffItems('校验 validation', a.validations ?? {}, b.validations ?? {}, (v) => {
      if (v === null) return '∅';
      const vv = v as ColumnValidation;
      const parts: string[] = [];
      if (vv.required) parts.push('必填');
      if (vv.regex) parts.push(`regex:${vv.regex.slice(0, 24)}`);
      if (vv.minValue) parts.push(`≥${vv.minValue}`);
      if (vv.maxValue) parts.push(`≤${vv.maxValue}`);
      if (vv.enum) parts.push(`enum:[${vv.enum.slice(0, 40)}]`);
      return parts.length > 0 ? parts.join(' · ') : '空';
    }, colLabelFn));
    if (JSON.stringify(a.filterColumn) !== JSON.stringify(b.filterColumn) ||
        JSON.stringify(a.filterOp) !== JSON.stringify(b.filterOp) ||
        JSON.stringify(a.filterValue) !== JSON.stringify(b.filterValue)) {
      const fa = a.filterColumn === null ? '∅' : `列#${a.filterColumn} ${a.filterOp} "${a.filterValue}"`;
      const fb = b.filterColumn === null ? '∅' : `列#${b.filterColumn} ${b.filterOp} "${b.filterValue}"`;
      items.push({ field: '行过滤', before: fa, after: fb });
    }
    return items;
}

  // 分组策略：field 首 token 作为 groupKey
  // 「转换 transform ...」→「转换」；「空值策略 nullPolicy ...」→「空值策略」；「校验 validation ...」→「校验」
  // 其他 field 直接以自身为 groupKey（模式/严格校验/空串视为 null/批大小/行过滤）

export const classifyDiffGroup = (field: string): string => {
    const spaceIdx = field.indexOf(' ');
    const head = spaceIdx > 0 ? field.slice(0, spaceIdx) : field;
    return head;
};

export const densityAlpha = (n: number): number => {
    if (n <= 0) return 0.30;
    if (n >= 4) return 0.95;
    return 0.30 + (n - 1) * 0.22;
};

export const groupDiffItems = (items: DiffItem[]):
  { group: string; items: DiffItem[] }[] => {
    const map = new Map<string, DiffItem[]>();
    const order: string[] = [];
    for (const it of items) {
      const g = classifyDiffGroup(it.field);
      if (!map.has(g)) { map.set(g, []); order.push(g); }
      map.get(g)!.push(it);
    }
    return order.map((g) => ({ group: g, items: map.get(g)! }));
};

export interface DiffFilterOptions { search: string; caseSensitive: boolean; scope: 'all' | 'field' | 'before' | 'after'; }
export function filterDiffGroups(
  groups: { group: string; items: DiffItem[] }[],
  opts: DiffFilterOptions,
): { group: string; items: DiffItem[] }[] {
  const { search: diffSearch, caseSensitive: diffSearchCaseSensitive, scope: diffSearchScope } = opts;
    const qRaw = diffSearch.trim();
    if (!qRaw) return groups;
    const kws = splitDiffSearchKeywords(qRaw).map((k) => diffSearchCaseSensitive ? k : k.toLowerCase());
    if (kws.length === 0) return groups;
    const norm = (s: string) => diffSearchCaseSensitive ? s : s.toLowerCase();
    const matchItem = (it: { field: string; before: string; after: string }): boolean => {
      for (const k of kws) {
        if (diffSearchScope === 'field' && norm(it.field).includes(k)) return true;
        if (diffSearchScope === 'before' && norm(it.before).includes(k)) return true;
        if (diffSearchScope === 'after' && norm(it.after).includes(k)) return true;
        if (diffSearchScope === 'all' && (norm(it.field).includes(k) || norm(it.before).includes(k) || norm(it.after).includes(k))) return true;
      }
      return false;
    };
    const out: { group: string; items: DiffItem[] }[] = [];
    for (const g of groups) {
      const matchedItems = g.items.filter(matchItem);
      if (matchedItems.length > 0) out.push({ group: g.group, items: matchedItems });
    }
    return out;
}

export const copyDiffSummary = async (items: DiffItem[], title?: string) => {
    const lines = [
      `# polydb 撤销预览 diff${title ? ' — ' + title : ''}`,
      `> 方向：当前（✗） → 撤销后（✓）`,
      `> 共 ${items.length} 项变化`,
      ``,
      ...items.map((it) => `- **${it.field}**: ${it.before} → ${it.after}`),
    ];
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      publishEditorStatus({ message: `📋 已复制撤销 diff${title ? '（' + title + '）' : ''}（${items.length} 项变化，${text.length} 字符）`, messageAt: Date.now() });
    } catch {
      publishEditorStatus({ message: `撤销 diff 已生成（${text.length} 字符），但 clipboard 权限拒绝：请手动复制`, messageAt: Date.now() });
    }
};

export const copyAllItems = async (items: DiffItem[]) => {
    const groups = groupDiffItems(items);
    const lines: string[] = [
      '# polydb 撤销预览 diff（按分组）',
      `> 方向：当前（✗） → 撤销后（✓）`,
      `> 共 ${items.length} 项变化 / ${groups.length} 个分组`,
      ``,
    ];
    for (const g of groups) {
      lines.push(`## ${g.group} (${g.items.length})`);
      lines.push('');
      for (const it of g.items) lines.push(`- ${it.field}: ${it.before} → ${it.after}`);
      lines.push('');
    }
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      publishEditorStatus({ message: `📋 已按分组复制 diff（${items.length} 项 / ${groups.length} 组，${text.length} 字符）`, messageAt: Date.now() });
    } catch {
      publishEditorStatus({ message: `分组 diff 已生成（${text.length} 字符），但 clipboard 权限拒绝：请手动复制`, messageAt: Date.now() });
    }
};
