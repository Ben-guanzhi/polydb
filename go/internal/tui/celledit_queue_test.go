package tui

// 批量提交（changes 队列）的回归。真实执行要连服务端，这里用 spy 钉住：
// 整批在一个事务里跑完、任一条失败即回滚、空队列直接返回。

import (
	"errors"
	"strings"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
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

// probeSpy 让指定语句返回 0 行，并记录探针（SELECT 1 ...）的执行。
type probeSpy struct {
	fakeBackend
	stmts     []string
	zeroAt    int // 1-based：第 n 条提交语句返回 0 行；0 = 不归零
	probeSeen []string
	probeRows int // 探针返回的行数（1 = 行在，0 = 行没了）
	begins    int
	commits   int
	rollbacks int
	submits   int // 非探针语句计数
}

func (s *probeSpy) PrimaryKeys(namespace, table string) ([]string, error) {
	return []string{"id"}, nil
}

func (s *probeSpy) Begin() (string, error) { s.begins++; return "tx1", nil }

func (s *probeSpy) ExecuteInTx(txID, stmt string) (backend.Result, error) {
	if strings.HasPrefix(stmt, "SELECT 1 FROM") {
		s.probeSeen = append(s.probeSeen, stmt)
		frame := tuidata.New("1")
		if s.probeRows > 0 {
			frame.AppendRow([]any{"1"})
		}
		return backend.Result{Frame: frame}, nil
	}
	s.stmts = append(s.stmts, stmt)
	s.submits++
	if s.zeroAt > 0 && s.submits == s.zeroAt {
		return backend.Result{Exec: &backend.ExecResult{RowsAffected: 0}}, nil
	}
	return backend.Result{Exec: &backend.ExecResult{RowsAffected: 1}}, nil
}

func (s *probeSpy) Commit(txID string) error   { s.commits++; return nil }
func (s *probeSpy) Rollback(txID string) error { s.rollbacks++; return nil }

// TestCommitQueueZeroRowUpdateProbesKey pins the 0-row guard: an UPDATE that
// affected 0 rows triggers an in-transaction key probe. Row present = a MySQL
// changed-rows no-op, the batch commits; row gone = concurrent delete/PK
// change, the batch rolls back. (R2-02; TableProGo zeroRowVerdict 同课.)
func TestCommitQueueZeroRowUpdateProbesKey(t *testing.T) {
	// 行还在：探针返回 1 行 → 同值更新 no-op，整批提交。
	s := &probeSpy{probeRows: 1, zeroAt: 1}
	q := queueWith(t)
	n, err := commitQueue(s, "", "people", "", q)
	if err != nil {
		t.Fatalf("行在的 0 行更新应按 no-op 放行: %v", err)
	}
	if n != 2 || s.commits != 1 || s.rollbacks != 0 {
		t.Fatalf("no-op 放行应提交整批: n=%d commit=%d rollback=%d", n, s.commits, s.rollbacks)
	}
	if len(s.probeSeen) != 1 {
		t.Fatalf("应恰一次定位探针, got %d", len(s.probeSeen))
	}
	// 探针复用提交语句的定位谓词（表名 + 主键谓词，与提交语句同源同方言）。
	if !strings.Contains(s.probeSeen[0], "people") || !strings.Contains(s.probeSeen[0], "id") {
		t.Fatalf("探针应带表名与主键谓词: %q", s.probeSeen[0])
	}

	// 行没了：探针返回 0 行 → 并发删除/改键，整批回滚。
	s2 := &probeSpy{probeRows: 0, zeroAt: 1}
	q2 := queueWith(t)
	if _, err := commitQueue(s2, "", "people", "", q2); err == nil {
		t.Fatal("行已消失的 0 行更新必须报错")
	}
	if s2.commits != 0 || s2.rollbacks != 1 {
		t.Fatalf("行消失应回滚: commit=%d rollback=%d", s2.commits, s2.rollbacks)
	}
	if q2.Size() != 2 {
		t.Fatalf("失败后队列应保留变更待重试, got %d", q2.Size())
	}
	if !strings.Contains(strings.Join(s2.probeSeen, ";"), "people") {
		t.Fatalf("回滚前也应执行过探针: %v", s2.probeSeen)
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
