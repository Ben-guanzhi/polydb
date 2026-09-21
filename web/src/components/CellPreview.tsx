import { useState, type CSSProperties, type ReactNode } from 'react';

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
  try { const d = new Date(Number(s)); if (!Number.isNaN(d.getTime()) && s.match(/^\d+$/)) { out.push(`Unix ${s.length <= 10 ? 's' : 'ms'} (local): ${d.toString()}`); out.push(`Unix ${s.length <= 10 ? 's' : 'ms'} (ISO): ${d.toISOString()}`); } } catch { /* not number */ }
  if (out.length === 0) out.push('非可解析的时间值');
  return out.join('\n');
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

interface Props {
  /** 标题区内容，如「单元格预览 · 行 3 · 列 response (json)」 */
  label: ReactNode;
  value: unknown;
  onClose: () => void;
  /** 受控页签；不传则内部自管（默认 JSON 值自动进 JSON 页签） */
  tab?: PreviewTab;
  onTabChange?: (t: PreviewTab) => void;
}

export default function CellPreviewPanel({ label, value, onClose, tab, onTabChange }: Props) {
  const [innerTab, setInnerTab] = useState<PreviewTab>(() => (tryParseJson(value) ? 'json' : 'text'));
  const cur = tab ?? innerTab;
  const set = (t: PreviewTab) => { if (onTabChange) onTabChange(t); else setInnerTab(t); };
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
          {value === null || value === undefined ? 'NULL' : `${String(value).length} 字符`}
        </span>
        <button className="btn-ico" onClick={onClose} title="关闭预览" style={{ fontSize: 11 }}>×</button>
      </div>
      <div style={{ fontSize: 11, color: 'var(--muted)', marginBottom: 4 }}>
        {cur === 'json' && !tryParseJson(value) && '(无法解析为 JSON)'}
        {cur === 'time' && !(typeof value === 'string' || typeof value === 'number') && '(仅支持字符串/数字时间)'}
      </div>
      <pre style={preStyle}>
        {cur === 'text' ? (value === null || value === undefined ? 'NULL' : String(value))
          : cur === 'json' ? (tryParseJson(value) ?? String(value))
          : cur === 'hex' ? (value === null || value === undefined ? 'NULL' : bytesToHex(String(value)))
          : timeGuess(value)}
      </pre>
    </div>
  );
}
