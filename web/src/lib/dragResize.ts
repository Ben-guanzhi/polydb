import type { MouseEvent as ReactMouseEvent } from 'react';

export type DragAxis = 'x' | 'y';

export interface DragResizeOptions {
  axis?: DragAxis;
  onResize: (delta: number, clientX: number, clientY: number) => void;
  onEnd?: (finalX: number, finalY: number) => void;
  cursor?: 'col-resize' | 'row-resize' | 'ew-resize' | 'ns-resize';
}

export function startDragResize(e: ReactMouseEvent, opts: DragResizeOptions) {
  e.preventDefault();
  const axis: DragAxis = opts.axis ?? 'x';
  const start = axis === 'y' ? e.clientY : e.clientX;
  const startCursor = document.body.style.cursor;
  if (opts.cursor) document.body.style.cursor = opts.cursor;
  const prevUserSelect = document.body.style.userSelect;
  document.body.style.userSelect = 'none';

  let lastX = e.clientX;
  let lastY = e.clientY;
  const onMove = (ev: MouseEvent) => {
    lastX = ev.clientX;
    lastY = ev.clientY;
    const pos = axis === 'y' ? ev.clientY : ev.clientX;
    opts.onResize(pos - start, ev.clientX, ev.clientY);
  };
  const onUp = () => {
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
    document.body.style.cursor = startCursor;
    document.body.style.userSelect = prevUserSelect;
    opts.onEnd?.(lastX, lastY);
  };
  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}
