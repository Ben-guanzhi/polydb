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
