import type { MutableRefObject } from 'react';
import { qualityScoreColor, sampleForKind, typeBadgeColor } from '../../lib/importDisplay';
import type { ImportFormat, InputEncoding, ColumnProfile } from '../../lib/importData';
import type { Delimiter, CsvParseResult } from '../../lib/csvParser';
import type { SqlStatement } from '../../lib/sqlSplit';
import { type LintResult, severityLabel } from '../../lib/sqlLint';
import type { DataQualityReport, QualityTransformSuggestion } from '../../lib/dataQuality';
import { publishEditorStatus } from '../../lib/statusBus';
import { styles } from '../importStyles';
import type { DatabaseKind } from '../../api';

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