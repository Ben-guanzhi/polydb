import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import {
  formatValue,
  typeBadgeColor,
  qualityScoreColor,
  colDtypeColor,
  toMsg,
  formatTime,
  historyStatusBadge,
  AuditBadge,
  StepBar,
  STEP_LABEL,
  type Step,
} from './ImportModalParts';

describe('formatValue', () => {
  it('formats scalar and container values', () => {
    expect(formatValue(null)).toBe('NULL');
    expect(formatValue('a\'b')).toBe("'a''b'");
    expect(formatValue(true)).toBe('true');
    expect(formatValue(42)).toBe('42');
    expect(formatValue([1, null, 'x'])).toBe("[1, NULL, 'x']");
    expect(formatValue({ a: 1 })).toBe('{"a":1}');
  });
});

describe('typeBadgeColor', () => {
  it('maps every badge type to a color', () => {
    const types = ['string', 'integer', 'float', 'boolean', 'date', 'null'] as const;
    for (const t of types) {
      expect(typeBadgeColor(t)).toBeTruthy();
    }
    expect(typeBadgeColor('integer')).toBe('#3b82f6');
    expect(typeBadgeColor('date')).toBe('#eab308');
  });
});

describe('qualityScoreColor', () => {
  it('thresholds at 80/60/40', () => {
    expect(qualityScoreColor(95)).toBe('var(--ok, #10b981)');
    expect(qualityScoreColor(70)).toBe('#eab308');
    expect(qualityScoreColor(50)).toBe('var(--warn, #d97706)');
    expect(qualityScoreColor(10)).toBe('var(--danger, #dc2626)');
  });
});

describe('colDtypeColor', () => {
  it('classifies dtype keywords into color buckets', () => {
    expect(colDtypeColor('INTEGER')).toBe('#3b82f6');
    expect(colDtypeColor('double precision')).toBe('#06b6d4');
    expect(colDtypeColor('boolean')).toBe('#10b981');
    expect(colDtypeColor('timestamp')).toBe('#eab308');
    expect(colDtypeColor('varchar(64)')).toBe('var(--accent, #3b82f6)');
    expect(colDtypeColor('bytea')).toBe('var(--muted)');
    expect(colDtypeColor('mystery_type')).toBe('var(--muted)');
  });
});

describe('toMsg', () => {
  it('unwraps errors and falls back to String', () => {
    expect(toMsg(new Error('boom'))).toBe('boom');
    expect(toMsg('plain')).toBe('plain');
    expect(toMsg(undefined)).toBe('undefined');
  });
});

describe('formatTime', () => {
  it('formats local datetime with zero padding', () => {
    const at = new Date(2026, 0, 2, 3, 4, 5).getTime();
    expect(formatTime(at)).toBe('2026-01-02 03:04');
  });
});

describe('historyStatusBadge', () => {
  it('maps all four statuses', () => {
    expect(historyStatusBadge('success')).toBe('✓ 成功');
    expect(historyStatusBadge('partial')).toBe('◐ 部分');
    expect(historyStatusBadge('failed')).toBe('✕ 失败');
    expect(historyStatusBadge('cancelled')).toBe('⏹ 取消');
  });
});

describe('AuditBadge', () => {
  it('renders ok status with label and reason', () => {
    render(<AuditBadge a={{ status: 'ok', reason: '全部列存在' }} cachedAtMs={null} age={null} label="目标预设" />);
    expect(screen.getByText('✅ 目标预设：正常')).toBeTruthy();
    expect(screen.getByText('· 全部列存在')).toBeTruthy();
  });

  it('shows stale hint when cache is older than TTL half', () => {
    const old = Date.now() - 13 * 60 * 60 * 1000;
    render(<AuditBadge a={{ status: 'warn', reason: '缺 1 列' }} cachedAtMs={old} age="12h" label="归档预设" />);
    expect(screen.getByText('⚠️ stale')).toBeTruthy();
    expect(screen.getByText('⚠ 归档预设：部分列缺失')).toBeTruthy();
  });
});

describe('StepBar', () => {
  const base = {
    curStepIdx: 2, // 当前在 Step 3（map，index 2）
    stepWarnFlags: {} as Record<string, boolean>,
    stepFlash: false,
    parse: {} as unknown,
    selTable: 't' as unknown,
    mappedCount: 2,
  };

  it('renders all five step labels', () => {
    render(<StepBar step="map" {...base} setStep={() => {}} />);
    const all: Step[] = ['input', 'table', 'map', 'preview', 'done'];
    for (const s of all) {
      expect(screen.getByText(STEP_LABEL[s])).toBeTruthy();
    }
  });

  it('allows jumping back and blocks done step', () => {
    const setStep = vi.fn();
    render(<StepBar step="map" {...base} setStep={setStep} />);
    fireEvent.click(screen.getByText(STEP_LABEL['input']));
    expect(setStep).toHaveBeenCalledWith('input');
    setStep.mockClear();
    fireEvent.click(screen.getByText(STEP_LABEL['done']));
    expect(setStep).not.toHaveBeenCalled();
  });

  it('guards forward jump to Step 4 on mappedCount', () => {
    const setStep = vi.fn();
    render(<StepBar step="map" {...base} mappedCount={0} setStep={setStep} />);
    fireEvent.click(screen.getByText(STEP_LABEL['preview']));
    expect(setStep).not.toHaveBeenCalled();
  });

  it('allows forward jump to Step 4 when mappedCount > 0', () => {
    const setStep = vi.fn();
    render(<StepBar step="map" {...base} setStep={setStep} />);
    fireEvent.click(screen.getByText(STEP_LABEL['preview']));
    expect(setStep).toHaveBeenCalledWith('preview');
  });
});
