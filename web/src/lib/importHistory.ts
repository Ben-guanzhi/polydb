import type { DatabaseKind } from '../api';

export interface FailedRowSnapshot {
  csvRow: number;
  reason: string;
  preview: string;
}

export interface ImportHistoryEntry {
  id: string;
  at: number;
  connId: string;
  kind: DatabaseKind | null;
  schema: string;
  table: string;
  fileName: string | null;
  mode: 'insert' | 'update' | 'upsert';
  totalRows: number;
  insertedRows: number;
  failedRows: number;
  skippedRows: number;
  ms: number;
  status: 'success' | 'partial' | 'failed' | 'cancelled';
  failedRowsDetail?: FailedRowSnapshot[];
}

const KEY = 'polydb.importHistory.v1';
const MAX_ENTRIES = 100;

function loadAll(): ImportHistoryEntry[] {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) return parsed as ImportHistoryEntry[];
    return [];
  } catch {
    return [];
  }
}

function saveAll(list: ImportHistoryEntry[]): void {
  try {
    const trimmed = list.slice(0, MAX_ENTRIES);
    localStorage.setItem(KEY, JSON.stringify(trimmed));
  } catch { /* quota */ }
}

export function listHistory(connId?: string): ImportHistoryEntry[] {
  const all = loadAll();
  if (!connId) return all;
  return all.filter((e) => e.connId === connId);
}

export function addHistory(entry: Omit<ImportHistoryEntry, 'id' | 'at'>): ImportHistoryEntry {
  const cappedFailed = entry.failedRowsDetail && entry.failedRowsDetail.length > 50
    ? entry.failedRowsDetail.slice(0, 50)
    : entry.failedRowsDetail;
  const full: ImportHistoryEntry = {
    ...entry,
    failedRowsDetail: cappedFailed,
    id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    at: Date.now(),
  };
  const all = loadAll();
  all.unshift(full);
  saveAll(all);
  return full;
}

export function clearHistory(): void {
  try { localStorage.removeItem(KEY); } catch { /* ignore */ }
}

export function deleteHistory(id: string): void {
  try {
    const all = loadAll().filter((e) => e.id !== id);
    saveAll(all);
  } catch { /* ignore */ }
}
