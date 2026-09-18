import { useEffect, useMemo, useRef, useState } from 'react';

export interface CommandItem {
  id: string;
  label: string;
  category: string;
  hotkey?: string;
  keywords?: string[];
  icon?: React.ReactNode;
  run: () => void;
  enabled?: () => boolean | undefined;
}

interface Props {
  open: boolean;
  onClose: () => void;
  commands: CommandItem[];
  placeholder?: string;
}

function fuzzyScore(query: string, target: string): number {
  if (!query) return 1;
  const q = query.toLowerCase();
  const t = target.toLowerCase();
  let qi = 0;
  let score = 0;
  let streak = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) {
      qi++;
      streak++;
      score += 1 + streak * 2;
      if (ti === 0 || t[ti - 1] === ' ' || t[ti - 1] === '-' || t[ti - 1] === '/') score += 6;
    } else {
      streak = 0;
    }
  }
  return qi === q.length ? score : -1;
}

function isCmdEnabled(c: CommandItem): boolean {
  if (c.enabled) {
    let v: boolean | undefined;
    try { v = c.enabled(); } catch { v = false; }
    return v !== false;
  }
  return true;
}

function scoreCommand(query: string, cmd: CommandItem): number {
  if (!query) return 1;
  const parts = [cmd.label, cmd.category, ...(cmd.keywords ?? [])].join('  ');
  return fuzzyScore(query, parts);
}

export default function CommandPalette({ open, onClose, commands, placeholder = '输入命令…' }: Props) {
  const [query, setQuery] = useState('');
  const [selIdx, setSelIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (open) {
      setQuery('');
      setSelIdx(0);
      const t = setTimeout(() => inputRef.current?.focus(), 0);
      return () => clearTimeout(t);
    }
  }, [open]);

  const filtered = useMemo(() => {
    const scored = commands
      .map((c) => ({ c, s: scoreCommand(query, c) }))
      .filter((x) => x.s >= 0);
    scored.sort((a, b) => b.s - a.s || a.c.category.localeCompare(b.c.category) || a.c.label.localeCompare(b.c.label));
    return scored.map((x) => x.c);
  }, [commands, query]);

  useEffect(() => {
    if (selIdx >= filtered.length) setSelIdx(Math.max(0, filtered.length - 1));
  }, [filtered.length, selIdx]);

  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); e.stopPropagation(); setSelIdx((n) => Math.min(n + 1, Math.max(0, filtered.length - 1))); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); e.stopPropagation(); setSelIdx((n) => Math.max(0, n - 1)); }
      else if (e.key === 'Enter') {
        e.preventDefault(); e.stopPropagation();
        const c = filtered[selIdx];
        if (c && isCmdEnabled(c)) { c.run(); onClose(); }
      } else if (e.key === 'Tab') { e.preventDefault(); setSelIdx((n) => (n + 1) % Math.max(1, filtered.length)); }
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
  }, [open, filtered, selIdx, onClose]);

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-idx="${selIdx}"]`);
    el?.scrollIntoView({ block: 'nearest' });
  }, [selIdx]);

  const groups = useMemo(() => {
    const order: [string, CommandItem[]][] = [];
    const idx = new Map<string, number>();
    for (const c of filtered) {
      const i = idx.get(c.category);
      if (i === undefined) { idx.set(c.category, order.length); order.push([c.category, [c]]); }
      else order[i][1].push(c);
    }
    return order;
  }, [filtered]);

  if (!open) return null;

  let flat = 0;
  const rows: { cat: string; isGroupStart: boolean; items: CommandItem[] }[] = groups.map(([cat, items], i) => ({
    cat, isGroupStart: i > 0, items,
  }));

  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.35)',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'center',
        padding: '12vh 16px 16px',
      }}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div
        style={
          {
            width: '100%', maxWidth: 640, height: 'min(65vh, 560px)',
            background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6,
            boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
            display: 'flex', flexDirection: 'column', overflow: 'hidden',
          } as React.CSSProperties
        }
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 12px', borderBottom: '1px solid var(--border)' }}>
          <span style={{ color: 'var(--muted)', fontSize: 14 }}>⌘</span>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setSelIdx(0); }}
            placeholder={placeholder}
            style={{
              flex: 1, background: 'transparent', border: 'none', outline: 'none',
              color: 'var(--fg)', fontSize: 14, fontFamily: 'inherit',
            }}
          />
          <kbd style={{ fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 3, padding: '1px 5px' }}>Esc</kbd>
        </div>
        <div ref={listRef} style={{ flex: 1, overflow: 'auto', padding: 4 }}>
          {filtered.length === 0 ? (
            <div style={{ padding: '20px 12px', textAlign: 'center', color: 'var(--muted)', fontSize: 12 }}>
              无匹配命令
            </div>
          ) : (
            rows.map((row, gi) => (
              <div key={gi}>
                {row.isGroupStart && <div style={{ height: 1, background: 'var(--border)', margin: '4px 8px' }} />}
                <div style={{ padding: '4px 12px 2px', fontSize: 10, color: 'var(--muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
                  {row.cat}
                </div>
                {row.items.map((c) => {
                  const idx = flat++;
                  const selected = idx === selIdx;
                  const enabled = isCmdEnabled(c);
                  return (
                    <div
                      key={c.id}
                      data-idx={idx}
                      onMouseEnter={() => setSelIdx(idx)}
                      onClick={() => { if (!enabled) return; c.run(); onClose(); }}
                      style={selected && enabled
                        ? { display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', borderRadius: 3, background: 'var(--accent-dim)', color: 'var(--fg)', cursor: 'pointer' }
                        : { display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', borderRadius: 3, color: enabled ? 'var(--fg)' : 'var(--muted)', cursor: enabled ? 'pointer' : 'default', opacity: enabled ? 1 : 0.5 }}
                    >
                      {c.icon}
                      <span style={{ flex: 1, fontSize: 13 }}>{c.label}</span>
                      {c.hotkey && (
                        <kbd style={{ fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 3, padding: '1px 5px' }}>
                          {c.hotkey}
                        </kbd>
                      )}
                    </div>
                  );
                })}
              </div>
            ))
          )}
        </div>
        {filtered.length > 0 && (
          <div style={
            {
              display: 'flex', gap: 12, alignItems: 'center', padding: '4px 12px',
              borderTop: '1px solid var(--border)', fontSize: 10, color: 'var(--muted)', flexWrap: 'wrap',
            } as React.CSSProperties
          }>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 2 }}>↑↓</kbd> 选择</span>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 2 }}>Enter</kbd> 执行</span>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 2 }}>Esc</kbd> 关闭</span>
            <span style={{ marginLeft: 'auto' }}>{filtered.length} / {commands.length}</span>
          </div>
        )}
      </div>
    </div>
  );
}
