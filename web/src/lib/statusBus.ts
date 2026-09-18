export interface EditorStatus {
  transport: 'http' | 'ws';
  language: 'sql' | 'redis';
  rows: number;
  columns: number;
  elapsed: number | null;
  busy: boolean;
  cursorLine: number;
  cursorCol: number;
  cursorSel: number;
  paramCount: number;
  tabTitle: string;
  tabCount: number;
  lintErrors: number;
  lintWarnings: number;
  lintInfos: number;
  message: string;
  messageAt: number;
}

type Listener = (s: Partial<EditorStatus>) => void;

const listeners = new Set<Listener>();
let state: Partial<EditorStatus> = {};

export function publishEditorStatus(patch: Partial<EditorStatus>) {
  state = { ...state, ...patch };
  for (const l of listeners) l(state);
}

export function onEditorStatus(cb: Listener): () => void {
  listeners.add(cb);
  cb(state);
  return () => { listeners.delete(cb); };
}

export function clearEditorStatus() {
  state = {};
  for (const l of listeners) l(state);
}

export function clearMessage() {
  delete state.message;
  delete state.messageAt;
  const next = { ...state };
  state = next;
  for (const l of listeners) l(state);
}
