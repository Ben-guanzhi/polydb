package tui

// 编辑/删除走事务的回归。真实事务要连服务端，这里用 spy 钉住调用序列：
// 成功才提交，任一步失败都要回滚。

import (
	"errors"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
)

// txSpy 记录事务各步被调用的次数，并可按用例注入失败。
type txSpy struct {
	fakeBackend
	begins, execs, commits, rollbacks int
	failExec, failCommit              bool
}

func (s *txSpy) Begin() (string, error) { s.begins++; return "tx1", nil }

func (s *txSpy) ExecuteInTx(txID, stmt string) (backend.Result, error) {
	s.execs++
	if s.failExec {
		return backend.Result{}, errors.New("exec boom")
	}
	return backend.Result{Exec: &backend.ExecResult{RowsAffected: 1}}, nil
}

func (s *txSpy) Commit(txID string) error {
	s.commits++
	if s.failCommit {
		return errors.New("commit boom")
	}
	return nil
}

func (s *txSpy) Rollback(txID string) error { s.rollbacks++; return nil }

func TestRunInTxSuccessCommits(t *testing.T) {
	s := &txSpy{}
	res, err := runInTx(s, "UPDATE t SET a=1 WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	if s.begins != 1 || s.execs != 1 || s.commits != 1 || s.rollbacks != 0 {
		t.Fatalf("成功路径调用序列 = begin%d exec%d commit%d rollback%d",
			s.begins, s.execs, s.commits, s.rollbacks)
	}
	if res.Exec == nil || res.Exec.RowsAffected != 1 {
		t.Fatalf("应带回影响行数, got %+v", res.Exec)
	}
}

// TestRunInTxRollsBackOnExecFailure pins atomicity: a failed statement must
// not leave a half-applied edit behind.
func TestRunInTxRollsBackOnExecFailure(t *testing.T) {
	s := &txSpy{failExec: true}
	if _, err := runInTx(s, "UPDATE t SET a=1 WHERE id=1"); err == nil {
		t.Fatal("执行失败应返回错误")
	}
	if s.commits != 0 {
		t.Fatal("执行失败不应提交")
	}
	if s.rollbacks != 1 {
		t.Fatalf("执行失败应回滚一次, got %d", s.rollbacks)
	}
}

func TestRunInTxRollsBackOnCommitFailure(t *testing.T) {
	s := &txSpy{failCommit: true}
	if _, err := runInTx(s, "UPDATE t SET a=1 WHERE id=1"); err == nil {
		t.Fatal("提交失败应返回错误")
	}
	if s.rollbacks != 1 {
		t.Fatalf("提交失败应回滚一次, got %d", s.rollbacks)
	}
}

// TestRunInTxFallsBackWithoutTxRunner pins that a backend with no transaction
// support still works (degraded to non-atomic, but not broken).
func TestRunInTxFallsBackWithoutTxRunner(t *testing.T) {
	be := fakeBackend{}
	if _, err := runInTx(be, "UPDATE t SET a=1 WHERE id=1"); err != nil {
		t.Fatalf("不支持事务的后端应退回直接执行而不是报错：%v", err)
	}
}
