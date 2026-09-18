import type { DatabaseKind } from '../api';
import { splitSql } from './sqlSplit';

export type DiagnosticSeverity = 'error' | 'warning' | 'info';

export interface SqlDiagnostic {
  startLine: number;
  startCol: number;
  endLine: number;
  endCol: number;
  message: string;
  severity: DiagnosticSeverity;
  code: string;
}

export interface LintResult {
  diagnostics: SqlDiagnostic[];
  errorCount: number;
  warningCount: number;
  infoCount: number;
}

export const SEV_NUM: Record<DiagnosticSeverity, number> = {
  error: 1,
  warning: 2,
  info: 3,
};

function lineColAt(sql: string, offset: number): { line: number; col: number } {
  const clamped = Math.max(0, Math.min(offset, sql.length));
  let line = 1;
  let lastNl = -1;
  for (let i = 0; i < clamped; i++) {
    if (sql[i] === '\n') { line++; lastNl = i; }
  }
  return { line, col: clamped - lastNl };
}

function skipWsForward(sql: string, offset: number): number {
  let i = offset;
  while (i < sql.length && /\s/.test(sql[i])) i++;
  return i;
}

function stmtStartPos(sql: string, st: { start: number }): { line: number; col: number } {
  return lineColAt(sql, skipWsForward(sql, st.start));
}

export function lintSql(sql: string, kind: DatabaseKind | null): LintResult {
  const diagnostics: SqlDiagnostic[] = [];
  const stmts = splitSql(sql);

  for (const st of stmts) {
    const trimmed = st.sql.trim();
    if (!trimmed) continue;
    // Lint 定位用 statement 起始行的第一个非空字符（近似即可）
    const first = lineColAt(sql, st.start);

    if (/^select\b/i.test(trimmed)) {
      if (/\bselect\s+\*\s+from\b/i.test(trimmed) || /^select\s+\*$/i.test(trimmed)) {
        diagnostics.push({
          startLine: first.line, startCol: first.col,
          endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
          message: '避免 `SELECT *`：显式列出列可减少扫描、防表结构变更影响下游',
          severity: 'warning',
          code: 'avoid-select-star',
        });
      }
      const hasLimit = /^(sqlite|postgres|mysql)$/i.test(kind ?? '')
        ? /\blimit\s+\d+/i.test(trimmed)
        : kind === 'oracle'
          ? /\bfetch\s+first\s+\d+\s+rows/i.test(trimmed)
          : kind === 'mssql'
            ? /\boffset\s+\d+\s+rows\s+fetch\s+next\s+\d+\s+rows/i.test(trimmed)
            : /\blimit\s+\d+/i.test(trimmed);
      if (!hasLimit) {
        diagnostics.push({
          startLine: first.line, startCol: first.col,
          endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
          message: '大结果集未限制：建议加 LIMIT / FETCH FIRST 或 OFFSET FETCH',
          severity: 'warning',
          code: 'missing-limit',
        });
      }
    }

    if (/^update\b/i.test(trimmed)) {
      if (!/\bwhere\b/i.test(trimmed)) {
        diagnostics.push({
          startLine: first.line, startCol: first.col,
          endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
          message: '`UPDATE` 缺少 WHERE 子句，将修改全表',
          severity: 'error',
          code: 'update-missing-where',
        });
      }
    }
    if (/^delete\b/i.test(trimmed)) {
      if (!/\bwhere\b/i.test(trimmed)) {
        diagnostics.push({
          startLine: first.line, startCol: first.col,
          endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
          message: '`DELETE` 缺少 WHERE 子句，将删除全表',
          severity: 'error',
          code: 'delete-missing-where',
        });
      }
    }

    if (/\bdrop\s+table\b/i.test(trimmed) || /\bdrop\s+database\b/i.test(trimmed)) {
      if (!/\bif\s+exists\b/i.test(trimmed)) {
        diagnostics.push({
          startLine: first.line, startCol: first.col,
          endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
          message: '破坏性 DDL：建议加 IF EXISTS 或先确认目标存在',
          severity: 'warning',
          code: 'destructive-ddl',
        });
      }
    }

    if (/\/\*[\s\S]*?\*/.test(st.sql)) {
      const m = /\/\*/.exec(st.sql);
      const offset = m ? m.index : st.start;
      const pos = lineColAt(sql, st.start + offset);
      diagnostics.push({
        startLine: pos.line, startCol: pos.col,
        endLine: pos.line, endCol: pos.col + 2,
        message: '包含块注释 /* ... */',
        severity: 'info',
        code: 'has-block-comment',
      });
    }

    if (/\bgroup\s+by\b/i.test(trimmed) && !/\bhaving\b/i.test(trimmed) && /\b(count|sum|avg|max|min)\s*\(/i.test(trimmed)) {
      diagnostics.push({
        startLine: first.line, startCol: first.col,
        endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
        message: '`GROUP BY` 聚合查询可考虑加 HAVING 过滤聚合结果',
        severity: 'info',
        code: 'group-by-no-having',
      });
    }

    if (/^select\b/i.test(trimmed) && /\bcount\s*\(\s*distinct\s+/i.test(trimmed)) {
      diagnostics.push({
        startLine: first.line, startCol: first.col,
        endLine: first.line, endCol: Math.max(first.col + 1, first.col + 12),
        message: '`COUNT(DISTINCT ...)` 在数据量大时性能较差，可用子查询/窗口函数替代',
        severity: 'info',
        code: 'count-distinct-cost',
      });
    }

    if (!/\s*;$/.test(st.sql)) {
      const end = lineColAt(sql, st.end);
      diagnostics.push({
        startLine: end.line, startCol: end.col,
        endLine: end.line, endCol: end.col + 1,
        message: '建议以 `;` 结尾',
        severity: 'info',
        code: 'no-semicolon',
      });
    }
  }

  const errorCount = diagnostics.filter((d) => d.severity === 'error').length;
  const warningCount = diagnostics.filter((d) => d.severity === 'warning').length;
  const infoCount = diagnostics.filter((d) => d.severity === 'info').length;
  return { diagnostics, errorCount, warningCount, infoCount };
}

export function lintResultToMonaco(result: LintResult): { severity: number; message: string; source?: string }[] {
  return result.diagnostics.map((d) => ({
    severity: SEV_NUM[d.severity],
    message: d.message,
    source: d.code,
  }));
}

export function severityLabel(r: LintResult): string {
  if (r.errorCount + r.warningCount + r.infoCount === 0) return 'clean';
  const parts: string[] = [];
  if (r.errorCount) parts.push(`${r.errorCount} error`);
  if (r.warningCount) parts.push(`${r.warningCount} warn`);
  if (r.infoCount) parts.push(`${r.infoCount} info`);
  return parts.join(' · ');
}
