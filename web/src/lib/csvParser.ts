// RFC 4180 CSV parser。
// 支持：逗号/分号/制表符/竖线分隔符；"" 转义引号；字段内换行；首行可选 header；BOM 头。
// 不做类型推断，只负责把原始文本切成二维字符串。

export type Delimiter = ',' | ';' | '\t' | '|' | ' ';

export interface CsvOptions {
  delimiter?: Delimiter;
  hasHeader?: boolean;
  quoteChar?: '"' | "'";
}

export interface CsvParseResult {
  columns: string[];
  rows: string[][];
  rawLines: number;
  headerSkipped: boolean;
  truncated: boolean;
}

const MAX_ROWS = 200_000;

export function detectDelimiter(sample: string): Delimiter {
  const line = sample.split(/\r?\n/)[0] ?? '';
  const counts: [Delimiter, number][] = [
    [',', countOcc(line, ',')],
    [';', countOcc(line, ';')],
    ['\t', countOcc(line, '\t')],
    ['|', countOcc(line, '|')],
    [' ', countOcc(line, ' ')],
  ];
  counts.sort((a, b) => b[1] - a[1]);
  return counts[0][1] > 0 ? counts[0][0] : ',';
}

function countOcc(s: string, c: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) if (s[i] === c) n++;
  return n;
}

function stripBom(s: string): string {
  return s.charCodeAt(0) === 0xFEFF ? s.slice(1) : s;
}

export function parseCsv(input: string, opts: CsvOptions = {}): CsvParseResult {
  const delimiter = opts.delimiter ?? detectDelimiter(input);
  const hasHeader = opts.hasHeader ?? true;
  const quote = opts.quoteChar ?? '"';
  const src = stripBom(input);

  const records: string[][] = [];
  let row: string[] = [];
  let field = '';
  let inQuotes = false;
  let i = 0;
  const n = src.length;

  while (i < n) {
    const c = src[i];

    if (inQuotes) {
      if (c === quote) {
        if (i + 1 < n && src[i + 1] === quote) { field += quote; i += 2; continue; }
        inQuotes = false; i++; continue;
      }
      field += c; i++; continue;
    }

    if (c === quote && field.length === 0) {
      inQuotes = true; i++; continue;
    }
    if (c === delimiter) {
      row.push(field); field = ''; i++; continue;
    }
    if (c === '\r') {
      row.push(field); field = '';
      records.push(row); row = [];
      i++;
      if (i < n && src[i] === '\n') i++;
      continue;
    }
    if (c === '\n') {
      row.push(field); field = '';
      records.push(row); row = [];
      i++;
      continue;
    }
    field += c; i++;
  }
  if (field.length > 0 || row.length > 0) {
    row.push(field);
    records.push(row);
  }

  while (records.length > 0) {
    const last = records[records.length - 1];
    if (last.length === 1 && last[0] === '') { records.pop(); continue; }
    break;
  }

  let truncated = false;
  let headerSkipped = false;
  let headerNames: string[] = [];
  if (hasHeader && records.length > 0) {
    headerNames = records[0].slice();
    if (headerNames.length > 0) {
      records.shift();
      headerSkipped = true;
    }
  }

  if (records.length > MAX_ROWS) {
    records.length = MAX_ROWS;
    truncated = true;
  }

  const maxLen = Math.max(
    0,
    records.reduce((m, r) => Math.max(m, r.length), 0),
    headerNames.length,
  );
  const cols = Array.from({ length: maxLen }, (_, idx) => {
    if (headerSkipped) {
      const named = headerNames[idx];
      if (named && named.length > 0) return named;
    }
    return `col${idx + 1}`;
  });

  const normalized = records.map((r) => {
    if (r.length >= cols.length) return r.slice(0, cols.length);
    const padded = r.slice();
    while (padded.length < cols.length) padded.push('');
    return padded;
  });

  return {
    columns: cols,
    rows: normalized,
    rawLines: src.split(/\r?\n/).length,
    headerSkipped,
    truncated,
  };
}
