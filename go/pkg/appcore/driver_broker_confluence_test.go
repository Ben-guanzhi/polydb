package appcore

import (
	"fmt"
	"sort"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 12：broker 收敛性穷举——Thm 80 的运行时对应（behavior.md §16.18.4）。
//
// Thm 80：「动态历史不留痕迹」——任意交错的注册/撤回/衰减序列到达 quiescent
// 后，**终态 = 按每 key 最后一次操作（net intent）直接重放的终态**（静态组装
// 终态）。对 register / revoke / set_granted 的全部操作序列（长度 ≤ 4，
// key ∈ {k1, k2}，操作逐枚举）断言这一点，与 §16.17（补偿侧 pending 穷举）
// 互补：那里锁定 emission 侧的补偿，这里锁定 acquisition 侧的注册表。

type thmOp struct {
	kind int // 0 = register, 1 = revoke, 2 = set_granted
	key  int // 0 = k1, 1 = k2
	// set_granted 的生效集合：收紧到只剩 query。
	tighten bool
}

var thmKeys = []string{"k1", "k2"}

func thmProvider(key string, commutative bool) protocol.DriverProvider {
	nullable := false
	_ = nullable
	return protocol.DriverProvider{
		Key:             key,
		Kind:            "duckdb",
		ProtocolVersion: BrokerProtocolVersion,
		Capabilities: []protocol.DriverCapability{
			protocol.DriverCapQuery,
			protocol.DriverCapExecute,
		},
		Commutative: commutative,
	}
}

func enumerateThmSeqs(maxLen int) [][]thmOp {
	var out [][]thmOp
	ops := []thmOp{
		{kind: 0, key: 0}, {kind: 0, key: 1},
		{kind: 1, key: 0}, {kind: 1, key: 1},
		{kind: 2, key: 0, tighten: true}, {kind: 2, key: 1, tighten: true},
	}
	var rec func(cur []thmOp)
	rec = func(cur []thmOp) {
		if len(cur) > 0 {
			out = append(out, append([]thmOp(nil), cur...))
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

// applyThmOp 把一个操作施加到 broker，返回是否产生了转移（成功）。
// 失败的操作是合法交错的一部分——守卫拒绝它们（Thm 70/§16.8）；按论文
// Bounds「excludes failed fibers」，收敛性断言只由**成功**转移构成。
func applyThmOp(b *DriverBroker, op thmOp) bool {
	key := thmKeys[op.key]
	switch op.kind {
	case 0:
		return b.Register(thmProvider(key, false)).Error == nil
	case 1:
		return b.Revoke(key).Error == nil
	default:
		caps := []protocol.DriverCapability{protocol.DriverCapQuery}
		return b.SetGranted(key, caps).Error == nil
	}
}

// thmSnapshot 把 broker 终态规范化成可比较形态（key 排序 + granted 集合排序）。
func thmSnapshot(b *DriverBroker) string {
	sts := b.Snapshot()
	out := ""
	for _, st := range sts {
		out += st.Key + ":" + fmt.Sprint(st.Reliance) + ":"
		caps := append([]protocol.DriverCapability(nil), st.GrantedCapabilities...)
		sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
		out += fmt.Sprint(caps) + ";"
	}
	return out
}

// TestBrokerConfluenceExhaustive 对全部操作序列断言 Thm 80 的运行时对应：
// **幂等重放**——同一序列执行两遍，第二遍零状态变化（「动态历史不留痕迹」；
// 守卫使 register/revoke 的重放全部被拒，set_granted 的重放是幂等收紧）。
// 注：net-intent 重放（按每 key 最后一次操作重放）不是合法的收敛判据——
// 收紧操作依赖其前置 register，单独重放会丢失前驱依赖（穷举曾命中该反例：
// seq = [reg, reg✗, grant, reg✗] 的收紧必须连同首次注册一起重放才有效）。
func TestBrokerConfluenceExhaustive(t *testing.T) {
	seqs := enumerateThmSeqs(4)
	if len(seqs) < 500 {
		t.Fatalf("enumeration too small: %d", len(seqs))
	}
	for si, seq := range seqs {
		full := NewDriverBroker()
		applied := make([]bool, len(seq))
		for i, op := range seq {
			applied[i] = applyThmOp(full, op)
		}
		// 幂等重放（第二遍执行同一序列）**仅对全成功序列**成立：重放的
		// register 重复被拒、revoke 的对象已不存在、set_granted 幂等收紧。
		// 含失败操作的序列不能做幂等重放——第一遍失败的 grant（当时未注册）
		// 在第二遍会成功（注册已存在），那是守卫语义的正确行为，不是收敛性
		// 破坏（穷举曾命中该反例 seq#39）。
		allOK := true
		for _, a := range applied {
			allOK = allOK && a
		}
		afterFirst := thmSnapshot(full)
		if allOK {
			for _, op := range seq {
				applyThmOp(full, op)
			}
			if thmSnapshot(full) != afterFirst {
				t.Fatalf("seq#%d: replay changed state (Thm 80 violated): %q -> %q",
					si, afterFirst, thmSnapshot(full))
			}
		}
		// net-intent 静态组装终态：只由**成功**转移构成（Bounds: excludes
		// failed fibers）——per-key 取最后一次成功的 register（起点）与其后
		// 全部成功的 set_granted。grant 依赖前置 register，两者必须一起重放。
		net := map[int][]thmOp{}
		for i, op := range seq {
			if !applied[i] {
				continue
			}
			switch op.kind {
			case 0:
				net[op.key] = []thmOp{op}
			case 1:
				delete(net, op.key)
			case 2:
				if l, ok := net[op.key]; ok && len(l) > 0 {
					net[op.key] = append(l, op)
				}
			}
		}
		want := NewDriverBroker()
		for _, k := range []int{0, 1} {
			for _, op := range net[k] {
				applyThmOp(want, op)
			}
		}
		if thmSnapshot(full) != thmSnapshot(want) {
			t.Fatalf("seq#%d: replayed state %q != net-intent %q (Thm 80 violated)",
				si, thmSnapshot(full), thmSnapshot(want))
		}
	}
}

// TestBrokerCommutativeRegisterIdempotent 锁定 §16.18.3：commutative key 的
// 重复声明（能力集一致）是幂等中性；能力集漂移仍拒绝。
func TestBrokerCommutativeRegisterIdempotent(t *testing.T) {
	b := NewDriverBroker()
	// 首次注册：activating。
	tr := b.Register(thmProvider("k1", true))
	if tr.Error != nil || tr.Event != protocol.DriverBrokerEventActivating {
		t.Fatalf("first register: %v %v", tr.Error, tr.Event)
	}
	// 第二实例重复声明（同能力集）：幂等中性。
	tr = b.Register(thmProvider("k1", true))
	if tr.Error != nil {
		t.Fatalf("idempotent register failed: %v", tr.Error)
	}
	if tr.Event != protocol.DriverBrokerEventNeutral {
		t.Fatalf("idempotent register must be neutral, got %v", tr.Event)
	}
	// 声明漂移（能力集缩小）：拒绝。
	drift := thmProvider("k1", true)
	drift.Capabilities = []protocol.DriverCapability{protocol.DriverCapQuery}
	if tr := b.Register(drift); tr.Error == nil {
		t.Fatal("capability drift must be rejected")
	}
	// 非 commutative 重复注册：拒绝（Def 19 不变）。
	if tr := b.Register(thmProvider("k1", false)); tr.Error == nil {
		t.Fatal("non-commutative duplicate must be rejected")
	}
}
