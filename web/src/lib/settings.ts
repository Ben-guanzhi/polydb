export interface Settings {
  theme: 'vs' | 'vs-dark' | 'hc-black';
  // M15 应用级主题（<html data-theme>；与编辑器配色 theme 分离）
  appTheme: 'light' | 'dark';
  editorFontSize: number;
  editorTabSize: number;
  editorWordWrap: boolean;
  editorMinimap: boolean;
  editorLineNumbers: 'on' | 'off';
  defaultTransport: 'http' | 'ws';
  gridWrap: boolean;
}

export const DEFAULT_SETTINGS: Settings = {
  theme: 'vs',
  appTheme: 'light',
  editorFontSize: 13,
  editorTabSize: 2,
  editorWordWrap: false,
  editorMinimap: true,
  editorLineNumbers: 'on',
  defaultTransport: 'http',
  gridWrap: false,
};

const KEY = 'polydb.settings.v1';

export function loadSettings(): Settings {
  const base = { ...DEFAULT_SETTINGS };
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return base;
    const obj = JSON.parse(raw);
    if (obj && typeof obj === 'object') {
      for (const k of Object.keys(DEFAULT_SETTINGS) as (keyof Settings)[]) {
        const v = obj[k];
        if (v === undefined) continue;
        if (k === 'theme' && (v === 'vs' || v === 'vs-dark' || v === 'hc-black')) base.theme = v;
        else if (k === 'appTheme' && (v === 'light' || v === 'dark')) base.appTheme = v;
        else if (k === 'editorFontSize' && typeof v === 'number') base.editorFontSize = clampNum(v, 10, 28);
        else if (k === 'editorTabSize' && typeof v === 'number') base.editorTabSize = clampNum(v, 1, 8);
        else if (k === 'editorWordWrap' && typeof v === 'boolean') base.editorWordWrap = v;
        else if (k === 'editorMinimap' && typeof v === 'boolean') base.editorMinimap = v;
        else if (k === 'editorLineNumbers' && (v === 'on' || v === 'off')) base.editorLineNumbers = v;
        else if (k === 'defaultTransport' && (v === 'http' || v === 'ws')) base.defaultTransport = v;
        else if (k === 'gridWrap' && typeof v === 'boolean') base.gridWrap = v;
      }
    }
  } catch { /* ignore */ }
  return base;
}

function clampNum(v: number, lo: number, hi: number): number {
  if (!Number.isFinite(v)) return lo;
  return Math.max(lo, Math.min(hi, Math.round(v)));
}

// 把应用主题应用到 <html data-theme>（CSS 变量切换）。
export function applyAppTheme(appTheme: 'light' | 'dark'): void {
  const el = typeof document !== 'undefined' ? document.documentElement : null;
  if (!el) return;
  if (appTheme === 'dark') el.setAttribute('data-theme', 'dark');
  else el.removeAttribute('data-theme');
}

export function saveSettings(s: Settings) {
  try { localStorage.setItem(KEY, JSON.stringify(s)); } catch { /* ignore */ }
}

export function updateSettings(patch: Partial<Settings>): Settings {
  const next = { ...loadSettings(), ...patch };
  saveSettings(next);
  try { localStorage.setItem('polydb.theme', next.theme); } catch { /* ignore */ }
  applyAppTheme(next.appTheme);
  window.dispatchEvent(new CustomEvent('polydb-settings-changed', { detail: { settings: next } }));
  window.dispatchEvent(new CustomEvent('polydb-theme-changed', { detail: { theme: next.theme } }));
  return next;
}
