package contract

// 契约测试的**后端进程回收**（PD-27，2026-11-10）。
//
// 背景：本波为 net 场景补了 6 个新测试文件后，契约测试全量在真实五库下
// 撞上了 go test 的默认 10 分钟超时（表现为 "FAIL ... 600.313s"，且
// **所有具名测试都是 PASS**——只是整体没跑完）。
//
// 根因不是测试慢，而是**后端进程泄漏**：`startRustBackend` 每次测试都起
// 一个 `polydb-server` 进程（`backends.go:71`），靠 `t.Cleanup` 里的
// `cmd.Process.Kill()` 回收。但：
//
//   1. Go 的测试**超时**（或 panic）时，`t.Cleanup` **不执行**——
//      进程留下；
//   2. `cmd.Wait()` 与 `Process.Kill()` 在 Windows 上对已退出的进程
//      可能阻塞；
//   3. 于是每跑一轮就攒一批 `polydb-server`（实测累积到 **200 个**），
//      Windows 上进程创建越来越慢 → 越跑越慢 → 更容易超时 → 泄漏更多，
//      恶性循环。
//
// 本文件提供两个机制：
//
//   - `TestMain`：整个包的测试跑完后（含超时/panic）兜底清掉所有本包
//     启动的后端进程；
//   - `killRustBackends`：Windows 上按镜像名匹配并清理，避免只 Kill 父
//     进程而留下子进程。
//
// 同时给 CI 与 Makefile 的契约测试命令加了 `-timeout 30m`（见 Makefile 与
// .github/workflows/ci.yml）——单测本身不需要那么久，是泄漏拖慢的。

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// rustBackendMarker 是本包启动 Rust 后端时在进程命令行里留下的标记
// （POLYDB_ADDR / POLYDB_DATA_DIR 是 Rust server 的专属环境变量，
// Windows 上可用 wmic/PowerShell 的 CommandLine 匹配）。
const rustBackendMarker = "polydb-server.exe"

// TestMain 兜底清理：包内测试全部结束（含超时）后杀掉遗留的 Rust 后端。
func TestMain(m *testing.M) {
	code := m.Run()
	killRustBackends()
	os.Exit(code)
}

// killRustBackends 杀掉所有仍在运行的 Rust 后端进程。
//
// 用 taskkill 而非 os.Process：Windows 上 `Process.Kill` 只作用于创建时
// 拿到的那一个进程，Rust server 若派生了子进程就漏掉了；而 taskkill /T
// 按进程树清理。
func killRustBackends() {
	// taskkill /F /IM polydb-server.exe —— 按镜像名杀全部同名进程。
	// 注意：只在 Windows 上用；其它平台由 t.Cleanup 的 Kill 兜底即可。
	out, err := exec.Command("taskkill", "/F", "/IM", rustBackendMarker).CombinedOutput()
	if err == nil || strings.Contains(string(out), "not found") {
		return
	}
	// taskkill 不存在（非 Windows）时忽略——那不是错误。
}
