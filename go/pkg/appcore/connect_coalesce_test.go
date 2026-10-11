package appcore

// 懒连接并发归并回归（第二轮 §1.2 的 P1）：
// ensure 在 RLock 未命中后进入 singleflight，保证同一 id 的并发首次使用只跑
// 一次 Connect（SSH 隧道 + 拨号 + Ping），其余共享其结果。没有归并时两个
// goroutine 各开一条隧道/连接，后进者覆盖先到者再 Close：不泄漏但白做一遍，
// 且先到者的调用方可能拿到"连接失败"的假错误。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/polydb/polydb/pkg/dbcore"
)

// TestConcurrentEnsureCoalescesToSingleConnection 并发首次 ensure 同一 id：
// 所有调用都必须成功返回，且注册表里恰好一条连接、驱动指针一致。
func TestConcurrentEnsureCoalescesToSingleConnection(t *testing.T) {
	a, id := newSQLiteApp(t)
	ctx := context.Background()

	if a.IsConnected(id) {
		t.Fatalf("测试前提不成立：连接已经打开")
	}

	const n = 16
	var wg sync.WaitGroup
	var failures atomic.Int32
	drivers := make([]dbcore.Driver, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := a.ensure(ctx, id)
			if err != nil {
				failures.Add(1)
				return
			}
			drivers[i] = c.Driver()
		}(i)
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("并发 ensure 失败 %d 次（应为 0；归并没生效会让先到者拿到假错误）", failures.Load())
	}
	a.mu.RLock()
	count := len(a.connections)
	a.mu.RUnlock()
	if count != 1 {
		t.Fatalf("注册表连接数 = %d，want 1", count)
	}
	for i := 1; i < n; i++ {
		if drivers[i] != drivers[0] {
			t.Fatalf("第 %d 个调用方拿到的驱动与第 0 个不同（归并没生效）", i)
		}
	}
}
