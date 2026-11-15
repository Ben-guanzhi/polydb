package server

import (
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// fakeSession 造一个带发送通道的假会话：hub 的降级回执会往 writeCh 投递，
// 没有 writeCh 会阻塞到 wsSendTimeout（30s）。
func fakeSession() *wsSession {
	return &wsSession{writeCh: make(chan []byte, 8)}
}

// fakeEndpoint 造一个会话端点（§16.12.1 kind 1）与其接收端。
func fakeEndpoint(id uint64, key string) (*SessionEndpoint, chan []byte) {
	s := fakeSession()
	return NewSessionEndpoint(id, s), s.writeCh
}

// TestProviderHubAttachPolicy 覆盖 behavior.md §16.10.1 的 attach 门禁：
// deny-by-default、白名单内可用。
func TestProviderHubAttachPolicy(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:ok")

	// 白名单外 → PERMISSION_DENIED，且不产生执行端。
	epNo, _ := fakeEndpoint(1, "pkg:no")
	err, attached := hub.Attach("pkg:no", epNo, false)
	if err == nil || err.Code != protocol.ErrPermissionDenied {
		t.Fatalf("unauthorized attach: %v", err)
	}
	if len(attached) != 0 {
		t.Fatalf("refused attach must not register: %v", attached)
	}
	if hub.ProviderEndpoint("pkg:no") != nil {
		t.Fatal("refused attach must not create an endpoint")
	}

	// 白名单内 → 成功。
	epOk, _ := fakeEndpoint(2, "pkg:ok")
	err, attached = hub.Attach("pkg:ok", epOk, false)
	if err != nil {
		t.Fatalf("allowed attach failed: %v", err)
	}
	if len(attached) != 1 || attached[0] != "pkg:ok" {
		t.Fatalf("attached = %v", attached)
	}
}

// TestProviderHubDetachOwnership 验证 detach 只移除本端点的登记
// （rolling update 后旧端点不能 detach 掉新端点的）。
func TestProviderHubDetachOwnership(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:a")
	first, _ := fakeEndpoint(1, "pkg:a")
	second, _ := fakeEndpoint(2, "pkg:a")
	if err, _ := hub.Attach("pkg:a", first, false); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 新端点接管同一 key。
	if err, _ := hub.Attach("pkg:a", second, false); err != nil {
		t.Fatalf("re-attach: %v", err)
	}

	if err, _ := hub.Detach("pkg:a", first); err == nil || err.Code != protocol.ErrInvalidParam {
		t.Fatalf("old endpoint must not detach the new one: %v", err)
	}
	if hub.ProviderEndpoint("pkg:a") == nil {
		t.Fatal("new registration must survive")
	}
}

// TestProviderHubDropEndpoint 验证断连回收只移除该端点的 key（Thm 68）。
func TestProviderHubDropEndpoint(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:a", "pkg:b")
	mine, _ := fakeEndpoint(1, "pkg:a")
	other, _ := fakeEndpoint(2, "pkg:b")
	if err, _ := hub.Attach("pkg:a", mine, false); err != nil {
		t.Fatalf("attach a: %v", err)
	}
	if err, _ := hub.Attach("pkg:b", other, false); err != nil {
		t.Fatalf("attach b: %v", err)
	}
	dropped := hub.DropEndpoint(mine)
	if len(dropped) != 1 || dropped[0] != "pkg:a" {
		t.Fatalf("dropped = %v", dropped)
	}
	if hub.ProviderEndpoint("pkg:a") != nil {
		t.Fatal("own key must be removed")
	}
	if hub.ProviderEndpoint("pkg:b") == nil {
		t.Fatal("other endpoint's key must survive")
	}
}

// TestProviderHubLateResultWithheld 验证迟到结果被扣住（§16.9.2 step 5）。
func TestProviderHubLateResultWithheld(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:a")
	ep, _ := fakeEndpoint(1, "pkg:a")
	if err, _ := hub.Attach("pkg:a", ep, false); err != nil {
		t.Fatalf("attach: %v", err)
	}
	ch, _ := hub.RegisterPending("q1", "pkg:a")
	if !hub.Complete("q1", providerOutcome{err: &protocol.PolyDBError{Code: protocol.ErrUnknown}}) {
		t.Fatal("first complete must be delivered")
	}
	select {
	case <-ch:
	default:
		t.Fatal("result must be delivered")
	}
	// 重复完成 → 扣住（pending 已清除）。
	if hub.Complete("q1", providerOutcome{err: &protocol.PolyDBError{Code: protocol.ErrUnknown}}) {
		t.Fatal("late result must be withheld")
	}
}

// TestProviderHubRollingUpdate 覆盖 §16.9.4：新端点接管同一 key；旧端点退役，
// 服务完在途后自动降级（回 deactivating 回执），新端点的登记保留。
func TestProviderHubRollingUpdate(t *testing.T) {
	hub := NewProviderHubWithAllowed("pkg:a")
	first, firstCh := fakeEndpoint(1, "pkg:a")
	second, _ := fakeEndpoint(2, "pkg:a")
	if err, _ := hub.Attach("pkg:a", first, false); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 旧端点有一个在途 exec。
	hub.RegisterPending("q1", "pkg:a")

	// 新端点接管。
	if err, _ := hub.Attach("pkg:a", second, false); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if hub.ProviderEndpoint("pkg:a").ID() != second.ID() {
		t.Fatal("new endpoint must become the current one")
	}

	// 旧端点在途归零 → 自动降级：回 deactivating 回执。
	hub.Complete("q1", providerOutcome{err: &protocol.PolyDBError{Code: protocol.ErrUnknown}})
	if hub.ProviderEndpoint("pkg:a").ID() != second.ID() {
		t.Fatal("new endpoint's registration must survive the retired one draining")
	}
	// 旧端点收到降级回执。
	select {
	case data := <-firstCh:
		if len(data) == 0 {
			t.Fatal("empty frame")
		}
	default:
		t.Fatal("retired endpoint must be notified")
	}
}

// TestSessionEndpointEmits 验证会话端点的投递与回执（§16.12.1）。
func TestSessionEndpointEmits(t *testing.T) {
	s := fakeSession()
	ep := NewSessionEndpoint(7, s)
	if ep.ID() != 7 {
		t.Fatalf("id = %d", ep.ID())
	}
	if !ep.Emit(&protocol.ServerMessage{Type: protocol.ServerMsgProviderExec, QueryID: "q1"}) {
		t.Fatal("emit must succeed")
	}
	select {
	case <-s.writeCh:
	default:
		t.Fatal("emitted frame must be queued")
	}
	ep.NotifyRetired("pkg:a", []protocol.DriverProviderKey{"pkg:a"})
	select {
	case <-s.writeCh:
	default:
		t.Fatal("retire notice must be queued")
	}
}
