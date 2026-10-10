import { styles } from '../importStyles';
import type { Step } from './types';
import { STEP_LABEL, STEP_ORDER } from './types';

// M30.51 向导头部步骤徽章：可点击跳步（回跳恒可；前进按各步前置守卫），done 恒禁
export function StepBar({ step, curStepIdx, stepWarnFlags, stepFlash, parse, selTable, mappedCount, setStep }: {
  step: Step;
  curStepIdx: number;
  stepWarnFlags: Partial<Record<Step, boolean>>;
  stepFlash: boolean;
  parse: unknown;
  selTable: unknown;
  mappedCount: number;
  setStep: (s: Step) => void;
}) {
  return (
    <div style={styles.stepBar}>
      {STEP_ORDER.map((s, i) => {
        const isCurrent = step === s;
        const isDone = i < curStepIdx || (step === 'done' && s !== 'done');
        const hasWarn = !isCurrent && !isDone && stepWarnFlags[s];
        const icon = isCurrent
          ? (hasWarn ? '⚠' : '⏳')
          : isDone
            ? (stepWarnFlags[s] ? '⚠' : '✓')
            : hasWarn
              ? '⚠'
              : '·';
        const iconColor = isCurrent
          ? (hasWarn ? 'var(--warning, #f59e0b)' : 'var(--accent, #3b82f6)')
          : isDone
            ? (stepWarnFlags[s] ? 'var(--warning, #f59e0b)' : 'var(--success, #10b981)')
            : hasWarn
              ? 'var(--warning, #f59e0b)'
              : 'var(--muted)';
        const iconTitle = isCurrent
          ? (hasWarn ? '当前步骤（有警告需处理）' : '当前步骤')
          : isDone
            ? (stepWarnFlags[s] ? '已完成（含警告）' : '已完成')
            : hasWarn
              ? '尚未开始（有警告信号）'
              : '尚未开始';
        // 前置数据守卫：跳步必须满足前置
        let canNav = false;
        let navTitle: string;
        if (s === 'done') {
          canNav = false;
          navTitle = '完成步只可通过执行导入进入';
        } else if (i < curStepIdx) {
          canNav = true;
          navTitle = `回跳「${STEP_LABEL[s]}」`;
        } else if (i > curStepIdx) {
          if (i === 1) { canNav = !!parse; navTitle = parse ? `前进「${STEP_LABEL[s]}」` : '需先解析数据'; }
          else if (i === 2) { canNav = !!parse && !!selTable; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需先选目标表'; }
          else if (i === 3) { canNav = !!parse && !!selTable && mappedCount > 0; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需至少映射一列'; }
          else { canNav = false; navTitle = '不可前进'; }
        } else {
          canNav = false;
          navTitle = '当前步骤';
        }
        return (
          <div
            key={s}
            className={isCurrent && stepFlash ? 'step-badge-flash' : undefined}
            style={{
              ...styles.stepItem,
              ...(step === s ? styles.stepItemActive : {}),
              ...(i < curStepIdx ? styles.stepItemDone : {}),
              display: 'flex', alignItems: 'center', gap: 4,
              cursor: canNav ? 'pointer' : 'default',
              opacity: canNav ? 1 : 0.7,
              userSelect: 'none',
            }}
            title={canNav ? `${navTitle}（${iconTitle}；Ctrl+Alt+${i + 1}）` : `${iconTitle}（${navTitle}）`}
            role="button"
            aria-disabled={!canNav}
            onClick={() => {
              if (!canNav || isCurrent) return;
              setStep(s);
            }}
          >
            <span style={{ color: iconColor, fontWeight: 700, fontSize: 10, flexShrink: 0, minWidth: 10, textAlign: 'center' }}>{icon}</span>
            <span>{STEP_LABEL[s]}</span>
          </div>
        );
      })}
      {/* M30.96 向导头部键盘提示徽标：绝对定位 Ctrl+Alt+1..4 与相对遍历 Ctrl+Alt+[ ] */}
      <button
        style={{
          marginLeft: 'auto',
          padding: '0 6px',
          background: 'transparent',
          border: '1px solid var(--border)',
          borderRadius: 3,
          color: 'var(--muted)',
          cursor: 'help',
          fontSize: 10,
          flexShrink: 0,
          lineHeight: '16px',
          display: 'flex',
          alignItems: 'center',
          gap: 2,
        }}
        title={
          [
            '向导键盘快捷键：',
            'Ctrl+Alt+1 → 粘贴或选择数据',
            'Ctrl+Alt+2 → 目标表 & 模式',
            'Ctrl+Alt+3 → 列映射 & 转换',
            'Ctrl+Alt+4 → 预览 & 执行',
            'Ctrl+Alt+[  → 上一步',
            'Ctrl+Alt+]  → 下一步',
            'Ctrl+Enter → Step 4 提交导入',
            'Ctrl+/    → 快捷键速查（全部）',
            '完成步只可通过执行导入进入',
          ].join('\n')
        }
        aria-label="显示向导键盘快捷键"
      >⌨</button>
    </div>
  );
}
