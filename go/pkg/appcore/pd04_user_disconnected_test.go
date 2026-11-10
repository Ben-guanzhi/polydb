package appcore

// PD-04（2026-11-09）回归测试：用户显式断开的连接，心跳（Ping）不得复活它。
//
// 背景：Ping 是断开连接事实上的重连器——对不可达主机每 30s 重建一次 SSH
// 隧道 + 连接。若用户在 UI 上点了「断开」，心跳仍会把它连回来，等于用户的
// 断开意图被后台悄悄推翻（且对不可达主机会无限重试）。
//
// 三条被钉住的语义：
//  1. Disconnect 之后 Ping 必须失败，且**不得**触发任何连接建立；
//  2. 显式 Connect 之后 Ping 恢复成功（心跳重新开始工作）；
//  3. execute 的懒连接语义不变——断开后首次查询仍可免显式 Connect 懒连上。
//
// 对端语义基线是 Rust 侧 rust/crates/app-core/src/lib.rs：
//   - disconnect 在 lib.rs:781 `user_disconnected.write().insert(id)`；
//   - connect 首行 lib.rs:563 `user_disconnected.write().remove(&id)`；
//   - ping 首行 lib.rs:811 判 `user_disconnected` 命中即返回
//     CoreError::Connection("connection is disconnected")。
// 两端判定范围必须严格镜像（AGENTS.md §7 双实现一致性红线）。

import (
	"context"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// TestPingDoesNotReviveUserDisconnectedConnection 钉住 (1)(2)：
// Disconnect → Ping 失败；Connect → Ping 成功。
func TestPingDoesNotReviveUserDisconnectedConnection(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	// 先连上，确保后面的失败不是因为「本来就连不上」。
	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := a.Ping(ctx, id); err != nil {
		t.Fatalf("ping after connect: %v", err)
	}

	a.Disconnect(id)

	// 断开后 Ping 必须失败。
	err := a.Ping(ctx, id)
	if err == nil {
		t.Fatalf("断开后 ping 仍成功——心跳把用户主动断开的连接复活了")
	}
	if code := codeOf(err); code != protocol.ErrConnectionFailed {
		t.Errorf("断开后 ping 的错误码 = %q，want %q（与 Rust 侧同文案同码）", code, protocol.ErrConnectionFailed)
	}

	// 显式 Connect 后 Ping 必须恢复。
	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if err := a.Ping(ctx, id); err != nil {
		t.Fatalf("显式重连后 ping 仍失败：%v", err)
	}
}

// TestPingAfterDisconnectDoesNotOpenConnection 钉住 (1) 的**强形式**：
// 断开后 Ping 不仅要报错，还必须**没有**把连接悄悄建起来
// （否则「报错」只是表象，连接照样建立，泄漏与重连都还在）。
func TestPingAfterDisconnectDoesNotOpenConnection(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("connect: %v", err)
	}
	a.Disconnect(id)

	if err := a.Ping(ctx, id); err == nil {
		t.Fatalf("断开后 ping 成功")
	}

	// 连接注册表里不该有这条连接——若 Ping 走了 ensure() 懒连接，这里就会有。
	a.mu.RLock()
	_, present := a.connections[id]
	a.mu.RUnlock()
	if present {
		t.Fatalf("断开后 ping 把连接建起来了（注册表里又有它了）——心跳仍在复活连接")
	}
}

// TestExecuteStillLazilyConnectsAfterDisconnect 钉住 (3)：
// execute 的懒连接语义**不受** user_disconnected 影响——首次查询仍可免显式
// Connect 连上。这是有意的：只有心跳（后台重连器）受约束，用户主动发起的
// 第一次查询不该被上一次的断开意图挡住。
func TestExecuteStillLazilyConnectsAfterDisconnect(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("connect: %v", err)
	}
	a.Disconnect(id)

	// 断开后直接查询：应当懒连接成功。
	if _, err := a.Execute(ctx, id, "SELECT 1"); err != nil {
		t.Fatalf("断开后首次查询失败（懒连接语义被破坏）：%v", err)
	}
}
