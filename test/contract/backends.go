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
	// hub 仅 Go 后端非 nil（进程内 server 的 ProviderHub）：测试观测 hub 层
	// 状态用（如 §16.19 配置对账的撤下证据）。Rust 后端为 nil。
	hub *server.ProviderHub
}

// backends 提供全部可用后端：Go 进程内 + Rust 二进制（若已构建）。
func backends(t *testing.T) []*Backend {
	t.Helper()
	return backendsWithToken(t, "")
}

// backendsWithPolicy 启动带 provider attach 白名单的后端（allow 为 key 列表；
// nil = 不授权任何 key，attach 一律被拒）。
func backendsWithPolicy(t *testing.T, token string, allow []string) []*Backend {
	t.Helper()
	return backendsWithProviderProcs(t, token, allow, nil)
}

// backendsWithProviderProcs 启动带 attach 白名单与 provider 进程配置的后端。
//
// providerProcs 是原始 `key=命令` 对的列表（behavior.md §16.13.1）：宿主按
// 配置拉起 provider 进程，客户端不能指定命令。**同 key 可出现多条**——
// commutative key（§16.18）的多实例即由此配置。nil = 不配置任何进程。
func backendsWithProviderProcs(t *testing.T, token string, allow []string, providerProcs []string) []*Backend {
	t.Helper()
	out := []*Backend{startGoBackendWithPolicy(t, token, allow, providerProcs)}
	if b, err := startRustBackendWithPolicy(t, token, allow, providerProcs); err != nil {
		t.Logf("rust backend skipped: %v", err)
	} else {
		out = append(out, b)
	}
	return out
}

// backendWithProviderConfigFile 启动双后端，配置完全由**配置文件**驱动
// （§16.19）：POLYDB_PROVIDER_CONFIG_FILE + 短轮询周期。白名单/进程表经
// 文件对账增量生效。
func backendWithProviderConfigFile(t *testing.T, cfgPath string) []*Backend {
	t.Helper()
	out := []*Backend{startGoBackendWithConfigFile(t, cfgPath)}
	if b, err := startRustBackendWithConfigFile(t, cfgPath); err != nil {
		t.Logf("rust backend skipped: %v", err)
	} else {
		out = append(out, b)
	}
	return out
}

func startGoBackendWithConfigFile(t *testing.T, cfgPath string) *Backend {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "contract-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	// §16.19：配置对账的源头是文件（在 server 构造前生效——New 时启动 watcher）。
	// 初始白名单为空：由文件首次读取覆盖。
	t.Setenv("POLYDB_PROVIDER_CONFIG_FILE", cfgPath)
	t.Setenv("POLYDB_PROVIDER_CONFIG_POLL_MS", "200")
	srv := server.NewWithToken(appcore.New(db, kr), "")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
		db.Close()
	})
	return &Backend{name: "go", base: ts.URL, hub: srv.Hub()}
}

func startRustBackendWithConfigFile(t *testing.T, cfgPath string) (*Backend, error) {
	t.Helper()
	bin := rustBinary()
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("rust server binary not found at %s", bin)
	}
	addr := freeAddr(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"POLYDB_ADDR="+addr,
		"POLYDB_DATA_DIR="+t.TempDir(),
		"POLYDB_PROVIDER_CONFIG_FILE="+cfgPath,
		"POLYDB_PROVIDER_CONFIG_POLL_MS=200",
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	// 诊断用：POLYDB_CONTRACT_VERBOSE=1 时透传后端日志（与
	// startRustBackendWithPolicy 的机制一致）。
	if os.Getenv("POLYDB_CONTRACT_VERBOSE") != "" {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	// 记录 stderr 尾部供诊断（diag_config_test 打印）。
	rustServerStderrTail = func() string {
		s := out.String()
		if len(s) > 2000 {
			return s[len(s)-2000:]
		}
		return s
	}
	_ = rustServerStderrTail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	base := "http://" + addr
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := waitForHealth(base, 15*time.Second); err != nil {
		return nil, err
	}
	return &Backend{name: "rust", base: base}, nil
}

// rustServerStderrTail 是诊断辅助：最近一次启动的 Rust 后端 stderr 尾部
// （diag_config_test 打印；非诊断路径不读）。
var rustServerStderrTail = func() string { return "" }

func startGoBackend(t *testing.T, token string) *Backend {
	t.Helper()
	return startGoBackendWithPolicy(t, token, nil, nil)
}

func startGoBackendWithPolicy(t *testing.T, token string, allow []string, providerProcs []string) *Backend {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "contract-test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	// provider 进程与 attach 白名单都在 server 构造前生效（New 读它们，
	// §16.10.1 / §16.13.1）。用 t.Setenv 而非注入：进程端点注册在构造时
	// 创建的 hub 上，事后替换 hub 会丢掉它们。
	t.Setenv("POLYDB_PROVIDER_ALLOW", strings.Join(allow, ","))
	if len(providerProcs) > 0 {
		t.Setenv("POLYDB_PROVIDER_PROCS", strings.Join(providerProcs, ","))
	}
	srv := server.NewWithToken(appcore.New(db, kr), token)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		// Close 终止并回收 provider 进程（§16.13.4：不留僵尸、不锁住
		// provider 二进制文件——否则临时目录清理会失败）。
		srv.Close()
		ts.Close()
		db.Close()
	})
	return &Backend{name: "go", base: ts.URL}
}

// （多实例：同 key 可出现多条，§16.18——直接 strings.Join 原始 pair。）

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

func startRustBackend(t *testing.T, token string) (*Backend, error) {
	t.Helper()
	return startRustBackendWithPolicy(t, token, nil, nil)
}

// startRustBackendWithPolicy 启动带 provider attach 白名单与 provider 进程配置的
// Rust 后端。白名单经 POLYDB_PROVIDER_ALLOW 传入，进程配置经
// POLYDB_PROVIDER_PROCS 传入（deny-by-default）。
func startRustBackendWithPolicy(t *testing.T, token string, allow []string, providerProcs []string) (*Backend, error) {
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
		"POLYDB_PROVIDER_ALLOW="+strings.Join(allow, ","),
		"POLYDB_PROVIDER_PROCS="+strings.Join(providerProcs, ","),
	)
	if token != "" {
		cmd.Env = append(cmd.Env, "POLYDB_SERVER_TOKEN="+token)
	}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	// 诊断用：POLYDB_CONTRACT_VERBOSE=1 时把后端日志透传到测试输出
	//（Rust server 的 tracing 与 provider 进程的问题都体现在这里）。
	if os.Getenv("POLYDB_CONTRACT_VERBOSE") != "" {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
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
