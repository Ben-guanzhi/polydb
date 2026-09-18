import type { ColumnInfo, DatabaseKind, Value } from '../api';

export type GuessType = 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null';

export interface Mapping {
  csvIndex: number;
  csvName: string;
  targetColumn: string | null; // null = 跳过
}

export type ImportMode = 'insert' | 'update' | 'upsert';

export type ColumnTransform =
  | 'none'
  | 'trim'
  | 'lower'
  | 'upper'
  | 'null-if-empty'
  | 'strip-zero-padding'
  | 'regex-replace'
  | 'date-parse-iso'
  | 'date-parse-us'
  | 'date-parse-ymd'
  | 'scale-x100'
  | 'scale-div100';

export type NullPolicy = 'null' | 'empty-string' | 'skip-row' | 'use-default';

export const DEFAULT_NULL_POLICY: NullPolicy = 'null';

export function isNonDefaultNullPolicy(p: NullPolicy | undefined): boolean {
  return p !== undefined && p !== 'null';
}

export interface ImportOptions {
  emptyAsNull: boolean;
  batchSize: number;
  inferTypes: boolean;
  skipFailed: boolean;
  mode: ImportMode;
  transforms: Record<number, ColumnTransform>;
  /** Per-column params for transforms that take arguments (regex-replace etc). */
  transformParams: Record<number, Record<string, string>>;
  /** Optional row filter; rows not matching the CSV column value are excluded. */
  filterColumn: number | null;
  filterOp: 'eq' | 'neq' | 'contains' | 'regex';
  filterValue: string;
  kind: DatabaseKind | null;
  /** Per-column validation rules; only columns listed here are checked. */
  validations: Record<number, ColumnValidation>;
  /** When true, rows failing validation are dropped before execution. */
  strictValidation: boolean;
  /** Per-column null policy; only non-default entries override the global emptyAsNull. */
  nullPolicies: Record<number, NullPolicy>;
}

export const DEFAULT_IMPORT_OPTIONS: ImportOptions = {
  emptyAsNull: true,
  batchSize: 500,
  inferTypes: true,
  skipFailed: false,
  mode: 'insert',
  transforms: {},
  transformParams: {},
  filterColumn: null,
  filterOp: 'eq',
  filterValue: '',
  kind: null,
  validations: {},
  strictValidation: true,
  nullPolicies: {},
};

export function rowPassesFilter(
  row: string[],
  opts: Pick<ImportOptions, 'filterColumn' | 'filterOp' | 'filterValue'>,
): boolean {
  if (opts.filterColumn === null || opts.filterValue === '') return true;
  const v = row[opts.filterColumn] ?? '';
  switch (opts.filterOp) {
    case 'eq': return v === opts.filterValue;
    case 'neq': return v !== opts.filterValue;
    case 'contains': return v.includes(opts.filterValue);
    case 'regex':
      try { return new RegExp(opts.filterValue).test(v); }
      catch { return true; }
    default: return true;
  }
}

const BOOL_TRUE = new Set(['true', 't', 'yes', 'y', '1', 'on']);
const BOOL_FALSE = new Set(['false', 'f', 'no', 'n', '0', 'off']);

export function guessType(value: string): GuessType {
  if (value === '' || value === 'null' || value === 'NULL') return 'null';
  if (/^-?\d+$/.test(value)) {
    const n = Number(value);
    if (Number.isSafeInteger(n)) return 'integer';
    return 'float';
  }
  if (/^-?\d*\.\d+([eE][-+]?\d+)?$/.test(value)) return 'float';
  if (BOOL_TRUE.has(value.toLowerCase()) || BOOL_FALSE.has(value.toLowerCase())) return 'boolean';
  if (/^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2})?(\.\d+)?Z?)?$/.test(value)) return 'date';
  return 'string';
}

export function applyTransform(raw: string, transform: ColumnTransform | undefined): string {
  let v = raw;
  if (transform === 'trim') v = v.trim();
  else if (transform === 'lower') v = v.toLowerCase();
  else if (transform === 'upper') v = v.toUpperCase();
  else if (transform === 'strip-zero-padding') v = v.replace(/^0+(\d)/, '$1');
  return v;
}

/** Lightweight preview-only transform: string → string | null, no null-policy or dtype coercion. */
export function transformOnly(
  raw: string,
  transform: ColumnTransform | undefined,
  params?: Record<string, string>,
): string | null {
  if (raw === '') return null;
  let v = raw;
  switch (transform) {
    case 'trim': v = v.trim(); break;
    case 'lower': v = v.toLowerCase(); break;
    case 'upper': v = v.toUpperCase(); break;
    case 'strip-zero-padding': v = v.replace(/^0+(\d)/, '$1'); break;
    case 'null-if-empty': break;
    case 'regex-replace':
      if (params?.pattern) {
        try { v = v.replace(new RegExp(params.pattern, params.flags ?? 'g'), params.replacement ?? ''); }
        catch { /* invalid regex */ }
      }
      break;
    case 'date-parse-iso': v = parseDateToISO(v); break;
    case 'date-parse-us': v = parseUSDateToISO(v); break;
    case 'date-parse-ymd': v = parseYMDDateToISO(v); break;
    case 'scale-x100': {
      const n = parseFloat(v);
      if (!Number.isNaN(n)) v = String(Math.round(n * 100));
      break;
    }
    case 'scale-div100': {
      const n = parseFloat(v);
      if (!Number.isNaN(n)) v = String(Math.round(n / 100));
      break;
    }
    default: break;
  }
  return v === '' ? null : v;
}

/** Sentinel: the row was dropped by a per-column null policy (skip-row). */
export const SKIP_ROW = Symbol('SKIP_ROW');
export type ValueOrSkip = Value | typeof SKIP_ROW;

export function convertValue(
  value: string,
  targetCol: ColumnInfo | null,
  emptyAsNull: boolean,
  transform: ColumnTransform | undefined,
  transformParams: Record<string, string> | undefined,
  nullPolicy?: NullPolicy,
): ValueOrSkip {
  let v = value;
  if (transform === 'trim') v = v.trim();
  else if (transform === 'lower') v = v.toLowerCase();
  else if (transform === 'upper') v = v.toUpperCase();
  else if (transform === 'strip-zero-padding') v = v.replace(/^0+(\d)/, '$1');
  else if (transform === 'regex-replace' && transformParams?.pattern) {
    try {
      const flags = transformParams.flags ?? 'g';
      v = v.replace(new RegExp(transformParams.pattern, flags), transformParams.replacement ?? '');
    } catch { /* invalid regex, leave v */ }
  } else if (transform === 'date-parse-iso') v = parseDateToISO(v);
  else if (transform === 'date-parse-us') v = parseUSDateToISO(v);
  else if (transform === 'date-parse-ymd') v = parseYMDDateToISO(v);
  else if (transform === 'scale-x100') {
    const n = parseFloat(v);
    if (!Number.isNaN(n)) v = String(Math.round(n * 100));
  } else if (transform === 'scale-div100') {
    const n = parseFloat(v);
    if (!Number.isNaN(n)) v = String(Math.round(n / 100));
  }
  if (v === '') {
    const p = nullPolicy;
    if (p === 'skip-row') return SKIP_ROW;
    if (p === 'use-default') {
      const dv = targetCol?.default_value;
      if (dv != null && dv !== '') return coerceToDtype(dv, targetCol!);
    }
    if (p === 'empty-string') return '';
    return (transform === 'null-if-empty' || emptyAsNull) ? null : '';
  }
  if (targetCol) {
    return coerceToDtype(v, targetCol);
  }
  return v;
}

/** Coerce a string to the target column's data_type; fall back to string if no match. */
function coerceToDtype(v: string, targetCol: ColumnInfo): Value {
  const dtype = targetCol.data_type.toLowerCase();
  if (/int|serial|bigserial|smallint|tinyint/.test(dtype) && /^-?\d+$/.test(v)) {
    const n = parseInt(v, 10);
    return Number.isSafeInteger(n) ? n : v;
  }
  if (/float|double|decimal|numeric|real/.test(dtype)) {
    const n = parseFloat(v);
    if (!Number.isNaN(n)) return n;
  }
  if (/bool|bit/.test(dtype)) {
    if (BOOL_TRUE.has(v.toLowerCase())) return true;
    if (BOOL_FALSE.has(v.toLowerCase())) return false;
  }
  return v;
}

function parseDateToISO(v: string): string {
  // 已是 ISO 或基本形式，走 Date parse
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : d.toISOString();
}

function parseUSDateToISO(v: string): string {
  // MM/dd/yyyy 或 MM/dd/yyyy HH:mm:ss
  const m = v.match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})(.*)$/);
  if (!m) return parseDateToISO(v);
  const [, mm, dd, yyyy, time] = m;
  const d = new Date(Number(yyyy), Number(mm) - 1, Number(dd), ...time.trim().split(':').slice(0, 3).map((x) => Number(x || 0)));
  return Number.isNaN(d.getTime()) ? v : d.toISOString();
}

function parseYMDDateToISO(v: string): string {
  // yyyy-MM-dd HH:mm:ss → ISO
  const m = v.match(/^(\d{4})-(\d{1,2})-(\d{1,2})(.*)$/);
  if (!m) return parseDateToISO(v);
  const [, yyyy, mm, dd, time] = m;
  const d = new Date(Number(yyyy), Number(mm) - 1, Number(dd), ...time.trim().split(' ').pop()?.split(':').slice(0, 3).map((x) => Number(x || 0)) ?? []);
  return Number.isNaN(d.getTime()) ? v : d.toISOString();
}

export function inferMapping(csvColumns: string[], targetCols: ColumnInfo[]): Mapping[] {
  const autoInc = new Set(targetCols.filter((c) => c.is_auto_increment).map((c) => c.name));
  return csvColumns.map((name, idx) => {
    const lower = name.toLowerCase();
    const matched = targetCols.find((c) => {
      if (autoInc.has(c.name)) return false;
      return c.name.toLowerCase() === lower;
    });
    return { csvIndex: idx, csvName: name, targetColumn: matched ? matched.name : null };
  });
}

export interface MappingSuggestion {
  csvIndex: number;
  csvName: string;
  targetColumn: string;
  score: number;
  reason: string;
}

function normalize(s: string): string {
  return s.toLowerCase().replace(/[\s\-]+/g, '').replace(/_/g, '');
}

function levenshtein(a: string, b: string): number {
  if (a === b) return 0;
  const m = a.length;
  const n = b.length;
  if (m === 0) return n;
  if (n === 0) return m;
  const row = new Array(n + 1);
  for (let j = 0; j <= n; j++) row[j] = j;
  for (let i = 1; i <= m; i++) {
    let prev = row[0];
    row[0] = i;
    for (let j = 1; j <= n; j++) {
      const cur = row[j];
      row[j] = a[i - 1] === b[j - 1] ? prev : Math.min(prev + 1, row[j] + 1, cur + 1);
      prev = cur;
    }
  }
  return row[n];
}

export function suggestMappings(csvColumns: string[], targetCols: ColumnInfo[], mappings: Mapping[]): MappingSuggestion[] {
  const autoInc = new Set(targetCols.filter((c) => c.is_auto_increment).map((c) => c.name));
  const targetSet = new Set(mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!));
  const targetCandidates = targetCols.filter((c) => !autoInc.has(c.name) && !targetSet.has(c.name));
  const suggestions: MappingSuggestion[] = [];
  const claimed = new Set<string>();

  for (const m of mappings) {
    if (m.targetColumn) continue;
    const csvNorm = normalize(m.csvName);
    let best: { name: string; score: number; reason: string } | null = null;
    for (const c of targetCandidates) {
      if (claimed.has(c.name)) continue;
      const tNorm = normalize(c.name);
      let score = 0;
      let reason = '';
      if (csvNorm === tNorm) {
        score = 100;
        reason = '归一化后完全匹配';
      } else if (tNorm.includes(csvNorm) && csvNorm.length >= 3) {
        score = 70 + Math.min(20, Math.round(20 * csvNorm.length / tNorm.length));
        reason = `目标列包含 CSV 列名（${Math.round(100 * csvNorm.length / tNorm.length)}%）`;
      } else if (csvNorm.includes(tNorm) && tNorm.length >= 3) {
        score = 70 + Math.min(20, Math.round(20 * tNorm.length / csvNorm.length));
        reason = `CSV 列名包含目标列（${Math.round(100 * tNorm.length / csvNorm.length)}%）`;
      } else {
        const dist = levenshtein(csvNorm, tNorm);
        const max = Math.max(csvNorm.length, tNorm.length);
        if (max > 0) {
          const similarity = 1 - dist / max;
          if (similarity >= 0.7) {
            score = Math.round(similarity * 100);
            reason = `相似度 ${score}%（编辑距离 ${dist}）`;
          }
        }
      }
      if (score > 0 && (!best || score > best.score)) {
        best = { name: c.name, score, reason };
      }
    }
    if (best) {
      suggestions.push({
        csvIndex: m.csvIndex,
        csvName: m.csvName,
        targetColumn: best.name,
        score: best.score,
        reason: best.reason,
      });
      claimed.add(best.name);
    }
  }

  return suggestions.sort((a, b) => b.score - a.score);
}

export function quoteIdent(name: string): string {
  return `"${name.replace(/"/g, '""')}"`;
}

export function qualTable(schema: string | null, table: string): string {
  if (schema && !/^main$/i.test(schema) && !/^public$/i.test(schema)) {
    return `${quoteIdent(schema)}.${quoteIdent(table)}`;
  }
  return quoteIdent(table);
}

function buildUpdateStatement(
  schema: string | null,
  table: string,
  dataCols: string[],
  pkCols: string[],
): string {
  const sets = dataCols.map((c) => `${quoteIdent(c)} = ?`).join(', ');
  const wheres = pkCols.map((c) => `${quoteIdent(c)} = ?`).join(' AND ');
  return `UPDATE ${qualTable(schema, table)} SET ${sets} WHERE ${wheres}`;
}

function buildPgUpsert(
  schema: string | null,
  table: string,
  allCols: string[],
  dataCols: string[],
  pkCols: string[],
): string {
  const ph = allCols.map(() => '?').join(', ');
  const sets = dataCols.map((c) => `${quoteIdent(c)} = EXCLUDED.${quoteIdent(c)}`).join(', ');
  return `INSERT INTO ${qualTable(schema, table)} (${allCols.map(quoteIdent).join(', ')}) VALUES (${ph}) ON CONFLICT (${pkCols.map(quoteIdent).join(', ')}) DO UPDATE SET ${sets}`;
}

function buildMySqlUpsert(
  schema: string | null,
  table: string,
  allCols: string[],
  dataCols: string[],
): string {
  const ph = allCols.map(() => '?').join(', ');
  const sets = dataCols.map((c) => `${quoteIdent(c)} = VALUES(${quoteIdent(c)})`).join(', ');
  return `INSERT INTO ${qualTable(schema, table)} (${allCols.map(quoteIdent).join(', ')}) VALUES (${ph}) ON DUPLICATE KEY UPDATE ${sets}`;
}

function buildMssqlUpsert(
  schema: string | null,
  table: string,
  allCols: string[],
  dataCols: string[],
  pkCols: string[],
): string {
  const cols = allCols.map(quoteIdent).join(', ');
  const ph = allCols.map(() => '?').join(', ');
  const wheres = pkCols.map((c) => `t.${quoteIdent(c)} = s.${quoteIdent(c)}`).join(' AND ');
  const sets = dataCols.length > 0
    ? dataCols.map((c) => `s.${quoteIdent(c)} = t.${quoteIdent(c)}`).join(', ')
    : `s.${quoteIdent(pkCols[0])} = t.${quoteIdent(pkCols[0])}`;
  return `MERGE INTO ${qualTable(schema, table)} AS s USING (VALUES (${ph})) AS t (${cols}) ON ${wheres} WHEN MATCHED THEN UPDATE SET ${sets} WHEN NOT MATCHED THEN INSERT (${cols}) VALUES (${allCols.map((c) => `t.${quoteIdent(c)}`).join(', ')})`;
}

function buildOracleUpsert(
  schema: string | null,
  table: string,
  allCols: string[],
  dataCols: string[],
  pkCols: string[],
): string {
  const cols = allCols.map(quoteIdent).join(', ');
  const selectList = allCols.map((c) => `? AS ${quoteIdent(c)}`).join(', ');
  const wheres = pkCols.map((c) => `t.${quoteIdent(c)} = s.${quoteIdent(c)}`).join(' AND ');
  const sets = dataCols.length > 0
    ? dataCols.map((c) => `s.${quoteIdent(c)} = t.${quoteIdent(c)}`).join(', ')
    : `s.${quoteIdent(pkCols[0])} = t.${quoteIdent(pkCols[0])}`;
  return `MERGE INTO ${qualTable(schema, table)} s USING (SELECT ${selectList} FROM DUAL) t ON (${wheres}) WHEN MATCHED THEN UPDATE SET ${sets} WHEN NOT MATCHED THEN INSERT (${cols}) VALUES (${allCols.map((c) => `t.${quoteIdent(c)}`).join(', ')})`;
}

export function buildInsertStatement(
  schema: string | null,
  table: string,
  targetCols: string[],
  placeholders: string,
): string {
  return `INSERT INTO ${qualTable(schema, table)} (${targetCols.map(quoteIdent).join(', ')}) VALUES (${placeholders})`;
}

export function buildStatements(
  schema: string | null,
  table: string,
  mappings: Mapping[],
  rows: string[][],
  targetCols: ColumnInfo[],
  opts: ImportOptions,
): { sql: string; params: Value[]; origIdx: number }[] {
  const activeIdx = mappings
    .map((m, i) => ({ m, i }))
    .filter(({ m }) => m.targetColumn != null)
    .map(({ i }) => i);

  if (activeIdx.length === 0) return [];

  let effectiveRows = rows;
  let origIdxOfStmt: number[];
  const hasFilter = opts.filterColumn !== null && opts.filterValue !== '';
  const hasValidation = opts.validations && Object.keys(opts.validations).length > 0;
  const strict = opts.strictValidation && hasValidation;
  const nullPolicies = opts.nullPolicies ?? {};
  const skipRowIdx = new Set<number>();
  for (const [iStr, p] of Object.entries(nullPolicies)) {
    if (p === 'skip-row') skipRowIdx.add(Number(iStr));
  }
  if (hasFilter || strict || skipRowIdx.size > 0) {
    const kept: string[][] = [];
    origIdxOfStmt = [];
    for (let i = 0; i < rows.length; i++) {
      if (hasFilter && !rowPassesFilter(rows[i], opts)) continue;
      if (strict) {
        const msgs = validateRow(rows[i], opts.validations);
        if (msgs.length > 0) continue;
      }
      if (skipRowIdx.size > 0) {
        let skip = false;
        for (const ci of skipRowIdx) {
          if ((rows[i][ci] ?? '') === '') { skip = true; break; }
        }
        if (skip) continue;
      }
      kept.push(rows[i]);
      origIdxOfStmt.push(i);
    }
    effectiveRows = kept;
  } else {
    origIdxOfStmt = rows.map((_, i) => i);
  }

  const colInfo = new Map(targetCols.map((c) => [c.name, c]));
  const pkCols = targetCols.filter((c) => c.is_primary_key).map((c) => c.name);
  const isPkIdx = (i: number) => pkCols.includes(mappings[i].targetColumn!);
  const idxForCol = (name: string) => activeIdx.find((i) => mappings[i].targetColumn === name);

  const convertParams = (row: string[]): Value[] =>
    activeIdx.map((i) => {
      const m = mappings[i];
      const col = m.targetColumn ? colInfo.get(m.targetColumn) ?? null : null;
      const raw = row[i] ?? '';
      // 预过滤已保证 skip-row 策略不会命中 SKIP_ROW
      return convertValue(
        raw, col, opts.emptyAsNull,
        opts.transforms?.[i],
        opts.transformParams?.[i],
        nullPolicies[i],
      ) as Value;
    });

  if (opts.mode === 'insert') {
    const names = activeIdx.map((i) => mappings[i].targetColumn!);
    const ph = names.map(() => '?').join(', ');
    const sql = buildInsertStatement(schema, table, names, ph);
    return effectiveRows.map((row, i) => ({ sql, params: convertParams(row), origIdx: origIdxOfStmt[i] }));
  }

  if (opts.mode === 'update') {
    if (pkCols.length === 0) return [];
    const dataCols = activeIdx.filter((i) => !isPkIdx(i)).map((i) => mappings[i].targetColumn!);
    if (dataCols.length === 0) return [];
    const sql = buildUpdateStatement(schema, table, dataCols, pkCols);
    return effectiveRows.map((row, i) => {
      const all = convertParams(row);
      const data = dataCols.map((name) => {
        const j = idxForCol(name);
        return j !== undefined ? all[j] : null;
      });
      const pk = pkCols.map((name) => {
        const j = idxForCol(name);
        return j !== undefined ? all[j] : null;
      });
      return { sql, params: [...data, ...pk], origIdx: origIdxOfStmt[i] };
    });
  }

  // upsert
  const pkIdx = activeIdx.filter((i) => isPkIdx(i));
  const dataIdx = activeIdx.filter((i) => !isPkIdx(i));
  const allIdx = [...pkIdx, ...dataIdx];
  const allNames = allIdx.map((i) => mappings[i].targetColumn!);
  const dataNames = dataIdx.map((i) => mappings[i].targetColumn!);

  if (pkCols.length === 0) {
    // 无 PK：upsert 退化为 INSERT
    const ph = allNames.map(() => '?').join(', ');
    const sql = buildInsertStatement(schema, table, allNames, ph);
    return effectiveRows.map((row, i) => {
      const all = convertParams(row);
      return { sql, params: allIdx.map((j) => all[j]), origIdx: origIdxOfStmt[i] };
    });
  }

  let sql: string;
  switch (opts.kind) {
    case 'postgres': sql = buildPgUpsert(schema, table, allNames, dataNames, pkCols); break;
    case 'mysql': sql = buildMySqlUpsert(schema, table, allNames, dataNames); break;
    case 'mssql': sql = buildMssqlUpsert(schema, table, allNames, dataNames, pkCols); break;
    case 'oracle': sql = buildOracleUpsert(schema, table, allNames, dataNames, pkCols); break;
    case 'sqlite': sql = buildPgUpsert(schema, table, allNames, dataNames, pkCols); break;
    default: sql = buildPgUpsert(schema, table, allNames, dataNames, pkCols); break;
  }
  return effectiveRows.map((row, i) => {
    const all = convertParams(row);
    return { sql, params: allIdx.map((j) => all[j]), origIdx: origIdxOfStmt[i] };
  });
}

export interface ColumnProfile {
  index: number;
  name: string;
  type: GuessType;
  nullCount: number;
  total: number;
  samples: string[];
}

const TYPE_COMPAT: Record<string, GuessType[]> = {
  int: ['integer', 'null'],
  bigserial: ['integer', 'null'],
  smallint: ['integer', 'null'],
  tinyint: ['integer', 'null'],
  serial: ['integer', 'null'],
  float: ['float', 'integer', 'null'],
  double: ['float', 'integer', 'null'],
  decimal: ['float', 'integer', 'null'],
  numeric: ['float', 'integer', 'null'],
  real: ['float', 'integer', 'null'],
  bool: ['boolean', 'null'],
  bit: ['boolean', 'null'],
  date: ['date', 'string', 'null'],
  timestamp: ['date', 'string', 'null'],
  time: ['date', 'string', 'null'],
  datetime: ['date', 'string', 'null'],
  text: ['string', 'integer', 'float', 'boolean', 'date', 'null'],
  varchar: ['string', 'integer', 'float', 'boolean', 'date', 'null'],
  char: ['string', 'integer', 'float', 'boolean', 'date', 'null'],
  blob: ['string', 'null'],
  clob: ['string', 'null'],
};

export function profileColumns(rows: string[][], columns: string[]): ColumnProfile[] {
  return columns.map((name, idx) => {
    const typeCounts: Record<GuessType, number> = {
      string: 0, integer: 0, float: 0, boolean: 0, date: 0, null: 0,
    };
    let nullCount = 0;
    const samples: string[] = [];
    for (const row of rows) {
      const v = row[idx] ?? '';
      if (v === '') { nullCount++; continue; }
      const t = guessType(v);
      typeCounts[t]++;
      if (samples.length < 3) samples.push(v);
    }
    let dominant: GuessType = 'string';
    let maxCount = -1;
    (Object.keys(typeCounts) as GuessType[]).forEach((t) => {
      if (typeCounts[t] > maxCount) { maxCount = typeCounts[t]; dominant = t; }
    });
    return {
      index: idx, name,
      type: nullCount === rows.length ? 'null' : dominant,
      nullCount, total: rows.length, samples,
    };
  });
}

export function compatible(targetDataType: string, gType: GuessType): boolean {
  const dt = targetDataType.toLowerCase();
  const matchKey = Object.keys(TYPE_COMPAT).find((k) => dt.includes(k));
  if (!matchKey) return true;
  return (TYPE_COMPAT[matchKey] ?? []).includes(gType);
}

export function chunk<T>(arr: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < arr.length; i += size) {
    out.push(arr.slice(i, i + size));
  }
  return out;
}

export interface BlankTemplateResult {
  csv: string;
  usedCols: number;
  skippedCols: number;
}

function sampleFor(dtype: string): string {
  const dt = dtype.toLowerCase();
  if (/auto|bigserial|serial|identity/.test(dt)) return '';
  if (/int|serial|bigserial|smallint|tinyint/.test(dt)) return '1';
  if (/float|double|decimal|numeric|real/.test(dt)) return '3.14';
  if (/bool|bit/.test(dt)) return 'true';
  if (/datetime|timestamp/.test(dt)) return '2024-03-15 10:30:00';
  if (/date/.test(dt)) return '2024-03-15';
  if (/blob|bytea|image/.test(dt)) return '';
  if (/json/.test(dt)) return '{"k":"v"}';
  return 'sample';
}

export function generateBlankTemplate(
  schema: string | null,
  table: string,
  cols: ColumnInfo[],
  opts: { includeAutoInc: boolean },
): BlankTemplateResult {
  const included = cols.filter((c) => opts.includeAutoInc || !c.is_auto_increment);
  if (included.length === 0) return { csv: '', usedCols: 0, skippedCols: cols.length };
  const header = included.map((c) => quoteIdent(c.name)).join(',');
  const line1 = included.map((c) => {
    const s = sampleFor(c.data_type);
    return s.includes(',') || s.includes('"') || s.includes('\n')
      ? `"${s.replace(/"/g, '""')}"`
      : s;
  }).join(',');
  return {
    csv: [header, line1, line1].join('\n'),
    usedCols: included.length,
    skippedCols: cols.length - included.length,
  };
}

export type ImportFormat = 'csv' | 'jsonl' | 'sql';

export type InputEncoding = 'utf-8' | 'gbk' | 'utf-16le' | 'utf-16be';

export interface ColumnValidation {
  required: boolean;
  regex: string;
  minValue: string;
  maxValue: string;
  enum: string;
}

export const EMPTY_VALIDATION: ColumnValidation = {
  required: false,
  regex: '',
  minValue: '',
  maxValue: '',
  enum: '',
};

export function hasAnyValidation(v: ColumnValidation | undefined): boolean {
  if (!v) return false;
  return v.required || v.regex !== '' || v.minValue !== '' || v.maxValue !== '' || v.enum !== '';
}

export function validateCell(
  raw: string,
  v: ColumnValidation | undefined,
): string | null {
  if (!v) return null;
  if (v.required && raw === '') return '必填字段为空';
  if (raw === '' && !v.required) return null;
  if (v.regex) {
    try {
      if (!new RegExp(v.regex).test(raw)) return `不匹配正则 /${v.regex}/`;
    } catch {
      return `无效正则: ${v.regex}`;
    }
  }
  if (v.enum) {
    const allowed = v.enum.split(',').map((s) => s.trim()).filter(Boolean);
    if (allowed.length > 0 && !allowed.includes(raw)) return `不在枚举 {${allowed.join(', ')}}`;
  }
  const n = Number(raw);
  if (!Number.isNaN(n) && v.minValue !== '' && v.maxValue === '') {
    const lo = Number(v.minValue);
    if (Number.isFinite(lo) && n < lo) return `小于下限 ${v.minValue}`;
  } else if (!Number.isNaN(n) && v.maxValue !== '' && v.minValue === '') {
    const hi = Number(v.maxValue);
    if (Number.isFinite(hi) && n > hi) return `大于上限 ${v.maxValue}`;
  } else if (!Number.isNaN(n) && v.minValue !== '' && v.maxValue !== '') {
    const lo = Number(v.minValue), hi = Number(v.maxValue);
    if (Number.isFinite(lo) && Number.isFinite(hi)) {
      if (n < lo) return `小于下限 ${v.minValue}`;
      if (n > hi) return `大于上限 ${v.maxValue}`;
    }
  }
  return null;
}

export function validateRow(
  row: string[],
  validations: Record<number, ColumnValidation>,
): string[] {
  const msgs: string[] = [];
  for (const idxStr of Object.keys(validations)) {
    const idx = Number(idxStr);
    const v = validations[idx];
    if (!hasAnyValidation(v)) continue;
    const raw = row[idx] ?? '';
    const msg = validateCell(raw, v);
    if (msg) msgs.push(`列${idx + 1}: ${msg}`);
  }
  return msgs;
}

export interface RowValidationResult {
  violations: { csvRow: number; reasons: string[] }[];
  perRowInvalid: boolean[];
}

export function validateAllRows(
  rows: string[][],
  validations: Record<number, ColumnValidation>,
): RowValidationResult {
  const perRowInvalid: boolean[] = [];
  const violations: { csvRow: number; reasons: string[] }[] = [];
  for (let i = 0; i < rows.length; i++) {
    const msgs = validateRow(rows[i], validations);
    perRowInvalid.push(msgs.length > 0);
    if (msgs.length > 0) violations.push({ csvRow: i + 1, reasons: msgs });
  }
  return { violations, perRowInvalid };
}

export function detectEncoding(buf: ArrayBuffer): { encoding: InputEncoding; text: string } {
  const b = new Uint8Array(buf);
  const first = (n: number) => Array.from(b.slice(0, n));
  if (b.length >= 2 && b[0] === 0xff && b[1] === 0xfe) {
    return { encoding: 'utf-16le', text: new TextDecoder('utf-16le').decode(b.slice(2)) };
  }
  if (b.length >= 2 && b[0] === 0xfe && b[1] === 0xff) {
    return { encoding: 'utf-16be', text: new TextDecoder('utf-16be').decode(b.slice(2)) };
  }
  if (b.length >= 3 && b[0] === 0xef && b[1] === 0xbb && b[2] === 0xbf) {
    return { encoding: 'utf-8', text: new TextDecoder('utf-8').decode(b.slice(3)) };
  }
  const utf8 = new TextDecoder('utf-8').decode(b);
  if (utf8.charCodeAt(0) !== 0xfffd && !utf8.includes('\uFFFD')) {
    return { encoding: 'utf-8', text: utf8 };
  }
  const gbk = new TextDecoder('gbk').decode(b);
  if (!gbk.includes('\uFFFD')) return { encoding: 'gbk', text: gbk };
  const utf16le = new TextDecoder('utf-16le').decode(b);
  if (!utf16le.includes('\uFFFD')) return { encoding: 'utf-16le', text: utf16le };
  return { encoding: 'utf-8', text: utf8 };
}

export function decodeWithEncoding(buf: ArrayBuffer, encoding: InputEncoding): string {
  const b = new Uint8Array(buf);
  if (encoding === 'utf-16le' && b.length >= 2 && b[0] === 0xff && b[1] === 0xfe) {
    return new TextDecoder('utf-16le').decode(b.slice(2));
  }
  if (encoding === 'utf-16be' && b.length >= 2 && b[0] === 0xfe && b[1] === 0xff) {
    return new TextDecoder('utf-16be').decode(b.slice(2));
  }
  if (encoding === 'utf-8' && b.length >= 3 && b[0] === 0xef && b[1] === 0xbb && b[2] === 0xbf) {
    return new TextDecoder('utf-8').decode(b.slice(3));
  }
  return new TextDecoder(encoding).decode(b);
}

export interface JsonlParseResult {
  columns: string[];
  rows: string[][];
  truncated: boolean;
  rawLines: number;
}

const JSON_MAX_ROWS = 100_000;

export function parseJsonl(input: string): { result: JsonlParseResult; error: string | null } {
  const trimmed = input.trim();
  if (!trimmed) return { result: { columns: [], rows: [], truncated: false, rawLines: 0 }, error: null };

  const toRows = (objs: Record<string, unknown>[]): JsonlParseResult => {
    const cols = new Set<string>();
    objs.forEach((o) => Object.keys(o).forEach((k) => cols.add(k)));
    const columns = Array.from(cols);
    const truncated = objs.length > JSON_MAX_ROWS;
    const rows = objs.slice(0, JSON_MAX_ROWS).map((o) =>
      columns.map((c) => {
        const v = o[c];
        if (v === null || v === undefined) return '';
        if (typeof v === 'boolean') return v ? 'true' : 'false';
        if (typeof v === 'number') return String(v);
        if (typeof v === 'string') return v;
        return JSON.stringify(v);
      })
    );
    return { columns, rows, truncated, rawLines: objs.length };
  };

  if (trimmed[0] === '[') {
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      if (!Array.isArray(parsed)) return { result: { columns: [], rows: [], truncated: false, rawLines: 0 }, error: '顶层 JSON 必须是对象数组' };
      const objs: Record<string, unknown>[] = [];
      for (const item of parsed) {
        if (item && typeof item === 'object' && !Array.isArray(item)) {
          objs.push(item as Record<string, unknown>);
        }
      }
      return { result: toRows(objs), error: null };
    } catch (e) {
      return { result: { columns: [], rows: [], truncated: false, rawLines: 0 }, error: `JSON 数组解析失败：${String(e)}` };
    }
  }

  const lines = trimmed.split(/\r?\n/);
  const objs: Record<string, unknown>[] = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    try {
      const parsed = JSON.parse(line) as unknown;
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        objs.push(parsed as Record<string, unknown>);
      } else {
        return { result: { columns: [], rows: [], truncated: false, rawLines: i + 1 }, error: `第 ${i + 1} 行不是 JSON 对象` };
      }
    } catch (e) {
      return { result: { columns: [], rows: [], truncated: false, rawLines: i + 1 }, error: `第 ${i + 1} 行 JSON 解析失败：${String(e)}` };
    }
  }
  if (objs.length === 0) return { result: { columns: [], rows: [], truncated: false, rawLines: 0 }, error: null };
  return { result: toRows(objs), error: null };
}
