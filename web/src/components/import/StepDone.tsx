import { failCategoryMeta } from '../../lib/importDisplay';
import type { ImportFormat } from '../../lib/importData';
import { styles } from '../importStyles';
import type { Value } from '../../api';
import type { Step } from './types';

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