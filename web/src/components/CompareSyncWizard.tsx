import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ConnectionInfo, DdlScript, DdlStatement, SchemaDiffEntry, SchemaDiffRequest } from '../api';
import * as api from '../lib/api';
import {
  STEP_LABEL,
  STEP_ORDER,
  candidateConnections,
  defaultSelection,
  emptyOutcome,
  fillGroupGaps,
  groupByPhase,
  groupGaps,
  isComparableKind,
  loadSyncState,
  OP_LABEL,
  planExecution,
  rebuildLikelyTables,
  saveSyncState,
  selectedIds,
  summarizeScript,
  toggleEntry,
  type ApplyOutcome,
  type FailedStatement,
  type SyncState,
  type SyncStep,
} from '../lib/compareSync';
import { styles, toMsg } from './ImportModalParts';

interface Props {
  open: boolean;
  onClose: () => void;
  connections: ConnectionInfo[];
  /** 从库表视图右键「与…结构比对」带入：预选基准侧连接 */
  prefill?: { connId: string } | null;
}

const DIR_HINT = '执行方向：语句作用在「基准侧」，把它改成期望侧的样子（behavior.md §14.2）。';

export default function CompareSyncWizard({ open, onClose, connections, prefill }: Props) {
  const [st, setSt] = useState<SyncState>(() => loadSyncState());
  const [entries, setEntries] = useState<SchemaDiffEntry[] | null>(null);
  const [sel, setSel] = useState<string[]>([]);
  const [script, setScript] = useState<DdlScript | null>(null);
  const [busy, setBusy] = useState<'' | 'diff' | 'ddl' | 'apply' | 'rollback'>('');
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [filter, setFilter] = useState('');
  const [progress, setProgress] = useState({ done: 0, total: 0 });
  const [outcome, setOutcome] = useState<ApplyOutcome | null>(null);
  // outcome 对应哪一份脚本：失败详情/灰显只对同一份列表有意义
  const [outcomeRun, setOutcomeRun] = useState<'forward' | 'rollback'>('forward');
  const [confirmText, setConfirmText] = useState('');
  const [askRollback, setAskRollback] = useState(false);
  const [showRollbackScript, setShowRollbackScript] = useState(false);
  const [schemas, setSchemas] = useState<{ source: string[]; target: string[] }>({ source: [], target: [] });

  const cancelRef = useRef(false);
  const txIdRef = useRef<string | null>(null);
  // 正向脚本是否真跑成功过：回滚脚本只对"本会话应用过的这一份"有意义
  // （§15.6：不可逆语句跑完就退不回去，脚本换一份也不能沿用旧记录）
  const [applied, setApplied] = useState(false);

  useEffect(() => { saveSyncState(st); }, [st]);

  useEffect(() => {
    if (!open) return;
    setError('');
    setNote('');
    if (prefill?.connId) setSt((s) => ({ ...s, sourceId: prefill.connId, step: 'source' }));
  }, [open, prefill]);

  // 选了连接就顺手把 schema 列出来；只有一个（SQLite 恒 'main'）时不显示下拉
  useEffect(() => {
    if (!open) return;
    let alive = true;
    const load = (id: string, side: 'source' | 'target') => {
      if (!id) { setSchemas((s) => ({ ...s, [side]: [] })); return; }
      void api.listSchemas(id)
        .then((r) => { if (alive) setSchemas((s) => ({ ...s, [side]: r.map((x) => x.name) })); })
        .catch(() => { if (alive) setSchemas((s) => ({ ...s, [side]: [] })); });
    };
    load(st.sourceId, 'source');
    load(st.targetId, 'target');
    return () => { alive = false; };
  }, [open, st.sourceId, st.targetId]);

  const patch = useCallback((p: Partial<SyncState>) => setSt((s) => ({ ...s, ...p })), []);

  const source = connections.find((c) => c.id === st.sourceId) ?? null;
  const target = connections.find((c) => c.id === st.targetId) ?? null;
  const targets = useMemo(() => candidateConnections(connections, source), [connections, source]);
  const kindBlocked = (!!source && !isComparableKind(source.kind)) || (!!target && !isComparableKind(target.kind));
  const kindMismatch = !!source && !!target && source.kind !== target.kind;

  // ─── 步序守卫 ────────────────────────────────────────────
  const guards: Record<SyncStep, { ok: boolean; why: string }> = {
    source: { ok: !!source, why: '先选基准侧连接' },
    target: { ok: !!target && !!entries, why: entries ? '再选期望侧连接' : '先完成一次比对' },
    review: { ok: !!entries && sel.length > 0, why: sel.length ? '至少勾一条变更' : '还没有勾选任何变更条目' },
    apply: { ok: !!script && script.statements.length > 0, why: '先生成脚本' },
  };

  const goto = (s: SyncStep) => {
    if (s === st.step) return;
    const idx = STEP_ORDER.indexOf(s);
    // 前进要逐步过守卫，后退恒放行
    if (idx > STEP_ORDER.indexOf(st.step)) {
      for (let i = 0; i < idx; i++) {
        const g = guards[STEP_ORDER[i]];
        if (!g.ok) { setError(g.why); return; }
      }
    }
    setError('');
    patch({ step: s });
  };

  // ─── 比对 ────────────────────────────────────────────────
  const diffReq = useMemo((): SchemaDiffRequest | null => {
    if (!source || !target) return null;
    const withSchema = (id: string, schema: string) => (schema ? { connection_id: id, schema } : { connection_id: id });
    return {
      source: withSchema(st.sourceId, st.sourceSchema),
      target: withSchema(st.targetId, st.targetSchema),
      options: { ignore_defaults: st.ignoreDefaults, select_destructive: st.selectDestructive },
    };
  }, [source, target, st.sourceId, st.sourceSchema, st.targetId, st.targetSchema, st.ignoreDefaults, st.selectDestructive]);

  const runDiff = useCallback(async () => {
    if (!diffReq) { setError('基准侧与期望侧都要选连接'); return; }
    setBusy('diff');
    setError('');
    setNote('');
    try {
      const plan = await api.schemaDiff(diffReq);
      const list = plan.entries ?? [];
      setEntries(list);
      setSel(defaultSelection(list));
      setScript(null);
      setApplied(false);
      setOutcome(null);
      setProgress({ done: 0, total: 0 });
      if (!list.length) setNote('两侧结构一致，无需同步。');
      patch({ step: list.length ? 'review' : st.step });
    } catch (e) {
      setError(toMsg(e));
    } finally {
      setBusy('');
    }
  }, [diffReq, patch, st.step]);

  const gaps = useMemo(() => (entries ? groupGaps(entries, sel) : []), [entries, sel]);
  const groups = useMemo(() => {
    if (!entries) return [];
    const q = filter.trim().toLowerCase();
    const list = q ? entries.filter((e) => e.table.toLowerCase().includes(q) || e.id.toLowerCase().includes(q)) : entries;
    return groupByPhase(list);
  }, [entries, filter]);

  const chosen = entries?.filter((e) => sel.includes(e.id)) ?? [];
  const chosenDestructive = chosen.filter((e) => e.destructive).length;
  const rebuildTables = useMemo(() => (entries ? rebuildLikelyTables(entries) : []), [entries]);

  // ─── 生成脚本 ────────────────────────────────────────────
  const generate = useCallback(async () => {
    if (!diffReq || !entries) return;
    const picked = selectedIds(entries, sel);
    if (!picked.length) { setError('至少要勾选一条变更'); return; }
    setBusy('ddl');
    setError('');
    setNote('');
    try {
      const sc = await api.schemaDdl({ ...diffReq, selected: picked });
      setScript(sc);
      setApplied(false);
      setOutcome(null);
      setProgress({ done: 0, total: 0 });
      if (!sc.statements.length) {
        setError('服务端返回空脚本：勾选的条目可能已不在当前计划里，请重新比对。');
        return;
      }
      patch({ step: 'apply' });
    } catch (e) {
      // 陈旧 id / 未整组勾选都在这里被服务端兜住（§15.5）
      const fixable = gaps.length > 0 && /整表重建|整组勾选/.test(toMsg(e));
      if (fixable) setNote(`${toMsg(e)} —— 可点「整表一起勾」补齐后重试。`);
      setError(toMsg(e));
    } finally {
      setBusy('');
    }
  }, [diffReq, entries, gaps.length, patch, sel]);

  // ─── 执行编排（正向与回滚共用）────────────────────────────
  const runStatement = useCallback(async (connId: string, s: DdlStatement, txId: string | null) => {
    if (txId) await api.executeInTx(txId, { sql: s.sql, connection_id: connId });
    else await api.executeQuery(connId, { sql: s.sql });
  }, []);

  // 执行一份脚本：正向与回滚走同一条通道，调用方传哪份就跑哪份。
  const applyScript = useCallback(async (sc: DdlScript): Promise<ApplyOutcome> => {
    const out = emptyOutcome();
    if (!source) return out;
    const connId = source.id;
    const plan = planExecution(sc);
    setProgress({ done: 0, total: sc.statements.length });
    cancelRef.current = false;

    // 分派（behavior.md §15.4）：
    //   ddl_in_tx + pragmas_outside_tx ⇒ 首尾 PRAGMA 事务外跑，主体包一个事务；
    //   ddl_in_tx=false                ⇒ 该方言 DDL 会隐式提交，全脚本逐条跑（失败留中间态）。
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
        reason: toMsg(e),
        entry_ids: s.entry_ids,
      });
      out.skipped = Math.max(0, steps.length - index - 1);
    };

    let tx: string | null = null;
    for (let i = 0; i < steps.length; i++) {
      const { stmt, inTx } = steps[i];
      if (cancelRef.current) {
        fail(i, stmt, new Error('已取消：本条及剩余语句未执行'));
        break;
      }
      try {
        if (inTx && !tx) {
          const begun = await api.beginTransaction(connId);
          tx = begun.id;
          txIdRef.current = begun.id;
        }
        // 主体跑完接着是事务外 PRAGMA：先提交，PRAGMA 不能进事务
        if (!inTx && tx) {
          await api.commitTransaction(tx);
          txIdRef.current = null;
          tx = null;
        }
        await runStatement(connId, stmt, inTx ? tx : null);
        out.ran++;
        setProgress({ done: out.ran, total: steps.length });
      } catch (e) {
        fail(i, stmt, e);
        break;
      }
    }

    if (tx) {
      if (out.failed.length) {
        try {
          await api.rollbackTransaction(tx);
          out.rolledBack = true;
          txIdRef.current = null;
        } catch { /* 回滚本身失败：留着 tx id 让 finishAndClose 再兜一次 */ }
      } else {
        try {
          await api.commitTransaction(tx);
        } catch (e) {
          const last = steps[steps.length - 1];
          fail(steps.length - 1, last.stmt, e);
        }
        txIdRef.current = null;
      }
    }
    return out;
  }, [runStatement, source]);

  const forwardSummary = useMemo(() => (script ? summarizeScript(script.statements) : null), [script]);
  const rollbackSummary = useMemo(() => (script ? summarizeScript(script.rollback) : null), [script]);
  const irreversibleRollback = useMemo(
    () => (script ? script.rollback.filter((s) => s.non_reversible) : []),
    [script],
  );
  const rollbackNameOk = !!source && confirmText.trim() === source.name;

  const doApply = useCallback(async () => {
    if (!script) return;
    setBusy('apply');
    setError('');
    setNote('');
    const out = await applyScript(script);
    setOutcome(out);
    setOutcomeRun('forward');
    setApplied(!out.failed.length);
    setBusy('');
  }, [applyScript, script]);

  const doRollback = useCallback(async () => {
    if (!script || !applied || !rollbackNameOk) return;
    setAskRollback(false);
    setConfirmText('');
    setBusy('rollback');
    setError('');
    const out = await applyScript({ ...script, statements: script.rollback });
    setOutcome(out);
    setOutcomeRun('rollback');
    if (!out.failed.length) {
      setApplied(false);
      setNote('回滚脚本已执行完毕。它只是把结构退回去——正向里丢的数据找不回来（§15.6）。');
    }
    setBusy('');
  }, [applied, applyScript, rollbackNameOk, script]);

  const recheck = useCallback(async () => {
    if (!diffReq) return;
    setBusy('diff');
    try {
      const plan = await api.schemaDiff(diffReq);
      const left = plan.entries ?? [];
      setNote(left.length
        ? `复查：基准侧与期望侧仍有 ${left.length} 条差异（未勾选的部分本就不会应用）。`
        : '复查：基准侧已与期望侧一致。');
    } catch (e) {
      setError(toMsg(e));
    } finally {
      setBusy('');
    }
  }, [diffReq]);

  const finishAndClose = useCallback(() => {
    if (busy) return;
    const tx = txIdRef.current;
    if (tx) void api.rollbackTransaction(tx).catch(() => { /* 静默：关窗不该被服务端报错拦住 */ });
    txIdRef.current = null;
    onClose();
  }, [busy, onClose]);

  // Esc 关闭（焦点在输入控件里时不抢键），busy 时禁止关闭
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      if (askRollback) { e.preventDefault(); e.stopPropagation(); setAskRollback(false); return; }
      const t = document.activeElement as HTMLElement | null;
      const typing = t?.tagName === 'INPUT' || t?.tagName === 'TEXTAREA' || t?.tagName === 'SELECT' || t?.isContentEditable;
      if (typing || busy) return;
      e.preventDefault();
      finishAndClose();
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
  }, [askRollback, busy, finishAndClose, open]);

  // 卸载兜底：还有挂着的事务就回滚，别让服务端留开放事务
  useEffect(() => () => {
    const tx = txIdRef.current;
    if (tx) void api.rollbackTransaction(tx).catch(() => { /* ignore */ });
  }, []);

  if (!open) return null;

  const curIdx = STEP_ORDER.indexOf(st.step);
  const firstFailed = outcome?.failed.length ? outcome.failed[0].index : Infinity;

  return (
    <div style={styles.overlay} onClick={(e) => { if (e.target === e.currentTarget) finishAndClose(); }}>
      <div style={{ ...styles.panel, position: 'relative', width: 980, maxWidth: '95vw' }} role="dialog" aria-label="结构比对与同步向导">
        <div style={styles.header}>
          <span style={styles.title}>结构比对与同步 · Compare &amp; Sync</span>
          <span style={styles.muted}>v1 只生成 SQLite 方言</span>
          <button style={styles.closeBtn} onClick={finishAndClose} disabled={!!busy} title={busy ? '执行中不能关闭' : '关闭 (Esc)'}>×</button>
        </div>

        <div style={styles.stepBar}>
          {STEP_ORDER.map((s, i) => {
            const isCur = i === curIdx;
            const done = i < curIdx;
            return (
              <div
                key={s}
                role="button"
                aria-disabled={!guards[s].ok && !done && !isCur}
                style={{
                  ...styles.stepItem,
                  ...(isCur ? styles.stepItemActive : {}),
                  ...(done ? styles.stepItemDone : {}),
                  cursor: done || isCur ? 'pointer' : 'default',
                }}
                title={done || isCur ? STEP_LABEL[s] : guards[s].why}
                onClick={() => { if (done || isCur || guards[s].ok) goto(s); }}
              >
                <span>{done ? '✓' : isCur ? '▶' : '·'}</span>
                <span>{STEP_LABEL[s]}</span>
              </div>
            );
          })}
        </div>

        <div style={styles.body}>
          {error && <div style={styles.errorBox}>{error}</div>}
          {note && <div style={styles.mutedBox}>{note}</div>}

          {(st.step === 'source' || st.step === 'target') && (
            <div style={styles.field}>
              <label style={styles.label}>基准侧（当前库，将被修改）</label>
              <select
                style={styles.select}
                value={st.sourceId}
                onChange={(e) => patch({ sourceId: e.target.value, sourceSchema: '', targetId: '', targetSchema: '' })}
              >
                <option value="">— 选择连接 —</option>
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}{c.database ? ` (${c.database})` : ''} · {c.kind}{c.read_only ? ' · 只读' : ''}
                  </option>
                ))}
              </select>
              {schemas.source.length > 1 && (
                <>
                  <label style={styles.label}>基准侧 schema（留空用服务端默认）</label>
                  <select style={styles.select} value={st.sourceSchema} onChange={(e) => patch({ sourceSchema: e.target.value })}>
                    <option value="">默认</option>
                    {schemas.source.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                </>
              )}

              <label style={styles.label}>期望侧（目标样子）</label>
              <select
                style={styles.select}
                value={st.targetId}
                onChange={(e) => patch({ targetId: e.target.value, targetSchema: '' })}
              >
                <option value="">— 选择连接 —</option>
                {targets.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}{c.database ? ` (${c.database})` : ''} · {c.kind}
                  </option>
                ))}
              </select>
              {schemas.target.length > 1 && (
                <>
                  <label style={styles.label}>期望侧 schema（留空用服务端默认）</label>
                  <select style={styles.select} value={st.targetSchema} onChange={(e) => patch({ targetSchema: e.target.value })}>
                    <option value="">默认</option>
                    {schemas.target.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                </>
              )}

              {kindBlocked && <div style={styles.warnText}>Redis 无模式概念，不参与结构比对（服务端 501）。</div>}
              {kindMismatch && <div style={styles.warnText}>跨种类比对未支持：v1 只比同一种数据库。</div>}
              <div style={styles.muted}>{DIR_HINT} 比对由服务端重算，前端不缓存结构。</div>

              <div style={{ display: 'flex', gap: 16, alignItems: 'center' }}>
                <label style={styles.check}>
                  <input type="checkbox" checked={st.ignoreDefaults} onChange={(e) => patch({ ignoreDefaults: e.target.checked })} />
                  忽略默认值差异
                </label>
                <label style={styles.check}>
                  <input type="checkbox" checked={st.selectDestructive} onChange={(e) => patch({ selectDestructive: e.target.checked })} />
                  破坏性动作默认勾选
                </label>
                <button style={styles.btnPrimary} onClick={() => void runDiff()} disabled={!!busy || !diffReq}>
                  {busy === 'diff' ? '比对中…' : '开始比对'}
                </button>
              </div>
            </div>
          )}

          {st.step === 'review' && entries && (
            <div style={styles.field}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <input
                  style={{ ...styles.inputSm, flex: 1 }}
                  placeholder="按表名或条目 id 过滤…"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                />
                <button style={styles.btnSm} onClick={() => setSel(defaultSelection(entries))}>恢复默认勾选</button>
                <button style={styles.btnSm} onClick={() => setSel(entries.filter((e) => !e.destructive).map((e) => e.id))}>只勾非破坏性</button>
                <button style={styles.btnSm} onClick={() => setSel(entries.map((e) => e.id))}>全选</button>
                <button style={styles.btnSm} onClick={() => setSel([])}>清空</button>
                <button style={styles.btn} onClick={() => void runDiff()} disabled={!!busy}>重新比对</button>
              </div>

              {gaps.length > 0 && (
                <div style={{ ...styles.warnText, display: 'flex', gap: 8, alignItems: 'center' }}>
                  <span>
                    {gaps.map((g) => `表 ${g.table} 需整表重建，还差 ${g.missing.length} 条同表变更`).join('；')}
                  </span>
                  <button style={styles.btnSm} onClick={() => setSel(fillGroupGaps(entries, sel))}>整表一起勾</button>
                </div>
              )}
              {rebuildTables.length > 0 && (
                <div style={styles.muted}>含改列/改外键的表会走整表重建：{rebuildTables.join('、')}（触发器与 CHECK 会丢，脚本里逐条告警）</div>
              )}

              {groups.map((g) => (
                <div key={g.phase}>
                  <div style={{ ...styles.label, marginTop: 10 }}>
                    {g.label} <span style={styles.muted}>相位 {g.phase} · {g.entries.length} 条</span>
                  </div>
                  {g.entries.map((e) => {
                    const on = sel.includes(e.id);
                    return (
                      <div
                        key={e.id}
                        style={{ ...styles.mapRow, borderColor: e.destructive ? 'var(--danger)' : 'var(--border)' }}
                      >
                        <input
                          type="checkbox"
                          checked={on}
                          onChange={() => setSel((prev) => toggleEntry(prev, e.id))}
                          title={e.id}
                        />
                        <span style={{ ...styles.typeBadge, background: 'var(--bg-alt)' }}>{OP_LABEL[e.op]}</span>
                        <span style={{ minWidth: 180 }}>
                          {e.table}{e.object ? ` · ${e.object}` : ''}
                          {e.destructive && <span style={{ color: 'var(--danger)', marginLeft: 6 }}>破坏性</span>}
                        </span>
                        <span style={{ ...styles.muted, flex: 1 }}>
                          {e.from || e.to ? `${e.from ?? '∅'} → ${e.to ?? '∅'}｜` : ''}
                          {e.reason}
                          {e.flags && e.flags.length ? `（${e.flags.join('/')}）` : ''}
                        </span>
                      </div>
                    );
                  })}
                </div>
              ))}
              {!groups.length && <div style={styles.mutedCenter}>过滤后没有条目。</div>}

              <div style={styles.summary}>
                已勾 {chosen.length} / {entries.length} 条
                {chosenDestructive ? `，其中 ${chosenDestructive} 条破坏性` : ''}
              </div>
              <button style={styles.btnPrimary} onClick={() => void generate()} disabled={!!busy || !chosen.length}>
                {busy === 'ddl' ? '生成中…' : '生成 DDL 脚本'}
              </button>
            </div>
          )}

          {st.step === 'apply' && script && forwardSummary && (
            <div style={styles.field}>
              <div style={styles.muted}>
                目标：<b>{source?.name}</b> · 正向 {forwardSummary.total} 条（不可逆 {forwardSummary.nonReversible}、带告警 {forwardSummary.warned}）
                {script.pragmas_outside_tx ? ' · 首尾 PRAGMA 在事务外执行' : ''}
                {script.ddl_in_tx ? ' · 主体包一个事务，失败整体回滚' : ' · 该方言 DDL 不能进事务，将逐条执行'}
              </div>
              {source?.read_only && <div style={styles.warnText}>基准侧连接标记为只读，服务端会拒绝写语句。</div>}

              <div style={{ maxHeight: 260, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4, padding: 6 }}>
                {script.statements.map((s, i) => (
                  <div key={`${s.sql}-${i}`} style={{ ...styles.mapRow, opacity: outcomeRun === 'forward' && firstFailed <= i ? 0.45 : 1 }}>
                    <span style={styles.muted}>{i + 1}</span>
                    <span style={{ ...styles.pre, flex: 1, margin: 0 }}>{s.sql}</span>
                    {s.non_reversible && <span style={{ color: 'var(--danger)' }}>不可逆</span>}
                    {!!s.warnings?.length && <span style={styles.warnText} title={s.warnings.join('\n')}>⚠ {s.warnings.length}</span>}
                  </div>
                ))}
              </div>

              {progress.total > 0 && (
                <div style={styles.progressOuter}>
                  <div style={{ ...styles.progressInner, width: `${Math.round((progress.done / progress.total) * 100)}%` }} />
                </div>
              )}

              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                <button style={styles.btnPrimary} onClick={() => void doApply()} disabled={!!busy || !chosen.length}>
                  {busy === 'apply' ? '执行中…' : `执行正向脚本（${forwardSummary.total} 条）`}
                </button>
                {busy === 'apply' && (
                  <button style={styles.btnGhost} onClick={() => { cancelRef.current = true; }}>取消剩余</button>
                )}
                <button style={styles.btn} onClick={() => void recheck()} disabled={!!busy}>复查差异</button>
                <button style={styles.btnGhost} onClick={() => setShowRollbackScript((v) => !v)}>
                  {showRollbackScript ? '收起回滚脚本' : `查看回滚脚本（${rollbackSummary?.total ?? 0} 条）`}
                </button>
              </div>

              {showRollbackScript && (
                <div style={{ maxHeight: 200, overflow: 'auto', border: '1px dashed var(--border)', borderRadius: 4, padding: 6 }}>
                  {!script.rollback.length && <div style={styles.mutedCenter}>回滚脚本为空。</div>}
                  {script.rollback.map((s, i) => (
                    <div key={`rb-${i}`} style={{ ...styles.mapRow, opacity: outcomeRun === 'rollback' && firstFailed <= i ? 0.45 : 1 }}>
                      <span style={styles.muted}>{i + 1}</span>
                      <span style={{ ...styles.pre, flex: 1, margin: 0 }}>{s.sql}</span>
                      {s.non_reversible && <span style={{ color: 'var(--danger)' }}>不可逆</span>}
                    </div>
                  ))}
                </div>
              )}

              {outcome && (
                <div style={outcome.failed.length ? styles.errorBox : styles.mutedBox}>
                  <div>
                    {outcomeRun === 'rollback' ? '回滚脚本' : '正向脚本'}：
                    {outcome.failed.length
                      ? <>失败 {outcome.failed.length} 条{outcome.rolledBack ? '，主体事务已整体回滚（基准侧未留下半成品）' : `，已跳过剩余 ${outcome.skipped} 条（该方言 DDL 不能进事务，中间态需自行处理）`}。</>
                      : <>全部 {outcome.ran} 条执行成功{outcomeRun === 'forward' && applied ? '；如需退回，用下方「执行回滚脚本」。' : '。'}</>}
                  </div>
                  {outcome.failed.map((f) => <FailedLine key={f.index} f={f} />)}
                </div>
              )}

              {applied && (
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <button style={{ ...styles.btn, color: 'var(--danger)', borderColor: 'var(--danger)' }} onClick={() => setAskRollback(true)} disabled={!!busy}>
                    执行回滚脚本（{rollbackSummary?.total ?? 0} 条）
                  </button>
                  <span style={styles.muted}>回滚也是脚本，不能撤销；其中 {irreversibleRollback.length} 条标了不可逆。</span>
                </div>
              )}
            </div>
          )}
        </div>

        <div style={styles.footer}>
          <button style={styles.btnGhost} onClick={() => goto(STEP_ORDER[Math.max(0, curIdx - 1)])} disabled={curIdx === 0 || !!busy}>上一步</button>
          {(() => {
            const next = STEP_ORDER[Math.min(STEP_ORDER.length - 1, curIdx + 1)];
            const atLast = curIdx === STEP_ORDER.length - 1;
            return (
              <button
                style={styles.btn}
                onClick={() => goto(next)}
                disabled={atLast || !guards[next].ok || !!busy}
                title={atLast ? '已是最后一步' : guards[next].ok ? STEP_LABEL[next] : guards[next].why}
              >
                下一步
              </button>
            );
          })()}
          <span style={styles.spacer} />
          <span style={styles.muted}>比对与生成都由服务端重算，前端只渲染与执行</span>
          <button style={styles.btn} onClick={finishAndClose} disabled={!!busy}>关闭</button>
        </div>

        {askRollback && script && (
          <div style={{ position: 'absolute', inset: 0, background: 'rgba(0,0,0,0.35)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <div style={{ ...styles.panel, width: 560, height: 'auto', maxHeight: '80%' }}>
              <div style={styles.header}><span style={styles.title}>确认执行回滚脚本</span></div>
              <div style={styles.body}>
                <div style={styles.warnText}>
                  回滚脚本会把基准侧退回到比对前的样子。这一步本身不可撤销
                  {irreversibleRollback.length ? `，其中 ${irreversibleRollback.length} 条是不可逆动作（数据/定义一旦执行就找不回）` : ''}。
                </div>
                {irreversibleRollback.map((s, i) => (
                  <div key={`ir-${i}`} style={{ ...styles.mapRow, borderColor: 'var(--danger)' }}>
                    <span style={{ ...styles.pre, flex: 1, margin: 0 }}>{s.sql}</span>
                  </div>
                ))}
                <label style={styles.label}>
                  请输入基准侧连接名 <b style={{ textTransform: 'none' }}>{source?.name}</b> 以确认
                </label>
                <input style={styles.inputSm} value={confirmText} onChange={(e) => setConfirmText(e.target.value)} />
              </div>
              <div style={styles.footer}>
                <button style={styles.btnGhost} onClick={() => { setAskRollback(false); setConfirmText(''); }}>取消</button>
                <span style={styles.spacer} />
                <button style={{ ...styles.btnPrimary, background: 'var(--danger)' }} onClick={() => void doRollback()} disabled={!rollbackNameOk}>
                  {busy === 'rollback' ? '回滚中…' : '确认执行回滚'}
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function FailedLine({ f }: { f: FailedStatement }) {
  return (
    <details style={{ marginLeft: 8 }}>
      <summary style={{ cursor: 'pointer' }}>
        #{f.index + 1} 相位 {f.phase}{f.table ? ` · ${f.table}` : ''} — {f.reason}
      </summary>
      <div style={{ ...styles.pre, whiteSpace: 'pre-wrap' }}>{f.sql}</div>
      {!!f.entry_ids.length && <div style={styles.muted}>承载条目：{f.entry_ids.join('、')}</div>}
    </details>
  );
}
