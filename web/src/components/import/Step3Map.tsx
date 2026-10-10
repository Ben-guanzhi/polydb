import type { MutableRefObject, Dispatch, SetStateAction, ReactNode } from 'react';
import { failCategoryMeta, qualityScoreColor, typeBadgeColor } from '../../lib/importDisplay';
import { applyPreset, deletePreset, listSnapshots, presetKey, restoreSnapshot, type ImportPreset } from '../../lib/importPresets';
import { generateBlankTemplate, hasAnyValidation, inferMapping, transformOnly, validateCell, EMPTY_VALIDATION, type ImportOptions, type Mapping, type ColumnProfile, type ColumnTransform, type ColumnValidation, type NullPolicy, type MappingSuggestion, type RowValidationResult } from '../../lib/importData';
import type { CsvParseResult } from '../../lib/csvParser';
import { type DataQualityReport, suggestTransforms } from '../../lib/dataQuality';
import { publishEditorStatus } from '../../lib/statusBus';
import { styles } from '../importStyles';
import type { ColumnInfo } from '../../api';
import type { MappingFilter, MappingSort, FixEntry, Step } from './types';
import { MAX_FIX_HISTORY } from './types';

// M30.03 列映射 & 转换步（Step 3）：列映射网格、过滤/排序/搜索、批量操作、
// 转换/空值/校验/枚举编辑、预设应用、一键优化、撤销栈。
export interface Step3MapProps {
  connId: string;
  selSchema: string;
  selTable: string;
  cols: ColumnInfo[];
  colInfoMap: Map<string, ColumnInfo>;
  mappings: Mapping[];
  opts: ImportOptions;
  parse: CsvParseResult | null;
  parseRef: MutableRefObject<CsvParseResult | null>;
  profiles: ColumnProfile[];
  mappingSuggestions: MappingSuggestion[];
  mappedCount: number;
  skippedCount: number;
  mappingFilter: MappingFilter;
  mappingSort: MappingSort;
  mappingSearch: string;
  mappingSearchHistory: string[];
  mappingSearchHistOpen: boolean;
  mappingSearchHistIdx: number;
  columnHealths: { i: number; health: number; band: 'green' | 'orange' | 'amber' | 'red'; warn: string | null }[] | null;
  batchSel: Set<number>;
  batchAnchor: number | null;
  jumpFlashIdx: number | null;
  presetApplied: { matched: number; total: number; removed: { csvName: string; targetColumn: string }[]; added: string[] } | null;
  appliedPreset: ImportPreset | null;
  qualityGate: { enabled: boolean; threshold: number };
  qualityReport: DataQualityReport | null;
  validationResult: RowValidationResult | null;
  mapSearchInputRef: MutableRefObject<HTMLInputElement | null>;
  applyOneClickOptimize: () => number;
  pushFix: (entry: FixEntry) => void;
  triggerDownload: (blob: Blob, filename: string) => void;
  persistMappingFilter: (v: MappingFilter) => void;
  persistMappingSort: (v: MappingSort) => void;
  saveMappingSearchHistory: (term: string) => void;
  highlightQuery: (text: string) => (string | ReactNode)[];
  warnFor: (i: number) => string | null;
  setError: Dispatch<SetStateAction<string | null>>;
  setMappings: Dispatch<SetStateAction<Mapping[]>>;
  setOpts: Dispatch<SetStateAction<ImportOptions>>;
  setMappingSearch: Dispatch<SetStateAction<string>>;
  setMappingSearchHistOpen: Dispatch<SetStateAction<boolean>>;
  setMappingSearchHistIdx: Dispatch<SetStateAction<number>>;
  setMappingSearchHistory: Dispatch<SetStateAction<string[]>>;
  setBatchSel: Dispatch<SetStateAction<Set<number>>>;
  setBatchAnchor: Dispatch<SetStateAction<number | null>>;
  setBatchStepN: Dispatch<SetStateAction<number>>;
  setDiffOpen: Dispatch<SetStateAction<boolean>>;
  setUndoStack: Dispatch<SetStateAction<FixEntry[]>>;
  setAppliedPreset: Dispatch<SetStateAction<ImportPreset | null>>;
  setPresetApplied: Dispatch<SetStateAction<Step3MapProps['presetApplied']>>;
  setPresetSnapshotsRefresh: Dispatch<SetStateAction<number>>;
  setStep: Dispatch<SetStateAction<Step>>;
}

export function Step3Map(p: Step3MapProps) {
  const {
    connId, selSchema, selTable, cols, colInfoMap, mappings, opts, parse, parseRef, profiles,
    mappingSuggestions, mappedCount, skippedCount, mappingFilter, mappingSort, mappingSearch,
    mappingSearchHistory, mappingSearchHistOpen, mappingSearchHistIdx, columnHealths, batchSel,
    batchAnchor, jumpFlashIdx, presetApplied, appliedPreset, qualityGate,
    qualityReport, validationResult, mapSearchInputRef, applyOneClickOptimize, pushFix,
    triggerDownload, persistMappingFilter, persistMappingSort, saveMappingSearchHistory,
    highlightQuery, warnFor, setError, setMappings, setOpts, setMappingSearch,
    setMappingSearchHistOpen, setMappingSearchHistIdx, setMappingSearchHistory, setBatchSel,
    setBatchAnchor, setBatchStepN, setDiffOpen, setUndoStack, setAppliedPreset,
    setPresetApplied, setPresetSnapshotsRefresh, setStep,
  } = p;
  return (
    <div style={styles.col}>
      <div style={{ ...styles.row2col, justifyContent: 'space-between', alignItems: 'center' }}>
                <span style={styles.label}>列映射（左侧 CSV 列 → 右侧表列）</span>
                <div style={{ display: 'flex', gap: 6 }}>
                  <button
                    onClick={() => {
                      const prev = JSON.parse(JSON.stringify(opts)) as ImportOptions;
                      const total = applyOneClickOptimize();
                      if (total === 0) return;
                      const meta = failCategoryMeta('type');
                      pushFix({
                        key: 'type',
                        label: meta.label,
                        icon: meta.icon,
                        snapshot: prev,
                        msg: '已按质量报告应用转换建议（trim / date-parse / scale 等）',
                        at: Date.now(),
                      });
                    }}
                    style={styles.btnSm}
                    title="根据 CSV 数据画像与目标列约束（NOT NULL / 默认值 / dtype / 列名）自动应用转换、空值策略、必填校验、正则校验、枚举约束（可 Ctrl+Z 撤销）"
                  >✨ 一键优化</button>
                  <button
                    onClick={() => {
                      if (!selTable || cols.length === 0) return;
                      const t = generateBlankTemplate(selSchema, selTable, cols, { includeAutoInc: false });
                      if (!t.csv) { setError('目标表全部为自增列，无法生成空白模板'); return; }
                      triggerDownload(
                        new Blob([t.csv], { type: 'text/csv' }),
                        `${selSchema}_${selTable}_import_template.csv`,
                      );
                      publishEditorStatus({
                        message: `已生成 ${selTable} 空白 CSV 模板（${t.usedCols} 列，跳过 ${t.skippedCols} 自增列）`,
                        messageAt: Date.now(),
                      });
                    }}
                    style={styles.btnSm}
                    title="根据目标表结构导出空白 CSV 模板，填完再导入"
                  >📄 下载空白 CSV 模板</button>
                </div>
              </div>
              {cols.length > 0 && (() => {
                const mappedTargets = new Set(mappings.map((m) => m.targetColumn).filter((t): t is string => t != null));
                const unmapped = cols.filter((c) => !mappedTargets.has(c.name));
                const mappedCount = mappedTargets.size;
                const pct = cols.length > 0 ? Math.round((mappedCount / cols.length) * 100) : 0;
                const barColor = pct === 100 ? 'var(--ok, #10b981)' : pct >= 50 ? 'var(--accent, #3b82f6)' : 'var(--warn, #d97706)';
                const shown = unmapped.slice(0, 10);
                const rest = unmapped.length - shown.length;
                return (
                  <div style={{ marginTop: 6, padding: '6px 8px', background: 'rgba(255,255,255,0.02)', border: '1px solid var(--border)', borderRadius: 3, fontSize: 11 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                      <span style={{ color: 'var(--muted)' }}>📊 覆盖率</span>
                      <div style={{ flex: 1, height: 6, background: 'rgba(255,255,255,0.05)', borderRadius: 3, overflow: 'hidden' }}>
                        <div style={{ width: `${pct}%`, height: '100%', background: barColor, transition: 'width 0.2s' }} />
                      </div>
                      <strong style={{ color: barColor }}>{mappedCount}/{cols.length}</strong>
                      <span style={styles.muted}>{pct}%</span>
                    </div>
                    {unmapped.length > 0 ? (
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
                        <span style={{ color: 'var(--muted)' }}>未映射目标列：</span>
                        {shown.map((c) => (
                          <span
                            key={c.name}
                            style={{
                              padding: '1px 6px',
                              borderRadius: 2,
                              fontSize: 10,
                              border: `1px solid ${!c.nullable ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)'}`,
                              color: !c.nullable ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)',
                              background: !c.nullable ? 'rgba(220,38,38,0.08)' : 'rgba(59,130,246,0.08)',
                            }}
                            title={c.nullable ? `${c.name} · 可空 · ${c.data_type}` : `⚠ ${c.name} · NOT NULL · ${c.data_type}${c.default_value != null ? ` · 默认值: ${c.default_value}` : ''}`}
                          >
                            {c.name}{!c.nullable && <span style={{ marginLeft: 2 }}>!</span>}
                          </span>
                        ))}
                        {rest > 0 && <span style={styles.muted}>…+{rest}</span>}
                      </div>
                    ) : (
                      <div style={{ color: 'var(--ok, #10b981)' }}>✓ 目标表所有列均已映射</div>
                    )}
                  </div>
                );
              })()}
              <div style={{ display: 'flex', gap: 4, marginTop: 4, alignItems: 'center', fontSize: 11 }}>
                {(() => {
                  // M30.90 上下文"下一步"提示：根据当前状态智能推荐下一步操作
                  const isDefaultView = mappingFilter === 'all' && mappingSort === 'index' && mappingSearch.trim() === '';
                  const unhealthyCount = columnHealths ? columnHealths.filter((h) => h.health < 80).length : 0;
                  const hasSelection = batchSel.size > 0;
                  // 优先级：有选中 > 有异常 > 全部默认
                  if (hasSelection) {
                    return (
                      <span
                        style={{
                          fontSize: 10, padding: '1px 6px', borderRadius: 2,
                          background: 'rgba(59,130,246,0.10)',
                          color: 'var(--accent, #3b82f6)',
                          border: '1px solid rgba(59,130,246,0.35)',
                          lineHeight: '16px',
                          whiteSpace: 'nowrap',
                          flexShrink: 0,
                        }}
                        title={`已选中 ${batchSel.size} 列，批量条下方有「⚡ 应用建议 / 清空设置 / 取消选择」等操作`}
                      >💡 下一步：在批量条应用建议或清空设置</span>
                    );
                  }
                  if (!isDefaultView) return null; // 用户在过滤/排序/搜索中，不主动打断
                  if (unhealthyCount > 0) {
                    return (
                      <span
                        style={{
                          fontSize: 10, padding: '1px 6px', borderRadius: 2,
                          background: 'rgba(217,119,6,0.10)',
                          color: 'var(--warn, #d97706)',
                          border: '1px solid rgba(217,119,6,0.35)',
                          lineHeight: '16px',
                          whiteSpace: 'nowrap',
                          flexShrink: 0,
                        }}
                        title={`共 ${unhealthyCount} 列健康分 < 80，建议先处理。Alt+4 或 Ctrl+J 快速定位`}
                      >💡 下一步：Alt+4 过滤低健康 · Ctrl+J 逐行跳</span>
                    );
                  }
                  return null;
                })()}
                <span style={styles.muted}>列过滤器：</span>
                {([
                  { key: 'all' as MappingFilter, label: '全部' },
                  { key: 'unmapped' as MappingFilter, label: `未映射${mappings.length > 0 ? `（${mappings.filter((m) => m.targetColumn == null).length}）` : ''}` },
                  { key: 'mapped' as MappingFilter, label: `已映射${mappings.length > 0 ? `（${mappings.filter((m) => m.targetColumn != null).length}）` : ''}` },
                  { key: 'low-health' as MappingFilter, label: `低健康${columnHealths ? `（${columnHealths.filter(h => h.health < 80).length}）` : ''}` },
                  { key: 'critical-health' as MappingFilter, label: `紧急${columnHealths ? `（${columnHealths.filter(h => h.health < 60).length}）` : ''}` },
                ]).map((f) => {
                  const isActive = mappingFilter === f.key;
                  // 低健康用 warn 橙、紧急用 danger 红、其余 accent 蓝
                  const activeColor = f.key === 'low-health' ? 'var(--warn, #d97706)' : f.key === 'critical-health' ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)';
                  const activeBg = f.key === 'low-health' ? 'rgba(217,119,6,0.10)' : f.key === 'critical-health' ? 'rgba(220,38,38,0.10)' : 'rgba(59,130,246,0.10)';
                  return (
                    <button
                      key={f.key}
                      onClick={() => persistMappingFilter(f.key)}
                      style={{
                        ...styles.btnSm, padding: '1px 8px', fontSize: 11,
                        color: isActive ? activeColor : 'var(--text)',
                        borderColor: isActive ? activeColor : 'var(--border)',
                        background: isActive ? activeBg : 'transparent',
                        fontWeight: isActive ? 600 : 'inherit',
                      }}
                      title={`按当前过滤器排序映射行（匹配行置顶，非匹配行下沉且半透显示）`}
                    >{f.label}</button>
                  );
                })}
                <button
                  type="button"
                  style={{
                    ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                    color: 'var(--muted)', borderColor: 'var(--border)',
                    background: 'transparent', cursor: 'default',
                  }}
                  title={
                    [
                      'Step 3 列映射快捷键：',
                      'Alt+1 → 全部',
                      'Alt+2 → 未映射',
                      'Alt+3 → 已映射',
                      'Alt+4 → 低健康',
                      'Alt+5 → 紧急（健康 < 60）',
                      'Alt+0 → 清空搜索',
                      'Ctrl+F → 聚焦列搜索框',
                      'Alt+R  → 重置视图（过滤器+排序+搜索）',
                      'Alt+N  → 跳到下一个未映射列',
                      'Alt+P  → 跳到上一个未映射列',
                      'Alt+H  → 跳到首个未映射列',
                      'Alt+L  → 跳到末位未映射列',
                      'Ctrl+J → 跳到下一个低健康行',
                      'Ctrl+K → 跳到上一个低健康行',
                    ].join('\n')
                  }
                  aria-label="显示列映射快捷键"
                >⌨</button>
                {mappingFilter !== 'all' && (
                  <span style={styles.muted} title="切换过滤器即恢复显示">· 当前只显示匹配项在前</span>
                )}
                <span style={{ flex: 1 }} />
                <span style={{ ...styles.muted, fontSize: 10 }} title="调整映射行显示顺序（数据索引不变）">排序：</span>
                <select
                  value={mappingSort}
                  onChange={(e) => persistMappingSort(e.target.value as MappingSort)}
                  style={{ ...styles.selectSm, padding: '1px 4px', fontSize: 10 }}
                  title="映射行排序：CSV 索引（默认）· 健康分升序（差→好）· 空值率降序（多→少）· 目标列名升序（未映射置尾）· CSV 名升序"
                >
                  <option value="index">CSV 索引</option>
                  <option value="health-asc">健康分 升</option>
                  <option value="empty-desc">空值率 降</option>
                  <option value="target-asc">目标列名 升</option>
                  <option value="csv-asc">CSV 名 升</option>
                </select>
                {(mappingFilter !== 'all' || mappingSort !== 'index' || mappingSearch.trim() !== '') && (
                  <button
                    type="button"
                    onClick={() => {
                      persistMappingFilter('all');
                      persistMappingSort('index');
                      setMappingSearch('');
                      setMappingSearchHistOpen(false);
                      setMappingSearchHistIdx(-1);
                    }}
                    style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--muted)' }}
                    title="一键重置：过滤器=全部 + 排序=CSV 索引 + 清空搜索 · 快捷键 Alt+R"
                    aria-label="重置过滤器、排序、搜索到默认"
                  >↺ 重置</button>
                )}
                {(() => {
                  // M30.88 状态摘要 chip：当 filter/sort/search 任一非默认时列出偏离项，方便用户一眼看清当前视图状态
                  const parts: string[] = [];
                  if (mappingFilter !== 'all') {
                    const labels: Record<MappingFilter, string> = {
                      'all': '全部', 'mapped': '已映射', 'unmapped': '未映射',
                      'low-health': '低健康', 'critical-health': '紧急',
                    };
                    parts.push(`过滤:${labels[mappingFilter]}`);
                  }
                  if (mappingSort !== 'index') {
                    const labels: Record<MappingSort, string> = {
                      'index': 'CSV 索引', 'health-asc': '健康分 升', 'empty-desc': '空值率 降',
                      'target-asc': '目标列名 升', 'csv-asc': 'CSV 名 升',
                    };
                    parts.push(`排序:${labels[mappingSort]}`);
                  }
                  if (mappingSearch.trim() !== '') parts.push(`搜索:"${mappingSearch.trim()}"`);
                  if (parts.length === 0) return null;
                  const text = parts.join(' · ');
                  return (
                    <span
                      style={{
                        fontSize: 10, padding: '1px 6px', borderRadius: 2,
                        background: 'rgba(59,130,246,0.10)',
                        color: 'var(--accent, #3b82f6)',
                        border: '1px solid rgba(59,130,246,0.35)',
                        fontFamily: 'var(--mono, ui-monospace)',
                        lineHeight: '16px',
                        maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}
                      title={`${text}\n点击 ↺ 重置 或按 Alt+R 回到全部+CSV 索引+空搜索`}
                    >{text}</span>
                  );
                })()}
                {mappings.length > 0 && (() => {
                  // 全选当前过滤器+搜索命中的映射行；无过滤/搜索时全选
                  const q = mappingSearch.trim().toLowerCase();
                  const idxs: number[] = [];
                  for (let i = 0; i < mappings.length; i++) {
                    const m = mappings[i];
                    let filterPass = true;
                    if (mappingFilter === 'unmapped') filterPass = m.targetColumn == null;
                    else if (mappingFilter === 'mapped') filterPass = m.targetColumn != null;
                    else if (mappingFilter === 'low-health') {
                      const ch = columnHealths?.[i];
                      filterPass = ch != null && ch.health < 80;
                    } else if (mappingFilter === 'critical-health') {
                      const ch = columnHealths?.[i];
                      filterPass = ch != null && ch.health < 60;
                    }
                    if (!filterPass) continue;
                    if (q) {
                      const searchPass = m.csvName.toLowerCase().includes(q)
                        || (m.targetColumn && m.targetColumn.toLowerCase().includes(q));
                      if (!searchPass) continue;
                    }
                    idxs.push(i);
                  }
                  const total = idxs.length;
                  if (total === 0) return null;
                  const hidden = mappings.length - total;
                  const chipColor = hidden > 0 ? 'var(--warn, #d97706)' : 'var(--muted)';
                  const chipBg = hidden > 0 ? 'rgba(217,119,6,0.10)' : 'rgba(255,255,255,0.03)';
                  return (
                    <>
                      <button
                        type="button"
                        onClick={() => {
                          if (idxs.length === 0) return;
                          setBatchSel(new Set<number>(idxs));
                          setBatchAnchor(idxs[0]);
                        }}
                        style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                        title="选中当前过滤器+搜索命中的映射行；无过滤/搜索时选中全部"
                      >全选（{total}）</button>
                      <button
                        type="button"
                        onClick={hidden > 0 ? () => {
                          persistMappingFilter('all');
                          setMappingSearch('');
                        } : undefined}
                        style={{
                          fontSize: 10, padding: '0 5px', borderRadius: 2,
                          fontFamily: 'var(--mono, ui-monospace)',
                          color: chipColor, background: chipBg,
                          cursor: hidden > 0 ? 'pointer' : 'default',
                          border: hidden > 0 ? `1px solid ${chipColor}` : 'none',
                          lineHeight: '16px',
                        }}
                        title={hidden > 0
                          ? `可见 ${total}/${mappings.length}（隐藏 ${hidden}）· 点击回到「全部」过滤器并清空搜索`
                          : `可见 ${total}/${mappings.length}（无隐藏）`}
                        aria-label={hidden > 0 ? `可见 ${total} 共 ${mappings.length}，点击重置过滤器与搜索` : `可见 ${total} 共 ${mappings.length}`}
                      >{total}/{mappings.length}</button>
                    </>
                  );
                })()}
              </div>
              <div style={{ display: 'flex', gap: 4, marginTop: 3, alignItems: 'center', fontSize: 11 }}>
                {(() => {
                  // 搜索命中数（M30.73）：在搜索激活时先算命中数，用于展示 + 一键选中
                  const q = mappingSearch.trim().toLowerCase();
                  if (!q) return null;
                  let hitCount = 0;
                  for (const m of mappings) {
                    if (m.csvName.toLowerCase().includes(q) || (m.targetColumn && m.targetColumn.toLowerCase().includes(q))) hitCount++;
                  }
                  return (
                    <span style={{
                      fontSize: 10, padding: '1px 6px', borderRadius: 2,
                      fontFamily: 'var(--mono, ui-monospace)',
                      color: hitCount === 0 ? 'var(--warn, #d97706)' : 'var(--accent, #3b82f6)',
                      background: hitCount === 0 ? 'rgba(217,119,6,0.10)' : 'rgba(59,130,246,0.10)',
                    }} title={`搜索「${mappingSearch.trim()}」命中 ${hitCount}/${mappings.length} 列`}>
                      {hitCount}/{mappings.length} 命中
                    </span>
                  );
                })()}
                <div style={{ position: 'relative', flex: 1, maxWidth: 320, minWidth: 200 }}>
                  <input
                    ref={(el) => { mapSearchInputRef.current = el; }}
                    type="text"
                    value={mappingSearch}
                    onChange={(e) => {
                      setMappingSearch(e.target.value);
                      // 输入新内容时重置到默认无选中态；有历史就展示下拉
                      setMappingSearchHistIdx(-1);
                      if (mappingSearchHistory.length > 0) setMappingSearchHistOpen(true);
                    }}
                    onFocus={() => {
                      if (mappingSearchHistory.length > 0) setMappingSearchHistOpen(true);
                    }}
                    onBlur={() => {
                      // 延迟以让 dropdown 的 mousedown 先触发；同时自动保存当前搜索词
                      const current = mappingSearch;
                      window.setTimeout(() => {
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                        if (current.trim()) saveMappingSearchHistory(current);
                      }, 120);
                    }}
                    onKeyDown={(e) => {
                      if (!mappingSearchHistOpen || mappingSearchHistory.length === 0) {
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          saveMappingSearchHistory(mappingSearch);
                        }
                        return;
                      }
                      if (e.key === 'ArrowDown') {
                        e.preventDefault();
                        setMappingSearchHistIdx((i) => (i + 1) % mappingSearchHistory.length);
                      } else if (e.key === 'ArrowUp') {
                        e.preventDefault();
                        setMappingSearchHistIdx((i) => (i - 1 + mappingSearchHistory.length) % mappingSearchHistory.length);
                      } else if (e.key === 'Enter') {
                        e.preventDefault();
                        if (mappingSearchHistIdx >= 0 && mappingSearchHistIdx < mappingSearchHistory.length) {
                          const picked = mappingSearchHistory[mappingSearchHistIdx];
                          setMappingSearch(picked);
                          saveMappingSearchHistory(picked);
                        } else {
                          saveMappingSearchHistory(mappingSearch);
                        }
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                      } else if (e.key === 'Escape') {
                        e.preventDefault();
                        setMappingSearch('');
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                      }
                    }}
                    placeholder={mappingSearchHistory.length > 0 ? '🔍 搜索列名…（↓ 历史）' : '🔍 搜索列名…'}
                    style={{ ...styles.inputSm, padding: '1px 6px', fontSize: 11, width: '100%', boxSizing: 'border-box' }}
                    title="按 CSV 列名或目标列名子串搜索（不区分大小写），匹配项置顶，非匹配项半透；↑/↓ 循环历史，Enter 应用，Esc 清空"
                  />
                  {mappingSearchHistOpen && mappingSearchHistory.length > 0 && (
                    <div style={{
                      position: 'absolute', top: '100%', left: 0, right: 0, marginTop: 2,
                      background: 'var(--bg, #1e1e2e)', border: '1px solid var(--border, #3b3b4f)',
                      borderRadius: 4, boxShadow: '0 4px 12px rgba(0,0,0,0.4)',
                      zIndex: 50, maxHeight: 240, overflowY: 'auto',
                      fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                      fontSize: 11,
                    }}>
                      {mappingSearchHistory.map((h, i) => {
                        const isSel = i === mappingSearchHistIdx;
                        return (
                          <div
                            key={`${h}-${i}`}
                            onMouseDown={(e) => {
                              e.preventDefault();
                              setMappingSearch(h);
                              saveMappingSearchHistory(h);
                              setMappingSearchHistOpen(false);
                              setMappingSearchHistIdx(-1);
                            }}
                            onMouseEnter={() => setMappingSearchHistIdx(i)}
                            style={{
                              padding: '3px 8px',
                              cursor: 'pointer',
                              background: isSel ? 'rgba(59,130,246,0.15)' : 'transparent',
                              color: isSel ? 'var(--accent, #3b82f6)' : 'var(--text)',
                              fontWeight: isSel ? 600 : 'inherit',
                              display: 'flex', alignItems: 'center', gap: 6,
                            }}
                          >
                            <span style={{ opacity: 0.5 }}>🕘</span>
                            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{h}</span>
                            <span
                              onMouseDown={(e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                setMappingSearchHistory((prev) => {
                                  const next = prev.filter((_, idx) => idx !== i);
                                  try { localStorage.setItem('polydb.mappingSearchHistory.v1', JSON.stringify(next)); } catch { /* ignore */ }
                                  return next;
                                });
                              }}
                              title="从历史中移除这条"
                              style={{
                                opacity: 0.5, cursor: 'pointer', padding: '0 4px',
                                fontSize: 10, borderRadius: 2,
                                ...(isSel ? { background: 'rgba(0,0,0,0.2)' } : {}),
                              }}
                            >✕</span>
                          </div>
                        );
                      })}
                      <div
                        onMouseDown={(e) => {
                          e.preventDefault();
                          setMappingSearchHistory([]);
                          try { localStorage.removeItem('polydb.mappingSearchHistory.v1'); } catch { /* ignore */ }
                        }}
                        style={{
                          padding: '3px 8px',
                          cursor: 'pointer',
                          borderTop: '1px solid var(--border, #3b3b4f)',
                          color: 'var(--muted, #8b8b9e)',
                          fontSize: 10,
                          textAlign: 'center',
                        }}
                        title="清空搜索历史"
                      >清空历史</div>
                    </div>
                  )}
                </div>
                {mappingSearch && (
                  <button
                    type="button"
                    onClick={() => {
                      // M30.73：一键选中所有搜索命中列，衔接 M30.61-71 批量选通道
                      const q = mappingSearch.trim().toLowerCase();
                      const hits: number[] = [];
                      for (let i = 0; i < mappings.length; i++) {
                        const m = mappings[i];
                        if (m.csvName.toLowerCase().includes(q) || (m.targetColumn && m.targetColumn.toLowerCase().includes(q))) hits.push(i);
                      }
                      if (hits.length === 0) return;
                      setBatchSel(new Set<number>(hits));
                      setBatchAnchor(hits[0]);
                    }}
                    style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                    title="选中所有搜索命中列（当前搜索词），继续可用批量转换/空值策略/清空设置"
                  >全选命中</button>
                )}
                {mappingSearch && (
                  <button
                    type="button"
                    onClick={() => setMappingSearch('')}
                    style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 11 }}
                    title="清空搜索"
                  >✕ 清空</button>
                )}
              </div>
              {parse && (() => {
                const total = parse.rows.length;
                if (total === 0) return null;
                // 健康汇总使用小 cap 控制全列遍历成本：10 万行 × N 列会阻塞渲染
                const cap = Math.min(total, 10000);
                let colsWithTransform = 0;
                let colsWithNonDefaultPolicy = 0;
                let colsWithValidation = 0;
                let totalEmpty = 0;
                let totalViolations = 0;
                let colsWithEmpty = 0;
                let colsWithViolations = 0;
                const transformIdxs: number[] = [];
                const policyIdxs: number[] = [];
                const valIdxs: number[] = [];
                const emptyIdxs: number[] = [];
                const violIdxs: number[] = [];
                // 预先算重复目标列：一个 target 对应 N 个 CSV 列时，除首列外的 N-1 列都会被覆盖
                const targetCount = new Map<string, number>();
                for (let i = 0; i < mappings.length; i++) {
                  const t = mappings[i].targetColumn;
                  if (!t) continue;
                  targetCount.set(t, (targetCount.get(t) ?? 0) + 1);
                }
                let shadowedCols = 0;
                for (const c of targetCount.values()) {
                  if (c > 1) shadowedCols += c - 1;
                }
                const n = mappings.length;
                // 收集重复目标列的所有索引（含胜出列），用于批量选中
                const dupAllIdxs: number[] = [];
                for (let i = 0; i < n; i++) {
                  if (opts.transforms?.[i] && opts.transforms[i] !== 'none') { colsWithTransform++; transformIdxs.push(i); }
                  if (opts.nullPolicies?.[i] && opts.nullPolicies[i] !== 'null') { colsWithNonDefaultPolicy++; policyIdxs.push(i); }
                  const t = mappings[i].targetColumn;
                  if (t && (targetCount.get(t) ?? 0) > 1) dupAllIdxs.push(i);
                  const v = opts.validations?.[i];
                  const hasVal = !!(v && hasAnyValidation(v));
                  if (hasVal) { colsWithValidation++; valIdxs.push(i); }
                  let empty = 0;
                  let viol = 0;
                  for (let r = 0; r < cap; r++) {
                    const cell = parse.rows[r]?.[i];
                    const rawStr = cell == null ? '' : String(cell);
                    if (rawStr === '') {
                      empty++;
                    } else if (hasVal) {
                      const reason = validateCell(rawStr, v!);
                      if (reason) viol++;
                    }
                  }
                  if (empty > 0) { colsWithEmpty++; emptyIdxs.push(i); }
                  if (viol > 0) { colsWithViolations++; violIdxs.push(i); }
                  totalEmpty += empty;
                  totalViolations += viol;
                }
                const emptyPct = (totalEmpty * 100) / cap;
                const violPct = (totalViolations * 100) / cap;
                const capped = total > cap;
                const fmt = (n: number) => capped ? `≥${n}` : String(n);
                return (
                  <div
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: 6,
                      padding: '5px 8px',
                      borderRadius: 3,
                      background: 'rgba(255,255,255,0.03)',
                      border: '1px solid var(--border)',
                      fontSize: 10,
                      fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                      alignItems: 'center',
                    }}
                    title={`映射健康汇总（分母为前 ${cap} 行数据）${capped ? `，超 ${cap} 行时抽样` : ''}`}
                  >
                    <span style={{ color: 'var(--muted)' }}>📋 {n} 列</span>
                    {colsWithTransform > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(transformIdxs));
                          setBatchAnchor(transformIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithTransform} 列（已配置列级转换）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--accent, #3b82f6)',
                          color: 'var(--accent, #3b82f6)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        ⚙ {colsWithTransform} 转换 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>⚙ 0 转换</span>
                    )}
                    {colsWithNonDefaultPolicy > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(policyIdxs));
                          setBatchAnchor(policyIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithNonDefaultPolicy} 列（已配置非默认空值策略：empty-string / skip-row / use-default）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--warn, #d97706)',
                          color: 'var(--warn, #d97706)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🎚 {colsWithNonDefaultPolicy} 空值策略 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>🎚 0 空值策略</span>
                    )}
                    {colsWithValidation > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(valIdxs));
                          setBatchAnchor(valIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithValidation} 列（已配置列级校验规则：required / regex / min-max / enum）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--accent, #3b82f6)',
                          color: 'var(--accent, #3b82f6)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🛡 {colsWithValidation} 校验 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>🛡 0 校验</span>
                    )}
                    {shadowedCols > 0 && (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(dupAllIdxs));
                          setBatchAnchor(dupAllIdxs[0]);
                        }}
                        title={`点击选中这 ${dupAllIdxs.length} 列（多列映射到同一目标列，除首列外都会被覆盖写入，建议只映射一次）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--danger, #dc2626)',
                          color: 'var(--danger, #dc2626)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🔁 {shadowedCols} 重复 →
                      </button>
                    )}
                    <span style={{ color: 'var(--muted)' }}>·</span>
                    {colsWithEmpty > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(emptyIdxs));
                          setBatchAnchor(emptyIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithEmpty} 列（前 ${cap} 行中存在空值）`}
                        style={{
                          background: 'transparent',
                          border: `1px solid ${totalEmpty / cap > 0.3 ? 'var(--warn, #d97706)' : 'var(--text)'}`,
                          color: totalEmpty / cap > 0.3 ? 'var(--warn, #d97706)' : 'var(--text)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        📊 {colsWithEmpty} 列有 {fmt(totalEmpty)} 空 ({emptyPct.toFixed(1)}%) →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--success, #22c55e)' }}>✅ 0 空</span>
                    )}
                    {colsWithViolations > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(violIdxs));
                          setBatchAnchor(violIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithViolations} 列（前 ${cap} 行中存在校验违规）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--danger, #dc2626)',
                          color: 'var(--danger, #dc2626)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        ⚠️ {colsWithViolations} 列有 {fmt(totalViolations)} 违规 ({violPct.toFixed(1)}%) →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--success, #22c55e)' }}>✅ 0 违规</span>
                    )}
                    {qualityGate.enabled && qualityReport && (() => {
                      // 用 qc.score 与 Step 4 门禁一致：Step 4 gate 用 overallScore = mean(qc.score)
                      // columnHealth 是不同口径（deduction 分），此处刻意不用
                      const below: number[] = [];
                      for (let i = 0; i < qualityReport.columns.length; i++) {
                        if (qualityReport.columns[i].score < qualityGate.threshold) below.push(i);
                      }
                      if (below.length === 0) {
                        return <span style={{ color: 'var(--success, #22c55e)' }} title={`所有列 qc.score ≥ 门禁阈值 ${qualityGate.threshold}，Step 4 整体质量评分大概率 ≥ 阈值`}>🚦 门禁 {qualityGate.threshold} ✅</span>;
                      }
                      return (
                        <button
                          type="button"
                          onClick={() => {
                            setBatchSel(new Set<number>(below));
                            setBatchAnchor(below[0]);
                          }}
                          title={`点击选中这 ${below.length} 列（qc.score < ${qualityGate.threshold}，Step 4 门禁会拉低整体评分）`}
                          style={{
                            background: 'transparent',
                            border: '1px solid var(--danger, #dc2626)',
                            color: 'var(--danger, #dc2626)',
                            cursor: 'pointer',
                            fontSize: 10,
                            padding: '0 4px',
                            borderRadius: 2,
                            fontFamily: 'inherit',
                            lineHeight: '16px',
                          }}
                        >
                          🚦 门禁 {qualityGate.threshold} · {below.length} 低分列 →
                        </button>
                      );
                    })()}
                  </div>
                );
              })()}
              <div style={styles.mapGrid}>
                {(() => {
                  // 按过滤器 + 搜索词重排显示顺序：匹配项在前，非匹配项下沉且 opacity 0.4
                  // 数据索引 i 保持不变（Record 键），仅影响视觉顺序
                  const items = mappings.map((m, i) => ({ m, i }));
                  const matched = (m: Mapping, i: number) => {
                    if (mappingFilter === 'unmapped') return m.targetColumn == null;
                    if (mappingFilter === 'mapped') return m.targetColumn != null;
                    if (mappingFilter === 'low-health') {
                      // 低健康档：columnHealths.health < 80（M30.57 4 档阈值的前两档）
                      const ch = columnHealths?.[i];
                      return ch != null && ch.health < 80;
                    }
                    if (mappingFilter === 'critical-health') {
                      // 紧急档：health < 60（amber+red，最严重的两档）
                      const ch = columnHealths?.[i];
                      return ch != null && ch.health < 60;
                    }
                    return true;
                  };
                  // 搜索命中：CSV 列名 或 目标列名 子串（不区分大小写）
                  const q = mappingSearch.trim().toLowerCase();
                  const searchMatch = (m: Mapping) => {
                    if (!q) return true;
                    if (m.csvName.toLowerCase().includes(q)) return true;
                    if (m.targetColumn && m.targetColumn.toLowerCase().includes(q)) return true;
                    return false;
                  };
                  let sorted = items;
                  // M30.81 按映射行排序（默认按 CSV 索引；stable sort 保等值原顺序）
                  if (mappingSort !== 'index') {
                    sorted = [...items].sort((a, b) => {
                      if (mappingSort === 'health-asc') {
                        const ha = columnHealths?.[a.i]?.health ?? 100;
                        const hb = columnHealths?.[b.i]?.health ?? 100;
                        return ha - hb;
                      }
                      if (mappingSort === 'empty-desc') {
                        const ea = profiles[a.i]?.nullCount ?? 0;
                        const eb = profiles[b.i]?.nullCount ?? 0;
                        return eb - ea;
                      }
                      if (mappingSort === 'target-asc') {
                        const ta = a.m.targetColumn ?? '\uffffunmapped';
                        const tb = b.m.targetColumn ?? '\uffffunmapped';
                        return ta.localeCompare(tb);
                      }
                      if (mappingSort === 'csv-asc') {
                        return a.m.csvName.toLowerCase().localeCompare(b.m.csvName.toLowerCase());
                      }
                      return 0;
                    });
                  }
                  if (mappingFilter !== 'all') {
                    sorted = [...sorted.filter((x) => matched(x.m, x.i)), ...sorted.filter((x) => !matched(x.m, x.i))];
                  }
                  if (q) {
                    sorted = [...sorted.filter((x) => searchMatch(x.m)), ...sorted.filter((x) => !searchMatch(x.m))];
                  }
                  // 命中搜索的集合，用于高亮行
                  const searchHits = new Set<number>();
                  if (q) for (const x of items) if (searchMatch(x.m)) searchHits.add(x.i);
                  // 重复映射检测：多列指向同一 targetColumn 时后续列会覆盖前面列的写入
                  const targetToIndices = new Map<string, number[]>();
                  for (let i = 0; i < mappings.length; i++) {
                    const t = mappings[i].targetColumn;
                    if (!t) continue;
                    const arr = targetToIndices.get(t);
                    if (arr) arr.push(i);
                    else targetToIndices.set(t, [i]);
                  }
                  const dupTargets = new Map<string, number[]>();
                  for (const [t, idxs] of targetToIndices) {
                    if (idxs.length > 1) dupTargets.set(t, idxs);
                  }
                  return sorted.map(({ m, i }) => {
                  const p = profiles[i];
                  const targetCol = m.targetColumn ? colInfoMap.get(m.targetColumn) : undefined;
                  const warn = warnFor(i);
                  const qc = qualityReport?.columns[i];
                  const nonNull = p ? p.total - p.nullCount : 0;
                  const suspiciousCount = qc?.suspiciousCount ?? 0;
                  const hasTransform = !!opts.transforms?.[i] && opts.transforms[i] !== 'none';
                  const dupIdxs = m.targetColumn ? dupTargets.get(m.targetColumn) : undefined;
                  const isDupShadowed = !!dupIdxs && i !== dupIdxs[dupIdxs.length - 1];
                  const dupLastIdx = dupIdxs ? dupIdxs[dupIdxs.length - 1] : -1;
                  const impactItems: Array<{ key: string; text: string; color: string; title: string }> = [];
                  if (isDupShadowed) {
                    impactItems.push({
                      key: 'dup-shadowed',
                      text: '🔁 将被覆盖',
                      color: 'var(--danger, #dc2626)',
                      title: `本列与 CSV 列 ${dupIdxs!.map((d) => '#' + (d + 1)).join('、')} 都映射到 ${m.targetColumn}。写入顺序按 CSV 列索引递增，#${dupLastIdx + 1} 的写入值会覆盖前面的所有值（含本列）。建议只映射一次；若需保留本列的值，可将其调整到最靠后的 CSV 列位置。`,
                    });
                  }
                  if (p && p.nullCount > 0) {
                    const pct = p.total > 0 ? Math.round((p.nullCount / p.total) * 100) : 0;
                    impactItems.push({ key: 'null', text: `∅${p.nullCount} (${pct}%)`, color: 'var(--muted)', title: `CSV 空值 ${p.nullCount}/${p.total}` });
                  }
                  if (suspiciousCount > 0 && qc) {
                    impactItems.push({ key: 'susp', text: `?${suspiciousCount}`, color: 'var(--warn, #d97706)', title: `可疑值 ${suspiciousCount}：前导0=${qc.suspiciousBreakdown.leadingZero} 多余空格=${qc.suspiciousBreakdown.extraSpaces} 尾引号=${qc.suspiciousBreakdown.trailingQuote} 孤立破折号=${qc.suspiciousBreakdown.dashOnly}` });
                  }
                  if (qc && qc.typePurity < 0.9 && qc.nonNull > 0) {
                    const pct = Math.round(qc.typePurity * 100);
                    impactItems.push({ key: 'pure', text: `T${pct}%`, color: 'var(--warn, #d97706)', title: `类型纯净度 ${pct}%（主导类型 ${qc.dominantType} 占非空值比例）` });
                  }
                  if (hasTransform) {
                    impactItems.push({ key: 'trf', text: `⚙${opts.transforms?.[i]}`, color: 'var(--accent, #3b82f6)', title: `已配置转换：${opts.transforms?.[i]}（将应用于 ${nonNull} 个非空值）` });
                  }
                  // 综合健康分 0-100：聚合 M30.52-56 的所有信号
                  let health = 100;
                  if (!m.targetColumn) health -= 40;
                  if (isDupShadowed) health -= 25;
                  const v = opts.validations?.[i];
                  const vCfg = !!(v && hasAnyValidation(v));
                  if (p && p.nullCount > 0 && !vCfg) health -= Math.min(15, Math.round((p.nullCount / Math.max(1, p.total)) * 15));
                  if (qc && qc.suspiciousCount > 0 && qc.nonNull > 0) health -= Math.min(15, Math.round((qc.suspiciousCount / qc.nonNull) * 100 * 0.15));
                  if (qc && qc.typePurity < 0.9 && qc.nonNull > 0) health -= Math.round((0.9 - qc.typePurity) * 50);
                  if (warn) health -= 10;
                  health = Math.max(0, health);
                  const healthColor = health >= 80 ? 'var(--success, #22c55e)'
                    : health >= 60 ? 'var(--warn, #d97706)'
                    : health >= 40 ? '#f59e0b'
                    : 'var(--danger, #dc2626)';
                  const isSelected = batchSel.has(i);
                  const onRowClick = (e: React.MouseEvent) => {
                    if (e.shiftKey && batchAnchor !== null) {
                      const lo = Math.min(batchAnchor, i);
                      const hi = Math.max(batchAnchor, i);
                      setBatchSel(new Set<number>(Array.from({length: hi - lo + 1}, (_, k) => lo + k)));
                    } else {
                      setBatchAnchor(i);
                      setBatchSel((prev) => {
                        const has = prev.has(i);
                        const next = new Set(prev);
                        if (has) next.delete(i); else next.add(i);
                        return next;
                      });
                    }
                  };
                  // 低健康分行底色：越差越红，方便用户一眼看到最需要处理的列
                  const healthBg = health >= 80
                    ? undefined
                    : health >= 60
                      ? 'rgba(217,119,6,0.05)'
                      : health >= 40
                        ? 'rgba(245,158,11,0.08)'
                        : 'rgba(220,38,38,0.10)';
                  const rowBg = isSelected ? 'rgba(59,130,246,0.10)' : healthBg;
                  // 搜索非命中项半透下沉（M30.72），命中项保持 1.0
                  const dimBySearch = q.length > 0 && !searchHits.has(i) ? 0.4 : 1;
                  // M30.77 行级过滤/搜索命中指示：仅在 mappingFilter 非 'all' 或有搜索时显示
                  const hasFilter = mappingFilter !== 'all' || q.length > 0;
                  const filterHit = hasFilter ? matched(m, i) : false;
                  const searchHit = q.length > 0 ? searchMatch(m) : false;
                  let filterDotColor = '';
                  let filterDotTitle = '';
                  if (hasFilter) {
                    const inFilter = mappingFilter === 'all' || filterHit;
                    const inSearch = q.length === 0 || searchHit;
                    if (inFilter && inSearch) {
                      filterDotColor = 'var(--accent, #3b82f6)';
                      filterDotTitle = '✓ 命中当前过滤器与搜索';
                    } else if (inFilter || inSearch) {
                      filterDotColor = 'var(--warn, #d97706)';
                      filterDotTitle = `⚠ 部分命中：过滤器 ${inFilter ? '命中' : '排除'}，搜索 ${inSearch ? '命中' : '排除'}`;
                    } else {
                      filterDotColor = 'var(--danger, #dc2626)';
                      filterDotTitle = '✕ 被过滤器和搜索共同排除';
                    }
                  }
                  return (
                    <div key={i} data-map-row-idx={i} className={jumpFlashIdx === i ? 'map-row-jump-flash' : undefined} style={{ ...styles.mapRow, position: 'relative', background: rowBg, borderLeft: isSelected ? '3px solid var(--accent, #3b82f6)' : '3px solid transparent', padding: '0 0 0 6px', boxSizing: 'border-box', opacity: dimBySearch }}>
                      <div style={{ ...styles.mapCell, flexDirection: 'column', alignItems: 'flex-start', gap: 2, cursor: 'pointer' }} onClick={onRowClick} title="点击选中 / Shift+Click 拖选多行，用于批量设置转换/空值策略">
                        <div style={{ display: 'flex', gap: 6, alignItems: 'center', width: '100%' }}>
                          {filterDotColor && (
                            <span
                              title={filterDotTitle}
                              style={{
                                display: 'inline-block', width: 6, height: 6, borderRadius: '50%',
                                background: filterDotColor,
                                flexShrink: 0,
                              }}
                            />
                          )}
                          <span style={styles.muted}>{m.csvIndex + 1}.</span>
                          <span style={{ fontFamily: 'monospace' }}>{highlightQuery(m.csvName)}</span>
                          {p && <span style={{ ...styles.typeBadge, color: typeBadgeColor(p.type) }}>{p.type}</span>}
                          <span style={{ flex: 1 }} />
                          {(() => {
                            const autoFixSuggestions = (health < 80 && qc) ? suggestTransforms(qc) : [];
                            const canAutoFix = autoFixSuggestions.length > 0;
                            const firstS = autoFixSuggestions[0];
                            return (
                            <span
                              style={{
                                fontSize: 9,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                color: healthColor,
                                fontWeight: 700,
                                padding: '0 4px',
                                borderRadius: 2,
                                background: 'rgba(255,255,255,0.05)',
                                minWidth: 26,
                                textAlign: 'center',
                                cursor: canAutoFix ? 'pointer' : 'default',
                                ...(canAutoFix ? { border: '1px solid currentColor' } : {}),
                              }}
                              title={canAutoFix
                                ? `综合健康分：${health}/100 · 点击应用建议「${firstS!.transform}」（${firstS!.reason}），共 ${autoFixSuggestions.length} 条建议取首条保守策略。≥80 绿 · 60-79 橙 · 40-59 黄 · <40 红`
                                : `综合健康分：${health}/100 · 基于空值率、类型纯净度、可疑值、校验配置、目标映射、覆盖冲突等信号综合计算。≥80 绿 · 60-79 橙 · 40-59 黄 · <40 红`}
                              onClick={(e) => {
                                if (!canAutoFix) return;
                                e.stopPropagation();
                                const s = autoFixSuggestions[0]!;
                                const next = { ...opts.transforms };
                                next[i] = s.transform;
                                const nextParams = { ...opts.transformParams };
                                if (s.transform === 'regex-replace') {
                                  nextParams[i] = {
                                    pattern: s.pattern,
                                    replacement: s.replacement,
                                    flags: s.flags ?? 'g',
                                  };
                                }
                                setOpts({ ...opts, transforms: next, transformParams: nextParams });
                              }}
                            >
                              {health}
                            </span>
                            );
                          })()}
                        </div>
                        {impactItems.length > 0 && (
                          <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', fontSize: 9 }}>
                            {impactItems.map((it) => (
                              <span
                                key={it.key}
                                title={it.title}
                                style={{
                                  padding: '0 4px', borderRadius: 2,
                                  background: 'rgba(255,255,255,0.05)',
                                  color: it.color,
                                  fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                  fontWeight: 600,
                                }}
                              >{it.text}</span>
                            ))}
                          </div>
                        )}
                        {warn && <div style={styles.warnText} title={warn}>⚠ {warn}</div>}
                      </div>
                      <span style={styles.arrow}>→</span>
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                        <select
                          value={m.targetColumn ?? ''}
                          onChange={(e) => {
                            const v = e.target.value || null;
                            const next = mappings.slice();
                            next[i] = { ...next[i], targetColumn: v };
                            setMappings(next);
                          }}
                          style={{ ...styles.selectSm, ...(warn ? styles.selectWarn : {}) }}
                        >
                          <option value="">— 跳过 —</option>
                          {cols.map((c) => (
                            <option key={c.name} value={c.name} disabled={c.is_auto_increment}>
                              {c.name}{c.is_primary_key ? ' (PK)' : ''}{c.is_auto_increment ? ' (自增)' : ''} · {c.data_type}
                            </option>
                          ))}
                        </select>
                        {targetCol && (() => {
                          const badges: Array<{ text: string; color: string; bg: string; title: string }> = [];
                          badges.push({ text: targetCol.data_type, color: 'var(--text)', bg: 'rgba(255,255,255,0.05)', title: `目标类型：${targetCol.data_type}` });
                          if (targetCol.is_primary_key) badges.push({ text: 'PK', color: '#fff', bg: 'var(--danger, #dc2626)', title: '主键列（唯一标识一行；update/upsert 模式用作 WHERE 条件）' });
                          if (targetCol.is_auto_increment) badges.push({ text: '自增', color: '#fff', bg: 'var(--accent, #3b82f6)', title: '自增列（一般无需在 CSV 中提供值）' });
                          if (!targetCol.nullable) badges.push({ text: 'NOT NULL', color: '#fff', bg: 'var(--warn, #d97706)', title: '非空列（CSV 空值会触发校验警告）' });
                          if (targetCol.default_value && String(targetCol.default_value).length > 0) {
                            const dv = String(targetCol.default_value);
                            badges.push({ text: `默认 ${dv.length > 12 ? dv.slice(0, 12) + '…' : dv}`, color: 'var(--muted)', bg: 'rgba(255,255,255,0.03)', title: `默认值：${dv}` });
                          }
                          return (
                            <div style={{ display: 'flex', gap: 3, flexWrap: 'wrap', fontSize: 10 }}>
                              {badges.map((b, bi) => (
                                <span
                                  key={bi}
                                  title={b.title}
                                  style={{
                                    padding: '1px 5px', borderRadius: 2,
                                    background: b.bg, color: b.color,
                                    fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                    fontWeight: 600, whiteSpace: 'nowrap',
                                  }}
                                >{b.text}</span>
                              ))}
                            </div>
                          );
                        })()}
                        <select
                          value={opts.transforms?.[i] ?? 'none'}
                          onChange={(e) => {
                            const v = e.target.value as ColumnTransform;
                            const next = { ...opts.transforms };
                            if (v === 'none') delete next[i];
                            else next[i] = v;
                            setOpts({ ...opts, transforms: next });
                          }}
                          style={{ ...styles.selectSm, marginTop: 2 }}
                          title="对该 CSV 列应用的字符串转换"
                        >
                          <option value="none">— 无转换 —</option>
                          <option value="trim">Trim 去首尾空格</option>
                          <option value="lower">Lower 转小写</option>
                          <option value="upper">Upper 转大写</option>
                          <option value="null-if-empty">Null if empty 空当 NULL</option>
                          <option value="strip-zero-padding">Strip 0-padding</option>
                          <option value="regex-replace">Regex Replace…</option>
                          <option value="date-parse-iso">Date → ISO 8601</option>
                          <option value="date-parse-us">Date US (MM/dd/yyyy) → ISO</option>
                          <option value="date-parse-ymd">Date YMD (yyyy-MM-dd) → ISO</option>
                          <option value="scale-x100">Scale ×100（元→分）</option>
                          <option value="scale-div100">Scale ÷100（分→元）</option>
                        </select>
                        {opts.transforms?.[i] === 'regex-replace' && (
                          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4, marginTop: 2 }}>
                            <input
                              type="text"
                              value={opts.transformParams?.[i]?.pattern ?? ''}
                              onChange={(e) => {
                                const params = { ...opts.transformParams };
                                params[i] = { ...params[i], pattern: e.target.value };
                                setOpts({ ...opts, transformParams: params });
                              }}
                              style={{ ...styles.inputSm, fontFamily: 'monospace', fontSize: 11 }}
                              placeholder="正则 /regex/"
                            />
                            <input
                              type="text"
                              value={opts.transformParams?.[i]?.replacement ?? ''}
                              onChange={(e) => {
                                const params = { ...opts.transformParams };
                                params[i] = { ...params[i], replacement: e.target.value };
                                setOpts({ ...opts, transformParams: params });
                              }}
                              style={{ ...styles.inputSm, fontFamily: 'monospace', fontSize: 11 }}
                              placeholder="替换为"
                            />
                          </div>
                        )}
                        {hasTransform && parse && (() => {
                          const t = opts.transforms?.[i];
                          const params = opts.transformParams?.[i];
                          const samples: Array<{ raw: string; out: string | null }> = [];
                          for (let r = 0; r < parse.rows.length && samples.length < 3; r++) {
                            const raw = parse.rows[r]?.[i];
                            if (raw == null || raw === '') continue;
                            const s = String(raw);
                            if (s === '') continue;
                            samples.push({ raw: s, out: transformOnly(s, t, params) });
                          }
                          if (samples.length === 0) return null;
                          return (
                            <div
                              style={{
                                marginTop: 3,
                                padding: '3px 6px',
                                borderRadius: 2,
                                background: 'rgba(59, 130, 246, 0.06)',
                                border: '1px solid rgba(59, 130, 246, 0.15)',
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                fontSize: 10,
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 1,
                              }}
                              title={`显示该列前 ${samples.length} 个非空 CSV 值经转换后的结果，用于确认转换规则符合预期`}
                            >
                              {samples.map((s, si) => {
                                const disp = (x: string | null, max: number) => {
                                  const t = x ?? 'null';
                                  return t.length > max ? t.slice(0, max) + '…' : t;
                                };
                                const changed = (s.out ?? 'null') !== s.raw;
                                return (
                                  <div key={si} style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                                    <span style={{ color: 'var(--muted)', flexShrink: 0 }} title={s.raw}>
                                      {disp(s.raw, 24)}
                                    </span>
                                    <span style={{ color: changed ? 'var(--accent, #3b82f6)' : 'var(--muted)', flexShrink: 0 }}>
                                      {changed ? '→' : '='}
                                    </span>
                                    <span
                                      style={{
                                        color: s.out == null ? 'var(--muted)' : changed ? 'var(--text)' : 'var(--muted)',
                                        fontStyle: s.out == null ? 'italic' : 'normal',
                                        flexShrink: 0,
                                      }}
                                      title={s.out ?? 'null（空值/空字符串将被视为 null）'}
                                    >
                                      {disp(s.out, 24)}
                                    </span>
                                  </div>
                                );
                              })}
                            </div>
                          );
                        })()}
                        <select
                          value={opts.nullPolicies?.[i] ?? 'null'}
                          onChange={(e) => {
                            const v = e.target.value as NullPolicy;
                            const next = { ...opts.nullPolicies };
                            if (v === 'null') delete next[i];
                            else next[i] = v;
                            setOpts({ ...opts, nullPolicies: next });
                          }}
                          style={{ ...styles.selectSm, marginTop: 2 }}
                          title="该 CSV 列为空时如何处理（默认跟随全局「空值转 NULL」开关）"
                        >
                          <option value="null">— 空值 → NULL（默认）—</option>
                          <option value="empty-string">空值 → 空字符串 ''</option>
                          <option value="skip-row">空值 → 跳过整行</option>
                          <option value="use-default" disabled={targetCol?.default_value == null || targetCol.default_value === ''}>
                            空值 → 列默认值{targetCol?.default_value ? ` (${targetCol.default_value})` : '（目标列无默认值）'}
                          </option>
                        </select>
                        {parse && (() => {
                          const policy = opts.nullPolicies?.[i] ?? 'null';
                          const total = parse.rows.length;
                          if (total === 0) return null;
                          const cap = Math.min(total, 100000);
                          let emptyCount = 0;
                          for (let r = 0; r < cap; r++) {
                            const raw = parse.rows[r]?.[i];
                            if (raw == null || String(raw) === '') emptyCount++;
                          }
                          if (emptyCount === 0) return null;
                          const pct = (emptyCount * 100) / cap;
                          const capped = total > cap;
                          const countLabel = capped ? `≥${emptyCount}` : String(emptyCount);
                          const color =
                            policy === 'skip-row' ? 'var(--danger, #dc2626)'
                            : policy === 'use-default' ? 'var(--warn, #d97706)'
                            : policy === 'empty-string' ? 'var(--accent, #3b82f6)'
                            : 'var(--muted)';
                          const effect =
                            policy === 'skip-row' ? '将跳过整行'
                            : policy === 'use-default' ? '将用默认值填充'
                            : policy === 'empty-string' ? '将填空字符串'
                            : '将置为 NULL';
                          const cappedNote = capped ? '（前 10 万行抽样）' : '';
                          return (
                            <div
                              style={{
                                marginTop: 2,
                                padding: '2px 6px',
                                borderRadius: 2,
                                background: 'rgba(255,255,255,0.03)',
                                borderLeft: `2px solid ${color}`,
                                fontSize: 10,
                                color: color,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                              }}
                              title={`该 CSV 列在 ${total} 行中有 ${emptyCount} 行为空（${pct.toFixed(1)}%）${cappedNote}，当前策略「${policy}」的影响：${effect}`}
                            >
                              📊 {countLabel} / {total} 空 ({pct.toFixed(1)}%) · {effect}
                            </div>
                          );
                        })()}
                        <details style={{ marginTop: 2 }} open={hasAnyValidation(opts.validations?.[i])}>
                          <summary style={{ fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                            🛡 校验规则 {hasAnyValidation(opts.validations?.[i]) ? '·已配置' : '·未配置'}
                          </summary>
                          {(() => {
                            const v = opts.validations?.[i] ?? EMPTY_VALIDATION;
                            const update = (patch: Partial<ColumnValidation>) => {
                              const next = { ...opts.validations };
                              const merged = { ...EMPTY_VALIDATION, ...v, ...patch };
                              if (!hasAnyValidation(merged)) delete next[i];
                              else next[i] = merged;
                              setOpts({ ...opts, validations: next });
                            };
                            return (
                              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4, marginTop: 4 }}>
                                <label style={{ ...styles.check, fontSize: 11 }}>
                                  <input
                                    type="checkbox"
                                    checked={v.required}
                                    onChange={(e) => update({ required: e.target.checked })}
                                  />
                                  <span>必填</span>
                                </label>
                                <div />
                                <input
                                  type="text"
                                  value={v.regex}
                                  onChange={(e) => update({ regex: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10, fontFamily: 'monospace' }}
                                  placeholder="正则 /.../"
                                />
                                <input
                                  type="text"
                                  value={v.enum}
                                  onChange={(e) => update({ enum: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="枚举 a,b,c"
                                />
                                <input
                                  type="number"
                                  value={v.minValue}
                                  onChange={(e) => update({ minValue: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="最小值"
                                />
                                <input
                                  type="number"
                                  value={v.maxValue}
                                  onChange={(e) => update({ maxValue: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="最大值"
                                />
                              </div>
                            );
                          })()}
                        </details>
                        {parse && opts.validations?.[i] && (() => {
                          const v = opts.validations[i];
                          if (!hasAnyValidation(v)) return null;
                          const total = parse.rows.length;
                          if (total === 0) return null;
                          const cap = Math.min(total, 100000);
                          let violations = 0;
                          const samples: Array<{ row: number; raw: string; reason: string }> = [];
                          for (let r = 0; r < cap; r++) {
                            const cell = parse.rows[r]?.[i];
                            const raw = cell == null ? '' : String(cell);
                            const reason = validateCell(raw, v);
                            if (reason) {
                              violations++;
                              if (samples.length < 3) {
                                const dispRaw = raw.length > 24 ? raw.slice(0, 24) + '…' : raw;
                                samples.push({ row: r + 1, raw: dispRaw, reason });
                              }
                            }
                          }
                          if (violations === 0) {
                            return (
                              <div
                                style={{
                                  marginTop: 2,
                                  padding: '2px 6px',
                                  borderRadius: 2,
                                  background: 'rgba(34,197,94,0.06)',
                                  borderLeft: '2px solid var(--success, #22c55e)',
                                  fontSize: 10,
                                  color: 'var(--success, #22c55e)',
                                  fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                }}
                                title={`所有 ${cap} 行都通过当前校验规则，可提交`}
                              >
                                ✅ 0 / {cap} 违规
                              </div>
                            );
                          }
                          const capped = total > cap;
                          const pct = (violations * 100) / cap;
                          const countLabel = capped ? `≥${violations}` : String(violations);
                          return (
                            <div
                              style={{
                                marginTop: 2,
                                padding: '3px 6px',
                                borderRadius: 2,
                                background: 'rgba(220,38,38,0.06)',
                                borderLeft: '2px solid var(--danger, #dc2626)',
                                fontSize: 10,
                                color: 'var(--danger, #dc2626)',
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 1,
                              }}
                              title={`当前校验规则在 ${cap} 行中发现 ${violations} 处违规（${pct.toFixed(1)}%）${capped ? '（前 10 万行抽样）' : ''}。开启「严格校验」时这些行会跳过导入。`}
                            >
                              <div>⚠️ {countLabel} / {cap} 违规 ({pct.toFixed(1)}%)</div>
                              {samples.map((s, si) => (
                                <div key={si} style={{ opacity: 0.85, color: 'var(--text)' }}>
                                  L{s.row}: <span style={{ opacity: 0.7 }}>「{s.raw}」</span> → {s.reason}
                                </div>
                              ))}
                            </div>
                          );
                        })()}
                      </div>
                    </div>
                  );
                  });
                })()}
              </div>
              {batchSel.size >= 1 && (() => {
                // 已选列在 "已选 N 行" 后面追加 CSV 名称（前 3 显全名，>3 截断 +N）
                // M30.61-70 各 chip 点击后都会调用 setBatchSel，用户需要立即看到选中了哪些列
                const sortedIdxs = [...batchSel].sort((a, b) => a - b);
                const showMax = 3;
                const names = sortedIdxs.slice(0, showMax).map((i) => mappings[i]?.csvName ?? `#${i}`);
                const extra = sortedIdxs.length - showMax;
                const namesText = extra > 0 ? `${names.join('、')} … +${extra}` : names.join('、');
                const healthsOfSelected = sortedIdxs
                  .map((i) => columnHealths?.[i]?.health)
                  .filter((h): h is number => h != null);
                const avgHealth = healthsOfSelected.length > 0
                  ? Math.round(healthsOfSelected.reduce((s, h) => s + h, 0) / healthsOfSelected.length)
                  : null;
                return (
                <div style={{
                  display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
                  padding: '6px 10px', marginTop: 4,
                  background: 'rgba(59,130,246,0.08)',
                  border: '1px solid var(--accent, #3b82f6)',
                  borderLeft: '3px solid var(--accent, #3b82f6)',
                  borderRadius: 4, fontSize: 11,
                }}>
                  <span style={{ color: 'var(--accent, #3b82f6)', fontWeight: 600 }} title={`已选中：${sortedIdxs.map(i => mappings[i]?.csvName ?? `#${i}`).join('、')}`}>
                    已选 {batchSel.size} 行
                    {avgHealth != null && (() => {
                      const c = avgHealth >= 80 ? 'var(--success, #22c55e)'
                        : avgHealth >= 60 ? 'var(--warn, #d97706)'
                        : avgHealth >= 40 ? '#f59e0b'
                        : 'var(--danger, #dc2626)';
                      return (
                        <span style={{ marginLeft: 6, padding: '0 4px', fontSize: 10, fontFamily: 'var(--mono, ui-monospace)', color: c, border: `1px solid ${c}`, borderRadius: 2, fontWeight: 700 }} title={`选中列平均健康分 ${avgHealth}/100`}>
                          ⚖ {avgHealth}
                        </span>
                      );
                    })()}
                  </span>
                  <span style={{ color: 'var(--muted)', fontSize: 10, fontFamily: 'var(--mono, ui-monospace)' }} title="选中列的 CSV 名称">
                    {namesText}
                  </span>
                  <span style={{ color: 'var(--muted)' }}>·</span>
                  <select
                    defaultValue=""
                    style={{ ...styles.selectSm, padding: '1px 4px' }}
                    title="对选中所有行统一设置字符串转换"
                    onChange={(e) => {
                      const v = e.target.value as ColumnTransform | '';
                      if (v === '') return;
                      const next = { ...opts.transforms };
                      for (const i of batchSel) {
                        if (v === 'none') delete next[i]; else next[i] = v;
                      }
                      setOpts({ ...opts, transforms: next });
                      e.target.value = '';
                    }}
                  >
                    <option value="" disabled>批量转换…</option>
                    <option value="none">— 无转换 —</option>
                    <option value="trim">Trim 去首尾空格</option>
                    <option value="lower">Lower 转小写</option>
                    <option value="upper">Upper 转大写</option>
                    <option value="null-if-empty">Null if empty 空当 NULL</option>
                    <option value="strip-zero-padding">Strip 0-padding</option>
                    <option value="date-parse-iso">Date → ISO 8601</option>
                    <option value="date-parse-us">Date US → ISO</option>
                    <option value="date-parse-ymd">Date YMD → ISO</option>
                    <option value="scale-x100">Scale ×100（元→分）</option>
                    <option value="scale-div100">Scale ÷100（分→元）</option>
                  </select>
                  <select
                    defaultValue=""
                    style={{ ...styles.selectSm, padding: '1px 4px' }}
                    title="对选中所有行统一设置空值策略"
                    onChange={(e) => {
                      const v = e.target.value as NullPolicy | '';
                      if (v === '') return;
                      const next = { ...opts.nullPolicies };
                      for (const i of batchSel) {
                        if (v === 'null') delete next[i]; else next[i] = v;
                      }
                      setOpts({ ...opts, nullPolicies: next });
                      e.target.value = '';
                    }}
                  >
                    <option value="" disabled>批量空值策略…</option>
                    <option value="null">空值 → NULL（默认）</option>
                    <option value="empty-string">空值 → 空字符串 ''</option>
                    <option value="skip-row">空值 → 跳过整行</option>
                    <option value="use-default">空值 → 列默认值</option>
                  </select>
                  <button
                    onClick={() => {
                      const nextT = { ...opts.transforms };
                      const nextV = { ...opts.validations };
                      const nextNP = { ...opts.nullPolicies };
                      for (const i of batchSel) { delete nextT[i]; delete nextV[i]; delete nextNP[i]; }
                      setOpts({ ...opts, transforms: nextT, validations: nextV, nullPolicies: nextNP });
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="清除选中行的转换/空值策略/校验规则"
                  >🧹 清空设置</button>
                  <span style={{ flex: 1 }} />
                  {(() => {
                    // M30.79：批量应用建议——仅对选中列应用首条建议（health<80 且有 qualityReport）
                    const applicable: number[] = [];
                    for (const idx of batchSel) {
                      const ch = columnHealths?.[idx];
                      if (!ch || ch.health >= 80) continue;
                      const qc = qualityReport?.columns[idx];
                      if (!qc) continue;
                      if (suggestTransforms(qc).length === 0) continue;
                      applicable.push(idx);
                    }
                    if (applicable.length === 0) return null;
                    return (
                      <button
                        onClick={() => {
                          const nextT = { ...opts.transforms };
                          const nextParams = { ...opts.transformParams };
                          for (const idx of applicable) {
                            const qc = qualityReport?.columns[idx];
                            if (!qc) continue;
                            const s = suggestTransforms(qc)[0];
                            if (!s) continue;
                            nextT[idx] = s.transform;
                            if (s.transform === 'regex-replace') {
                              nextParams[idx] = {
                                pattern: s.pattern,
                                replacement: s.replacement,
                                flags: s.flags ?? 'g',
                              };
                            }
                          }
                          setOpts({ ...opts, transforms: nextT, transformParams: nextParams });
                          publishEditorStatus({
                            message: `⚡ 已对选中列中的 ${applicable.length} 列应用首条建议转换`,
                            messageAt: Date.now(),
                          });
                        }}
                        style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                        title="对选中列中的低健康分列各应用首条质量建议（trim/strip-zero-padding/regex-replace/date-parse-* 等，取首条保守策略）；会覆盖已有的转换设置"
                      >⚡ 应用建议（{applicable.length}）</button>
                    );
                  })()}
                  <button
                    onClick={() => {
                      const allIdx = new Set<number>(Array.from({ length: mappings.length }, (_, k) => k));
                      const next = new Set<number>();
                      for (const i of allIdx) if (!batchSel.has(i)) next.add(i);
                      setBatchSel(next);
                      setBatchAnchor(next.size > 0 ? Math.min(...next) : null);
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="反选：切换选中集合（未选中的变成选中，已选中的变成未选中）"
                  >⇄ 反选</button>
                  <button
                    onClick={() => {
                      setBatchAnchor(null);
                      setBatchSel(new Set<number>());
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="清除选择"
                  >取消选择</button>
                </div>
                );
              })()}
              {mappingSuggestions.length > 0 && (
                <div style={{ ...styles.mutedBox, borderLeft: '3px solid var(--ok, #10b981)', display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={styles.label}>💡 智能映射建议</span>
                    <span style={{ fontSize: 11, color: 'var(--muted)' }}>
                      {mappingSuggestions.length} 条模糊匹配，未映射的 CSV 列可能对应以下目标列
                    </span>
                    <span style={styles.spacer} />
                    <button
                      onClick={() => {
                        const next = mappings.slice();
                        for (const s of mappingSuggestions) {
                          if (next[s.csvIndex]) next[s.csvIndex] = { ...next[s.csvIndex], targetColumn: s.targetColumn };
                        }
                        setMappings(next);
                      }}
                      style={{ ...styles.btnSm, fontSize: 11 }}
                    >全部接受</button>
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 3, maxHeight: 160, overflow: 'auto' }}>
                    {mappingSuggestions.map((s) => (
                      <div key={s.csvIndex} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto auto', gap: 8, alignItems: 'center', fontSize: 11, fontFamily: 'monospace', padding: '3px 6px', background: 'var(--panel)', borderRadius: 3, border: '1px solid var(--border)' }}>
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          <span style={{ color: 'var(--muted)' }}>{s.csvIndex + 1}.</span> {s.csvName}
                          <span style={{ color: 'var(--accent)', margin: '0 4px' }}>→</span>
                          <strong>{s.targetColumn}</strong>
                        </span>
                        <span style={{ color: qualityScoreColor(Math.min(100, s.score)), fontSize: 10, fontWeight: 700 }} title="匹配分">{Math.round(s.score)}</span>
                        <span style={{ fontSize: 10, color: 'var(--muted)', maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={s.reason}>{s.reason}</span>
                        <button
                          onClick={() => {
                            const next = mappings.slice();
                            if (next[s.csvIndex]) next[s.csvIndex] = { ...next[s.csvIndex], targetColumn: s.targetColumn };
                            setMappings(next);
                          }}
                          style={{ ...styles.btnSm, fontSize: 10, padding: '2px 6px' }}
                        >接受</button>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              <div style={{ ...styles.mutedBox, fontSize: 11 }}>
                <label style={{ ...styles.check }}>
                  <input
                    type="checkbox"
                    checked={opts.strictValidation}
                    onChange={(e) => setOpts({ ...opts, strictValidation: e.target.checked })}
                  />
                  <span>严格校验：违规行自动跳过（关闭时仅显示警告但继续导入）</span>
                </label>
                {validationResult && validationResult.violations.length > 0 && (
                  <div style={{ marginTop: 4, color: 'var(--warn, #d97706)' }}>
                    ⚠ {validationResult.violations.length} 行未通过校验
                    {opts.strictValidation && ' · 将被自动跳过'}
                    <details style={{ marginTop: 2 }}>
                      <summary style={{ cursor: 'pointer', fontSize: 10 }}>查看前 10 行违规</summary>
                      <pre style={{ ...styles.pre, marginTop: 4, maxHeight: 120 }}>
                        {validationResult.violations.slice(0, 10).map((r) =>
                          `第${r.csvRow}行: ${r.reasons.join('; ')}`
                        ).join('\n')}
                      </pre>
                    </details>
                  </div>
                )}
              </div>
              <div style={styles.mutedBox}>
                映射 {mappedCount} 列 · 跳过 {skippedCount} 列 · 目标 <code>{selSchema}.{selTable}</code>
                {(() => {
                  const warnCount = mappings.reduce((n, _m, i) => (warnFor(i) ? n + 1 : n), 0);
                  return warnCount > 0
                    ? <span style={{ color: 'var(--warn, #d97706)', marginLeft: 6 }}>· {warnCount} 处类型警告（可继续但可能失败）</span>
                    : null;
                })()}
                {presetApplied && (
                  <span style={{ color: 'var(--accent, #3b82f6)', marginLeft: 6 }}>
                    · 📌 已应用预设（{presetApplied.matched}/{presetApplied.total} 列匹配）
                    {presetApplied.removed.length > 0 && (
                      <span style={{ color: 'var(--warn, #d97706)' }}>· ⚠ {presetApplied.removed.length} 列目标已删除</span>
                    )}
                    {presetApplied.added.length > 0 && (
                      <span style={{ color: 'var(--muted)' }}>· 🆕 {presetApplied.added.length} 新列</span>
                    )}
                    <a
                      href="#"
                      onClick={(e) => {
                        e.preventDefault();
                        if (parseRef.current) setMappings(inferMapping(parseRef.current.columns, cols));
                        deletePreset(connId, selSchema, selTable);
                        setPresetApplied(null);
                        setAppliedPreset(null);
                        setPresetSnapshotsRefresh((n) => n + 1);
                      }}
                      style={{ marginLeft: 8, fontSize: 10, cursor: 'pointer' }}
                    >清除并重置</a>
                    {appliedPreset && (
                      <a
                        href="#"
                        onClick={(e) => {
                          e.preventDefault();
                          setDiffOpen(true);
                          // 复用 diff 面板，把 preset vs current 的字段差异塞进临时快照
                          const snapshot = JSON.parse(JSON.stringify(opts)) as ImportOptions;
                          const presetOpts: ImportOptions = {
                            ...snapshot,
                            mode: appliedPreset.mode,
                            transforms: appliedPreset.transforms ?? {},
                            transformParams: appliedPreset.transformParams ?? {},
                            emptyAsNull: appliedPreset.emptyAsNull,
                            batchSize: appliedPreset.batchSize,
                            skipFailed: appliedPreset.skipFailed,
                            filterColumn: appliedPreset.filterColumn,
                            filterOp: appliedPreset.filterOp,
                            filterValue: appliedPreset.filterValue,
                            validations: appliedPreset.validations ?? {},
                            strictValidation: appliedPreset.strictValidation,
                            nullPolicies: appliedPreset.nullPolicies ?? {},
                          };
                          const entry: FixEntry = {
                            at: Date.now(),
                            snapshot: presetOpts,
                            icon: '📌',
                            label: `预设版本（${new Date(appliedPreset.updatedAt).toLocaleTimeString('zh-CN')}）`,
                            key: 'preset',
                            msg: '当前配置与预设版本的字段级差异（用于查看，撤销将回到预设版本）',
                          };
                          setUndoStack((s) => [entry, ...s].slice(0, MAX_FIX_HISTORY));
                          setBatchStepN(1);
                          publishEditorStatus({
                            message: `🔍 已把「预设版本 vs 当前配置」推入 diff 面板（${new Date(appliedPreset.updatedAt).toLocaleTimeString('zh-CN')}）`,
                            messageAt: Date.now(),
                          });
                        }}
                        style={{ marginLeft: 6, fontSize: 10, cursor: 'pointer', color: 'var(--warn, #d97706)' }}
                        title="把当前应用的预设版本 vs 用户手动改动的当前配置 推入 diff 面板（不撤销，仅查看差异）"
                      >↔ 对比当前</a>
                    )}
                    {(() => {
                      const key = presetKey(connId, selSchema, selTable);
                      const snaps = listSnapshots(key);
                      if (snaps.length === 0) return null;
                      const fmtDate = (t: number) => {
                        const d = new Date(t);
                        const pad = (n: number) => String(n).padStart(2, '0');
                        return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
                      };
                      return (
                        <details style={{ display: 'inline-block', marginLeft: 6, verticalAlign: 'top' }}>
                          <summary style={{ cursor: 'pointer', fontSize: 10, userSelect: 'none', listStyle: 'none' }} title="预设版本历史（每次覆盖前保留旧版本，最多 5 个）">
                            🕘 历史 {snaps.length}
                          </summary>
                          <div
                            style={{
                              position: 'absolute', zIndex: 30, marginTop: 2, marginLeft: -4,
                              width: 320, maxHeight: 260, overflowY: 'auto',
                              background: 'var(--panel, #1e1e2e)', border: '1px solid var(--border, #444)',
                              borderRadius: 3, padding: 4,
                              boxShadow: '0 4px 12px rgba(0,0,0,0.4)', fontSize: 11,
                            }}
                          >
                            {snaps.map((s, i) => (
                              <div
                                key={`${s.updatedAt}-${i}`}
                                style={{
                                  display: 'flex', alignItems: 'center', gap: 6,
                                  padding: '3px 6px', marginBottom: 2,
                                  borderRadius: 2, background: 'rgba(255,255,255,0.03)',
                                }}
                              >
                                <span style={{ flex: 1, fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)' }}>
                                  {s.mode} · {s.mappings.length} 列 · {fmtDate(s.updatedAt)}
                                </span>
                                <a
                                  href="#"
                                  onClick={(e) => {
                                    e.preventDefault();
                                    restoreSnapshot(key, i);
                                    const r = applyPreset(s, cols);
                                    setMappings(r.mappings);
                                    setOpts((o) => ({
                                      ...o,
                                      mode: r.mode,
                                      transforms: r.transforms,
                                      transformParams: r.transformParams,
                                      emptyAsNull: r.opts.emptyAsNull,
                                      batchSize: r.opts.batchSize,
                                      skipFailed: r.opts.skipFailed,
                                      filterColumn: r.opts.filterColumn,
                                      filterOp: r.opts.filterOp,
                                      filterValue: r.opts.filterValue,
                                      validations: r.opts.validations,
                                      strictValidation: r.opts.strictValidation,
                                      nullPolicies: r.opts.nullPolicies,
                                    }));
                                    setPresetApplied({
                                      matched: r.matchedCols,
                                      total: r.totalCols,
                                      removed: r.removedTargets.map((t) => ({ csvName: t.csvName, targetColumn: t.targetColumn })),
                                      added: r.addedTargets,
                                    });
                                    setPresetSnapshotsRefresh((n) => n + 1);
                                  }}
                                  style={{ color: 'var(--accent, #3b82f6)', fontSize: 10, cursor: 'pointer', whiteSpace: 'nowrap' }}
                                >恢复</a>
                              </div>
                            ))}
                          </div>
                        </details>
                      );
                    })()}
                  </span>
                )}
                {presetApplied && (presetApplied.removed.length > 0 || presetApplied.added.length > 0) && (
                  <details style={{ marginTop: 4 }} open={presetApplied.removed.length > 0}>
                    <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--muted)' }}>
                      📋 预设与当前表结构差异（点击展开）
                    </summary>
                    <div style={{ marginTop: 4, fontSize: 11 }}>
                      {presetApplied.removed.length > 0 && (
                        <div style={{ color: 'var(--warn, #d97706)', marginBottom: 4 }}>
                          <div style={{ fontWeight: 600, marginBottom: 2 }}>⚠ 以下预设目标列在目标表中已不存在（这些 CSV 列已置为「跳过」，请重新映射或忽略）：</div>
                          {presetApplied.removed.slice(0, 10).map((r, i) => (
                            <div key={i} style={{ fontFamily: 'monospace', paddingLeft: 12 }}>
                              CSV <code>{r.csvName}</code> → 目标 <code>{r.targetColumn}</code> 已消失
                            </div>
                          ))}
                          {presetApplied.removed.length > 10 && (
                            <div style={{ paddingLeft: 12 }}>… 共 {presetApplied.removed.length} 列</div>
                          )}
                        </div>
                      )}
                      {presetApplied.added.length > 0 && (
                        <div style={{ color: 'var(--muted)' }}>
                          <div style={{ fontWeight: 600, marginBottom: 2 }}>🆕 目标表新增列（预设里没见过，如需映射请手动选择）：</div>
                          <div style={{ fontFamily: 'monospace', paddingLeft: 12 }}>
                            {presetApplied.added.slice(0, 20).join(', ')}
                            {presetApplied.added.length > 20 && `… 共 ${presetApplied.added.length} 列`}
                          </div>
                        </div>
                      )}
                    </div>
                  </details>
                )}
              </div>
              <div style={styles.footer}>
                <button onClick={() => setStep('table')} style={styles.btnGhost}>← 上一步</button>
                <span style={styles.spacer} />
                <button onClick={() => setStep('preview')} disabled={mappedCount === 0} title={mappedCount === 0 ? '请至少映射一列' : ''} style={styles.btnPrimary}>下一步 →</button>
              </div>
            </div>
  );
}