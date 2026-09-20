// M17：BYOK AI 助手面板。浏览器直连用户自配的 OpenAI 兼容端点，
// 密钥只存 localStorage、不经过 polydb server。能力：生成 SQL / 解释 / 优化。
import { useState } from 'react';
import type { AiSettings } from '../lib/aiSettings';
import { hasAiConfig } from '../lib/aiSettings';
import { buildMessages, chatCompletion, extractSql, type AiMode } from '../lib/aiSql';

interface Props {
  settings: AiSettings;
  onSettings: (s: AiSettings) => void;
  dialect?: string;
  schema?: string;
  table?: string;
  currentSql?: string;
  onInsertSql?: (sql: string) => void;
}

const inputStyle: React.CSSProperties = {
  fontSize: 11, padding: '2px 6px', border: '1px solid var(--border)',
  borderRadius: 3, background: 'var(--bg)', color: 'var(--fg)',
};

export default function AIPanel({ settings, onSettings, dialect, schema, table, currentSql, onInsertSql }: Props) {
  const [mode, setMode] = useState<AiMode>('generate');
  const [request, setRequest] = useState('');
  const [busy, setBusy] = useState(false);
  const [out, setOut] = useState('');
  const [err, setErr] = useState<string | null>(null);

  const canRun = mode === 'generate' ? request.trim() !== '' : (currentSql ?? '').trim() !== '';
  const configured = hasAiConfig(settings);

  const run = async () => {
    if (!configured || !canRun || busy) return;
    setBusy(true);
    setErr(null);
    setOut('');
    try {
      const msgs = buildMessages(mode, { dialect, schema, table, currentSql, request });
      const r = await chatCompletion(settings, msgs);
      setOut(r.content);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const insert = () => {
    if (!out) return;
    const sql = mode === 'generate' ? extractSql(out) : out;
    onInsertSql?.(sql);
  };

  const set = (patch: Partial<AiSettings>) => onSettings({ ...settings, ...patch });

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ fontSize: 11, color: 'var(--muted)' }}>
        密钥仅存浏览器，直连下方端点（不经过 polydb 服务端）
      </div>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        <input placeholder="Base URL（…/v1）" value={settings.baseUrl}
          onChange={(e) => set({ baseUrl: e.target.value })} style={{ ...inputStyle, width: 180 }} />
        <input placeholder="模型" value={settings.model}
          onChange={(e) => set({ model: e.target.value })} style={{ ...inputStyle, width: 120 }} />
        <input type="password" placeholder="API Key" value={settings.apiKey}
          onChange={(e) => set({ apiKey: e.target.value })} style={{ ...inputStyle, width: 160 }} />
      </div>

      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
        {([['generate', '生成 SQL'], ['explain', '解释 SQL'], ['optimize', '优化 SQL']] as Array<[AiMode, string]>).map(([m, label]) => (
          <button key={m} className={`btn ${mode === m ? 'primary' : ''}`} style={{ fontSize: 11, padding: '2px 8px' }}
            onClick={() => { setMode(m); setErr(null); }}>
            {label}
          </button>
        ))}
        <button className="btn primary" style={{ fontSize: 11, padding: '2px 10px', marginLeft: 'auto' }}
          disabled={busy || !configured || !canRun} onClick={() => void run()}>
          {busy ? '调用中…' : '▶ 运行'}
        </button>
      </div>

      {mode === 'generate' && (
        <textarea
          value={request}
          onChange={(e) => setRequest(e.target.value)}
          placeholder="用自然语言描述需求（如：统计各表最近 7 天的行数）"
          style={{ ...inputStyle, width: '100%', height: 54, resize: 'vertical', fontFamily: 'inherit' }}
        />
      )}
      {mode !== 'generate' && (
        <div style={{ fontSize: 10, color: 'var(--muted)' }}>
          针对当前编辑器 SQL（{currentSql ? `${currentSql.length} 字符` : '空'}）
        </div>
      )}

      {err && <div className="error-box">{err}</div>}
      {out && (
        <>
          <pre className="mono" style={{
            margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-word',
            maxHeight: 200, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 3,
            padding: 6, background: 'var(--bg)',
          }}>{out}</pre>
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="btn" style={{ fontSize: 11, padding: '2px 8px' }} onClick={insert}
              disabled={!onInsertSql}>
              ⧉ {mode === 'generate' ? '插入编辑器（SQL）' : '复制全文'}
            </button>
            {mode !== 'generate' && (
              <button className="btn" style={{ fontSize: 11, padding: '2px 8px' }}
                onClick={() => void navigator.clipboard.writeText(out).catch(() => {})}>
                📋 复制
              </button>
            )}
          </div>
        </>
      )}
      {!configured && <div style={{ fontSize: 10, color: 'var(--warn, #d97706)' }}>请先填写 Base URL 与 API Key。</div>}
    </div>
  );
}
