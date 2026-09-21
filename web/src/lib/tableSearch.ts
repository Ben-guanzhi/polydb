// U3 Quick Switcher 检索纯函数（从 CommandPalette 的模糊打分抽出并增强）。

export interface SearchableTable {
  schema: string;
  table: string;
  rowCount?: number | null;
}

/** 子序列模糊打分：命中返回 >=0（越大越优），不命中返回 -1。空 query 恒 1。 */
export function fuzzyScore(query: string, target: string): number {
  if (!query) return 1;
  const q = query.toLowerCase();
  const t = target.toLowerCase();
  let qi = 0;
  let score = 0;
  let streak = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) {
      qi++;
      streak++;
      score += 1 + streak * 2;
      if (ti === 0 || ' -_./'.includes(t[ti - 1])) score += 6;
    } else {
      streak = 0;
    }
  }
  return qi === q.length ? score : -1;
}

/** 表检索：目标取 `schema.table` 与裸 `table` 的较高分，短名惩罚低，按分降序截断。 */
export function rankTables(query: string, items: SearchableTable[], limit = 60): SearchableTable[] {
  if (!query.trim()) {
    return items.slice(0, limit);
  }
  const scored = items
    .map((t) => ({ t, raw: Math.max(fuzzyScore(query, `${t.schema}.${t.table}`), fuzzyScore(query, t.table)) }))
    .filter((x) => x.raw >= 0)
    .map((x) => ({ t: x.t, s: x.raw - (x.t.schema.length + x.t.table.length) * 0.1 }));
  scored.sort((a, b) => b.s - a.s || a.t.schema.localeCompare(b.t.schema) || a.t.table.localeCompare(b.t.table));
  return scored.slice(0, limit).map((x) => x.t);
}
