import type { ConnectionInfo, DdlScript, DdlStatement, SchemaDiffEntry, SchemaDiffOp } from '../api';

// Compare & Sync E4 的纯逻辑层：分组、勾选集、执行分派、状态持久化。
//
// 与 Rust/Go 服务端的关系：这里**只做客户端预检与呈现**，真相源始终在服务端——
// 陈旧 id 与「重建未整组勾选」由 behavior.md §15.5 的两条守卫报错，本层不重复判定、
// 只把缺的条目提示出来。

export type SyncStep = 'source' | 'target' | 'review' | 'apply';

export const STEP_ORDER: SyncStep[] = ['source', 'target', 'review', 'apply'];

export const STEP_LABEL: Record<SyncStep, string> = {
  source: '选基准侧',
  target: '选期望侧',
  review: '差异与勾选',
  apply: '脚本与应用',
};

/** 相位 → 中文段名（数值在 E1 定死，与服务端排序一致）。 */
export const PHASE_LABEL: Record<number, string> = {
  0: '事务外前置（PRAGMA）',
  10: '删除外键',
  20: '删除索引',
  30: '删除列',
  40: '删除表',
  50: '新建表',
  60: '新增列',
  70: '变更列',
  80: '新增索引',
  90: '新增外键',
  255: '事务外收尾（PRAGMA）',
};

export const OP_LABEL: Record<SchemaDiffOp, string> = {
  add_column: '加列',
  add_foreign_key: '加外键',
  add_index: '加索引',
  alter_column: '改列',
  create_table: '建表',
  drop_column: '删列',
  drop_foreign_key: '删外键',
  drop_index: '删索引',
  drop_table: '删表',
};

/** 必然走整表重建的动作（E3 规则 2）。drop_column / autoindex 的隐性重建判不出来，交服务端兜。 */
const ALWAYS_REBUILD_OPS: SchemaDiffOp[] = ['alter_column', 'add_foreign_key', 'drop_foreign_key'];

export interface PhaseGroup {
  phase: number;
  label: string;
  entries: SchemaDiffEntry[];
}

/** 按相位分组，保持服务端给的顺序（entries 已按 phase 排序）。 */
export function groupByPhase(entries: SchemaDiffEntry[]): PhaseGroup[] {
  const out: PhaseGroup[] = [];
  for (const e of entries) {
    const last = out[out.length - 1];
    if (last && last.phase === e.phase) {
      last.entries.push(e);
      continue;
    }
    out.push({ phase: e.phase, label: PHASE_LABEL[e.phase] ?? `相位 ${e.phase}`, entries: [e] });
  }
  return out;
}

/** 服务端给的默认勾选集（破坏性条目默认不勾，除非 select_destructive）。 */
export function defaultSelection(entries: SchemaDiffEntry[]): string[] {
  return entries.filter((e) => e.selected).map((e) => e.id);
}

export function toggleEntry(sel: string[], id: string): string[] {
  return sel.includes(id) ? sel.filter((s) => s !== id) : [...sel, id];
}

/** 勾选集还原成计划顺序的 id 数组——服务端按重算出的计划校验，顺序无关但去重要保序。 */
export function selectedIds(entries: SchemaDiffEntry[], sel: string[]): string[] {
  const set = new Set(sel);
  return entries.filter((e) => set.has(e.id)).map((e) => e.id);
}

export function entriesOfTable(entries: SchemaDiffEntry[], table: string): SchemaDiffEntry[] {
  return entries.filter((e) => e.table === table);
}

/** 本次计划里"大概率进重建路径"的表：用于提示整组勾选，不是判定。 */
export function rebuildLikelyTables(entries: SchemaDiffEntry[]): string[] {
  const out: string[] = [];
  for (const e of entries) {
    if (!ALWAYS_REBUILD_OPS.includes(e.op)) continue;
    const lower = e.table.toLowerCase();
    if (!out.some((t) => t.toLowerCase() === lower)) out.push(e.table);
  }
  return out;
}

export interface GroupGap {
  table: string;
  missing: string[];
}

/**
 * 已勾选某表的重建动作、却漏勾同表其他条目 ⇒ 服务端会 400。
 * 这里提前把缺口显示出来，并把 missing 补全成整组勾选。
 */
export function groupGaps(entries: SchemaDiffEntry[], sel: string[]): GroupGap[] {
  const gaps: GroupGap[] = [];
  const chosen = new Set(sel);
  for (const table of rebuildLikelyTables(entries)) {
    const group = entriesOfTable(entries, table);
    if (!group.some((e) => chosen.has(e.id) && ALWAYS_REBUILD_OPS.includes(e.op))) continue;
    const missing = group.filter((e) => !chosen.has(e.id)).map((e) => e.id);
    if (missing.length) gaps.push({ table, missing });
  }
  return gaps;
}

/** 把缺口一次性补齐（"整表一起勾"）。 */
export function fillGroupGaps(entries: SchemaDiffEntry[], sel: string[]): string[] {
  const out = [...sel];
  const seen = new Set(out);
  for (const gap of groupGaps(entries, sel)) {
    for (const id of gap.missing) {
      if (seen.has(id)) continue;
      seen.add(id);
      out.push(id);
    }
  }
  // 保持计划顺序，避免 id 顺序影响文案展示
  return entries.filter((e) => seen.has(e.id)).map((e) => e.id);
}

export interface ScriptSummary {
  total: number;
  nonReversible: number;
  warned: number;
  tables: string[];
}

export function summarizeScript(stmts: DdlStatement[]): ScriptSummary {
  const tables: string[] = [];
  let nonReversible = 0;
  let warned = 0;
  for (const s of stmts) {
    if (s.non_reversible) nonReversible++;
    if (s.warnings && s.warnings.length) warned++;
    if (s.table && !tables.some((t) => t.toLowerCase() === s.table.toLowerCase())) tables.push(s.table);
  }
  return { total: stmts.length, nonReversible, warned, tables };
}

export interface ExecutionPlan {
  /** 必须在事务外先跑（PRAGMA foreign_keys=OFF） */
  pragmaHead: DdlStatement | null;
  /** 必须在事务外收尾（PRAGMA foreign_keys=ON） */
  pragmaTail: DdlStatement | null;
  /** 主体语句 */
  body: DdlStatement[];
  /** 主体能否整体包进一个事务（false ⇒ 逐条跑，失败留中间态） */
  wrapInTx: boolean;
}

/**
 * 按响应里两个能力位分派执行方式（behavior.md §15.4）：
 * pragmas_outside_tx=true ⇒ 首尾两条走 /query 在事务外（事务内是 no-op）；
 * ddl_in_tx=false ⇒ 该方言的 DDL 会隐式提交，主体只能逐条跑。
 */
export function planExecution(script: DdlScript): ExecutionPlan {
  const head = script.statements.find((s) => s.phase === 0 && s.entry_ids.length === 0) ?? null;
  const tail = script.statements.find((s) => s.phase === 255 && s.entry_ids.length === 0) ?? null;
  const body = script.statements.filter((s) => s !== head && s !== tail);
  return { pragmaHead: head, pragmaTail: tail, body, wrapInTx: script.ddl_in_tx };
}

export interface FailedStatement {
  /** 在整份脚本（含 PRAGMA）里的下标 */
  index: number;
  sql: string;
  phase: number;
  table: string;
  reason: string;
  entry_ids: string[];
}

export interface ApplyOutcome {
  ran: number;
  failed: FailedStatement[];
  /** 主体是否走了事务；true 时失败已整体回滚 */
  rolledBack: boolean;
  /** 失败后剩余未跑的语句数 */
  skipped: number;
}

export function emptyOutcome(): ApplyOutcome {
  return { ran: 0, failed: [], rolledBack: false, skipped: 0 };
}

/** runScript 需要的最小执行面：由调用方注入（组件里接 api.ts，测试里接内存假实现）。 */
export interface ApplyDeps {
  execute(connId: string, sql: string): Promise<void>;
  /** 开事务；返回 tx id */
  begin(connId: string): Promise<string>;
  executeInTx(txId: string, sql: string): Promise<void>;
  commit(txId: string): Promise<void>;
  rollback(txId: string): Promise<void>;
  /** 每成功跑完一条回调一次（done 从 1 起） */
  onProgress?(done: number, total: number): void;
  /** 每条语句之前检查一次：置真即以"已取消"记失败并停住 */
  cancelled?(): boolean;
  /** 失败原因怎么变成人话（组件沿用 ImportModalParts 的 toMsg） */
  errText?(e: unknown): string;
}

/**
 * 执行一份脚本（正向与回滚共用：调用方把要跑的那一份放进 statements）。
 *
 * 分派（behavior.md §15.4）：
 *   - ddl_in_tx + pragmas_outside_tx ⇒ 首尾 PRAGMA 事务外跑，主体包**一个**事务；
 *   - pragmas_outside_tx=false       ⇒ 整份脚本（含任何 PRAGMA）包进同一个事务；
 *   - ddl_in_tx=false                ⇒ 该方言 DDL 会隐式提交，全脚本逐条跑（失败留中间态）。
 * 只记首个失败并停住；主体在事务里时失败即整体回滚，回滚本身失败则把 tx 留给调用方兜。
 * `ran` 计的是"执行过没报错"，不代表落地——事务分支回滚后库里什么都不会留下。
 */
export async function runScript(sc: DdlScript, connId: string, deps: ApplyDeps): Promise<ApplyOutcome> {
  const out = emptyOutcome();
  const plan = planExecution(sc);
  const text = deps.errText ?? ((e: unknown) => (e instanceof Error ? e.message : String(e)));

  const steps: { stmt: DdlStatement; inTx: boolean }[] = [];
  if (plan.wrapInTx && sc.pragmas_outside_tx) {
    if (plan.pragmaHead) steps.push({ stmt: plan.pragmaHead, inTx: false });
    for (const s of plan.body) steps.push({ stmt: s, inTx: true });
    if (plan.pragmaTail) steps.push({ stmt: plan.pragmaTail, inTx: false });
  } else {
    for (const s of sc.statements) steps.push({ stmt: s, inTx: plan.wrapInTx });
  }

  const fail = (index: number, s: DdlStatement, e: unknown): void => {
    out.failed.push({
      index,
      sql: s.sql,
      phase: s.phase,
      table: s.table,
      reason: text(e),
      entry_ids: s.entry_ids,
    });
    out.skipped = Math.max(0, steps.length - index - 1);
  };

  let tx: string | null = null;
  for (let i = 0; i < steps.length; i++) {
    const { stmt, inTx } = steps[i];
    if (deps.cancelled?.()) {
      fail(i, stmt, new Error('已取消：本条及剩余语句未执行'));
      break;
    }
    try {
      if (inTx && !tx) tx = await deps.begin(connId);
      // 主体跑完接着是事务外 PRAGMA：先提交，PRAGMA 进事务是 no-op。
      if (!inTx && tx) {
        await deps.commit(tx);
        tx = null;
      }
      if (inTx && tx) await deps.executeInTx(tx, stmt.sql);
      else await deps.execute(connId, stmt.sql);
      out.ran++;
      deps.onProgress?.(out.ran, steps.length);
    } catch (e) {
      fail(i, stmt, e);
      break;
    }
  }

  if (tx) {
    if (out.failed.length) {
      try {
        await deps.rollback(tx);
        out.rolledBack = true;
      } catch { /* 回滚失败：tx id 仍在 out 之外由调用方记住，收尾时再兜一次 */ }
    } else {
      try {
        await deps.commit(tx);
      } catch (e) {
        fail(steps.length - 1, steps[steps.length - 1].stmt, e);
      }
      tx = null;
    }
  }
  return out;
}

/** 目标侧候选：Redis 无模式（服务端 501），跨种类服务端同样拒绝 ⇒ 只允许同 kind。 */
export function candidateConnections(all: ConnectionInfo[], source: ConnectionInfo | null): ConnectionInfo[] {
  if (!source) return [];
  return all.filter((c) => c.kind === source.kind && c.kind !== 'redis');
}

export function isComparableKind(kind: string): boolean {
  return kind !== 'redis';
}

// ─── 状态持久化 ─────────────────────────────────────────────
//
// 只存连接/schema/选项/步序。**勾选集故意不落库**：服务端对不在本次计划里的 id
// 直接 400（§15.5 的过期守卫），存下来的勾选集下次打开必然可能是陈旧的。

const STATE_KEY = 'polydb.compareSync.v1';

export interface SyncState {
  sourceId: string;
  sourceSchema: string;
  targetId: string;
  targetSchema: string;
  ignoreDefaults: boolean;
  selectDestructive: boolean;
  step: SyncStep;
}

export const EMPTY_STATE: SyncState = {
  sourceId: '',
  sourceSchema: '',
  targetId: '',
  targetSchema: '',
  ignoreDefaults: false,
  selectDestructive: false,
  step: 'source',
};

export function loadSyncState(): SyncState {
  try {
    const raw = localStorage.getItem(STATE_KEY);
    if (!raw) return { ...EMPTY_STATE };
    const o = JSON.parse(raw) as Partial<SyncState>;
    const steps = STEP_ORDER as string[];
    return {
      sourceId: typeof o.sourceId === 'string' ? o.sourceId : '',
      sourceSchema: typeof o.sourceSchema === 'string' ? o.sourceSchema : '',
      targetId: typeof o.targetId === 'string' ? o.targetId : '',
      targetSchema: typeof o.targetSchema === 'string' ? o.targetSchema : '',
      ignoreDefaults: o.ignoreDefaults === true,
      selectDestructive: o.selectDestructive === true,
      // 差异条目与脚本只在内存里（陈旧 id 必须让服务端 400，见 §15.5），
      // 所以重载后最深的可续步骤是"选期望侧"——review/apply 一律回落。
      step: (o.step === 'review' || o.step === 'apply') ? 'target'
        : (o.step && steps.includes(o.step) ? o.step : 'source'),
    };
  } catch {
    return { ...EMPTY_STATE };
  }
}

export function saveSyncState(s: SyncState): void {
  try {
    localStorage.setItem(STATE_KEY, JSON.stringify(s));
  } catch { /* ignore */ }
}
