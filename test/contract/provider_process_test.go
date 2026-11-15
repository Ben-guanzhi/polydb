package contract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// buildContractProvider 编译参考 provider 进程（go/cmd/polydb-provider）。
// 契约测试用它当**真实的进程外 provider**：宿主经 stdio JSON-RPC 与它通信
// （behavior.md §16.12.2 / §16.13）。
func buildContractProvider(t *testing.T) string {
	t.Helper()
	name := "polydb-provider"
	if runtime.GOOS == "windows" {
		// Windows 的 exec 对无扩展名的文件不执行（只按 PATH 查找）。
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "github.com/polydb/polydb/cmd/polydb-provider")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build polydb-provider: %v\n%s", err, out)
	}
	return bin
}

// TestContractProviderProcess 覆盖 behavior.md §16.13 的进程端点：
// 宿主拉起独立 provider 进程，经 stdio JSON-RPC 转发 SQL，结果回到客户端；
// 进程被杀后在途查询立即失败（不挂起，§16.13.4）。
//
// 双端 parity：Go 后端与 Rust 后端都用同一个 provider 进程。
// TestContractProviderCallback 覆盖 behavior.md §16.13/§16.14 的进程端点：
// provider 进程**不管理连接、不接触凭据**，经仅回环的回调端点把 SQL 交回
// 宿主执行，拿到**真实结果**（表里的真实值，不是固定值）。
//
// §16.14.6 的其余场景（令牌错误被拒、handle 不可推导、只读判定对 provider
// 同样生效）在 pkg/server 的单元测试里覆盖（回调面是内部机制，不该从
// 客户端侧观测）。
func TestContractProviderCallback(t *testing.T) {
	bin := buildContractProvider(t)
	const key = "contract:cb"

	bs := backendsWithProviderProcs(t, "", []string{key}, []string{
		key + "=" + bin + " -key " + key + " -cap query",
	})
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			providerCallbackRealSQL(t, b, key)
		})
	}
}

// providerCallbackRealSQL 验证：provider 经回调执行真实 SQL 并拿到真实结果。
func providerCallbackRealSQL(t *testing.T, b *Backend, key string) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	c := NewClient(b.base)
	_, _, _ = c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE t(x INTEGER)"})
	_, _, _ = c.Do("POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO t VALUES(42)"})

	waitForProviderAttached(t, b, key)

	w := wsDial(t, b.base)
	defer w.Close()
	w.send(map[string]any{
		"type":          "query",
		"query_id":      "bbbbbbbb-0000-0000-0000-000000000001",
		"connection_id": id,
		"sql":           "SELECT x FROM t",
		"provider_key":  key,
	})
	typ, msg := w.recv(10 * time.Second)
	if typ == "query_started" {
		typ, msg = w.recv(10 * time.Second)
	}
	if typ != "query_result" {
		t.Fatalf("want query_result via callback, got %q: %v", typ, msg)
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("query_result missing result: %v", msg)
	}
	rows, _ := result["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("want 1 row from real SQL, got %v", rows)
	}
	row, _ := rows[0].([]any)
	if len(row) != 1 || fmt.Sprint(row[0]) != "42" {
		t.Fatalf("want real value [42], got %v", rows)
	}
}

// waitForProviderAttached 轮询直到进程端点可用（宿主启动时拉起进程，进程 hello
// 后才登记，§16.13.2）。
//
// 注意：探测期间**不能断开连接**——断开会让服务端撤回在途请求（withhold，
// §16.9.2），探测就永远等不到结果。每次用不同的 query_id，避免重复 id 被拒。
func waitForProviderAttached(t *testing.T, b *Backend, key string) {
	t.Helper()
	id := wsCreateSQLiteConn(t, NewClient(b.base))
	deadline := time.Now().Add(5 * time.Second)
	attempt := 0
	lastCode := ""
	lastMessage := ""
	for time.Now().Before(deadline) {
		attempt++
		// query_id 必须是合法 UUID（Rust 侧 ClientMessage::Query.query_id 是
		// uuid::Uuid；非法字符串解码失败）。
		qid := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", attempt)
		// 探测用独立连接：断开只会撤回**探测自己**的在途请求（withhold，
		// §16.9.2），不影响后续的真查询。
		w := wsDial(t, b.base)
		w.send(map[string]any{
			"type":          "query",
			"query_id":      qid,
			"connection_id": id,
			"sql":           "SELECT 1 AS v",
			"provider_key":  key,
		})
		typ, msg := w.recv(5 * time.Second)
		if typ == "query_started" {
			// 第一帧是 query_started（behavior.md §16.9.2）；结果在下一帧。
			typ, msg = w.recv(5 * time.Second)
		}
		w.Close()
		if typ == "query_result" {
			return // 已就绪
		}
		if typ == "query_error" {
			if e, ok := msg["error"].(map[string]any); ok {
				lastCode, _ = e["code"].(string)
				lastMessage, _ = e["message"].(string)
			}
			continue // 尚未就绪：provider 还没 attach / 能力未注册
		}
		t.Fatalf("unexpected frame while probing provider: %q: %v", typ, msg)
	}
	t.Fatalf("provider process %q never became ready (last %q: %s)", key, lastCode, lastMessage)
}
