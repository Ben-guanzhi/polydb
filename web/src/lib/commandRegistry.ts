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

interface Snapshot {
  version: number;
  commands: CommandItem[];
}

const listeners = new Set<(s: Snapshot) => void>();
const registry: Map<string, CommandItem> = new Map();
let version = 0;

function emit() {
  version++;
  const snap: Snapshot = { version, commands: Array.from(registry.values()) };
  for (const l of listeners) l(snap);
}

export function registerCommand(cmd: CommandItem): () => void {
  registry.set(cmd.id, cmd);
  emit();
  return () => {
    if (registry.get(cmd.id) === cmd) {
      registry.delete(cmd.id);
      emit();
    }
  };
}

export function onCommands(cb: (s: Snapshot) => void): () => void {
  listeners.add(cb);
  cb({ version, commands: Array.from(registry.values()) });
  return () => { listeners.delete(cb); };
}
