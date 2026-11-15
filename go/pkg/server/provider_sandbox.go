package server

// 阶段 11：provider 侧沙箱——OS 级执行边界（behavior.md §16.16）。与 Rust 侧
// rust/crates/server/src/provider_sandbox.rs 严格镜像。
//
// 承诺（§16.16.1/§16.16.2）：
//   - 生命周期绑定：宿主死 → provider 必死（Windows Job Object
//     KILL_ON_JOB_CLOSE；Unix 进程组 + provider 自检父进程）。
//   - 资源上限：内存（Windows Job 内存限制；Unix provider 自限 RLIMIT_AS）。
// 不承诺（§16.16.3）：文件系统/网络访问隔离、恶意进程防御、CPU 硬限。
//
// 平台差异：Windows 侧需要 golang.org/x/sys/windows 的 Job Object API；Unix
// 侧用标准库 syscall（Setpgid）。Rust 侧对应 windows-sys / libc。

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// providerMemLimitBytes 返回 provider 进程的内存上限（字节）。
// POLYDB_PROVIDER_MEM_MB 可覆盖；默认 512 MiB（§16.16.2）。
func providerMemLimitBytes() uint64 {
	const def = uint64(512) << 20
	raw := os.Getenv("POLYDB_PROVIDER_MEM_MB")
	if raw == "" {
		return def
	}
	var mb int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &mb); err != nil || mb <= 0 {
		return def
	}
	return uint64(mb) << 20
}

// applySandboxToCmd 在启动前为 provider 进程配置 OS 级沙箱属性（平台相关）。
// 返回的 cleanup 在进程退出后由调用方负责（Windows 上 Job 句柄的释放）。
func applySandboxToCmd(cmd *exec.Cmd) (func(), error) {
	return applySandboxPlatform(cmd)
}

// sandboxChildEnv 是 provider 进程自检父进程所需的键（Unix：父死自退；
// §16.16.1）。Windows 上由 Job Object 兜底，无需自检。
const sandboxParentPIDKey = "POLYDB_PROVIDER_PARENT_PID"

// sandboxParentPIDEnv 返回要注入 provider 进程环境的父进程 PID 条目。
// 无父进程信息（理论不可达）时返回空。
func sandboxParentPIDEnv() string {
	return sandboxParentPIDKey + "=" + fmt.Sprint(os.Getpid())
}

// splitCommand 已在 provider_process.go 定义。
