import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { JsonResultView, MessagesView, RowInspector } from './ResultViewPanels';
import { INSPECTOR_VALUE_MAX, type InspectorField, type StatementMessage } from '../lib/resultViews';

describe('JsonResultView', () => {
  it('显示对象数与可见/总列数，复制与下载各走自己的回调', () => {
    const onCopy = vi.fn();
    const onDownload = vi.fn();
    const { container } = render(
      <JsonResultView
        text={'[\n  {\n    "id": 1\n  }\n]'}
        objectCount={7}
        visibleColCount={2}
        totalColCount={5}
        onCopy={onCopy}
        onDownload={onDownload}
      />,
    );
    expect(screen.getByText(/7 个对象 · 2\/5 列/)).toBeTruthy();
    expect(screen.getByText(/跟随当前排序、过滤与隐藏列/)).toBeTruthy();
    expect(container.querySelector('pre')?.textContent).toBe('[\n  {\n    "id": 1\n  }\n]');
    fireEvent.click(screen.getByRole('button', { name: '复制 JSON' }));
    fireEvent.click(screen.getByRole('button', { name: '下载' }));
    expect(onCopy).toHaveBeenCalledTimes(1);
    expect(onDownload).toHaveBeenCalledTimes(1);
  });
});

const msg = (over: Partial<StatementMessage> = {}): StatementMessage => ({
  status: 'ok',
  title: 'SELECT',
  lines: ['✓ select 返回 2 行 · 2 列'],
  sql: 'select * from t',
  active: false,
  ...over,
});

describe('MessagesView', () => {
  it('无执行记录时给空态', () => {
    render(<MessagesView items={[]} pickable={false} onPick={vi.fn()} />);
    expect(screen.getByText('无执行记录。')).toBeTruthy();
  });

  it('逐条呈现标题、消息行与 SQL 摘要，多语句不可点击时不回调', () => {
    const onPick = vi.fn();
    render(
      <MessagesView
        items={[msg(), msg({ status: 'err', title: 'INSERT', lines: ['✕ no such table: nope'], sql: 'insert into nope values (1)' })]}
        pickable={false}
        onPick={onPick}
      />,
    );
    expect(screen.getByText('SELECT')).toBeTruthy();
    expect(screen.getByText('✓ select 返回 2 行 · 2 列')).toBeTruthy();
    expect(screen.getByText('✕ no such table: nope')).toBeTruthy();
    expect(screen.getByText('insert into nope values (1)')).toBeTruthy();
    fireEvent.click(screen.getByText('INSERT'));
    expect(onPick).not.toHaveBeenCalled();
  });

  it('pickable 时点击条目按序号回传，仅 active 条带强调底色', () => {
    const onPick = vi.fn();
    render(
      <MessagesView
        items={[msg({ active: true }), msg({ title: 'DROP', sql: 'drop table t' })]}
        pickable
        onPick={onPick}
      />,
    );
    fireEvent.click(screen.getByText('DROP'));
    expect(onPick).toHaveBeenCalledWith(1);
    fireEvent.click(screen.getByText('SELECT'));
    expect(onPick).toHaveBeenCalledWith(0);
  });
});

const field = (over: Partial<InspectorField> = {}): InspectorField => ({
  col: 0,
  name: 'id',
  type: 'integer',
  display: '7',
  isNull: false,
  truncated: false,
  ...over,
});

describe('RowInspector', () => {
  it('头部报行号与字段数，复制行与收起各走回调', () => {
    const onCopyRow = vi.fn();
    const onClose = vi.fn();
    render(
      <RowInspector
        rowIdx={2}
        fields={[field(), field({ col: 1, name: 'name', type: 'varchar', display: 'NULL', isNull: true })]}
        editable={() => false}
        onPreview={vi.fn()}
        onEdit={vi.fn()}
        onCopyRow={onCopyRow}
        onClose={onClose}
      />,
    );
    expect(screen.getByText('行详情 · 第 3 行')).toBeTruthy();
    expect(screen.getByText('2 字段')).toBeTruthy();
    expect(screen.getByText('NULL')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '复制行 JSON' }));
    fireEvent.click(screen.getByRole('button', { name: '×' }));
    expect(onCopyRow).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('超长值裁断加省略号，只有可编辑字段出「编辑」按钮', () => {
    const onEdit = vi.fn();
    const long = 'z'.repeat(INSPECTOR_VALUE_MAX + 5);
    render(
      <RowInspector
        rowIdx={0}
        fields={[
          field({ col: 0, name: 'id' }),
          field({ col: 1, name: 'big', type: 'text', display: long, truncated: true }),
        ]}
        editable={(col) => col === 0}
        onPreview={vi.fn()}
        onEdit={onEdit}
        onCopyRow={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText(long.slice(0, INSPECTOR_VALUE_MAX) + '…')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '编辑' }));
    expect(onEdit).toHaveBeenCalledWith(0);
    expect(screen.getAllByRole('button', { name: '完整值' })).toHaveLength(2);
  });
});
