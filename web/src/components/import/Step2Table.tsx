import { listPresets } from '../../lib/importPresets';
import { quoteIdent, type ImportMode, type ImportOptions, type Mapping } from '../../lib/importData';
import { styles } from '../importStyles';
import type { ColumnInfo, SchemaInfo, TableInfo } from '../../api';
import type { Step } from './types';

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