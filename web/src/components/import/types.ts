// 向导公共类型与常量（原 ImportModalParts.tsx 头部）：各步视图从此导入，
// 对外经 ../ImportModalParts.tsx 原样再导出，既有导入路径不变。
import type { ImportOptions } from '../../lib/importData';
import type { ImportPreset } from '../../lib/importPresets';

// Step 3（列映射 & 转换）专用类型/常量（原 ImportModal 组件体内，现上移为模块级供 Step3Map 复用）
export type MappingFilter = 'all' | 'mapped' | 'unmapped' | 'low-health' | 'critical-health';
export type MappingSort = 'index' | 'health-asc' | 'empty-desc' | 'target-asc' | 'csv-asc';
export interface FixEntry { key: string; label: string; icon: string; snapshot: ImportOptions; msg: string; at: number; }
export const MAX_FIX_HISTORY = 20;

// diff 面板搜索字段作用域（M30.33）：all=全部字段，field/before/after 只匹配对应字段
export type DiffSearchScope = 'all' | 'field' | 'before' | 'after';
// 导入失败行（原 ImportModal 局部 interface，上移供 Step4Preview 复用）
export interface FailedRow { csvRow: number; reason: string; preview: string; stmtIdx: number; }

// M30.159 D 备份应用后变更清单：按分组记录本次 apply 的项 key，供 banner 数字点击展开明细浮层
export interface BackupAppliedDetail {
  presetsAdded: string[];
  presetsOverwritten: string[];
  snapshotsAdded: string[];
  snapshotsOverwritten: string[];
  skipped: string[];
  elapsedMs: number;
  at: number;
}

// 删除预设的恢复快照（原 ImportModal 局部 state 形状）
export interface DelPresetRestoreState {
  preset: ImportPreset;
  snapshots: ImportPreset[];
  at: number;
  expiresAt: number;
  connId: string;
  schema: string;
  table: string;
}

export type Step = 'input' | 'table' | 'map' | 'preview' | 'done';


export const STEP_LABEL: Record<Step, string> = {
  input: '1 · 粘贴或选择数据',
  table: '2 · 目标表 & 模式',
  map: '3 · 列映射 & 转换',
  preview: '4 · 预览 & 执行',
  done: '5 · 完成',
};
export const STEP_ORDER: Step[] = ['input', 'table', 'map', 'preview', 'done'];