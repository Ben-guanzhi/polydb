import { useEffect, useState, type CSSProperties } from 'react';
import { DEFAULT_SETTINGS, loadSettings, updateSettings, type Settings } from '../lib/settings';
import { getServerToken, setServerToken } from '../lib/api';

const THEME_OPTIONS: { value: Settings['theme']; label: string }[] = [
  { value: 'vs', label: '亮色 (vs)' },
  { value: 'vs-dark', label: '暗色 (vs-dark)' },
  { value: 'hc-black', label: '高对比度 (hc-black)' },
];

const kbd: CSSProperties = {
  fontFamily: 'monospace', fontSize: 10,
  background: 'var(--bg-alt, rgba(0,0,0,0.15))',
  border: '1px solid var(--border)', borderRadius: 3,
  padding: '1px 5px', color: 'var(--fg)',
};

interface Props {
  open: boolean;
  onClose: () => void;
}

function Row({ label, desc, children }: { label: string; desc?: string; children: React.ReactNode }) {
  return (
    <div style={
      {
        display: 'grid', gridTemplateColumns: '220px 1fr', gap: 12,
        alignItems: 'center', padding: '8px 0',
        borderBottom: '1px solid var(--border)',
      } as CSSProperties
    }>
      <div>
        <div style={{ fontSize: 12, color: 'var(--fg)', fontWeight: 500 }}>{label}</div>
        {desc && <div style={{ fontSize: 11, color: 'var(--muted)', marginTop: 2 }}>{desc}</div>}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>{children}</div>
    </div>
  );
}

function SelectRow<T extends string | number>({ label, desc, value, options, onChange, width = 100 }: {
  label: string; desc?: string; value: T;
  options: { value: T; label: string }[]; onChange: (v: T) => void; width?: number;
}) {
  return (
    <Row label={label} desc={desc}>
      <select
        value={String(value)}
        onChange={(e) => {
          const raw = e.target.value;
          const opt = options.find((o) => String(o.value) === raw);
          if (opt) onChange(opt.value);
        }}
        style={{ width, fontFamily: 'inherit', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 3, padding: '3px 6px' }}
      >
        {options.map((o) => <option key={String(o.value)} value={String(o.value)}>{o.label}</option>)}
      </select>
    </Row>
  );
}

function CheckRow({ label, desc, checked, onChange }: { label: string; desc?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <Row label={label} desc={desc}>
      <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer', fontSize: 12 }}>
        <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
        <span>{checked ? '开启' : '关闭'}</span>
      </label>
    </Row>
  );
}

export default function SettingsPanel({ open, onClose }: Props) {
  const [s, setS] = useState<Settings>(() => loadSettings());
  const [token, setToken] = useState<string>(() => getServerToken());

  useEffect(() => {
    if (!open) return;
    setS(loadSettings());
    const h = (e: Event) => {
      const next = (e as CustomEvent<{ settings: Settings }>).detail?.settings;
      if (next) setS(next);
    };
    window.addEventListener('polydb-settings-changed', h);
    return () => { window.removeEventListener('polydb-settings-changed', h); };
  }, [open]);

  if (!open) return null;

  const patch = (p: Partial<Settings>) => {
    const next = updateSettings(p);
    setS(next);
  };
  const reset = () => {
    const next = updateSettings({ ...DEFAULT_SETTINGS });
    setS(next);
  };

  const groupStyle: CSSProperties = { fontSize: 11, color: 'var(--muted)', textTransform: 'uppercase', letterSpacing: 1, margin: '14px 0 4px', padding: '0 14px' };

  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 950,
        background: 'rgba(0,0,0,0.35)',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'center',
        padding: '12vh 16px 16px',
      }}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
      onKeyDown={(e) => { if (e.key === 'Escape') { e.preventDefault(); onClose(); } }}
      tabIndex={-1}
    >
      <div
        style={
          {
            width: '100%', maxWidth: 680, maxHeight: '76vh',
            background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6,
            boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
            display: 'flex', flexDirection: 'column', overflow: 'hidden',
          } as CSSProperties
        }
      >
        <div style={{ display: 'flex', alignItems: 'center', padding: '10px 14px', borderBottom: '1px solid var(--border)' }}>
          <span style={{ fontWeight: 600, fontSize: 14 }}>设置</span>
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--muted)' }}>Ctrl+, 打开 · Esc 关闭</span>
        </div>
        <div style={{ flex: 1, overflow: 'auto', padding: '0 14px' }} onKeyDown={(e) => { if (e.key === 'Escape') { e.preventDefault(); onClose(); } }}>
          <div style={groupStyle}>外观</div>
          <SelectRow label="主题" desc="编辑器配色方案" value={s.theme} options={THEME_OPTIONS} onChange={(v) => patch({ theme: v })} width={180} />

          <div style={groupStyle}>编辑器</div>
          <Row label="字号" desc="Monaco 编辑器字体大小（10–28）">
            <input
              type="number" min={10} max={28} value={s.editorFontSize}
              onChange={(e) => {
                const v = parseInt(e.target.value, 10);
                if (Number.isFinite(v) && v >= 10 && v <= 28) patch({ editorFontSize: v });
              }}
              style={{ width: 64, fontFamily: 'monospace', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 3, padding: '3px 6px' }}
            />
          </Row>
          <SelectRow label="缩进宽度" desc="Tab 键产生的空格数" value={s.editorTabSize}
            options={[1, 2, 4, 6, 8].map((v) => ({ value: v, label: String(v) }))}
            onChange={(v) => patch({ editorTabSize: v })} width={60} />
          <CheckRow label="自动换行" desc="行超出编辑器宽度时自动折行" checked={s.editorWordWrap} onChange={(v) => patch({ editorWordWrap: v })} />
          <CheckRow label="Minimap" desc="右侧缩略图导航" checked={s.editorMinimap} onChange={(v) => patch({ editorMinimap: v })} />
          <SelectRow label="行号" desc="是否显示左侧行号" value={s.editorLineNumbers}
            options={[{ value: 'on', label: '显示' }, { value: 'off', label: '隐藏' }]}
            onChange={(v) => patch({ editorLineNumbers: v })} width={90} />

          <div style={groupStyle}>连接 & 查询</div>
          <SelectRow label="默认传输" desc="新查询工作区的默认通信方式"
            value={s.defaultTransport}
            options={[{ value: 'http', label: 'HTTP msgpack' }, { value: 'ws', label: 'WebSocket' }]}
            onChange={(v) => patch({ defaultTransport: v })} width={140} />
          <CheckRow label="表格换行" desc="结果表格单元格内容自动折行" checked={s.gridWrap} onChange={(v) => patch({ gridWrap: v })} />
          <Row label="服务端 Token" desc="服务端启用 POLYDB_SERVER_TOKEN 时的 Bearer 凭据；留空表示服务端未开鉴权">
            <input
              type="password"
              value={token}
              placeholder="留空 = 不鉴权"
              onChange={(e) => { setToken(e.target.value); setServerToken(e.target.value.trim()); }}
              style={{ width: 220, fontFamily: 'monospace', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 3, padding: '3px 6px' }}
            />
          </Row>

          <div style={{ display: 'flex', gap: 8, padding: '12px 0' }}>
            <button
              onClick={reset}
              style={{
                fontFamily: 'inherit', fontSize: 12, padding: '4px 10px',
                background: 'var(--bg-alt, rgba(0,0,0,0.1))', color: 'var(--fg)',
                border: '1px solid var(--border)', borderRadius: 3, cursor: 'pointer',
              }}
            >
              恢复默认
            </button>
            <button
              onClick={onClose}
              style={{
                fontFamily: 'inherit', fontSize: 12, padding: '4px 10px',
                background: 'var(--accent, #2d68c8)', color: '#fff',
                border: '1px solid var(--accent, #2d68c8)', borderRadius: 3, cursor: 'pointer',
              }}
            >
              关闭
            </button>
            <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--muted)', fontFamily: 'monospace' }}>
              变更即时保存到 <span style={kbd}>polydb.settings.v1</span>
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
