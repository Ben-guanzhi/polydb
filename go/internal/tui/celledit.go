// Package tui celledit holds the commit helpers for single-cell edits and
// row deletes. In database mode it builds and runs UPDATE/DELETE statements
// keyed on the table's primary key; dialect quoting comes from pkg/changes
// (AGENTS.md §12：SQL 生成不散落在 UI 层）。
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
)

// PendingEdit captures an in-flight cell edit awaiting confirmation. The
// popup that collects the new value populates this and the app commits it
// via the "saveedit" command.
type PendingEdit struct {
	Frame     *tuidata.Frame
	Row       int
	Col       int
	ColName   string
	OldValue  string
	NewValue  string
	Table     string
	Namespace string
}

// PendingDelete captures one or more rows awaiting deletion. The app populates
// it from the multi-select set (or the single cursor row) and commits it via
// the "deleterows" command.
type PendingDelete struct {
	Frame     *tuidata.Frame
	Rows      []int
	Table     string
	Namespace string
}

// editsCommittedMsg 报告整批提交的结果（成功执行了多少条语句）。
type editsCommittedMsg struct{ stmts int }

// commitPendingEdits 把当前标签队列里的编辑整批提交：一个事务、要么全成要么
// 全回滚。成功后由调用方刷新整表。
func commitPendingEdits(ctx AppContext, ns, table string) tea.Cmd {
	be := ctx.Backend()
	if be == nil {
		return func() tea.Msg { return ErrorMsg{Err: fmt.Errorf("no backend, cannot commit edits")} }
	}
	q := ctx.EditQueue()
	if q == nil || q.Size() == 0 {
		return func() tea.Msg { return ErrorMsg{Err: fmt.Errorf("没有待提交的编辑")} }
	}
	return func() tea.Msg {
		n, err := commitQueue(be, ns, table, "", q)
		if err != nil {
			return ErrorMsg{Err: err}
		}
		return editsCommittedMsg{stmts: n}
	}
}

// cellSavedMsg carries the outcome of an async UPDATE back to the app.
type cellSavedMsg struct {
	rows int64
	err  error
}

// rowsDeletedMsg carries the outcome of an async DELETE back to the app.
type rowsDeletedMsg struct {
	rows int64
	err  error
}

// quoteIdent quotes a SQL identifier for the backend's dialect（委托 pkg/changes）。
func quoteIdent(kind protocol.DatabaseKind, name string) string {
	return changes.QuoteIdent(kind, name)
}

// sqlLiteral wraps a string value as a SQL literal with the dialect's own
// escaping（委托 pkg/changes；MySQL 的反斜杠翻倍由 QuoteString 按 kind 处理）。
func sqlLiteral(kind protocol.DatabaseKind, s string) string {
	return changes.QuoteString(kind, s)
}

// buildUpdate returns a single-row UPDATE keyed on the table's primary key.
// The table identifier is dialect-quoted; PK values are read from frame. It
// errors when a named PK column is absent from the frame.
func buildUpdate(kind protocol.DatabaseKind, table string, pks []string, frame *tuidata.Frame, row int, colName, val string) (string, error) {
	setClause := quoteIdent(kind, colName) + "=" + sqlLiteral(kind, val)
	var where []string
	for _, pk := range pks {
		idx := frame.ColumnIndex(pk)
		if idx < 0 {
			return "", fmt.Errorf("primary key column not found: %s", pk)
		}
		where = append(where, quoteIdent(kind, pk)+"="+sqlLiteral(kind, frame.CellString(row, idx)))
	}
	return "UPDATE " + quoteIdent(kind, table) + " SET " + setClause + " WHERE " + strings.Join(where, " AND "), nil
}

// buildDelete returns a DELETE keyed on the table's primary key. Single-PK
// tables produce a "WHERE pk IN (...)" clause; multi-PK tables produce a
// "WHERE (pk0=v AND pk1=v) OR (...)" clause. It errors when there are no PKs
// (the caller checks len(pks)==0 first) or a PK column is missing.
func buildDelete(kind protocol.DatabaseKind, table string, pks []string, frame *tuidata.Frame, rows []int) (string, error) {
	if len(pks) == 0 {
		return "", fmt.Errorf("no primary key")
	}
	pkIdx := make([]int, len(pks))
	for i, pk := range pks {
		idx := frame.ColumnIndex(pk)
		if idx < 0 {
			return "", fmt.Errorf("primary key column not found: %s", pk)
		}
		pkIdx[i] = idx
	}
	tbl := quoteIdent(kind, table)
	if len(pks) == 1 {
		vals := make([]string, 0, len(rows))
		for _, r := range rows {
			vals = append(vals, sqlLiteral(kind, frame.CellString(r, pkIdx[0])))
		}
		return "DELETE FROM " + tbl + " WHERE " + quoteIdent(kind, pks[0]) + " IN (" + strings.Join(vals, ",") + ")", nil
	}
	ors := make([]string, 0, len(rows))
	for _, r := range rows {
		ands := make([]string, 0, len(pks))
		for i, pk := range pks {
			ands = append(ands, quoteIdent(kind, pk)+"="+sqlLiteral(kind, frame.CellString(r, pkIdx[i])))
		}
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "DELETE FROM " + tbl + " WHERE " + strings.Join(ors, " OR "), nil
}

// commitCellEdit runs the edit described by pe: it issues a PK-keyed UPDATE
// and returns a command producing cellSavedMsg.
func commitCellEdit(ctx AppContext, pe PendingEdit) tea.Cmd {
	be := ctx.Backend()
	if be == nil {
		return func() tea.Msg { return ErrorMsg{Err: fmt.Errorf("no backend, cannot edit")} }
	}
	kind := protocol.DatabaseKind(be.Kind())
	pks, err := be.PrimaryKeys(pe.Namespace, pe.Table)
	if err != nil {
		e := err
		return func() tea.Msg { return ErrorMsg{Err: e} }
	}
	if len(pks) == 0 {
		return func() tea.Msg { return ErrorMsg{Err: fmt.Errorf("no primary key, cannot edit")} }
	}
	stmt, err := buildUpdate(kind, pe.Table, pks, pe.Frame, pe.Row, pe.ColName, pe.NewValue)
	if err != nil {
		e := err
		return func() tea.Msg { return ErrorMsg{Err: e} }
	}
	return func() tea.Msg {
		res, err := runInTx(be, stmt)
		if err != nil {
			return cellSavedMsg{err: err}
		}
		n := int64(0)
		if res.Exec != nil {
			n = res.Exec.RowsAffected
		}
		return cellSavedMsg{rows: n}
	}
}

// enqueueCellEdit 把一次编辑放进当前标签的待提交队列，而不是立刻执行。
//
// 队列模型下本地帧不做乐观改写：待提交的编辑由 Review（队列）呈现，提交成功
// 后整表刷新，用户看到的就是库里的真实值。这样既不用维护「本地改了但库里没改」
// 的双重真相，也不会出现刷新把未提交编辑冲掉的问题。
func enqueueCellEdit(ctx AppContext, pe PendingEdit) (*changes.Queue, error) {
	be := ctx.Backend()
	if be == nil {
		return nil, fmt.Errorf("no backend, cannot edit")
	}
	q := ctx.EditQueue()
	if q == nil {
		return nil, fmt.Errorf("no active pane, cannot queue edit")
	}
	pks, err := be.PrimaryKeys(pe.Namespace, pe.Table)
	if err != nil {
		return nil, err
	}
	if len(pks) == 0 {
		return nil, fmt.Errorf("no primary key, cannot edit")
	}
	rowID := pkRowID(pe.Frame, pe.Row, pks)
	if rowID == "" {
		return nil, fmt.Errorf("cannot identify the edited row (primary key missing/empty)")
	}
	q.AddCellChange(rowID, pe.ColName,
		protocol.NewStringValue(pe.OldValue),
		protocol.NewStringValue(pe.NewValue),
		changesRow(pe.Frame, pe.Row))
	return q, nil
}

// pkRowID 用主键列的值拼出行标识；复合主键用 US 分隔，避免值本身含逗号时歧义。
func pkRowID(f *tuidata.Frame, row int, pks []string) string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, len(pks))
	for _, pk := range pks {
		i := f.ColumnIndex(pk)
		if i < 0 {
			return ""
		}
		parts = append(parts, f.CellString(row, i))
	}
	return strings.Join(parts, "\x1f")
}

// changesRow 把结果集的一行转成 changes.Row（提交时定位原始行用）。
func changesRow(f *tuidata.Frame, row int) changes.Row {
	if f == nil || row < 0 || row >= f.NumRows() {
		return nil
	}
	cols := f.ColumnNames()
	out := make(changes.Row, 0, len(cols))
	for i, c := range cols {
		out = append(out, changes.Cell{Column: c, Value: protocol.NewStringValue(f.CellString(row, i))})
	}
	return out
}

// commitQueue 把队列里累积的编辑整批提交：语句由 pkg/changes 生成（SQL 生成
// 不散落在 UI 层，AGENTS.md §12），再在一个事务里依次跑完 —— 要么全成、
// 要么全回滚，不会留下半数生效的编辑。
//
// 返回实际执行的语句数。后端不支持事务时逐条执行并沿用原来的非原子语义：
// 不支持不是错误，绝不让提交功能挂掉（失败的那条会中断后续，队列不清空）。
func commitQueue(be backend.Backend, ns, table, schema string, q *changes.Queue) (int, error) {
	if q == nil || q.Size() == 0 {
		return 0, nil
	}
	pks, err := be.PrimaryKeys(ns, table)
	if err != nil {
		return 0, err
	}
	if len(pks) == 0 {
		return 0, fmt.Errorf("no primary key, cannot commit edits")
	}
	kind := protocol.DatabaseKind(be.Kind())
	stmts := changes.BuildStatements(q.All(), schema, table, pks, kind)
	if len(stmts) == 0 {
		return 0, nil
	}

	tx, ok := be.(backend.TxRunner)
	if !ok {
		for _, s := range stmts {
			// Statement.SQL 的字面量已内联（Review 预览同款）；参数化版本是
			// ParamSQL，等 ExecuteInTx 支持参数后再切过去。
			if _, err := be.Run(s.SQL); err != nil {
				return 0, err
			}
		}
		q.Clear()
		return len(stmts), nil
	}

	id, err := tx.Begin()
	if err != nil {
		return 0, err
	}
	for _, s := range stmts {
		if _, err := tx.ExecuteInTx(id, s.SQL); err != nil {
			_ = tx.Rollback(id)
			return 0, err
		}
	}
	if err := tx.Commit(id); err != nil {
		_ = tx.Rollback(id)
		return 0, err
	}
	q.Clear()
	return len(stmts), nil
}

// runInTx 在事务里跑一条写语句：成功才提交，任一步失败都回滚，保证编辑是
// 原子的（要么全成、要么全不改），而不是半途生效。
//
// 后端不支持事务时退回直接执行（be.Run）—— 不支持不是错误，只是退化成
// 原来的非原子行为。
func runInTx(be backend.Backend, stmt string) (backend.Result, error) {
	tx, ok := be.(backend.TxRunner)
	if !ok {
		return be.Run(stmt)
	}
	id, err := tx.Begin()
	if err != nil {
		return backend.Result{}, err
	}
	res, err := tx.ExecuteInTx(id, stmt)
	if err != nil {
		_ = tx.Rollback(id)
		return backend.Result{}, err
	}
	if err := tx.Commit(id); err != nil {
		_ = tx.Rollback(id)
		return backend.Result{}, err
	}
	return res, nil
}

// commitRowDelete runs the delete described by pd: it issues a PK-keyed
// DELETE and returns a command producing rowsDeletedMsg.
func commitRowDelete(ctx AppContext, pd PendingDelete) tea.Cmd {
	be := ctx.Backend()
	if be == nil {
		return func() tea.Msg { return ErrorMsg{Err: fmt.Errorf("no backend, cannot delete")} }
	}
	kind := protocol.DatabaseKind(be.Kind())
	pks, err := be.PrimaryKeys(pd.Namespace, pd.Table)
	if err != nil {
		e := err
		return func() tea.Msg { return ErrorMsg{Err: e} }
	}
	stmt, err := buildDelete(kind, pd.Table, pks, pd.Frame, pd.Rows)
	if err != nil {
		e := err
		return func() tea.Msg { return ErrorMsg{Err: e} }
	}
	return func() tea.Msg {
		res, err := runInTx(be, stmt)
		if err != nil {
			return rowsDeletedMsg{err: err}
		}
		n := int64(0)
		if res.Exec != nil {
			n = res.Exec.RowsAffected
		}
		return rowsDeletedMsg{rows: n}
	}
}

// 编译期证明 Adapter 满足 celledit 对 backend 的最低要求。
var _ interface {
	Kind() string
	Run(stmt string) (backend.Result, error)
	PrimaryKeys(namespace, table string) ([]string, error)
} = (*backend.Adapter)(nil)
