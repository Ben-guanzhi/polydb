import { useEffect, useState, type CSSProperties, type ReactNode } from 'react';
import * as api from '../lib/api';
import { asBlobValue, blobIdOfRef, blobLabel } from '../lib/blobValue';

// ─── 共享单元格预览面板（查询结果网格 + 表数据浏览复用）───

export type PreviewTab = 'text' | 'json' | 'hex' | 'time';

export function tryParseJson(v: unknown): string | null {
  if (v === null || v === undefined) return null;
  if (typeof v === 'object') return JSON.stringify(v, null, 2);
  const s = String(v).trim();
  if (!s.startsWith('{') && !s.startsWith('[')) return null;
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return null; }
}

export function bytesToHex(s: string, chunk = 16): string {
  const bytes: number[] = [];
  for (let i = 0; i < s.length; i++) {
    const code = s.charCodeAt(i);
    if (code < 128) bytes.push(code);
    else if (code < 2048) { bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f)); }
    else { bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f)); }
  }
  const hexBytes = (b: number) => b.toString(16).padStart(2, '0');
  const printable = (b: number) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·');
  const lines: string[] = [];
  for (let i = 0; i < bytes.length; i += chunk) {
    const slice = bytes.slice(i, i + chunk);
    const hex = slice.map(hexBytes).join(' ');
    const text = slice.map(printable).join('');
    lines.push(`${i.toString(16).padStart(8, '0')}  ${hex.padEnd(chunk * 3 - 1)}  |${text}|`);
  }
  return lines.length > 0 ? lines.join('\n') : '(empty)';
}

function timeGuess(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  const s = String(v);
  const out: string[] = [];
  try { const d = new Date(s); out.push(`Parsed (local): ${d.toString()}`); out.push(`ISO 8601: ${d.toISOString()}`); } catch { /* not date */ }
  try { const d = new Date(Number(s)); if (!Number.isNaN(d.getTime()) && s.match(/^\d+$/)) { out.push(`Unix ${s.length <= 10 ? 's' : 'ms'} (local): ${d.toString()}`); out.push(`ISO 8601: ${d.toISOString()}`); } } catch { /* not number */ }
  if (out.length === 0) out.push('非可解析的时间值');
  return out.join('\n');
}

/** Uint8Array → Latin-1 字符串（bytesToHex 的输入形态）。 */
function u8ToBin(bytes: Uint8Array): string {
  const chunk = 0x8000;
  let s = '';
  for (let i = 0; i < bytes.length; i += chunk) {
    s += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return s;
}

const preStyle: CSSProperties = {
  margin: 0,
  padding: 8,
  background: 'var(--bg-alt, rgba(0,0,0,0.15))',
  border: '1px solid var(--border)',
  borderRadius: 3,
  fontFamily: 'monospace',
  fontSize: 12,
  maxHeight: 240,
  overflow: 'auto',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  color: 'var(--fg)',
};

const pillBtn: CSSProperties = {
  padding: '2px 8px',
  fontSize: 11,
  background: 'transparent',
  color: 'var(--fg)',
  border: '1px solid var(--border)',
  borderRadius: 2,
  cursor: 'pointer',
  fontFamily: 'inherit',
};

interface Props {
  /** 标题区内容，如「单元格预览 · 行 3 · 列 response (json)」 */
  label: ReactNode;
  value: unknown;
  onClose: () => void;
  /** 受控页签；不传则内部自管（默认 JSON 值自动进 JSON 页签） */
  tab?: PreviewTab;
  onTabChange?: (t: PreviewTab) => void;
  /** 连接 id：$blob 旁路引用经 REST 取回（encoding.md §5.2）；缺省时不显示取回按钮 */
  connId?: string;
}

export default function CellPreviewPanel({ label, value, onClose, tab, onTabChange, connId }: Props) {
  const [innerTab, setInnerTab] = useState<PreviewTab>(() => (tryParseJson(value) ? 'json' : 'text'));
  const cur = tab ?? innerTab;
  const set = (t: PreviewTab) => { if (onTabChange) onTabChange(t); else setInnerTab(t); };

  // ─── §5.2 旁路引用取回 ───
  const blob = asBlobValue(value);
  const refId = blob?.ref ? blobIdOfRef(blob.ref) : null;
  const isRef = refId !== null;
  const [fetched, setFetched] = useState<Uint8Array<ArrayBuffer> | null>(null);
  const [fetching, setFetching] = useState(false);
  const [fetchErr, setFetchErr] = useState<string | null>(null);
  useEffect(() => {
    setFetched(null);
    setFetching(false);
    setFetchErr(null);
  }, [refId]);

  const doFetch = () => {
    if (!connId || !refId) return;
    setFetching(true);
    setFetchErr(null);
    api.fetchBlob(connId, refId)
      .then((r) => {
        setFetched(new Uint8Array(r.data));
        set('text');
      })
      .catch((e) => setFetchErr(api.blobFetchErrorMessage(e)))
      .finally(() => setFetching(false));
  };

  const downloadFetched = () => {
    if (!fetched || !refId) return;
    const url = URL.createObjectURL(new Blob([fetched]));
    const a = document.createElement('a');
    a.href = url;
    a.download = `blob-${refId}`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  };

  // 未取回时的占位说明；404 后取 fetchErr（spec 固定文案「已过期，请重新执行查询」）。
  const refHint = fetchErr ?? (fetched ? null : '（内容未取回 — 点击「取回内容」）');

  const preContent: ReactNode = isRef
    ? cur === 'text'
      ? (fetched ? new TextDecoder().decode(fetched) : refHint)
      : cur === 'hex'
        ? (fetched ? bytesToHex(u8ToBin(fetched)) : refHint)
        : (refHint ?? '（已取回二进制内容，本页签不适用）')
    : cur === 'text'
      ? (value === null || value === undefined ? 'NULL' : String(value))
      : cur === 'json'
        ? (tryParseJson(value) ?? String(value))
        : cur === 'hex'
          ? (value === null || value === undefined ? 'NULL' : bytesToHex(String(value)))
          : timeGuess(value);

  return (
    <div style={{ padding: '6px 10px', borderTop: '1px solid var(--border)', background: 'var(--bg)', maxWidth: '100%' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <strong style={{ fontSize: 12, color: 'var(--muted)' }}>{label}</strong>
        <div style={{ display: 'flex', gap: 2 }}>
          {(['text', 'json', 'hex', 'time'] as const).map((t) => (
            <button
              key={t}
              className="btn-ico"
              onClick={() => set(t)}
              style={{
                padding: '2px 8px',
                fontSize: 11,
                background: cur === t ? 'var(--accent-dim)' : 'transparent',
                color: cur === t ? 'var(--fg)' : 'var(--muted)',
                border: '1px solid var(--border)',
                borderRadius: 2,
              }}
            >
              {t === 'text' ? '文本' : t === 'json' ? 'JSON' : t === 'hex' ? 'HEX' : '时间'}
            </button>
          ))}
        </div>
        <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--muted)' }}>
          {isRef && blob
            ? (fetched ? `${fetched.length} 字节已取回` : `待取回 · ${blob.len} 字节`)
            : value === null || value === undefined ? 'NULL' : `${String(value).length} 字符`}
        </span>
        <button className="btn-ico" onClick={onClose} title="关闭预览" style={{ fontSize: 11 }}>×</button>
      </div>
      {isRef && blob && refId && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, fontSize: 11, flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--muted)', fontFamily: 'monospace', wordBreak: 'break-all' }}>
            {blobLabel(blob.len)} · $polydb_blob:{refId}
          </span>
          <button
            className="btn-ico"
            style={pillBtn}
            onClick={doFetch}
            disabled={fetching || !connId}
            title={connId ? '经 REST 取回大字段内容（encoding.md §5.2）' : '缺少连接上下文，无法取回'}
          >
            {fetching ? '取回中…' : fetched ? '重新取回' : '取回内容'}
          </button>
          {fetched && (
            <button className="btn-ico" style={pillBtn} onClick={downloadFetched} title="下载为文件">
              下载
            </button>
          )}
          {fetchErr && <span style={{ color: 'var(--danger, #c0392b)' }}>{fetchErr}</span>}
        </div>
      )}
      <div style={{ fontSize: 11, color: 'var(--muted)', marginBottom: 4 }}>
        {cur === 'json' && !isRef && !tryParseJson(value) && '(无法解析为 JSON)'}
        {cur === 'time' && !isRef && !(typeof value === 'string' || typeof value === 'number') && '(仅支持字符串/数字时间)'}
      </div>
      <pre style={preStyle}>
        {preContent}
      </pre>
    </div>
  );
}
