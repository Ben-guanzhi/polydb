package tui

// 批量提交（changes 队列）的回归。真实执行要连服务端，这里用 spy 钉住：
// 整批在一个事务里跑完、任一条失败即回滚、空队列直接返回。

import (
	"errors"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
)

// batchSpy 记录事务调用，并可让第 n 条语句失败。
type batchSpy struct {
	fakeBackend
	stmts           []string
	begins, commits int
	rollbacks       int
	failAt          int // 0 = 不失败
	pks             []string
}

func (s *batchSpy) PrimaryKeys(namespace, table string) ([]string, error) {
	return s.pks, nil
}

func (s *batchSpy) Begin() (string, error) { s.begins++; return "tx1", nil }

func (s *batchSpy) ExecuteInTx(txID, stmt string) (backend.Result, error) {
	s.stmts = append(s.stmts, stmt)
	if s.failAt > 0 && len(s.stmts) == s.failAt {
		return backend.Result{}, errors.New("boom")
	}
	return backend.Result{Exec: &backend.ExecResult{RowsAffected: 1}}, nil
}

func (s *batchSpy) Commit(txID string) error   { s.commits++; return nil }
func (s *batchSpy) Rollback(txID string) error { s.rollbacks++; return nil }

// queueWith 造一个含两条单元格修改的队列。
func queueWith(t *testing.T) *changes.Queue {
	t.Helper()
	q := changes.New()
	row := changes.Row{
		{Column: "id", Value: protocol.NewIntValue(1)},
		{Column: "name", Value: protocol.NewStringValue("alice")},
	}
	q.AddCellChange("1", "name", protocol.NewStringValue("alice"), protocol.NewStringValue("bob"), row)
	q.AddCellChange("2", "name", protocol.NewStringValue("carol"), protocol.NewStringValue("dave"), row)
	if q.Size() != 2 {
		t.Fatalf("队列应含 2 条变更, got %d", q.Size())
	}
	return q
}

func TestCommitQueueEmptyIsNoop(t *testing.T) {
	s := &batchSpy{pks: []string{"id"}}
	n, err := commitQueue(s, "", "people", "", changes.New())
	if err != nil || n != 0 {
		t.Fatalf("空队列应直接返回 (0, nil), got (%d, %v)", n, err)
	}
	if s.begins != 0 {
		t.Fatal("空队列不应开事务")
	}
}

func TestCommitQueueNilQueueIsNoop(t *testing.T) {
	s := &batchSpy{pks: []string{"id"}}
	if n, err := commitQueue(s, "", "people", "", nil); err != nil || n != 0 {
		t.Fatalf("nil 队列应直接返回 (0, nil), got (%d, %v)", n, err)
	}
}

// TestCommitQueueRunsAllInOneTx pins the point of the change: N edits produce
// N statements but exactly ONE transaction.
func TestCommitQueueRunsAllInOneTx(t *testing.T) {
	s := &batchSpy{pks: []string{"id"}}
	q := queueWith(t)

	n, err := commitQueue(s, "", "people", "", q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应执行 2 条语句, got %d", n)
	}
	if len(s.stmts) != 2 {
		t.Fatalf("事务内语句数 = %d, want 2", len(s.stmts))
	}
	if s.begins != 1 || s.commits != 1 || s.rollbacks != 0 {
		t.Fatalf("应只开并提交一个事务: begin%d commit%d rollback%d",
			s.begins, s.commits, s.rollbacks)
	}
	if q.Size() != 0 {
		t.Fatalf("提交后队列应清空, got %d", q.Size())
	}
}

// TestCommitQueueRollsBackMidBatch pins atomicity across the batch: a failure
// on the second statement must roll the first one back too.
func TestCommitQueueRollsBackMidBatch(t *testing.T) {
	s := &batchSpy{pks: []string{"id"}, failAt: 2}
	q := queueWith(t)

	if _, err := commitQueue(s, "", "people", "", q); err == nil {
		t.Fatal("批中失败应返回错误")
	}
	if s.commits != 0 {
		t.Fatal("批中失败不应提交")
	}
	if s.rollbacks != 1 {
		t.Fatalf("批中失败应回滚一次, got %d", s.rollbacks)
	}
	if q.Size() != 2 {
		t.Fatalf("失败后队列应保留变更待重试, got %d", q.Size())
	}
}

func TestCommitQueueRequiresPrimaryKey(t *testing.T) {
	s := &batchSpy{pks: nil}
	if _, err := commitQueue(s, "", "people", "", queueWith(t)); err == nil {
		t.Fatal("无主键应报错（无法定位要改的行）")
	}
}

// TestCommitQueueFallsBackWithoutTx pins that a backend with no transaction
// support still commits (non-atomically) instead of failing outright.
func TestCommitQueueFallsBackWithoutTx(t *testing.T) {
	be := &noTxBackend{pks: []string{"id"}}
	q := queueWith(t)
	n, err := commitQueue(be, "", "people", "", q)
	if err != nil {
		t.Fatalf("不支持事务的后端应退回逐条执行：%v", err)
	}
	if n != 2 || len(be.ran) != 2 {
		t.Fatalf("应逐条执行 2 条, got n=%d ran=%d", n, len(be.ran))
	}
}

// noTxBackend 是不实现 backend.TxRunner 的后端（只有 Run）。
type noTxBackend struct {
	fakeBackend
	pks []string
	ran []string
}

func (b *noTxBackend) PrimaryKeys(namespace, table string) ([]string, error) {
	return b.pks, nil
}

func (b *noTxBackend) Run(stmt string) (backend.Result, error) {
	b.ran = append(b.ran, stmt)
	return backend.Result{Exec: &backend.ExecResult{RowsAffected: 1}}, nil
}
