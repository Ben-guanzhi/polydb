// 风险白名单持久化（M30.154/M30.155/M30.156/M30.157）：localStorage polydb.riskWhitelist.v1
// 纯函数层：读/写/过期清理/导出/导入解析/合并。UI 回调留在 ImportModal，只调用这里。
export const WHITELIST_TTL_MS = 30 * 24 * 60 * 60 * 1000; // 30 天
export const RISK_WHITELIST_KEY = 'polydb.riskWhitelist.v1';

export type WhitelistEntry = { key: string; addedAt: number };
export type WhitelistPersisted = { v: 2; items: WhitelistEntry[] };

// 首读：兼容旧 v1（string[] 无时间戳 → 当前时间兜底）与 v2（{key, addedAt}）；过 TTL 静默剔除
export function loadRiskWhitelist(): Map<string, number> {
  try {
    const raw = localStorage.getItem(RISK_WHITELIST_KEY);
    if (!raw) return new Map();
    const parsed = JSON.parse(raw);
    const now = Date.now();
    const out = new Map<string, number>();
    if (Array.isArray(parsed)) {
      // 旧 v1：仅 key 字符串数组，无时间戳——用当前时间兜底（不主动清，用户手动清）
      for (const k of parsed) if (typeof k === 'string') out.set(k, now);
    } else if (parsed && typeof parsed === 'object' && Array.isArray(parsed.items)) {
      for (const e of parsed.items) {
        if (e && typeof e === 'object' && typeof e.key === 'string' && typeof e.addedAt === 'number') {
          if (now - e.addedAt <= WHITELIST_TTL_MS) out.set(e.key, e.addedAt);
        }
      }
    }
    return out;
  } catch { return new Map(); }
}

// 写 v2 payload（失败静默——白名单是辅助功能，不阻塞主流程）
export function persistRiskWhitelist(map: Map<string, number>): void {
  try {
    const payload: WhitelistPersisted = { v: 2, items: Array.from(map, ([k, addedAt]) => ({ key: k, addedAt })) };
    localStorage.setItem(RISK_WHITELIST_KEY, JSON.stringify(payload));
  } catch { /* ignore */ }
}

// 过期清理：返回剔除后的新 map 与被剔除数量（TTL 到期静默剔除）
export function removeExpiredWhitelist(map: Map<string, number>): { next: Map<string, number>; removed: number } {
  const now = Date.now();
  let removed = 0;
  const next = new Map<string, number>();
  for (const [k, addedAt] of map) {
    if (now - addedAt > WHITELIST_TTL_MS) removed += 1;
    else next.set(k, addedAt);
  }
  return { next, removed };
}

// 白名单 key 展示名：schema::table::[kind] → schema.table（短展示）
export function whitelistKeyLabel(key: string): string {
  const p = key.split('::');
  return p.length >= 3 ? `${p[1]}.${p[2]}` : key;
}

// 导出：JSON / CSV（RFC 4180 引号转义），文件名带本地时间戳
export function buildRiskWhitelistExport(map: Map<string, number>, format: 'json' | 'csv'): { text: string; filename: string; mime: string } {
  const items = Array.from(map, ([key, addedAt]) => ({ key, addedAt }))
    .sort((a, b) => a.key.localeCompare(b.key));
  const now = new Date();
  const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
  if (format === 'csv') {
    // CSV 格式：手动引号转义防中文乱码，表头 key,addedAt
    const esc = (s: string) => {
      if (/["\n\r,]/.test(s)) {
        return '"' + s.replace(/"/g, '""') + '"';
      }
      return s;
    };
    const rows = items.map((it) => `${esc(it.key)},${it.addedAt}`);
    return { text: 'key,addedAt\n' + rows.join('\n'), filename: `polydb-risk-whitelist-${ts}.csv`, mime: 'text/csv;charset=utf-8' };
  }
  return {
    text: JSON.stringify({ version: 2, exportedAt: new Date().toISOString(), items }, null, 2),
    filename: `polydb-risk-whitelist-${ts}.json`,
    mime: 'application/json',
  };
}

// JSON 导入解析：兼容 v1 (string[]) 与 v2 ({key, addedAt}[]) 两种 item 形态
export function parseRiskWhitelistJson(text: string): { ok: true; items: WhitelistEntry[] } | { ok: false; error: string } {
  let parsed: { version?: number; exportedAt?: string; items?: unknown };
  try {
    parsed = JSON.parse(text);
  } catch (e) {
    return { ok: false, error: `❌ JSON 解析失败：${(e as Error).message}` };
  }
  if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.items)) {
    return { ok: false, error: '❌ 文件结构无效：缺少 items 数组' };
  }
  const now = Date.now();
  const incoming: WhitelistEntry[] = [];
  for (const x of parsed.items) {
    if (typeof x === 'string') {
      incoming.push({ key: x, addedAt: now });
    } else if (x && typeof x === 'object' && typeof (x as { key?: unknown }).key === 'string') {
      const k = (x as { key: string; addedAt?: unknown }).key;
      const a = (x as { addedAt?: unknown }).addedAt;
      incoming.push({ key: k, addedAt: typeof a === 'number' ? a : now });
    }
  }
  return { ok: true, items: incoming };
}

// CSV 导入解析（RFC 4180 简易解析：引号包裹 + 双引号转义；表头 key,addedAt，无表头时首行即数据）
export function parseRiskWhitelistCsv(text: string): { ok: true; items: WhitelistEntry[] } | { ok: false; error: string } {
  const lines = text.split(/\r?\n/);
  if (lines.length === 0) return { ok: false, error: '⚠ CSV 文件为空' };
  const parseLine = (line: string): string[] => {
    const out: string[] = [];
    let cur = '';
    let inQuote = false;
    for (let i = 0; i < line.length; i++) {
      const ch = line[i];
      if (inQuote) {
        if (ch === '"') {
          if (i + 1 < line.length && line[i + 1] === '"') { cur += '"'; i++; }
          else inQuote = false;
        } else cur += ch;
      } else {
        if (ch === '"') inQuote = true;
        else if (ch === ',') { out.push(cur); cur = ''; }
        else cur += ch;
      }
    }
    out.push(cur);
    return out;
  };
  // 首行是表头：识别 key / addedAt 列位置（大小写不敏感）
  const header = parseLine(lines[0]).map((h) => h.trim().toLowerCase());
  let keyIdx = header.indexOf('key');
  let addedAtIdx = header.indexOf('addedAt');
  let dataStart = 1;
  // 无表头：假定顺序为 key,addedAt，第一行就是数据
  if (keyIdx < 0) {
    if (lines.length < 1) {
      return { ok: false, error: '❌ CSV 缺少表头或缺少 key 列' };
    }
    keyIdx = 0;
    addedAtIdx = 1;
    dataStart = 0;
  }
  const now = Date.now();
  const incoming: WhitelistEntry[] = [];
  for (let li = dataStart; li < lines.length; li++) {
    const raw = lines[li];
    if (!raw.trim()) continue;
    const cells = parseLine(raw);
    const k = cells[keyIdx] ?? '';
    if (!k) continue;
    const aRaw = cells[addedAtIdx];
    const a = aRaw ? Number(aRaw) : now;
    incoming.push({ key: k, addedAt: Number.isFinite(a) && a > 0 ? a : now });
  }
  return { ok: true, items: incoming };
}

// 合并语义：merge=逐项覆盖写入现有 map；replace=全新 map
export function mergeWhitelistItems(prev: Map<string, number>, items: WhitelistEntry[], mode: 'merge' | 'replace'): Map<string, number> {
  const next = mode === 'replace' ? new Map<string, number>() : new Map(prev);
  for (const { key, addedAt } of items) next.set(key, addedAt);
  return next;
}
