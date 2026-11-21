import { describe, it, expect } from 'vitest';

import { computeVisibleRange, ROW_HEIGHT, OVERSCAN } from './gridVirtual';

// 波 100：虚拟窗口的算术契约——与 TableProGo 的 virtual.test.ts、db-dev 的
// virtual_grid_test.js 同一套约定（窗口跨度与总量无关、钳制不倒置、spacer
// 撑住 scrollHeight）。

describe('computeVisibleRange', () => {
  it('renders at most the viewport plus overscan on each side', () => {
    // 10000 rows, a viewport 20 rows tall, scrolled 10 rows in. floor() on
    // both ends adds at most one row of rounding slop.
    const total = 10000;
    const r = computeVisibleRange(ROW_HEIGHT * 10, ROW_HEIGHT * 20, 0, total);
    const span = r.i1 - r.i0 + 1;
    expect(span).toBeGreaterThanOrEqual(20);
    expect(span).toBeLessThanOrEqual(20 + OVERSCAN * 2 + 1);
    expect(r.i0).toBeGreaterThanOrEqual(10 - OVERSCAN - 1);
    expect(r.i1).toBeLessThanOrEqual(10 + 20 + OVERSCAN + 1);
  });

  it('window span does not grow with total (10k vs 100k scroll the same)', () => {
    const a = computeVisibleRange(ROW_HEIGHT * 500, ROW_HEIGHT * 20, 0, 10000);
    const b = computeVisibleRange(ROW_HEIGHT * 500, ROW_HEIGHT * 20, 0, 100000);
    expect(a.i1 - a.i0).toBe(b.i1 - b.i0);
    expect(a.i0).toBe(b.i0);
  });

  it('clamps to the valid range and never inverts', () => {
    // A stale scrollTop far past the end (a result swap left it behind).
    const r = computeVisibleRange(ROW_HEIGHT * 99999, ROW_HEIGHT * 20, 0, 50);
    expect(r.i0).toBe(49);
    expect(r.i1).toBe(49);
    expect(r.i0).toBeLessThanOrEqual(r.i1);
  });

  it('top at scrollTop 0 renders from row 0 (overscan clamped to 0)', () => {
    const r = computeVisibleRange(0, ROW_HEIGHT * 20, 0, 1000);
    expect(r.i0).toBe(0);
    expect(r.topPad).toBe(0);
  });

  it('spacers plus rendered rows keep the scrollbar honest', () => {
    const total = 3000;
    const r = computeVisibleRange(ROW_HEIGHT * 137, ROW_HEIGHT * 20, 0, total);
    const rendered = r.i1 - r.i0 + 1;
    // The three pieces tile the whole virtual height (overscan rows overlap
    // the spacers by design, so the sum is exact: topPad + window + bottomPad
    // covers [0, total) with the window counted once).
    expect(r.topPad + rendered * ROW_HEIGHT + r.bottomPad).toBe(total * ROW_HEIGHT);
  });

  it('empty results produce an empty, zero-padded window', () => {
    const r = computeVisibleRange(100, 500, 0, 0);
    expect(r.i0).toBe(0);
    expect(r.i1).toBe(-1);
    expect(r.topPad).toBe(0);
    expect(r.bottomPad).toBe(0);
  });

  it('a sticky header shifts the window, not the count', () => {
    const headerH = 30;
    const withHeader = computeVisibleRange(headerH + ROW_HEIGHT * 10, ROW_HEIGHT * 20, headerH, 10000);
    const noHeader = computeVisibleRange(ROW_HEIGHT * 10, ROW_HEIGHT * 20, 0, 10000);
    expect(withHeader.i0).toBe(noHeader.i0);
    expect(withHeader.i1).toBe(noHeader.i1);
  });
});
