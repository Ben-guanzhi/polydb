package tui

// 在途查询取消（F8）的回归。真实取消要连服务端（见
// backend/adapter_remote_test.go 的 httptest + Bearer msgpack 证明），这里只钉
// 住状态机与「没有在途查询」这条分支。

import (
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
)

// Adapter 必须实现 Canceller，否则 F8 走不到 ExecuteQuery/CancelQuery。
var _ backend.Canceller = (*backend.Adapter)(nil)

func TestInFlightQuerySetAndClear(t *testing.T) {
	SetInFlightQuery("q1")
	if got := currentInFlight(); got != "q1" {
		t.Fatalf("记录在途 ID = %q, want %q", got, "q1")
	}
	ClearInFlightQuery("q1")
	if got := currentInFlight(); got != "" {
		t.Fatalf("清掉后应为空白, got %q", got)
	}
}

// TestInFlightClearIgnoresStaleID pins that a finishing query only clears its
// own round: without this, a slow query ending after a newer one started would
// wipe the ID F8 needs.
func TestInFlightClearIgnoresStaleID(t *testing.T) {
	SetInFlightQuery("q2")
	ClearInFlightQuery("q1") // 上一轮结束时清的是自己那个 ID
	if got := currentInFlight(); got != "q2" {
		t.Fatalf("过期 ID 不应清掉当前在途查询, got %q", got)
	}
	ClearInFlightQuery("q2")
	if got := currentInFlight(); got != "" {
		t.Fatalf("收尾后应为空, got %q", got)
	}
}

// TestCancelInFlightWithoutQuery pins the empty state: F8 with nothing running
// says so instead of firing a cancel at the server.
func TestCancelInFlightWithoutQuery(t *testing.T) {
	ClearInFlightQuery(currentInFlight()) // 保证起点干净
	msg := CancelInFlightQuery(nil)()
	tm, ok := msg.(ToastMsg)
	if !ok {
		t.Fatalf("消息类型 = %T, want ToastMsg", msg)
	}
	if tm.Text != "没有在途查询可取消" {
		t.Fatalf("提示 = %q, want %q", tm.Text, "没有在途查询可取消")
	}
}
