package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/polydb/polydb/pkg/protocol"
)

// buildTestProvider 编译参考 provider 进程（cmd/polydb-provider）供单测使用。
//
// 用 go build 而不是 go run：go run 会引入额外的编译进程与时序抖动，且在
// -race 下更慢。产物放在临时目录，测试结束自动清理。
func buildTestProvider(t *testing.T) string {
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

// TestProcessEndpointForwardsResults 验证进程端点：拉起参考 provider 进程，
// hello 声明后建立登记，exec 的结果经 stdio 回到宿主（§16.13.2/§16.13.3）。
func TestProcessEndpointForwardsResults(t *testing.T) {
	bin := buildTestProvider(t)
	hub := NewProviderHubWithAllowed("proc:ok")
	cmdLine := bin + " -key proc:ok -cap query -rows 2"

	ep, err := NewProcessEndpoint(hub.NextEndpointID(), hub, cmdLine, "proc:ok")
	if err != nil {
		t.Fatalf("spawn provider: %v", err)
	}
	ep.onDead = func() { hub.DropEndpoint(ep) }
	defer ep.Close()

	// hello 声明后应自动建立登记（最多等 3s）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ProviderEndpoint("proc:ok") != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ProviderEndpoint("proc:ok") == nil {
		t.Fatal("provider process did not attach after hello")
	}

	// exec：结果经 stdio 回到宿主。
	wait, _ := hub.RegisterPending("q1", "proc:ok")
	execMsg := &protocol.ServerMessage{
		Type:    protocol.ServerMsgProviderExec,
		QueryID: "q1",
		SQL:     "SELECT 1",
	}
	if !ep.Emit(execMsg) {
		t.Fatal("emit must succeed")
	}
	select {
	case out := <-wait:
		if out.err != nil {
			t.Fatalf("unexpected error: %v", out.err)
		}
		if out.result == nil {
			t.Fatal("expected a result")
		}
		if len(out.result.Rows) != 2 {
			t.Fatalf("want 2 rows, got %d", len(out.result.Rows))
		}
		if len(out.result.Columns) != 1 || out.result.Columns[0].Name != "v" {
			t.Fatalf("unexpected columns: %+v", out.result.Columns)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provider result")
	}
}

// TestProcessEndpointUnreachableAfterExit 验证进程退出后端点不可达：
// 在途请求失败（不挂起，§16.13.4），登记被移除。
func TestProcessEndpointUnreachableAfterExit(t *testing.T) {
	bin := buildTestProvider(t)
	hub := NewProviderHubWithAllowed("proc:die")
	// -exit-after 1：处理一条 exec 后立即退出（模拟崩溃）。
	cmdLine := bin + " -key proc:die -cap query -exit-after 1"

	ep, err := NewProcessEndpoint(hub.NextEndpointID(), hub, cmdLine, "proc:die")
	if err != nil {
		t.Fatalf("spawn provider: %v", err)
	}
	ep.onDead = func() { hub.DropEndpoint(ep) }
	defer ep.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ProviderEndpoint("proc:die") != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ProviderEndpoint("proc:die") == nil {
		t.Fatal("provider process did not attach after hello")
	}

	wait, _ := hub.RegisterPending("q1", "proc:die")
	_ = ep.Emit(&protocol.ServerMessage{
		Type:    protocol.ServerMsgProviderExec,
		QueryID: "q1",
		SQL:     "SELECT 1",
	})
	select {
	case out := <-wait:
		// 进程可能先回结果再退出，也可能直接死掉——两种都合规：
		// 关键是不挂起（§16.13.4）。
		if out.err == nil && out.result == nil {
			t.Fatal("outcome must carry either a result or an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request must not hang after provider exit")
	}

	// 进程退出后登记被移除。
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ProviderEndpoint("proc:die") == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("provider registration must be removed after process exit")
}

// TestProviderProcSpecsParsing 验证 POLYDB_PROVIDER_PROCS 的解析
// （§16.13.1：key=命令，逗号分隔；畸形项被忽略；同 key 多条保留——§16.18）。
func TestProviderProcSpecsParsing(t *testing.T) {
	t.Setenv("POLYDB_PROVIDER_PROCS", "a=/bin/a, b=/bin/b --x ,malformed,=noname,c=,a=/bin/a2")
	specs := providerProcSpecs()
	if len(specs) != 3 {
		t.Fatalf("want 3 specs (multi-instance keeps both a-entries), got %+v", specs)
	}
	if specs[0].key != "a" || specs[0].cmdLine != "/bin/a" {
		t.Fatalf("spec0 = %+v", specs[0])
	}
	if specs[1].key != "b" || !strings.HasPrefix(specs[1].cmdLine, "/bin/b") {
		t.Fatalf("spec1 = %+v", specs[1])
	}
	if specs[2].key != "a" || specs[2].cmdLine != "/bin/a2" {
		t.Fatalf("spec2 = %+v", specs[2])
	}
}
