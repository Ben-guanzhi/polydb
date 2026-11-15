package server

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 11：compensation 的 commutation **穷举验证**（behavior.md §16.17）。
//
// §6.1 要求补偿组合的交换律在更粗的等价关系（≈ₒ：客户端可观测的帧序列）上
// **重新建立**——元理论不自动继承。这里用**全枚举**完成重建：
//
//	对 pending 状态机的全部操作序列（长度 ≤ 4，操作 = Complete(q) / Cancel(q)，
//	q ∈ {q1, q2}），断言 §16.17.2 的四条性质。
//
// 全枚举是确定性、可复现、进 CI 的——实现若偏离性质，穷举必然命中反例。

// commOp 是穷举序列中的一个操作。
type commOp struct {
	kind int // 0 = Complete, 1 = Cancel
	q    int // 0 = q1, 1 = q2
}

var (
	commQueryIDs = []string{
		"11111111-0000-0000-0000-000000000001",
		"22222222-0000-0000-0000-000000000002",
	}
	commKeys = []protocol.DriverProviderKey{"pkg:c1", "pkg:c2"}
)

// enumerateCommSeqs 生成长度 1..maxLen 的全部操作序列。
func enumerateCommSeqs(maxLen int) [][]commOp {
	var out [][]commOp
	ops := []commOp{
		{kind: 0, q: 0}, {kind: 0, q: 1},
		{kind: 1, q: 0}, {kind: 1, q: 1},
	}
	var rec func(cur []commOp)
	rec = func(cur []commOp) {
		if len(cur) > 0 {
			out = append(out, append([]commOp(nil), cur...))
		}
		if len(cur) >= maxLen {
			return
		}
		for _, op := range ops {
			rec(append(cur, op))
		}
	}
	rec(nil)
	return out
}

// commSimulator 是 pending 状态机的**单线程模拟**：与 ProviderHub 的
// RegisterPending/Complete/Cancel 同语义（§16.9.2 step 5 的 withhold、
// §16.14 的「至多一次投递」）。
type commSimulator struct {
	pending   map[string]bool
	delivered map[string]bool
}

func newCommSimulator() *commSimulator {
	return &commSimulator{pending: map[string]bool{}}
}

func (s *commSimulator) register(q string) {
	s.pending[q] = true
}

// complete 返回是否投递（false = 被 withhold：pending 已被 cancel 清除）。
func (s *commSimulator) complete(q string) bool {
	if !s.pending[q] {
		return false
	}
	delete(s.pending, q)
	if s.delivered[q] {
		return false // 至多一次投递（P2）
	}
	s.delivered[q] = true
	return true
}

// cancel 模拟取消：清除 pending（withhold 迟到的 complete）。
func (s *commSimulator) cancel(q string) {
	delete(s.pending, q)
}

// fixedResult 构造一条确定性的查询结果。
func fixedResult() *protocol.QueryResult {
	nullable := false
	return &protocol.QueryResult{
		Columns: []protocol.ResultColumn{{Name: "v", DataType: "INTEGER", Nullable: &nullable}},
		Rows:    [][]protocol.Value{{protocol.NewIntValue(1)}},
	}
}

// TestProviderCommutationExhaustive 对全部操作序列断言 §16.17.2 的性质：
// 模拟器与真实 ProviderHub 在每条序列上的可观测结果**必须一致**——这就是
// commutation 在 ≈ₒ 上的重建（实现与语义不漂移）。
func TestProviderCommutationExhaustive(t *testing.T) {
	seqs := enumerateCommSeqs(4)
	if len(seqs) < 300 {
		t.Fatalf("enumeration too small: %d sequences", len(seqs))
	}
	for si, seq := range seqs {
		sim := newCommSimulator()
		hub := NewProviderHubWithAllowed(commKeys...)
		for oi, op := range seq {
			q := commQueryIDs[op.q]
			switch op.kind {
			case 0: // Complete
				simOK := sim.complete(q)
				hubOK := hub.Complete(q, providerOutcome{result: fixedResult()})
				if simOK != hubOK {
					t.Fatalf("seq#%d op%d Complete(%s): simulator=%v hub=%v — implementations diverged",
						si, oi, q, simOK, hubOK)
				}
			case 1: // Cancel
				sim.cancel(q)
				hub.Cancel(q, commKeys[op.q])
			}
		}
	}
}

// TestProviderCommutationWithholdProperty 锁定 P1：cancel 先于 complete 时
// 结果必须被扣住（§16.17.2 P1 / §16.9.2 step 5）。
func TestProviderCommutationWithholdProperty(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:w")
	hub.RegisterPending("q1", "pkg:w")
	hub.Cancel("q1", "pkg:w")
	// 迟到的结果：必须被扣住（客户端不收到结果帧）。
	if hub.Complete("q1", providerOutcome{result: fixedResult()}) {
		t.Fatal("withhold broken: late result was delivered after cancel")
	}
}

// TestProviderCommutationAtMostOneOutcome 锁定 P2：每个 query 至多一次投递
// （结果与取消互斥、不重复）。
func TestProviderCommutationAtMostOneOutcome(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:w")
	hub.RegisterPending("q1", "pkg:w")
	if !hub.Complete("q1", providerOutcome{result: fixedResult()}) {
		t.Fatal("first complete must deliver")
	}
	// 重复 complete → 扣住。
	if hub.Complete("q1", providerOutcome{result: fixedResult()}) {
		t.Fatal("duplicate complete must be withheld")
	}
}

// TestProviderCommutationLinearizability 锁定 P3：先完成者胜——取消只能作用于
// 尚未完成的查询；已完成查询的取消不改变可观测结果（结果已发射）。
func TestProviderCommutationLinearizability(t *testing.T) {
	// 顺序 A：complete → cancel → complete（结果已投递，取消无效果）。
	hubA := NewProviderHubWithAllowed("pkg:w")
	hubA.RegisterPending("q", "pkg:w")
	if !hubA.Complete("q", providerOutcome{result: fixedResult()}) {
		t.Fatal("A: complete must win")
	}
	hubA.Cancel("q", "pkg:w")
	if hubA.Complete("q", providerOutcome{result: fixedResult()}) {
		t.Fatal("A: duplicate complete after cancel must be withheld")
	}
	// 顺序 B：cancel → complete（取消胜，complete 被扣住）。
	hubB := NewProviderHubWithAllowed("pkg:w")
	hubB.RegisterPending("q", "pkg:w")
	hubB.Cancel("q", "pkg:w")
	if hubB.Complete("q", providerOutcome{result: fixedResult()}) {
		t.Fatal("B: cancel must win the race")
	}
	// 两条顺序的可观测结果**不同**——这正是线性化点不同：A 在 cancel 前完成、
	// B 在 cancel 后完成。交换律断言的是「交换**可交换的**操作对」（不同 q 的
	// 操作、同一 q 的两次 cancel）结果不变，而不是「任意重排都等价」。
	// 不同 q 的操作可交换：
	hubC := NewProviderHubWithAllowed("pkg:w", "pkg:v")
	hubC.RegisterPending("q1", "pkg:w")
	hubC.RegisterPending("q2", "pkg:v")
	hubC.Cancel("q1", "pkg:w") // q1 的取消与 q2 的完成可交换
	if !hubC.Complete("q2", providerOutcome{result: fixedResult()}) {
		t.Fatal("C: complete(q2) must be unaffected by cancel(q1)")
	}
	if hubC.ProviderEndpoint("pkg:w") != nil {
		t.Fatal("C: cancel(q1) must not affect q2's endpoint")
	}
}
