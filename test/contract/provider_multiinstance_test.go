package contract

import (
	"testing"
	"time"
)

// TestContractProviderMultiInstance 覆盖 behavior.md §16.18 的多实例服务：
// 同 key 的两个 **commutative**（witnessed coeffect，Thm 47）provider 进程
// **并存**（不替换），宿主按轮询分派；一个实例退出后**其余实例继续服务**
// （continuous availability）——key 不降级、无需重新 attach。
func TestContractProviderMultiInstance(t *testing.T) {
	bin := buildContractProvider(t)
	const key = "contract:multi"

	// 两个实例同 key 并存；实例 A 处理第一次 exec 后自杀（-exit-after 1），
	// 模拟"一个 provider 中途退出"——退出窗口之后的查询必须全部由 B 服务。
	cmd := bin + " -key " + key + " -cap query -commutative"
	bs := backendsWithProviderProcs(t, "", []string{key}, []string{
		key + "=" + cmd + " -exit-after 1",
		key + "=" + cmd,
	})
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			multiInstanceAvailability(t, b, key)
		})
	}
}

// multiInstanceAvailability 的断言链：
//  1. 两个实例并存（探测查询连续成功——若 hub 误用替换语义，第二个 hello
//     会把第一个退役，轮询会命中退役端点而在途失败）；
//  2. 连续 5 个查询全部拿到 query_result——覆盖 A 的退出窗口：无论轮询
//     先命中谁，A 退出后的查询必须全部由 B 服务（若 hub 在实例退出时错误地
//     降级了整个 key，后续查询会收到 DRIVER_NOT_AVAILABLE）。
func multiInstanceAvailability(t *testing.T, b *Backend, key string) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))

	waitForProviderAttached(t, b, key)
	// 给第二个实例的 hello 留到达时间（两个进程同时 spawn，hello 顺序无保证；
	// 探测查询只证明至少一个就绪）。200ms 足够本机进程握手。
	time.Sleep(200 * time.Millisecond)

	w := wsDial(t, b.base)
	defer w.Close()
	for i := 1; i <= 5; i++ {
		qid := "cccccccc-0000-0000-0000-" + itoa12(i)
		w.send(map[string]any{
			"type":          "query",
			"query_id":      qid,
			"connection_id": id,
			"sql":           "SELECT 1 AS v",
			"provider_key":  key,
		})
		typ, msg := w.recv(10 * time.Second)
		if typ == "query_started" {
			typ, msg = w.recv(10 * time.Second)
		}
		if typ != "query_result" {
			t.Fatalf("query %d: want query_result (continuous availability), got %q: %v", i, typ, msg)
		}
	}
}

// itoa12 生成 12 位零填充编号（query_id 的最后一段）。
func itoa12(n int) string {
	s := "000000000000"
	d := []byte(s)
	for i := len(d) - 1; i >= 0 && n > 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d)
}
