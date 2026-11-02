package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 黑洞兜底的三个形状（behavior.md §2.3：timeout_ms=0 是合法的"无超时"，
// 因此兜底只作用于调用方 ctx 不带 deadline 的请求，且可关闭）：
//
//  1. ctx 无 deadline + 服务端挂死 → 兜底到点返回错误，不再永不返回；
//  2. 兜底关闭（SetRequestTimeout(0)）→ 完全跟随调用方 ctx（Background 即等到底）；
//  3. 调用方 ctx 自带 deadline → 兜底不叠加，调用方的 deadline 生效。

func hungServer(t *testing.T, hold time.Duration) *httptest.Server {
	t.Helper()
	// The hold is released via a test signal, NOT r.Context().Done(): net/http
	// only cancels a handler's context on client disconnect while it is
	// background-reading the socket, and a handler that never reads the body
	// (our black-hole server) may not notice the abort at all. Relying on
	// context detection made the test wall time equal the hold time.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(hold):
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(func() { close(release) })
	t.Cleanup(srv.Close)
	return srv
}

func TestRemoteBackstopBoundsHungServer(t *testing.T) {
	srv := hungServer(t, 5*time.Second)
	r := NewRemote(srv.URL)
	r.SetRequestTimeout(50 * time.Millisecond)

	start := time.Now()
	_, err := r.ExecuteQuery(context.Background(), "", "c1", "SELECT 1")
	if err == nil {
		t.Fatal("hung server must yield an error under the backstop")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("backstop did not fire in time: %v", d)
	}
}

func TestRemoteBackstopDisabledFollowsCallerCtx(t *testing.T) {
	srv := hungServer(t, 150*time.Millisecond)
	r := NewRemote(srv.URL)
	r.SetRequestTimeout(0) // off: timeout_ms=0 semantics stay reachable

	if _, err := r.ExecuteQuery(context.Background(), "", "c1", "SELECT 1"); err != nil {
		t.Fatalf("disabled backstop must not cut a normal (slow) request: %v", err)
	}
}

func TestRemoteCallerDeadlineWinsOverBackstop(t *testing.T) {
	srv := hungServer(t, 5*time.Second)
	r := NewRemote(srv.URL)
	// backstop 10min stays at default; the caller's 50ms deadline must fire first
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.ExecuteQuery(ctx, "", "c1", "SELECT 1")
	if err == nil {
		t.Fatal("caller deadline must yield an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("caller deadline did not fire in time: %v", d)
	}
}
