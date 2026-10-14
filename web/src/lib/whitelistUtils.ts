import { WHITELIST_TTL_MS } from './riskWhitelist';

export interface WhitelistEntry {
  key: string;
  addedAt: number;
}

export type WhitelistKindFilter = 'all' | 'preset' | 'snapshot';
export type WhitelistSortBy = 'key' | 'addedAt' | 'expiring';

export function filterWhitelist(
  entries: [string, number][],
  search: string,
  kindFilter: WhitelistKindFilter,
): [string, number][] {
  const kw = search.trim().toLowerCase();
  const searchFiltered = entries.filter(([k]) => !kw || k.toLowerCase().includes(kw));
  if (kindFilter === 'all') return searchFiltered;
  return searchFiltered.filter(([k]) => {
    const parts = k.split('::');
    return parts.length >= 4 && parts[3] === kindFilter;
  });
}

export function countWhitelistByKind(
  entries: [string, number][],
): { presetCount: number; snapshotCount: number } {
  const kw = '';
  const searchFiltered = entries.filter(([k]) => true);
  const presetCount = searchFiltered.filter(([k]) => {
    const parts = k.split('::');
    return parts.length >= 4 && parts[3] === 'preset';
  }).length;
  const snapshotCount = searchFiltered.filter(([k]) => {
    const parts = k.split('::');
    return parts.length >= 4 && parts[3] === 'snapshot';
  }).length;
  return { presetCount, snapshotCount };
}

export function sortWhitelist(
  entries: [string, number][],
  sortBy: WhitelistSortBy,
): [string, number][] {
  const sorted = [...entries];
  if (sortBy === 'key') {
    sorted.sort((a, b) => a[0].localeCompare(b[0]));
  } else if (sortBy === 'expiring') {
    sorted.sort((a, b) => a[1] - b[1]);
  } else {
    sorted.sort((a, b) => b[1] - a[1]);
  }
  return sorted;
}

export function formatRemaining(addedAt: number, now?: number): string {
  const current = now ?? Date.now();
  const remainMs = WHITELIST_TTL_MS - (current - addedAt);
  const days = Math.ceil(remainMs / (24 * 60 * 60 * 1000));
  return days > 0 ? `${days}天` : '已过期';
}

export function getExpiredKeys(entries: [string, number][], now?: number): string[] {
  const current = now ?? Date.now();
  const expired: string[] = [];
  for (const [k, addedAt] of entries) {
    if (current - addedAt > WHITELIST_TTL_MS) expired.push(k);
  }
  return expired;
}
