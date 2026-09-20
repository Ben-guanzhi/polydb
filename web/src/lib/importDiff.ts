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
