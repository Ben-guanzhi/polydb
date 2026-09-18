import type { DatabaseKind } from '../api';

export interface RunStat {
  sql: string;
  kind: DatabaseKind;
  connId: string;
  elapsed_ms: number;
  server_ms: number;
  rows: number;
  truncated: boolean;
  ts: number;
}

const STAT_KEY = 'polydb.runStats';
const MAX_PER_HASH = 20;
const MAX_TOTAL = 200;

export function normalizeSqlForHash(sql: string): string {
  return sql
    .replace(/--[^\n]*/g, ' ')
    .replace(/\/\*[\s\S]*?\*\*/g, ' ')
    .replace(/\s+/g, ' ')
    .replace(/[ \t]+/g, ' ')
    .trim()
    .toLowerCase()
    .replace(/;+$/, '');
}

function hashStr(s: string): string {
  let h = 5381;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h) ^ s.charCodeAt(i);
    h |= 0;
  }
  return (h >>> 0).toString(36);
}

export function hashSql(sql: string): string {
  return hashStr(normalizeSqlForHash(sql));
}

export function loadStats(): RunStat[] {
  try {
    const raw = localStorage.getItem(STAT_KEY);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    return arr.filter(
      (x) =>
        x &&
        typeof x.sql === 'string' &&
        typeof x.ts === 'number' &&
        typeof x.connId === 'string' &&
        x.connId.length > 0,
    );
  } catch {
    return [];
  }
}

export function saveStats(stats: RunStat[]): void {
  try {
    localStorage.setItem(STAT_KEY, JSON.stringify(stats));
  } catch { /* quota */ }
}

export function addStat(s: RunStat): RunStat[] {
  const all = loadStats();
  all.unshift(s);
  const seen = new Map<string, number>();
  const out: RunStat[] = [];
  for (const x of all) {
    const h = hashSql(x.sql);
    const n = seen.get(h) ?? 0;
    if (n >= MAX_PER_HASH) continue;
    seen.set(h, n + 1);
    out.push(x);
  }
  const trimmed = out.slice(0, MAX_TOTAL);
  saveStats(trimmed);
  return trimmed;
}

export interface StatSummary {
  count: number;
  min: number;
  p50: number;
  p95: number;
  max: number;
  avg: number;
  lastTs: number;
}

export function summarize(stats: RunStat[], sql: string): StatSummary | null {
  const h = hashSql(sql);
  const xs = stats.filter((s) => hashSql(s.sql) === h);
  if (xs.length === 0) return null;
  const arr = xs.map((s) => s.elapsed_ms).sort((a, b) => a - b);
  const pct = (p: number) => arr[Math.min(arr.length - 1, Math.floor(p * arr.length))];
  return {
    count: xs.length,
    min: arr[0],
    p50: pct(0.5),
    p95: pct(0.95),
    max: arr[arr.length - 1],
    avg: arr.reduce((a, b) => a + b, 0) / arr.length,
    lastTs: xs[0].ts,
  };
}
