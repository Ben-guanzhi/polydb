package contract

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
)

// Backend 表示一个被测试的服务端实现。
type Backend struct {
	name string
	base string
}

// backends 提供全部可用后端：Go 进程内 + Rust 二进制（若已构建）。
func backends(t *testing.T) []*Backend {
	t.Helper()
	return backendsWithToken(t, "")
}

// backendsWithToken 同 backends，但服务端以指定 token 启用 Bearer 鉴权（behavior.md §12.1）。
func backendsWithToken(t *testing.T, token string) []*Backend {
	t.Helper()
	out := []*Backend{startGoBackend(t, token)}
	if b, err := startRustBackend(t, token); err != nil {
		t.Logf("rust backend skipped: %v", err)
	} else {
		out = append(out, b)
	}
	return out
}

func startGoBackend(t *testing.T, token string) *Backend {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "contract-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	srv := httptest.NewServer(server.NewWithToken(appcore.New(db, kr), token).Handler())
	t.Cleanup(func() {
		srv.Close()
		db.Close()
	})
	return &Backend{name: "go", base: srv.URL}
}

func startRustBackend(t *testing.T, token string) (*Backend, error) {
	t.Helper()
	bin := rustBinary()
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("rust server binary not found at %s (build with: cd rust && cargo build -p polydb-server)", bin)
	}
	addr := freeAddr(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"POLYDB_ADDR="+addr,
		"POLYDB_DATA_DIR="+t.TempDir(),
	)
	if token != "" {
		cmd.Env = append(cmd.Env, "POLYDB_SERVER_TOKEN="+token)
	}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start rust server: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	base := "http://" + addr
	// 30s 窗口：Rust debug 构建的 axum server 在 Windows 上首次启动可能 >10s
	// （tokio 冷启动 + axum 路由表初始化），给足余量避免 CI 上 flaky。
	if err := waitForHealth(base, 30*time.Second); err != nil {
		state := ""
		if cmd.ProcessState != nil {
			state = cmd.ProcessState.String()
		}
		return nil, fmt.Errorf("rust server not ready: %v; proc=%s pid=%d bin=%s addr=%s; logs:\n%s",
			err, state, cmd.Process.Pid, bin, addr, out.String())
	}
	return &Backend{name: "rust", base: base}, nil
}

func rustBinary() string {
	if b := os.Getenv("POLYDB_RUST_BIN"); b != "" {
		return b
	}
	exe := "polydb-server"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return filepath.Join("..", "..", "rust", "target", "debug", exe)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitForHealth(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("health check timed out")
}
