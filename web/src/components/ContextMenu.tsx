import { useEffect, useRef, useState } from 'react';

export interface ContextMenuItem {
  key: string;
  label: string;
  shortcut?: string;
  icon?: string;
  checked?: boolean;
  disabled?: boolean;
  onClick?: () => void;
  children?: ContextMenuEntry[];
}

export type ContextMenuEntry = ContextMenuItem | '---' | { header: string };

interface Props {
  x: number;
  y: number;
  items: ContextMenuEntry[];
  onClose: () => void;
  width?: number;
  maxHeight?: number;
}

const ITEM_H = 26;
const SEP_H = 9;
const HEADER_H = 26;

export default function ContextMenu({ x, y, items, onClose, width = 240, maxHeight = 380 }: Props) {
  const [focused, setFocused] = useState<number>(() => firstEnabled(items));
  const [openSubIdx, setOpenSubIdx] = useState<number | null>(null);
  const [subFocused, setSubFocused] = useState<number>(-1);
  const menuRef = useRef<HTMLUListElement | null>(null);
  const subRef = useRef<HTMLUListElement | null>(null);

  const openSub: ContextMenuItem['children'] | undefined =
    openSubIdx != null ? ((items[openSubIdx] as ContextMenuItem | undefined)?.children) : undefined;

  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        ev.preventDefault();
        if (openSubIdx != null) {
          setOpenSubIdx(null);
          setSubFocused(-1);
        } else {
          onClose();
        }
        return;
      }
      if (openSubIdx != null && openSub) {
        if (ev.key === 'ArrowLeft') {
          ev.preventDefault();
          setOpenSubIdx(null);
          setSubFocused(-1);
          return;
        }
        if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
          ev.preventDefault();
          setSubFocused((f) => moveFocus(openSub, f, ev.key === 'ArrowDown' ? 1 : -1));
          return;
        }
        if (ev.key === 'Enter') {
          ev.preventDefault();
          const it = openSub[subFocused];
          if (it && typeof it === 'object' && !('header' in it) && !it.disabled) {
            it.onClick?.();
            onClose();
          }
          return;
        }
      }
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
        ev.preventDefault();
        setFocused((f) => moveFocus(items, f, ev.key === 'ArrowDown' ? 1 : -1));
        return;
      }
      if (ev.key === 'ArrowRight') {
        const it = items[focused];
        if (it && typeof it === 'object' && !('header' in it) && !it.disabled && it.children && it.children.length > 0) {
          ev.preventDefault();
          setOpenSubIdx(focused);
          setSubFocused(firstEnabled(it.children));
        }
        return;
      }
      if (ev.key === 'Enter') {
        ev.preventDefault();
        const it = items[focused];
        if (it && typeof it === 'object' && !('header' in it) && !it.disabled) {
          if (it.children && it.children.length > 0) {
            setOpenSubIdx(focused);
            setSubFocused(firstEnabled(it.children));
          } else {
            it.onClick?.();
            onClose();
          }
        }
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  }, [items, focused, onClose, openSubIdx, openSub, subFocused]);

  useEffect(() => {
    if (menuRef.current) {
      const li = menuRef.current.querySelector(`li[data-idx="${focused}"]`) as HTMLLIElement | undefined;
      li?.focus({ preventScroll: false });
    }
    if (openSubIdx != null && subRef.current) {
      const li = subRef.current.querySelector(`li[data-idx="${subFocused}"]`) as HTMLLIElement | undefined;
      li?.focus({ preventScroll: false });
    }
  }, [focused, openSubIdx, subFocused]);

  const menuHeightEstimate = estimateHeight(items);
  const left = Math.max(0, Math.min(x, window.innerWidth - width - 8));
  const top = Math.max(0, Math.min(y, window.innerHeight - menuHeightEstimate - 8));

  const subLeft = openSubIdx != null ? Math.max(0, Math.min(left + width + 4, window.innerWidth - 220)) : 0;
  const subTop = openSubIdx != null ? Math.max(0, top + Math.min(openSubIdx * ITEM_H, window.innerHeight - 200)) : 0;

  return (
    <>
      <div
        style={{ position: 'fixed', inset: 0, zIndex: 99, background: 'transparent' }}
        onClick={() => { setOpenSubIdx(null); onClose(); }}
        onContextMenu={(e) => { e.preventDefault(); setOpenSubIdx(null); onClose(); }}
      />
      <ul
        ref={menuRef}
        style={{
          position: 'fixed',
          left,
          top,
          width,
          maxHeight,
          overflowY: 'auto',
          background: 'var(--bg-elev)',
          border: '1px solid var(--border)',
          borderRadius: 4,
          boxShadow: '0 4px 14px rgba(0,0,0,0.2)',
          padding: 4,
          margin: 0,
          listStyle: 'none',
          zIndex: 100,
          fontSize: 12,
          color: 'var(--fg)',
          fontFamily: 'inherit',
        }}
      >
        {items.map((it, i) => {
          if (it === '---') {
            return <li key={`sep-${i}`} style={{ height: 1, background: 'var(--border)', margin: '4px 0', listStyle: 'none' }} />;
          }
          if (typeof it === 'object' && 'header' in it) {
            return (
              <li
                key={`h-${i}`}
                style={{
                  padding: '6px 10px 4px',
                  fontSize: 11,
                  color: 'var(--muted)',
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                  borderBottom: '1px solid var(--border)',
                  marginBottom: 4,
                  listStyle: 'none',
                }}
              >
                {it.header}
              </li>
            );
          }
          const isFocused = i === focused;
          const hasChildren = !!(it.children && it.children.length > 0);
          return (
            <li
              key={it.key}
              data-idx={i}
              tabIndex={-1}
              onClick={(e) => {
                if (it.disabled) return;
                if (hasChildren) {
                  e.stopPropagation();
                  if (openSubIdx === i) {
                    setOpenSubIdx(null);
                    setSubFocused(-1);
                  } else {
                    setOpenSubIdx(i);
                    setSubFocused(firstEnabled(it.children!));
                  }
                } else {
                  it.onClick?.();
                  onClose();
                }
              }}
              onMouseEnter={() => { if (!it.disabled) setFocused(i); }}
              onKeyDown={(ev) => {
                if (ev.key === ' ' || ev.key === 'Enter') {
                  ev.preventDefault();
                  if (it.disabled) return;
                  if (hasChildren) {
                    if (openSubIdx === i) {
                      setOpenSubIdx(null);
                      setSubFocused(-1);
                    } else {
                      setOpenSubIdx(i);
                      setSubFocused(firstEnabled(it.children!));
                    }
                  } else {
                    it.onClick?.();
                    onClose();
                  }
                }
              }}
              style={{
                padding: '5px 10px',
                cursor: it.disabled ? 'not-allowed' : 'pointer',
                opacity: it.disabled ? 0.4 : 1,
                borderRadius: 3,
                whiteSpace: 'nowrap',
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                outline: 'none',
                background: isFocused ? 'var(--accent-dim)' : 'transparent',
              }}
            >
              <span style={{ width: 14, textAlign: 'center', flexShrink: 0, color: it.checked ? 'var(--accent)' : 'transparent', fontSize: 12 }}>
                {it.checked ? '✓' : '·'}
              </span>
              {it.icon && <span style={{ width: 14, textAlign: 'center', flexShrink: 0, fontSize: 12 }}>{it.icon}</span>}
              <span style={{ flex: 1 }}>{it.label}</span>
              {it.shortcut && (
                <span style={{ fontSize: 10, color: 'var(--muted)', fontFamily: 'monospace' }}>{it.shortcut}</span>
              )}
              {hasChildren && (
                <span style={{ fontSize: 10, color: 'var(--muted)', margin: '0 4px' }}>›</span>
              )}
            </li>
          );
        })}
      </ul>
      {openSubIdx != null && openSub && (
        <ul
          ref={subRef}
          style={{
            position: 'fixed',
            left: subLeft,
            top: subTop,
            width: 220,
            maxHeight: 380,
            overflowY: 'auto',
            background: 'var(--bg-elev)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            boxShadow: '0 4px 14px rgba(0,0,0,0.2)',
            padding: 4,
            margin: 0,
            listStyle: 'none',
            zIndex: 101,
            fontSize: 12,
            color: 'var(--fg)',
            fontFamily: 'inherit',
          }}
        >
          {openSub.map((it, i) => {
            if (it === '---') {
              return <li key={`sep-${i}`} style={{ height: 1, background: 'var(--border)', margin: '4px 0', listStyle: 'none' }} />;
            }
            if (typeof it === 'object' && 'header' in it) {
              return (
                <li
                  key={`h-${i}`}
                  style={{
                    padding: '6px 10px 4px',
                    fontSize: 11,
                    color: 'var(--muted)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.04em',
                    borderBottom: '1px solid var(--border)',
                    marginBottom: 4,
                    listStyle: 'none',
                  }}
                >
                  {it.header}
                </li>
              );
            }
            const isFocused = i === subFocused;
            return (
              <li
                key={it.key}
                data-idx={i}
                tabIndex={-1}
                onClick={(e) => {
                  e.stopPropagation();
                  if (it.disabled) return;
                  it.onClick?.();
                  onClose();
                }}
                onMouseEnter={() => { if (!it.disabled) setSubFocused(i); }}
                onKeyDown={(ev) => {
                  if (ev.key === ' ' || ev.key === 'Enter') {
                    ev.preventDefault();
                    if (it.disabled) return;
                    it.onClick?.();
                    onClose();
                  }
                }}
                style={{
                  padding: '5px 10px',
                  cursor: it.disabled ? 'not-allowed' : 'pointer',
                  opacity: it.disabled ? 0.4 : 1,
                  borderRadius: 3,
                  whiteSpace: 'nowrap',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  outline: 'none',
                  background: isFocused ? 'var(--accent-dim)' : 'transparent',
                }}
              >
                <span style={{ width: 14, textAlign: 'center', flexShrink: 0, color: it.checked ? 'var(--accent)' : 'transparent', fontSize: 12 }}>
                  {it.checked ? '✓' : '·'}
                </span>
                {it.icon && <span style={{ width: 14, textAlign: 'center', flexShrink: 0, fontSize: 12 }}>{it.icon}</span>}
                <span style={{ flex: 1 }}>{it.label}</span>
                {it.shortcut && (
                  <span style={{ fontSize: 10, color: 'var(--muted)', fontFamily: 'monospace' }}>{it.shortcut}</span>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}

function firstEnabled(items: ContextMenuEntry[]): number {
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (it === '---' || (typeof it === 'object' && 'header' in it)) continue;
    if (!it.disabled) return i;
  }
  return -1;
}

function moveFocus(items: ContextMenuEntry[], from: number, step: 1 | -1): number {
  const len = items.length;
  if (len === 0) return -1;
  for (let i = 1; i <= len; i++) {
    const idx = (from + step * i + len * 4) % len;
    const it = items[idx];
    if (it === '---' || (typeof it === 'object' && 'header' in it)) continue;
    if (it.disabled) continue;
    return idx;
  }
  return from;
}

function estimateHeight(items: ContextMenuEntry[]): number {
  let h = 8;
  for (const it of items) {
    if (it === '---') h += SEP_H;
    else if (typeof it === 'object' && 'header' in it) h += HEADER_H;
    else h += ITEM_H;
  }
  return h;
}
