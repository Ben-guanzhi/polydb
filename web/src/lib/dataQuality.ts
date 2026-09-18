import { guessType } from './importData';
import type { ColumnProfile } from './importData';

export interface ColumnQuality {
  index: number;
  name: string;
  total: number;
  nonNull: number;
  completeness: number; // 0..1
  dominantType: 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null';
  typePurity: number; // 0..1 = 主导类型占比
  uniqueRatio: number; // 0..1
  suspiciousCount: number;
  suspiciousBreakdown: { leadingZero: number; extraSpaces: number; trailingQuote: number; dashOnly: number };
  score: number; // 0..100
  issues: string[];
  suggestions: QualityTransformSuggestion[];
}

export interface DataQualityReport {
  columns: ColumnQuality[];
  overallScore: number;
  issues: { severity: 'error' | 'warning' | 'info'; message: string }[];
}

const SUSPICIOUS_PATTERNS: { key: 'leadingZero' | 'extraSpaces' | 'trailingQuote' | 'dashOnly'; re: RegExp; label: string }[] = [
  { key: 'leadingZero', re: /^0{3,}/, label: '前导 0' },
  { key: 'extraSpaces', re: /\s{2,}/, label: '多余空格' },
  { key: 'trailingQuote', re: /['"]$/, label: '尾引号' },
  { key: 'dashOnly', re: /^-\s*$/, label: '孤立破折号' },
];

function dominantType(samples: string[]): { type: 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null'; ratio: number } {
  const counts: Record<string, number> = {};
  let total = 0;
  for (const s of samples) {
    if (s === '') continue;
    const t = guessType(s);
    counts[t] = (counts[t] ?? 0) + 1;
    total++;
  }
  if (total === 0) return { type: 'null', ratio: 1 };
  let best: 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null' = 'string';
  let bestN = 0;
  for (const [k, v] of Object.entries(counts)) {
    if (v > bestN) { best = k as 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null'; bestN = v; }
  }
  return { type: best, ratio: bestN / total };
}

export function scoreColumn(index: number, name: string, rows: string[][], profile: ColumnProfile): ColumnQuality {
  const total = profile.total;
  const nonNull = total - profile.nullCount;
  const completeness = total > 0 ? nonNull / total : 0;
  const samples = rows.slice(0, 200).map((r) => r[index] ?? '');
  const { type: domType, ratio: typePurity } = dominantType(samples);

  const uniq = new Set(samples.filter((s) => s !== ''));
  const uniqueRatio = nonNull > 0 ? uniq.size / nonNull : 0;

  const breakdown: { leadingZero: number; extraSpaces: number; trailingQuote: number; dashOnly: number } = {
    leadingZero: 0, extraSpaces: 0, trailingQuote: 0, dashOnly: 0,
  };
  let suspiciousCount = 0;
  for (const s of samples) {
    if (s === '') continue;
    for (const { key, re } of SUSPICIOUS_PATTERNS) {
      if (re.test(s)) { breakdown[key]++; suspiciousCount++; break; }
    }
  }

  const issues: string[] = [];
  if (completeness < 0.5) issues.push(`完整度低：${(completeness * 100).toFixed(0)}%`);
  else if (completeness < 0.9) issues.push(`完整度偏低：${(completeness * 100).toFixed(0)}%`);
  if (typePurity < 0.8 && nonNull > 0) issues.push(`类型混杂：主导 ${domType} ${Math.round(typePurity * 100)}%`);
  if (uniqueRatio === 1 && nonNull > 1) issues.push('可能主键：所有非空值唯一');
  if (uniqueRatio < 0.02 && nonNull > 5) issues.push(`低基数：仅 ${uniq.size} 种值`);
  if (suspiciousCount > 0) issues.push(`${suspiciousCount} 个可疑值（前导 0/多余空格/尾引号/孤立破折号）`);

  const score = Math.round(
    Math.min(100, Math.max(0,
      completeness * 55 +
      typePurity * 30 +
      (suspiciousCount === 0 ? 15 : Math.max(0, 15 - suspiciousCount))
    ))
  );

  return {
    index, name, total, nonNull, completeness,
    dominantType: domType, typePurity, uniqueRatio,
    suspiciousCount, suspiciousBreakdown: breakdown, score, issues,
    suggestions: suggestTransforms({ index, name, total, nonNull, completeness, dominantType: domType, typePurity, uniqueRatio, suspiciousCount, suspiciousBreakdown: breakdown, score, issues } as ColumnQuality),
  };
}

export type QualityTransformSuggestion =
  | { transform: 'trim'; reason: string }
  | { transform: 'strip-zero-padding'; reason: string }
  | { transform: 'regex-replace'; pattern: string; replacement: string; flags: string; reason: string }
  | { transform: 'date-parse-iso'; reason: string }
  | { transform: 'date-parse-ymd'; reason: string }
  | { transform: 'null-if-empty'; reason: string };

export function suggestTransforms(c: ColumnQuality): QualityTransformSuggestion[] {
  const out: QualityTransformSuggestion[] = [];
  const b = c.suspiciousBreakdown;
  const samples = c.total > 0 ? c.total : 1;
  const rate = (n: number) => n / samples;

  if (b.extraSpaces >= 3 || rate(b.extraSpaces) > 0.05) {
    out.push({ transform: 'trim', reason: `${b.extraSpaces} 处多余空格 → Trim` });
  }
  if (b.leadingZero >= 3 || rate(b.leadingZero) > 0.05) {
    out.push({ transform: 'strip-zero-padding', reason: `${b.leadingZero} 处前导 0 → 去除前导 0` });
  }
  if (b.trailingQuote >= 1) {
    out.push({
      transform: 'regex-replace',
      pattern: '[\'"]$', replacement: '', flags: 'g',
      reason: `${b.trailingQuote} 处尾引号 → 正则去除`,
    });
  }
  if (c.dominantType === 'date') {
    out.push({ transform: 'date-parse-iso', reason: '主类型 date → 统一转 ISO 8601' });
  }
  return out;
}

export function scoreDataQuality(rows: string[][], columns: string[], profiles: ColumnProfile[]): DataQualityReport {
  const cols = columns.map((name, i) => scoreColumn(i, name, rows, profiles[i]));
  const overallScore = cols.length > 0 ? Math.round(cols.reduce((s, c) => s + c.score, 0) / cols.length) : 0;
  const issues: { severity: 'error' | 'warning' | 'info'; message: string }[] = [];
  for (const c of cols) {
    for (const msg of c.issues) {
      const sev: 'error' | 'warning' | 'info' =
        c.completeness < 0.5 || c.typePurity < 0.8 ? 'warning'
        : c.suspiciousCount > 0 ? 'warning'
        : 'info';
      issues.push({ severity: sev, message: `${c.name}: ${msg}` });
    }
  }
  return { columns: cols, overallScore, issues };
}
