package appcore

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func brokerTestProvider(key string) protocol.DriverProvider {
	return protocol.DriverProvider{
		Key:             protocol.DriverProviderKey(key),
		Kind:            "duckdb",
		ProtocolVersion: BrokerProtocolVersion,
		Capabilities: []protocol.DriverCapability{
			protocol.DriverCapQuery,
			protocol.DriverCapExecute,
			protocol.DriverCapMetadata,
		},
	}
}

// TestRegisterIsTrackedEffectWithLIFOInverse 对应 Rust 同名测试。
func TestRegisterIsTrackedEffectWithLIFOInverse(t *testing.T) {
	b := NewDriverBroker()
	r := b.Register(brokerTestProvider("acme:duckdb"))
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Event != protocol.DriverBrokerEventActivating || len(r.State) != 1 {
		t.Fatalf("bad activating transition: %+v", r)
	}
	if b.AccumulatorLen() != 1 {
		t.Fatal("登记必须压入 accumulator")
	}

	r = b.Revoke("acme:duckdb")
	if r.Error != nil || r.Event != protocol.DriverBrokerEventDeactivating || len(r.State) != 0 {
		t.Fatalf("bad deactivating transition: %+v", r)
	}
	if b.AccumulatorLen() != 0 {
		t.Fatal("撤回必须弹出 accumulator")
	}
}

// TestRecoveryIsExactUnderInterleaving：Thm 68，只撤回 B，A 的贡献原封不动。
func TestRecoveryIsExactUnderInterleaving(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))
	b.Register(brokerTestProvider("pkg:b"))
	if b.AccumulatorLen() != 2 {
		t.Fatal("两次登记应有两条逆")
	}

	r := b.Revoke("pkg:b")
	if r.Error != nil || len(r.State) != 1 || r.State[0].Key != "pkg:a" {
		t.Fatalf("bad partial recovery: %+v", r)
	}
	if b.AccumulatorLen() != 1 {
		t.Fatal("只弹出 B 的逆，A 的逆仍在")
	}

	r = b.Revoke("pkg:a")
	if len(r.State) != 0 {
		t.Fatal("confluence: 动态历史不留痕迹")
	}
	if b.AccumulatorLen() != 0 {
		t.Fatal("终态 accumulator 应为空")
	}
}

// TestConfluenceIndependentOfRegistrationOrder：Thm 80。
func TestConfluenceIndependentOfRegistrationOrder(t *testing.T) {
	b1 := NewDriverBroker()
	b1.Register(brokerTestProvider("pkg:a"))
	b1.Register(brokerTestProvider("pkg:b"))
	b1.Revoke("pkg:a")

	b2 := NewDriverBroker()
	b2.Register(brokerTestProvider("pkg:b"))

	s1, s2 := b1.Snapshot(), b2.Snapshot()
	if len(s1) != len(s2) || len(s1) != 1 || s1[0].Key != s2[0].Key {
		t.Fatalf("两条路线静止态应相同: %+v vs %+v", s1, s2)
	}
}

// TestCannotProvideTwiceAndRevokeAbsentAreNoTransitions：Def 19。
func TestCannotProvideTwiceAndRevokeAbsentAreNoTransitions(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))

	dup := b.Register(brokerTestProvider("pkg:a"))
	if dup.Error == nil {
		t.Fatal("重复注册必须报错")
	}
	if len(dup.State) != 1 || b.AccumulatorLen() != 1 {
		t.Fatal("失败不产生转移、不压 accumulator")
	}

	absent := b.Revoke("pkg:missing")
	if absent.Error == nil || len(absent.State) != 1 {
		t.Fatal("撤回未登记 key 必须报错且不产生转移")
	}
}

// TestVersionProbePrecedesRegistration：先探版本、再解引用。
func TestVersionProbePrecedesRegistration(t *testing.T) {
	b := NewDriverBroker()
	p := brokerTestProvider("pkg:a")
	p.ProtocolVersion = BrokerProtocolVersion + 1
	r := b.Register(p)
	if r.Error == nil || len(b.Snapshot()) != 0 {
		t.Fatal("版本不符绝不解引用/登记")
	}
}

// TestGuardBlocksWithdrawalWhileRelied：¬ relied_n(γ) 守卫。
func TestGuardBlocksWithdrawalWhileRelied(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))
	if n, ok := b.Acquire("pkg:a"); !ok || n != 1 {
		t.Fatal("acquire 应递增 reliance")
	}

	r := b.Revoke("pkg:a")
	if r.Error == nil {
		t.Fatal("¬ relied_n(γ) 必须拒绝撤回")
	}
	if r.Event != protocol.DriverBrokerEventDeactivating || len(r.State) != 1 {
		t.Fatal("决定与动作分离：守卫拒绝不卸载")
	}
	if b.AccumulatorLen() != 1 {
		t.Fatal("守卫拒绝不应用 accumulator")
	}

	if n, ok := b.Release("pkg:a"); !ok || n != 0 {
		t.Fatal("release 应递减 reliance")
	}
	r = b.Revoke("pkg:a")
	if r.Error != nil || len(r.State) != 0 {
		t.Fatalf("reliance 归零后应可撤回: %+v", r)
	}
}

// TestInterceptionIsNeutralAndNeverReloads：§6.3。
func TestInterceptionIsNeutralAndNeverReloads(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))
	r := b.SetGranted("pkg:a", []protocol.DriverCapability{
		protocol.DriverCapQuery, protocol.DriverCapMetadata,
	})
	if r.Error != nil || r.Event != protocol.DriverBrokerEventNeutral {
		t.Fatalf("interception 必须是 neutral: %+v", r)
	}
	s := r.State[0]
	if len(s.Capabilities) != 3 || len(s.GrantedCapabilities) != 2 {
		t.Fatalf("声明不变、生效能力衰减: %+v", s)
	}
	if b.AccumulatorLen() != 1 {
		t.Fatal("interception 不是 effect，不动 accumulator")
	}
}

// TestCheckAndAcquireIsAtomicAndEnforcesCapabilities 对应 Rust 同名测试（§16.8）。
func TestCheckAndAcquireIsAtomicAndEnforcesCapabilities(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))

	if pe := b.CheckAndAcquire("pkg:a"); pe != nil {
		t.Fatalf("unexpected error: %v", pe)
	}
	if b.Snapshot()[0].Reliance != 1 {
		t.Fatal("acquire 应递增 reliance")
	}
	b.Release("pkg:a")

	tr := b.SetGranted("pkg:a", []protocol.DriverCapability{protocol.DriverCapMetadata})
	if tr.Error != nil {
		t.Fatalf("set_granted: %v", tr.Error)
	}
	pe := b.CheckAndAcquire("pkg:a")
	if pe == nil || pe.Code != protocol.ErrPermissionDenied {
		t.Fatalf("衰减后 query 应被拒: %v", pe)
	}
	if b.Snapshot()[0].Reliance != 0 {
		t.Fatal("失败不许占 reliance")
	}

	pe = b.CheckAndAcquire("pkg:missing")
	if pe == nil || pe.Code != protocol.ErrDriverNotAvailable {
		t.Fatalf("未注册 key 应被拒: %v", pe)
	}
}

// TestRelianceGuardBlocksRevokeThroughoutInFlightLifetime 对应 Rust 同名（Thm 70）。
func TestRelianceGuardBlocksRevokeThroughoutInFlightLifetime(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))
	if pe := b.CheckAndAcquire("pkg:a"); pe != nil {
		t.Fatalf("acquire: %v", pe)
	}

	r := b.Revoke("pkg:a")
	if r.Error == nil || r.Error.Code != protocol.ErrDriverNotAvailable {
		t.Fatalf("¬ relied_n(γ) 必须拒绝: %+v", r)
	}
	if len(r.State) != 1 {
		t.Fatal("守卫拒绝不产生转移")
	}

	b.Release("pkg:a")
	r = b.Revoke("pkg:a")
	if r.Error != nil || len(r.State) != 0 {
		t.Fatalf("release 后应可撤回: %+v", r)
	}
}

// TestGrantedMustBeSubsetOfDeclared：能力不可越权放大。
func TestGrantedMustBeSubsetOfDeclared(t *testing.T) {
	b := NewDriverBroker()
	b.Register(brokerTestProvider("pkg:a"))
	r := b.SetGranted("pkg:a", []protocol.DriverCapability{protocol.DriverCapKv})
	if r.Error == nil {
		t.Fatal("Kv 未声明，越权必须拒绝")
	}
	if len(r.State[0].GrantedCapabilities) != 3 {
		t.Fatal("失败不产生转移")
	}
}
