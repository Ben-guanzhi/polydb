/**
 * Virtual windowing for the result grid (波 100：渲染层极致轻量化).
 *
 * The grid renders O(viewport) DOM; the scroller's scrollHeight stays honest
 * through two spacer rows. This is TableProGo's `datagrid/virtual.ts` recipe
 * (itself ported from db-dev's mountVirtualGrid), fitted to polydb's grid:
 *
 *  - rows carry their `origIdx` (the identity selection, edits and exports
 *    key on), so the window is a slice of the DISPLAY order and the slice's
 *    position i is the display row number;
 *  - the default (no-wrap) grid has a fixed row height — 12px font + 8px
 *    vertical padding + 2px borders ≈ 27px — which is what makes the
 *    arithmetic exact;
 *  - wrap mode changes row heights, so windowing there would lie: the caller
 *    falls back to the incremental render-limit behaviour, which never
 *    misplaces a row but does accumulate DOM.
 *
 * Contracts (mirrors virtual_grid_test.js / TableProGo's virtual.test.ts):
 *  - window span is independent of `total` (10k rows and 100k rows scroll the
 *    same);
 *  - the window is clamped to [0, total-1] and never inverted;
 *  - topPad + rendered rows + bottomPad = total * rowH, so the scrollbar
 *    stays honest.
 */

/** Default row height in px: 12px font + 8px vertical padding + 2px borders. */
export const ROW_HEIGHT = 27;

/** Rows rendered beyond the viewport on each side (smoothing + scrollbar). */
export const OVERSCAN = 8;

export interface VisibleRange {
  /** First display-row index to render (inclusive), clamped to [0, total-1]. */
  i0: number;
  /** Last display-row index to render (inclusive), clamped to [0, total-1]. */
  i1: number;
  /** Height of the spacer ABOVE the window: i0 * rowH. */
  topPad: number;
  /** Height of the spacer BELOW the window: (total-1-i1) * rowH. */
  bottomPad: number;
}

/**
 * Pure window arithmetic. `headerH` is the sticky thead height; when it is 0
 * the scroller's content starts at scrollTop 0, so no offset is subtracted.
 * Non-integer scroll positions are floored — windows are row indices.
 */
export function computeVisibleRange(
  scrollTop: number,
  viewportH: number,
  headerH: number,
  total: number,
  rowH = ROW_HEIGHT,
  overscan = OVERSCAN,
): VisibleRange {
  if (total <= 0) {
    return { i0: 0, i1: -1, topPad: 0, bottomPad: 0 };
  }
  const scrollable = Math.max(0, scrollTop - headerH);
  const first = Math.floor(scrollable / rowH) - overscan;
  const last = Math.floor((scrollable + viewportH) / rowH) + overscan;
  // Clamp FIRST to the valid index range, then widen: clamping i0 before i1
  // would let an out-of-range scrollTop (stale state after a result swap)
  // produce first > total-1 and an inverted window.
  const i0 = Math.min(total - 1, Math.max(0, first));
  const i1 = Math.min(total - 1, Math.max(i0, last));
  return {
    i0,
    i1,
    topPad: i0 * rowH,
    bottomPad: (total - 1 - i1) * rowH,
  };
}
