import type { DatabaseKind } from '../api';

export type ParamType = 'string' | 'number' | 'bool' | 'null';
export interface ParamItem {
  type: ParamType;
  value: string;
}

export interface TabContext {
  label: string;
  schema: string;
  table: string;
}

export interface EditorTab {
  id: string;
  title: string;
  sql: string;
  params: ParamItem[];
  context: TabContext | null;
}

export interface TabState {
  tabs: EditorTab[];
  activeId: string;
  cursor: number;
}

const TAB_KEY = 'polydb.tabs.v1';
const LEGACY_SQL_KEY = 'polydb.sql';

const DEFAULT_SQL = "-- 示例：\nSELECT 1 AS id, 'hello' AS greeting;";

export function genId(): string {
  return Math.random().toString(36).slice(2, 10) + Date.now().toString(36).slice(-4);
}

function defaultTab(n: number): EditorTab {
  return {
    id: genId(),
    title: `查询 ${n}`,
    sql: DEFAULT_SQL,
    params: [],
    context: null,
  };
}

export function loadTabState(connId: string): TabState {
  try {
    const raw = localStorage.getItem(TAB_KEY);
    if (raw) {
      const obj = JSON.parse(raw);
      const stored = obj?.[connId];
      if (stored && Array.isArray(stored.tabs) && stored.tabs.length > 0) {
        const tabs: EditorTab[] = stored.tabs
          .filter((t: EditorTab) => t && typeof t.id === 'string' && typeof t.sql === 'string')
          .map((t: EditorTab, i: number) => ({
            id: t.id,
            title: typeof t.title === 'string' && t.title ? t.title : `查询 ${i + 1}`,
            sql: t.sql,
            params: Array.isArray(t.params) ? t.params.filter((p: ParamItem) => p && typeof p.type === 'string') : [],
            context: t.context && typeof t.context.label === 'string' ? t.context : null,
          }));
        if (tabs.length > 0) {
          const activeId =
            typeof stored.activeId === 'string' && tabs.some((t) => t.id === stored.activeId)
              ? stored.activeId
              : tabs[0].id;
          return { tabs, activeId, cursor: 1 };
        }
      }
    }
  } catch { /* quota / private mode / malformed */ }

  // Migration from legacy single-SQL storage.
  let legacySql: string | null = null;
  let legacyParams: ParamItem[] = [];
  try {
    const raw = localStorage.getItem(LEGACY_SQL_KEY);
    if (raw) {
      const obj = JSON.parse(raw);
      const v = obj?.[connId];
      if (typeof v === 'string') legacySql = v;
      else if (v && typeof v === 'object') {
        if (typeof v.sql === 'string') legacySql = v.sql;
        if (Array.isArray(v.params)) {
          legacyParams = (v.params as ParamItem[])
            .filter((p) => p && typeof p.type === 'string' && typeof p.value === 'string')
            .map((p) => ({ type: p.type as ParamType, value: p.value }));
        }
      }
    }
  } catch { /* ignore */ }

  const first: EditorTab = {
    id: genId(),
    title: '查询 1',
    sql: legacySql ?? DEFAULT_SQL,
    params: legacyParams,
    context: null,
  };
  const state: TabState = { tabs: [first], activeId: first.id, cursor: 2 };
  saveTabState(connId, state);
  return state;
}

export function saveTabState(connId: string, state: TabState): void {
  try {
    const raw = localStorage.getItem(TAB_KEY);
    const obj = raw ? JSON.parse(raw) : {};
    if (!obj || typeof obj !== 'object') return;
    obj[connId] = {
      tabs: state.tabs.map((t) => ({
        id: t.id,
        title: t.title,
        sql: t.sql,
        params: t.params,
        context: t.context,
      })),
      activeId: state.activeId,
    };
    localStorage.setItem(TAB_KEY, JSON.stringify(obj));
  } catch { /* quota / private mode */ }
}

export function nextTabTitle(tabs: EditorTab[]): string {
  let max = 0;
  for (const t of tabs) {
    const m = /^查询\s*(\d+)$/.exec(t.title);
    if (m) max = Math.max(max, Number(m[1]));
  }
  return `查询 ${max + 1}`;
}
