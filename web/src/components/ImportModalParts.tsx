import type { CSSProperties, MutableRefObject } from 'react';
import { ApiError } from '../lib/api';
import { failCategoryMeta } from '../lib/importDisplay';
import { listPresets } from '../lib/importPresets';
import { quoteIdent, type ImportFormat, type ImportMode, type ImportOptions, type Mapping, type InputEncoding, type ColumnProfile } from '../lib/importData';
import { type Delimiter, type CsvParseResult } from '../lib/csvParser';
import { type SqlStatement } from '../lib/sqlSplit';
import { type LintResult, severityLabel } from '../lib/sqlLint';
import { type DataQualityReport, type QualityTransformSuggestion } from '../lib/dataQuality';
import { publishEditorStatus } from '../lib/statusBus';
import type { PresetAuditStatus } from '../lib/importPresets';
import type { ColumnInfo, DatabaseKind, SchemaInfo, TableInfo, Value } from '../api';

export type Step = 'input' | 'table' | 'map' | 'preview' | 'done';

export const STEP_LABEL: Record<Step, string> = {
  input: '1 · 粘贴或选择数据',
  table: '2 · 目标表 & 模式',
  map: '3 · 列映射 & 转换',
  preview: '4 · 预览 & 执行',
  done: '5 · 完成',
};
export const STEP_ORDER: Step[] = ['input', 'table', 'map', 'preview', 'done'];

// M30.51 向导头部步骤徽章：可点击跳步（回跳恒可；前进按各步前置守卫），done 恒禁
export function StepBar({ step, curStepIdx, stepWarnFlags, stepFlash, parse, selTable, mappedCount, setStep }: {
  step: Step;
  curStepIdx: number;
  stepWarnFlags: Partial<Record<Step, boolean>>;
  stepFlash: boolean;
  parse: unknown;
  selTable: unknown;
  mappedCount: number;
  setStep: (s: Step) => void;
}) {
  return (
    <div style={styles.stepBar}>
      {STEP_ORDER.map((s, i) => {
        const isCurrent = step === s;
        const isDone = i < curStepIdx || (step === 'done' && s !== 'done');
        const hasWarn = !isCurrent && !isDone && stepWarnFlags[s];
        const icon = isCurrent
          ? (hasWarn ? '⚠' : '⏳')
          : isDone
            ? (stepWarnFlags[s] ? '⚠' : '✓')
            : hasWarn
              ? '⚠'
              : '·';
        const iconColor = isCurrent
          ? (hasWarn ? 'var(--warning, #f59e0b)' : 'var(--accent, #3b82f6)')
          : isDone
            ? (stepWarnFlags[s] ? 'var(--warning, #f59e0b)' : 'var(--success, #10b981)')
            : hasWarn
              ? 'var(--warning, #f59e0b)'
              : 'var(--muted)';
        const iconTitle = isCurrent
          ? (hasWarn ? '当前步骤（有警告需处理）' : '当前步骤')
          : isDone
            ? (stepWarnFlags[s] ? '已完成（含警告）' : '已完成')
            : hasWarn
              ? '尚未开始（有警告信号）'
              : '尚未开始';
        // 前置数据守卫：跳步必须满足前置
        let canNav = false;
        let navTitle: string;
        if (s === 'done') {
          canNav = false;
          navTitle = '完成步只可通过执行导入进入';
        } else if (i < curStepIdx) {
          canNav = true;
          navTitle = `回跳「${STEP_LABEL[s]}」`;
        } else if (i > curStepIdx) {
          if (i === 1) { canNav = !!parse; navTitle = parse ? `前进「${STEP_LABEL[s]}」` : '需先解析数据'; }
          else if (i === 2) { canNav = !!parse && !!selTable; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需先选目标表'; }
          else if (i === 3) { canNav = !!parse && !!selTable && mappedCount > 0; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需至少映射一列'; }
          else { canNav = false; navTitle = '不可前进'; }
        } else {
          canNav = false;
          navTitle = '当前步骤';
        }
        return (
          <div
            key={s}
            className={isCurrent && stepFlash ? 'step-badge-flash' : undefined}
            style={{
              ...styles.stepItem,
              ...(step === s ? styles.stepItemActive : {}),
              ...(i < curStepIdx ? styles.stepItemDone : {}),
              display: 'flex', alignItems: 'center', gap: 4,
              cursor: canNav ? 'pointer' : 'default',
              opacity: canNav ? 1 : 0.7,
              userSelect: 'none',
            }}
            title={canNav ? `${navTitle}（${iconTitle}；Ctrl+Alt+${i + 1}）` : `${iconTitle}（${navTitle}）`}
            role="button"
            aria-disabled={!canNav}
            onClick={() => {
              if (!canNav || isCurrent) return;
              setStep(s);
            }}
          >
            <span style={{ color: iconColor, fontWeight: 700, fontSize: 10, flexShrink: 0, minWidth: 10, textAlign: 'center' }}>{icon}</span>
            <span>{STEP_LABEL[s]}</span>
          </div>
        );
      })}
      {/* M30.96 向导头部键盘提示徽标：绝对定位 Ctrl+Alt+1..4 与相对遍历 Ctrl+Alt+[ ] */}
      <button
        style={{
          marginLeft: 'auto',
          padding: '0 6px',
          background: 'transparent',
          border: '1px solid var(--border)',
          borderRadius: 3,
          color: 'var(--muted)',
          cursor: 'help',
          fontSize: 10,
          flexShrink: 0,
          lineHeight: '16px',
          display: 'flex',
          alignItems: 'center',
          gap: 2,
        }}
        title={
          [
            '向导键盘快捷键：',
            'Ctrl+Alt+1 → 粘贴或选择数据',
            'Ctrl+Alt+2 → 目标表 & 模式',
            'Ctrl+Alt+3 → 列映射 & 转换',
            'Ctrl+Alt+4 → 预览 & 执行',
            'Ctrl+Alt+[  → 上一步',
            'Ctrl+Alt+]  → 下一步',
            'Ctrl+Enter → Step 4 提交导入',
            'Ctrl+/    → 快捷键速查（全部）',
            '完成步只可通过执行导入进入',
          ].join('\n')
        }
        aria-label="显示向导键盘快捷键"
      >⌨</button>
    </div>
  );
}

// M30 示例数据：各数据库方言的 CSV / JSON 样例（Step 1 占位/填充用）
const SAMPLE_CSV: Record<string, { title: string; csv: string; json: string }> = {
  sqlite: {
    title: 'SQLite 示例',
    csv: `id,name,email,score,active
1,Alice,alice@example.com,95.5,true
2,Bob,bob@example.com,82.0,false
3,Charlie,charlie@example.com,77.75,true`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true}`,
  },
  postgres: {
    title: 'PostgreSQL 示例',
    csv: `id,name,email,score,active,born_at
1,Alice,alice@example.com,95.5,true,2024-03-15 10:30:00
2,Bob,bob@example.com,82.0,false,2024-05-20
3,Charlie,charlie@example.com,77.75,true,2025-01-05 09:00:00`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true,"born_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false,"born_at":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true,"born_at":"2025-01-05 09:00:00"}`,
  },
  mysql: {
    title: 'MySQL 示例',
    csv: `id,name,email,score,active,created_at
1,Alice,alice@example.com,95.5,1,'2024-03-15 10:30:00'
2,Bob,bob@example.com,82.0,0,'2024-05-20 08:00:00'
3,Charlie,charlie@example.com,77.75,1,'2025-01-05 09:00:00'`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_at":"2024-05-20 08:00:00"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_at":"2025-01-05 09:00:00"}`,
  },
  mssql: {
    title: 'MSSQL 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
  oracle: {
    title: 'Oracle 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
};

function sampleForKind(kind: DatabaseKind | null, format: ImportFormat): { title: string; text: string } {
  const s = (kind && SAMPLE_CSV[kind]) ?? SAMPLE_CSV.sqlite;
  return { title: s.title, text: format === 'jsonl' ? s.json : s.csv };
}

// M30.02 粘贴/选择数据步（Step 1）：格式/分隔符/表头/编码 + 内容粘贴 + 文件队列
// + SQL 解析预览 + Live Lint + 数据质量评分（含建议保存 / JSON 导出）。
export function Step1Input(p: {
  kind: DatabaseKind | null;
  inputFormat: ImportFormat;
  setInputFormat: (f: ImportFormat) => void;
  delimiter: Delimiter | null;
  setDelimiter: (d: Delimiter) => void;
  hasHeader: boolean;
  setHasHeader: (v: boolean) => void;
  encoding: InputEncoding | 'auto';
  redecodeWith: (enc: InputEncoding | 'auto') => void;
  csvText: string;
  setCsvText: (v: string) => void;
  csvTextRef: MutableRefObject<HTMLTextAreaElement | null>;
  csvRowFlash: number | null;
  fileInputRef: MutableRefObject<HTMLInputElement | null>;
  fileName: string | null;
  parse: CsvParseResult | null;
  detectedEncoding: InputEncoding | null;
  sqlStatements: SqlStatement[];
  sqlParamCounts: number[];
  sqlLintResult: LintResult | null;
  profiles: ColumnProfile[];
  qualityReport: DataQualityReport | null;
  qualityGate: { enabled: boolean; threshold: number };
  qualityGateBlocked: boolean;
  setPendingQualitySuggestions: (m: Record<number, QualityTransformSuggestion[]>) => void;
  selSchema: string;
  selTable: string;
  fileQueue: { file: File; format: ImportFormat; name: string }[];
  queuePos: number;
  cancelQueue: () => void;
  enqueueFiles: (files: File[]) => void;
  handleFile: (f: File, fmt?: ImportFormat) => Promise<void>;
  detectFormatFromFile: (name: string) => ImportFormat;
  handleParse: () => void;
}) {
  const {
    kind, inputFormat, setInputFormat, delimiter, setDelimiter, hasHeader, setHasHeader,
    encoding, redecodeWith, csvText, setCsvText, csvTextRef, csvRowFlash, fileInputRef,
    fileName, parse, detectedEncoding, sqlStatements, sqlParamCounts, sqlLintResult,
    profiles, qualityReport, qualityGate, qualityGateBlocked, setPendingQualitySuggestions,
    selSchema, selTable, fileQueue, queuePos, cancelQueue, enqueueFiles, handleFile,
    detectFormatFromFile, handleParse,
  } = p;

  const fillSample = () => {
    const s = sampleForKind(kind, inputFormat);
    setCsvText(s.text);
    if (inputFormat === 'csv' && kind === 'sqlite') setDelimiter(',');
  };

  return (
    <div style={styles.col}>
      <div style={styles.row2col}>
        <label style={styles.field}>
          <div style={styles.label}>格式</div>
          <select
            value={inputFormat}
            onChange={(e) => setInputFormat(e.target.value as ImportFormat)}
            style={styles.select}
          >
            <option value="csv">CSV</option>
            <option value="jsonl">JSONL / JSON 数组</option>
            <option value="sql">SQL 多语句文件</option>
          </select>
        </label>
        {inputFormat === 'csv' && (
          <label style={styles.field}>
            <div style={styles.label}>分隔符</div>
            <select
              value={delimiter ?? ','}
              onChange={(e) => {
                const v = e.target.value;
                const d: Delimiter = v === 'TAB' ? '\t' : (v === '|' ? '|' : v === ';' ? ';' : ',');
                setDelimiter(d);
              }}
              style={styles.select}
            >
              <option value=",">逗号 ,</option>
              <option value=";">分号 ;</option>
              <option value="TAB">制表符 Tab</option>
              <option value="|">竖线 |</option>
            </select>
          </label>
        )}
        {inputFormat === 'csv' && (
          <label style={styles.field}>
            <div style={styles.label}>首行是表头</div>
            <label style={styles.check}>
              <input type="checkbox" checked={hasHeader} onChange={(e) => setHasHeader(e.target.checked)} />
              <span>启用</span>
            </label>
          </label>
        )}
        {inputFormat !== 'sql' && (
          <label style={styles.field}>
            <div style={styles.label}>编码</div>
            <select
              value={encoding}
              onChange={(e) => redecodeWith(e.target.value as InputEncoding | 'auto')}
              style={styles.select}
              title="选择文件后生效；粘贴的文本使用浏览器默认编码"
            >
              <option value="auto">自动检测</option>
              <option value="utf-8">UTF-8</option>
              <option value="gbk">GBK / GB2312（Windows 中文）</option>
              <option value="utf-16le">UTF-16 LE</option>
              <option value="utf-16be">UTF-16 BE</option>
            </select>
          </label>
        )}
      </div>
      <div style={styles.label}>
        {inputFormat === 'sql' ? 'SQL 内容（多语句，按分号切分）' : (inputFormat === 'csv' ? 'CSV 内容' : 'JSONL / JSON 数组内容')}
      </div>
      <textarea
        ref={csvTextRef}
        value={csvText}
        onChange={(e) => setCsvText(e.target.value)}
        placeholder={inputFormat === 'sql'
          ? '粘贴 SQL 文件内容，或用「选择文件」载入 .sql\n支持多语句按分号切分，示例：\nCREATE TABLE ...;\nINSERT INTO ... VALUES (...);\nSELECT COUNT(*) FROM ...;'
          : inputFormat === 'csv'
            ? `粘贴 CSV，或点上方"选择文件"、拖拽文件到窗口…\n\n示例：\n${sampleForKind(kind, 'csv').text}`
            : `粘贴 JSONL（每行一个对象）或 JSON 数组…\n\n示例：\n${sampleForKind(kind, 'jsonl').text}`}
        style={{
          ...styles.textarea,
          ...(csvRowFlash != null
            ? {
                outline: '2px solid var(--accent, #3b82f6)',
                outlineOffset: -2,
                boxShadow: '0 0 0 3px rgba(59,130,246,0.25), inset 0 0 12px rgba(59,130,246,0.15)',
                transition: 'box-shadow 0.3s ease',
              }
            : {}),
        }}
        spellCheck={false}
      />
      <div style={styles.row2col}>
        <button
          onClick={() => {
            if (fileInputRef.current) fileInputRef.current.multiple = false;
            if (fileInputRef.current) fileInputRef.current.click();
          }}
          style={styles.btn}
          title="选择单个文件"
        >📂 选择文件</button>
        <button
          onClick={() => {
            if (fileInputRef.current) fileInputRef.current.multiple = true;
            if (fileInputRef.current) fileInputRef.current.click();
          }}
          style={styles.btnGhost}
          title="选择多个文件按顺序导入（完成后自动加载下一个）"
        >📚 多文件批量…</button>
        <button onClick={fillSample} style={styles.btnGhost}>
          填入 {sampleForKind(kind, inputFormat === 'sql' ? 'csv' : inputFormat).title}
        </button>
        <input
          ref={fileInputRef}
          type="file"
          accept={inputFormat === 'csv'
            ? '.csv,.tsv,.txt,text/csv,text/plain'
            : inputFormat === 'sql'
              ? '.sql,.txt,text/plain'
              : '.jsonl,.json,.ndjson,.txt,text/plain,application/json'}
          style={{ display: 'none' }}
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = '';
            if (files.length === 0) return;
            if (files.length > 1) {
              enqueueFiles(files);
            } else {
              void handleFile(files[0], detectFormatFromFile(files[0].name));
            }
          }}
        />
      </div>
      {fileName || parse || detectedEncoding ? (() => {
        const curFile = fileQueue[queuePos];
        const size = curFile?.file.size ?? null;
        const sizeStr = size != null
          ? (size < 1024 ? `${size} B` : size < 1024 * 1024 ? `${(size / 1024).toFixed(1)} KB` : `${(size / 1024 / 1024).toFixed(2)} MB`)
          : null;
        const items: Array<{ k: string; v: string; color?: string; title?: string }> = [];
        if (fileName) items.push({ k: '文件', v: fileName, title: '已载入的文件名' });
        if (sizeStr) items.push({ k: '大小', v: sizeStr, title: '文件字节大小' });
        if (detectedEncoding) items.push({ k: '编码', v: detectedEncoding, title: '自动检测或用户选择的编码' });
        if (inputFormat === 'csv' && delimiter) items.push({ k: '分隔符', v: delimiter === '\t' ? 'Tab' : delimiter, title: '当前使用的分隔符' });
        if (inputFormat === 'csv' && parse) {
          items.push({ k: '行 × 列', v: `${parse.rows.length.toLocaleString()} × ${parse.columns.length}` });
          if (parse.truncated) items.push({ k: '状态', v: '已截断 20 万行', color: 'var(--warn, #d97706)' });
        } else if (inputFormat === 'jsonl' && parse) {
          items.push({ k: '记录 × 字段', v: `${parse.rows.length.toLocaleString()} × ${parse.columns.length}` });
        } else if (inputFormat === 'sql') {
          if (sqlStatements.length > 0) items.push({ k: 'SQL 语句', v: `${sqlStatements.length} 条` });
        }
        if (items.length === 0) return null;
        return (
          <div style={{
            display: 'flex', flexWrap: 'wrap', gap: '4px 12px',
            padding: '4px 8px', marginBottom: 4,
            background: 'rgba(255,255,255,0.02)',
            border: '1px solid var(--border)',
            borderLeft: '3px solid var(--ok, #10b981)',
            borderRadius: 3, fontSize: 11,
          }}>
            <span style={{ color: 'var(--ok, #10b981)', fontWeight: 600, marginRight: 4 }}>📄</span>
            {items.map((it, i) => (
              <span key={i} style={{ color: 'var(--muted)' }} title={it.title}>
                {it.k}：<strong style={{ color: it.color ?? 'var(--text)' }}>{it.v}</strong>
              </span>
            ))}
          </div>
        );
      })() : null}
      {fileQueue.length > 0 && (
        <div style={styles.mutedBox}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
            <span style={{ fontWeight: 600 }}>📚 文件队列（{fileQueue.length} 个，已处理 {queuePos} 个）</span>
            <span style={styles.spacer} />
            <button
              onClick={cancelQueue}
              style={{ ...styles.btnSm, fontSize: 11, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
              title="取消整个导入队列（若正在执行会同时取消当前批次）"
            >⏹ 取消整个队列</button>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
            {fileQueue.map((q, i) => (
              <span key={i} style={{
                ...styles.typeBadge,
                fontSize: 10,
                color: i === queuePos ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                borderColor: i === queuePos ? 'var(--accent, #3b82f6)' : 'var(--border)',
              }}>
                {i < queuePos ? '✓ ' : ''}{i + 1}. {q.name}
              </span>
            ))}
          </div>
        </div>
      )}
      {inputFormat === 'sql' && (
        <div style={styles.mutedBox}>
          {sqlStatements.length > 0 ? (
            <>已解析 <strong>{sqlStatements.length}</strong> 条 SQL 语句 · 总参数 {sqlParamCounts.reduce((a, b) => a + b, 0)} 个</>
          ) : csvText.trim() ? (
            <span style={{ color: 'var(--warn, #d97706)' }}>⚠ 未检测到分号结尾语句</span>
          ) : null}
          <details style={{ marginTop: 4 }}>
            <summary style={{ cursor: 'pointer', fontSize: 11 }}>预览前 5 条</summary>
            <pre style={{ ...styles.pre, marginTop: 4 }}>
              {sqlStatements.slice(0, 5).map((s, i) => `#${i + 1}: ${s.sql}`).join('\n')}
            </pre>
          </details>
          {sqlLintResult && (sqlLintResult.errorCount + sqlLintResult.warningCount + sqlLintResult.infoCount) > 0 && (
            <div style={{ marginTop: 6, padding: 6, background: 'var(--bg-secondary, rgba(0,0,0,0.05))', borderRadius: 4, border: '1px solid var(--border)' }}>
              <div style={{ fontSize: 11, fontWeight: 600, marginBottom: 4 }}>
                <span style={{ color: 'var(--danger, #dc2626)' }}>🛡 Live Lint：{severityLabel(sqlLintResult)}</span>
              </div>
              <div style={{ ...styles.failedWrap, maxHeight: 180 }}>
                <table style={styles.failedTable}>
                  <thead>
                    <tr>
                      <th style={styles.failedTh}>行</th>
                      <th style={styles.failedTh}>级别</th>
                      <th style={styles.failedTh}>消息</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sqlLintResult.diagnostics.slice(0, 30).map((d, i) => (
                      <tr key={i}>
                        <td style={styles.failedTd}>L{d.startLine}:{d.startCol}</td>
                        <td style={{ ...styles.failedTd, color: d.severity === 'error' ? 'var(--danger, #dc2626)' : d.severity === 'warning' ? 'var(--warn, #d97706)' : 'var(--muted)' }}>
                          {d.severity === 'error' ? '✕' : d.severity === 'warning' ? '⚠' : 'ℹ'} {d.severity}
                        </td>
                        <td style={styles.failedTd} title={d.message}>{d.message}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {sqlLintResult.diagnostics.length > 30 && (
                  <div style={{ ...styles.muted, textAlign: 'center', padding: '2px 0' }}>… 共 {sqlLintResult.diagnostics.length} 条</div>
                )}
              </div>
            </div>
          )}
        </div>
      )}
      {parse && parse.rows.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={styles.mutedBox}>
            已解析 <strong>{parse.rows.length}</strong> 行 × <strong>{parse.columns.length}</strong> 列
            {parse.truncated && <span style={{ color: 'var(--warn, #d97706)' }}> · 已截断至 20 万行</span>}
          </div>
          <div style={styles.label}>前 10 行预览</div>
          <div style={styles.previewWrap}>
            <table style={styles.previewTable}>
              <thead>
                <tr>
                  <th style={styles.previewTh}>#</th>
                  {parse.columns.map((c, i) => {
                    const p = profiles[i];
                    return (
                      <th key={i} style={styles.previewTh} title={p ? `${p.type} · null ${p.nullCount}/${p.total}` : c}>
                        <div style={styles.previewThInner}>
                          <span>{c}</span>
                          {p && <span style={{ ...styles.typeBadge, color: typeBadgeColor(p.type) }}>{p.type}</span>}
                        </div>
                        {p && p.nullCount > 0 && <div style={styles.previewThSub}>{p.nullCount} 空</div>}
                      </th>
                    );
                  })}
                </tr>
              </thead>
              <tbody>
                {parse.rows.slice(0, 10).map((row, ri) => (
                  <tr key={ri}>
                    <td style={styles.previewTdMuted}>{ri + 1}</td>
                    {parse.columns.map((_, ci) => {
                      const v = row[ci] ?? '';
                      const isNull = v === '';
                      return (
                        <td key={ci} style={{ ...styles.previewTd, ...(isNull ? styles.previewTdNull : {}) }} title={v}>
                          {isNull ? <em>NULL</em> : v}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {qualityReport && (
        <div style={{ ...styles.mutedBox, borderLeft: '3px solid var(--accent)', display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span style={styles.label}>📊 数据质量</span>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flex: 1, minWidth: 200 }}>
              <div style={{ ...styles.progressOuter, flex: 1, maxWidth: 240 }}>
                <div style={{
                  ...styles.progressInner,
                  width: `${qualityReport.overallScore}%`,
                  background: qualityScoreColor(qualityReport.overallScore),
                }} />
              </div>
              <strong style={{ fontSize: 13, color: qualityScoreColor(qualityReport.overallScore), minWidth: 44, textAlign: 'right' }}>
                {qualityReport.overallScore}/100
              </strong>
            </div>
            {qualityReport.issues.length > 0 && (
              <span style={{ fontSize: 11, color: 'var(--warn, #d97706)' }}>
                {qualityReport.issues.length} 项待关注
              </span>
            )}
            {(() => {
              const totalSugg = qualityReport.columns.reduce((n, c) => n + c.suggestions.length, 0);
              if (totalSugg === 0) return null;
              return (
                <button
                  onClick={() => {
                    const map: Record<number, QualityTransformSuggestion[]> = {};
                    for (const c of qualityReport.columns) {
                      if (c.suggestions.length > 0) map[c.index] = c.suggestions;
                    }
                    setPendingQualitySuggestions(map);
                    publishEditorStatus({
                      message: `已保存 ${totalSugg} 条转换建议，进入 Step 3 后自动应用`,
                      messageAt: Date.now(),
                    });
                  }}
                  style={{ ...styles.btnSm, fontSize: 11 }}
                  title="将可疑值建议（Trim / 去前导 0 / 正则去除尾引号 / 日期转 ISO）保存到下一步的列映射"
                >🎯 保存建议（{totalSugg} 条）</button>
              );
            })()}
            <button
              onClick={() => {
                const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
                const target = selSchema && selTable ? `${selSchema}.${selTable}` : 'unmapped';
                const report = {
                  schema_version: 'polydb.dataQuality.v1',
                  generated_at: new Date().toISOString(),
                  source_file: fileName || 'inline-paste',
                  input_format: inputFormat,
                  encoding: detectedEncoding ?? 'unknown',
                  target: { kind: kind ?? null, schema: selSchema || null, table: selTable || null },
                  row_count: parse?.rows.length ?? 0,
                  column_count: parse?.columns.length ?? 0,
                  overall_score: qualityReport.overallScore,
                  gate: { enabled: qualityGate.enabled, threshold: qualityGate.threshold, passed: !qualityGateBlocked },
                  issues: qualityReport.issues,
                  columns: qualityReport.columns.map((c) => ({
                    index: c.index,
                    name: c.name,
                    total: c.total,
                    non_null: c.nonNull,
                    completeness: c.completeness,
                    dominant_type: c.dominantType,
                    type_purity: c.typePurity,
                    unique_ratio: c.uniqueRatio,
                    suspicious_count: c.suspiciousCount,
                    suspicious_breakdown: c.suspiciousBreakdown,
                    score: c.score,
                    issues: c.issues,
                    suggestions: c.suggestions,
                  })),
                };
                const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = `quality-${target}-${stamp}.json`;
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
                publishEditorStatus({ message: `📥 已导出质量报告 ${a.download}`, messageAt: Date.now() });
              }}
              style={{ ...styles.btnSm, fontSize: 11 }}
              title="导出当前质量评估为 JSON 快照（含每列明细 + 分类统计 + 门禁状态），便于审计/CI 追溯"
            >📥 导出 JSON</button>
          </div>
          <details>
            <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--muted)', userSelect: 'none' }}>
              查看每列评分
            </summary>
            <div style={{ marginTop: 6, fontSize: 11, maxHeight: 200, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
              <table style={{ width: '100%', borderCollapse: 'collapse', fontFamily: 'monospace' }}>
                <thead>
                  <tr>
                    <th style={styles.historyTh}>列</th>
                    <th style={styles.historyTh}>分数</th>
                    <th style={styles.historyTh}>完整度</th>
                    <th style={styles.historyTh}>主类型</th>
                    <th style={styles.historyTh}>可疑值</th>
                    <th style={styles.historyTh}>备注</th>
                  </tr>
                </thead>
                <tbody>
                  {qualityReport.columns.map((c) => (
                    <tr key={c.index}>
                      <td style={styles.historyTd} title={c.name}>{c.name}</td>
                      <td style={styles.historyTd} title={`${c.score}/100`}>
                        <strong style={{ color: qualityScoreColor(c.score) }}>{c.score}</strong>
                      </td>
                      <td style={styles.historyTd}>{(c.completeness * 100).toFixed(0)}%</td>
                      <td style={styles.historyTd}>{c.dominantType}</td>
                      <td style={styles.historyTd}>{c.suspiciousCount}</td>
                      <td style={{ ...styles.historyTd, maxWidth: 260, whiteSpace: 'normal', wordBreak: 'break-word' }}>
                        {c.issues.length === 0 ? <span style={{ color: 'var(--ok)' }}>✓</span> : c.issues.join(' · ')}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        </div>
      )}
      <div style={styles.footer}>
        {fileName && <span style={styles.muted}>文件：{fileName}</span>}
        {detectedEncoding && <span style={styles.muted}> · 编码：{detectedEncoding}</span>}
        <span style={styles.spacer} />
        <button onClick={handleParse} disabled={!csvText.trim()} style={styles.btnPrimary}>下一步 →</button>
      </div>
    </div>
  );
}

// M30.48 目标表 & 模式步：schema/表选择 + 导入模式 + 最近使用表推荐 + PK 提示
export function Step2Table({ connId, selSchema, setSelSchema, selTable, setSelTable, setCols, setMappings, opts, setOpts, schemaLoading, tablesLoading, colsLoading, schemas, tables, cols, parse, setStep }: {
  connId: string;
  selSchema: string;
  setSelSchema: (v: string) => void;
  selTable: string;
  setSelTable: (v: string) => void;
  setCols: (cols: ColumnInfo[]) => void;
  setMappings: (m: Mapping[]) => void;
  opts: ImportOptions;
  setOpts: (o: ImportOptions) => void;
  schemaLoading: boolean;
  tablesLoading: boolean;
  colsLoading: boolean;
  schemas: SchemaInfo[];
  tables: TableInfo[];
  cols: ColumnInfo[];
  parse: { rows: unknown[]; columns: string[]; truncated?: boolean } | null;
  setStep: (s: Step) => void;
}) {
  return (
    <div style={styles.col}>
      <div style={styles.row2col}>
        <label style={styles.field}>
          <div style={styles.label}>Schema</div>
          <select
            value={selSchema}
            onChange={(e) => { setSelSchema(e.target.value); setSelTable(''); setCols([]); setMappings([]); }}
            style={styles.select}
            disabled={schemaLoading}
          >
            {schemas.length === 0 && <option value="">（加载中…）</option>}
            {schemas.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
          </select>
        </label>
        <label style={styles.field}>
          <div style={styles.label}>表（选中自动加载列）</div>
          <select
            value={selTable}
            onChange={(e) => { setSelTable(e.target.value); setCols([]); setMappings([]); }}
            style={styles.select}
            disabled={tablesLoading || !selSchema}
          >
            {tables.length === 0 && <option value="">（{selSchema ? '加载中…' : '请先选 schema'}）</option>}
            {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
          </select>
        </label>
        <label style={styles.field}>
          <div style={styles.label}>导入模式</div>
          <select
            value={opts.mode}
            onChange={(e) => setOpts({ ...opts, mode: e.target.value as ImportMode })}
            style={styles.select}
          >
            <option value="insert">Insert（插入新行）</option>
            <option value="upsert">Upsert（存在则更新）</option>
            <option value="update">Update（仅更新已有 PK 行）</option>
          </select>
        </label>
      </div>
      {(() => {
        const presets = listPresets(connId);
        if (presets.length === 0) return null;
        const sorted = presets.slice().sort((a, b) => b.updatedAt - a.updatedAt);
        const same = selSchema ? sorted.filter((p) => p.schema === selSchema) : [];
        const pool = (same.length >= 3 ? same : sorted).slice(0, 3);
        if (pool.length === 0) return null;
        return (
          <div style={{ marginBottom: 6 }}>
            <div style={{ fontSize: 11, color: 'var(--muted)', marginBottom: 4 }}>🔥 最近使用的目标表</div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {pool.map((p) => (
                <button
                  key={p.key}
                  onClick={() => {
                    setSelSchema(p.schema);
                    setSelTable(p.table);
                    setCols([]);
                    setMappings([]);
                  }}
                  style={{
                    padding: '4px 10px',
                    background: 'rgba(255,255,255,0.03)',
                    border: '1px solid var(--border)',
                    borderRadius: 3,
                    color: 'var(--text)',
                    cursor: 'pointer',
                    fontSize: 12,
                    textAlign: 'left',
                  }}
                >
                  <span style={{ color: 'var(--muted)' }} title="schema">{p.schema}</span>
                  <span style={{ color: 'var(--muted)' }}>.</span>
                  <strong>{p.table}</strong>
                  <span style={{ color: 'var(--muted)', marginLeft: 4 }} title={new Date(p.updatedAt).toLocaleString()}>
                    · {p.mode.toUpperCase()} · {p.mappings.length} 列
                  </span>
                </button>
              ))}
            </div>
          </div>
        );
      })()}
      {parse && (
        <div style={styles.mutedBox}>
          已解析 <strong>{parse.rows.length}</strong> 行 × <strong>{parse.columns.length}</strong> 列
          {parse.truncated && <span style={{ color: 'var(--warn, #d97706)' }}> · 已截断至 20 万行</span>}
        </div>
      )}
      {(opts.mode === 'update' || opts.mode === 'upsert') && (
        <div style={{ ...styles.mutedBox, marginTop: 4 }}>
          {(() => {
            const pkCols = cols.filter((c) => c.is_primary_key).map((c) => c.name);
            if (cols.length === 0) return '目标表加载后自动检测主键。';
            if (pkCols.length === 0) {
              if (opts.mode === 'update') return <span style={{ color: 'var(--warn, #d97706)' }}>⚠ 目标表无主键，Update 模式不可用（将生成 0 条语句）</span>;
              return <span style={{ color: 'var(--warn, #d97706)' }}>⚠ 目标表无主键，Upsert 将退化为 Insert</span>;
            }
            return <>主键列：{pkCols.map(quoteIdent).join(', ')}{opts.kind === 'mysql' ? ' · MySQL 用 ON DUPLICATE KEY UPDATE' : opts.kind === 'mssql' ? ' · MSSQL 用 MERGE' : opts.kind === 'oracle' ? ' · Oracle 用 MERGE' : opts.kind === 'postgres' || opts.kind === 'sqlite' ? ' · ON CONFLICT DO UPDATE' : ''}</>;
          })()}
        </div>
      )}
      <div style={styles.footer}>
        <button onClick={() => setStep('input')} style={styles.btnGhost}>← 上一步</button>
        <span style={styles.spacer} />
        <span style={styles.muted}>
          {colsLoading ? '加载表结构中…' : (selTable ? '选择表后自动进入下一步' : '请选择目标表')}
        </span>
      </div>
    </div>
  );
}

// 完成步：成功摘要 + 失败分类筛选 + 自动预览配置 + 报告导出 + 队列提示
export function StepDone({ inputFormat, statements, insertedCount, failedRows, failureSummary, failCategoryFilter, setFailCategoryFilter, progress, autoPreviewOnSuccess, setAutoPreviewOnSuccess, autoPreviewRows, setAutoPreviewRows, fileQueue, queuePos, loadQueueNext, selTable, previewImported, downloadReport, copyReportSummary, finishAndClose, setStep }: {
  inputFormat: ImportFormat;
  statements: { sql: string; params?: Value[] }[];
  insertedCount: number;
  failedRows: { csvRow: number }[];
  failureSummary: { key: string; label: string; count: number; rows: number[]; hint: string }[];
  failCategoryFilter: string | null;
  setFailCategoryFilter: (k: string | null) => void;
  progress: { done: number; total: number } | null;
  autoPreviewOnSuccess: boolean;
  setAutoPreviewOnSuccess: (v: boolean) => void;
  autoPreviewRows: number;
  setAutoPreviewRows: (n: number) => void;
  fileQueue: { name: string }[];
  queuePos: number;
  loadQueueNext: () => void;
  selTable: string;
  previewImported: () => void;
  downloadReport: (format: 'csv' | 'json') => void;
  copyReportSummary: () => void;
  finishAndClose: () => void;
  setStep: (s: Step) => void;
}) {
  return (
    <div style={styles.col}>
      <div style={styles.doneIcon}>✓</div>
      <div style={styles.doneText}>
        {inputFormat === 'sql'
          ? <>已执行 <strong>{statements.length}</strong> 条 SQL</>
          : <>已导入 <strong>{insertedCount}</strong> 行</>}
      </div>
      {failedRows.length > 0 && (
        <div style={{ ...styles.mutedCenter, color: 'var(--warn, #d97706)' }}>
          跳过 {failedRows.length} {inputFormat === 'sql' ? '条' : '行'}失败（可返回上一步查看详情）
        </div>
      )}
      {failureSummary.length > 0 && (
        <div style={{ ...styles.mutedBox, marginTop: 8, borderLeft: '3px solid var(--warn, #d97706)', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, fontWeight: 600 }}>🔧 失败原因分类</span>
            <span style={styles.muted}>（点击分类筛选下方行表）</span>
            {failCategoryFilter && (
              <button
                onClick={() => setFailCategoryFilter(null)}
                style={{ ...styles.btnSm, padding: '1px 6px', marginLeft: 'auto', fontSize: 10 }}
                title="清除筛选，显示全部失败行"
              >✕ 清除筛选</button>
            )}
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
            {failureSummary.slice(0, 5).map((s) => {
              const active = failCategoryFilter === s.key;
              const meta = failCategoryMeta(s.key);
              return (
                <div
                  key={s.key}
                  onClick={() => {
                    setFailCategoryFilter(active ? null : s.key);
                    if (!active) setStep('preview');
                  }}
                  style={{
                    display: 'grid', gridTemplateColumns: 'auto 1fr auto', gap: 8,
                    alignItems: 'baseline', fontSize: 11,
                    padding: '3px 6px', borderRadius: 3, cursor: 'pointer',
                    border: '1px solid ' + (active ? meta.color : 'var(--border)'),
                    background: active ? 'var(--accent-dim, rgba(59,130,246,0.12))' : 'var(--panel)',
                  }}
                  title="点击筛选 Step 4 失败行表；再次点击取消筛选"
                >
                  <span style={{ fontFamily: 'monospace', color: meta.color, fontWeight: 600 }}>
                    {meta.icon} {s.count} 行
                  </span>
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }} title={s.hint}>
                    <strong>{s.label}</strong> · <span style={styles.muted}>{s.hint}</span>
                  </span>
                  <span style={styles.muted} title="受影响行号">
                    L{s.rows.slice(0, 3).join(',L')}{s.rows.length > 3 ? `…+${s.rows.length - 3}` : ''}
                  </span>
                </div>
              );
            })}
          </div>
        </div>
      )}
      {progress && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={styles.muted}>统计</span>
          <strong>{progress.done}</strong><span style={styles.muted}>/{progress.total} 完成</span>
        </div>
      )}
      {autoPreviewOnSuccess && (
        <div style={{ ...styles.mutedBox, marginTop: 8, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={autoPreviewOnSuccess}
              onChange={(e) => setAutoPreviewOnSuccess(e.target.checked)}
            />
            <span>🔍 导入成功后自动预览</span>
          </label>
          <span style={styles.muted}>下次导入成功后自动跑 SELECT 预览尾行</span>
          <span style={styles.muted} title="自动预览时只取最近 N 行；有 PK 时按 PK IN(...) 精准匹配，无 PK 时降级到 LIMIT N">N =</span>
          <input
            type="number"
            min={1}
            max={10000}
            value={autoPreviewRows}
            onChange={(e) => {
              const v = parseInt(e.target.value, 10);
              if (Number.isFinite(v) && v >= 1) setAutoPreviewRows(Math.min(v, 10000));
            }}
            style={{ width: 70, padding: '2px 6px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text)', fontSize: 11, fontFamily: 'inherit' }}
            title="自动预览最近 N 行（1–10000）"
          />
          <span style={{ display: 'inline-flex', gap: 3, marginLeft: 2 }}>
            {[10, 50, 100, 500, 1000].map((n) => (
              <button
                key={n}
                onClick={() => setAutoPreviewRows(n)}
                style={{ ...styles.btnSm, padding: '1px 5px', fontSize: 10, opacity: autoPreviewRows === n ? 1 : 0.6, color: autoPreviewRows === n ? 'var(--accent, #3b82f6)' : undefined, borderColor: autoPreviewRows === n ? 'var(--accent, #3b82f6)' : 'var(--border)' }}
                title={`快捷设为 N=${n}`}
              >{n}</button>
            ))}
          </span>
          <span style={styles.muted}>行（有 PK 时按 PK IN(...) 精准匹配，无 PK 时降级到 LIMIT N）</span>
          <span style={styles.muted}>({autoPreviewOnSuccess ? '已开启' : '已关闭'})</span>
        </div>
      )}
      {fileQueue.length > 0 && queuePos < fileQueue.length && (
        <div style={{ ...styles.mutedBox, marginTop: 8, color: 'var(--accent, #3b82f6)' }}>
          📚 队列还有 {fileQueue.length - queuePos} 个文件等待处理（点"下一个"继续）
        </div>
      )}
      <div style={{ ...styles.row2col, justifyContent: 'center', padding: '8px 0', gap: 8 }}>
        {inputFormat !== 'sql' && selTable && (
          <button onClick={() => previewImported()} style={styles.btn} title="生成 SELECT 语句并在编辑器中执行，验证导入结果">
            🔍 预览导入的数据（LIMIT 100）
          </button>
        )}
        <button onClick={() => downloadReport('csv')} style={styles.btn} title={failCategoryFilter ? `下载当前筛选「${failCategoryFilter}」的失败行为 CSV` : '下载全部失败行为 CSV（含分类/修复提示列）'}>
          📄 下载 CSV 报告
        </button>
        <button onClick={() => downloadReport('json')} style={styles.btn} title={failCategoryFilter ? `下载当前筛选「${failCategoryFilter}」的失败行为 JSON（含分类元数据）` : '下载全部失败行为 JSON（含分类元数据）'}>
          📄 下载 JSON 报告
        </button>
        <button onClick={() => void copyReportSummary()} style={styles.btn} title="复制导入摘要到剪贴板（表名/模式/行数/耗时/失败原因 Top-3），便于贴到工单或 PR">
          📋 复制报告摘要
        </button>
        {fileQueue.length > 0 && queuePos < fileQueue.length && (
          <button
            onClick={() => {
              void loadQueueNext();
              setStep('input');
            }}
            style={styles.btn}
            title="加载队列中的下一个文件"
          >▶ 加载下一个文件</button>
        )}
      </div>
      <div style={styles.footer}>
        <span style={styles.spacer} />
        <button onClick={finishAndClose} style={styles.btnPrimary}>关闭</button>
      </div>
    </div>
  );
}

export function formatValue(v: Value): string {
  if (v === null) return 'NULL';
  if (typeof v === 'string') return `'${v.replace(/'/g, "''")}'`;
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (typeof v === 'number') return String(v);
  if (Array.isArray(v)) return `[${v.map(formatValue).join(', ')}]`;
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

export function typeBadgeColor(t: 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null'): string {
  switch (t) {
    case 'integer': return '#3b82f6';
    case 'float': return '#06b6d4';
    case 'boolean': return '#10b981';
    case 'date': return '#eab308';
    case 'null': return 'var(--muted)';
    case 'string':
    default: return 'var(--muted)';
  }
}

export function qualityScoreColor(score: number): string {
  if (score >= 80) return 'var(--ok, #10b981)';
  if (score >= 60) return '#eab308';
  if (score >= 40) return 'var(--warn, #d97706)';
  return 'var(--danger, #dc2626)';
}

export function colDtypeColor(dtype: string): string {
  const d = dtype.toLowerCase();
  if (/\b(int|integer|bigint|smallint|tinyint|serial|bit|number|numeric|decimal)\b/.test(d)) return '#3b82f6';
  if (/\b(float|double|real|float)\b/.test(d)) return '#06b6d4';
  if (/\b(bool|boolean)\b/.test(d)) return '#10b981';
  if (/\b(date|time|timestamp|datetime)\b/.test(d)) return '#eab308';
  if (/\b(char|varchar|text|clob|nchar|nvarchar|string|uuid|json)\b/.test(d)) return 'var(--accent, #3b82f6)';
  if (/\b(blob|bytea|binary|varbinary|image)\b/.test(d)) return 'var(--muted)';
  return 'var(--muted)';
}

export function toMsg(e: unknown): string {
  if (e instanceof ApiError) return `${e.code}: ${e.message}`;
  if (e instanceof Error) return e.message;
  return String(e);
}

export function formatTime(at: number): string {
  const d = new Date(at);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// M30.127 三态审计徽章：ok=绿 / warn=橙 / dead=红；label 供区分「归档预设」/「目标预设」等上下文
// M30.128 stale 视觉提示：缓存年龄超过 AUDIT_STALE_MS（TTL 一半）时加半透明+"⚠️ stale"标签
// M30.130 busy 态：重审进行中按钮变 ⏳ + disabled，防误连点；age 徽章在 busy 时也置灰表示"刷新中"
const AUDIT_STALE_MS = 12 * 60 * 60 * 1000;
export const AuditBadge = ({ a, cachedAtMs, age, label, onReAudit, busy }: {
  a: { status: PresetAuditStatus; reason: string };
  cachedAtMs: number | null;
  age: string | null;
  label: string;
  onReAudit?: () => void;
  busy?: boolean;
}) => {
  const stale = cachedAtMs !== null && (Date.now() - cachedAtMs) > AUDIT_STALE_MS;
  const icon = a.status === 'ok' ? '✅' : a.status === 'warn' ? '⚠' : '❌';
  const color = a.status === 'ok' ? 'var(--success, #10b981)'
    : a.status === 'warn' ? 'var(--warn, #d97706)' : 'var(--danger, #dc2626)';
  const bg = a.status === 'ok' ? 'rgba(16,185,129,0.06)'
    : a.status === 'warn' ? 'rgba(217,119,6,0.08)' : 'rgba(220,38,38,0.08)';
  const border = a.status === 'ok' ? 'rgba(16,185,129,0.35)'
    : a.status === 'warn' ? 'rgba(217,119,6,0.4)' : 'rgba(220,38,38,0.4)';
  return (
    <div style={{
      display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center',
      padding: '2px 5px', borderRadius: 3,
      background: bg, border: `1px solid ${border}`,
      opacity: (stale ? 0.65 : 1) * (busy ? 0.7 : 1),
    }} title={`${busy ? '🔍 重审中…' : '审计：'}${a.reason}${age ? `（${age}${stale ? ' · ⚠️ stale' : ''}）` : ''}`}>
      <span style={{ color, fontWeight: 600 }}>
        {icon} {label}：{a.status === 'ok' ? '正常' : a.status === 'warn' ? '部分列缺失' : '已失效'}
      </span>
      <span style={{ color: 'var(--muted)' }}>· {a.reason}</span>
      {stale && (
        <span style={{
          padding: '0 4px', borderRadius: 3, fontSize: 8, fontWeight: 600,
          background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
          color: 'var(--warn, #d97706)',
        }} title="审计缓存已超过 12h（TTL 的一半），建议重新审计">⚠️ stale</span>
      )}
      {onReAudit && (
        <button
          type="button"
          disabled={busy}
          onClick={(e) => { e.stopPropagation(); if (!busy) onReAudit(); }}
          style={{
            padding: '0 4px', borderRadius: 3, fontSize: 8, fontWeight: 600,
            background: busy ? 'rgba(148,163,184,0.10)' : 'rgba(59,130,246,0.10)',
            border: `1px solid ${busy ? 'rgba(148,163,184,0.5)' : 'rgba(59,130,246,0.5)'}`,
            color: busy ? 'var(--muted)' : 'var(--accent, #3b82f6)',
            cursor: busy ? 'not-allowed' : 'pointer',
          }} title="对目标表列重跑 listColumns 检查（更新该预设的审计状态）">{busy ? '⏳ 重审中…' : '🔍 重审'}</button>
      )}
      {age && <span style={{ color: 'var(--muted)', fontSize: 8, marginLeft: 'auto', opacity: busy ? 0.5 : 1 }}>{busy ? '🔍 刷新中…' : `🕐 ${age}`}</span>}
    </div>
  );
};

export function historyStatusBadge(status: 'success' | 'partial' | 'failed' | 'cancelled'): string {
  switch (status) {
    case 'success': return '✓ 成功';
    case 'partial': return '◐ 部分';
    case 'failed': return '✕ 失败';
    case 'cancelled': return '⏹ 取消';
  }
}

export const styles: Record<string, CSSProperties> = {
  overlay: {
    position: 'fixed', inset: 0,
    background: 'rgba(0,0,0,0.45)',
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    zIndex: 100, padding: 20,
  },
  panel: {
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 6,
    boxShadow: '0 12px 40px rgba(0,0,0,0.35)',
    width: 900, maxWidth: '95vw',
    height: '85vh', maxHeight: 720,
    display: 'flex', flexDirection: 'column',
    overflow: 'hidden',
  },
  header: {
    display: 'flex', alignItems: 'center',
    padding: '10px 14px',
    borderBottom: '1px solid var(--border)',
    gap: 10,
  },
  title: { fontWeight: 700, fontSize: 14, flex: 1 },
  closeBtn: {
    background: 'transparent', border: 'none',
    color: 'var(--muted)', fontSize: 20,
    cursor: 'pointer', lineHeight: 1, padding: '0 6px',
  },
  stepBar: {
    display: 'flex', padding: '6px 14px', gap: 6,
    borderBottom: '1px solid var(--border)',
    fontFamily: 'monospace', fontSize: 11,
    color: 'var(--muted)', flexWrap: 'wrap',
  },
  stepItem: {
    padding: '2px 8px', borderRadius: 3,
    background: 'var(--panel)',
    border: '1px solid var(--border)',
  },
  stepItemActive: {
    background: 'var(--accent-dim, var(--accent))',
    color: 'var(--fg)', fontWeight: 700,
  },
  stepItemDone: {
    color: 'var(--ok)', borderColor: 'var(--ok)',
  },
  body: {
    flex: 1, padding: 14, overflow: 'auto',
    display: 'flex', flexDirection: 'column', gap: 10,
  },
  col: {
    display: 'flex', flexDirection: 'column',
    gap: 10, height: '100%',
  },
  row2col: { display: 'flex', gap: 12, flexWrap: 'wrap' },
  field: {
    display: 'flex', flexDirection: 'column', gap: 4,
    flex: 1, minWidth: 160,
  },
  label: {
    fontSize: 11, color: 'var(--muted)',
    textTransform: 'uppercase', letterSpacing: '0.05em',
    fontWeight: 600,
  },
  select: {
    padding: '6px 8px', fontSize: 13,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  selectSm: {
    padding: '4px 6px', fontSize: 12,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
    fontFamily: 'monospace',
  },
  textarea: {
    width: '100%', height: 160, padding: 10,
    fontSize: 13, fontFamily: 'monospace',
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, resize: 'vertical',
    boxSizing: 'border-box', outline: 'none',
  },
  inputSm: {
    padding: '4px 6px', fontSize: 12,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  inputNum: {
    padding: '6px 8px', fontSize: 13,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  check: {
    display: 'flex', alignItems: 'center', gap: 6,
    fontSize: 13, color: 'var(--fg)', cursor: 'pointer',
  },
  btn: {
    padding: '8px 12px', fontSize: 13,
    background: 'var(--panel)', color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, cursor: 'pointer',
  },
  btnGhost: {
    padding: '8px 12px', fontSize: 13,
    background: 'transparent', color: 'var(--muted)',
    border: '1px solid var(--border)',
    borderRadius: 4, cursor: 'pointer',
  },
  btnPrimary: {
    padding: '8px 14px', fontSize: 13,
    background: 'var(--accent)', color: '#fff',
    border: 'none', borderRadius: 4,
    cursor: 'pointer', fontWeight: 600,
  },
  btnSm: {
    padding: '2px 8px', fontSize: 11,
    background: 'var(--panel)', color: 'var(--accent, #3b82f6)',
    border: '1px solid var(--accent, #3b82f6)',
    borderRadius: 3, cursor: 'pointer',
    fontFamily: 'monospace',
  },
  footer: {
    display: 'flex', alignItems: 'center',
    gap: 8, marginTop: 'auto',
    padding: '8px 0 0',
    borderTop: '1px solid var(--border)',
  },
  spacer: { flex: 1 },
  muted: { fontSize: 11, color: 'var(--muted)' },
  mutedCenter: { fontSize: 11, color: 'var(--muted)', textAlign: 'center' },
  mutedBox: {
    fontSize: 12, color: 'var(--muted)',
    padding: '8px 10px',
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  errorBox: {
    fontSize: 13, color: 'var(--danger, #dc2626)',
    padding: '8px 10px',
    background: 'rgba(220,38,38,0.08)',
    border: '1px solid var(--danger, #dc2626)',
    borderRadius: 4,
  },
  mapGrid: {
    display: 'flex', flexDirection: 'column', gap: 4,
    maxHeight: 380, overflow: 'auto',
    padding: 4,
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  mapRow: {
    display: 'grid',
    gridTemplateColumns: '1fr 24px 1.4fr',
    gap: 6, alignItems: 'center',
  },
  mapCell: {
    display: 'flex', gap: 6, alignItems: 'center',
    fontSize: 13,
  },
  arrow: { textAlign: 'center', color: 'var(--muted)' },
  summary: {
    cursor: 'pointer', fontSize: 12,
    color: 'var(--accent)', userSelect: 'none',
  },
  pre: {
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4, padding: 8,
    fontFamily: 'monospace', fontSize: 12,
    overflow: 'auto', maxHeight: 180,
    whiteSpace: 'pre-wrap', wordBreak: 'break-all',
    margin: 0,
  },
  progressOuter: {
    width: '100%', height: 8,
    background: 'var(--panel)',
    borderRadius: 4, overflow: 'hidden',
    border: '1px solid var(--border)',
  },
  progressInner: {
    height: '100%', background: 'var(--accent)',
    transition: 'width 0.15s',
  },
  doneIcon: {
    fontSize: 44, textAlign: 'center',
    color: 'var(--ok)', marginTop: 20,
  },
  doneText: { fontSize: 16, textAlign: 'center' },
  previewWrap: {
    maxHeight: 260, overflow: 'auto',
    border: '1px solid var(--border)',
    borderRadius: 4,
    background: 'var(--panel)',
  },
  previewTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 12, fontFamily: 'monospace',
  },
  previewTh: {
    padding: '6px 8px',
    textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
  },
  previewThInner: {
    display: 'flex', alignItems: 'center', gap: 6,
  },
  previewThSub: {
    fontSize: 10, color: 'var(--muted)',
    fontFamily: 'inherit',
  },
  previewTd: {
    padding: '4px 8px',
    borderRight: '1px dashed var(--border)',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 200,
  },
  previewTdMuted: {
    color: 'var(--muted)',
    width: 32, textAlign: 'right',
    borderBottom: '1px solid var(--border)',
  },
  previewTdNull: {
    color: 'var(--muted)',
    fontStyle: 'italic',
  },
  typeBadge: {
    display: 'inline-block',
    padding: '1px 5px',
    fontSize: 10, borderRadius: 3,
    background: 'rgba(0,0,0,0.06)',
    border: '1px solid currentColor',
    fontFamily: 'monospace',
    fontWeight: 600,
    textTransform: 'lowercase',
  },
  warnText: {
    fontSize: 11,
    color: 'var(--warn, #d97706)',
    background: 'rgba(217,119,6,0.08)',
    border: '1px solid var(--warn, #d97706)',
    borderRadius: 3,
    padding: '1px 4px',
    maxWidth: '100%', overflow: 'hidden',
    textOverflow: 'ellipsis', whiteSpace: 'nowrap',
  },
  selectWarn: {
    borderColor: 'var(--warn, #d97706)',
    background: 'rgba(217,119,6,0.05)',
  },
  failedWrap: {
    maxHeight: 220, overflow: 'auto',
    border: '1px solid var(--warn, #d97706)',
    borderRadius: 4,
    background: 'rgba(217,119,6,0.04)',
  },
  failedTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 12, fontFamily: 'monospace',
  },
  failedTh: {
    padding: '6px 8px', textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
  },
  failedTd: {
    padding: '4px 8px',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 260,
  },
  dragHint: {
    position: 'fixed', inset: 0,
    pointerEvents: 'none',
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    background: 'rgba(59,130,246,0.08)',
    border: '4px dashed var(--accent)',
    zIndex: 200,
    fontSize: 20,
    color: 'var(--accent)',
    fontWeight: 700,
  },
  historyPanel: {
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: 10,
  },
  historyWrap: {
    maxHeight: 300,
    overflow: 'auto',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  historyTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 11, fontFamily: 'monospace',
  },
  historyTh: {
    padding: '6px 8px',
    textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
    fontSize: 11,
  },
  historyTd: {
    padding: '4px 8px',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 200,
  },
};
