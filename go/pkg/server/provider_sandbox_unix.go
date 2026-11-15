//go:build !windows

package server

import (
	"fmt"
	"os/exec"
	"syscall"
)

// applySandboxPlatform 把 provider 进程放入独立**进程组**（Setpgid）：
//
//   - 进程组隔离：provider 无法影响宿主进程组的信号与作业控制（§16.16.1）；
//   - 会话收尾可对整个进程组发信号（进程树级回收的 Unix 形态）。
//
// 内存上限由 provider 进程**启动时自限**（RLIMIT_AS，§16.16.2）——Go 的
// 标准库无法在 fork 时替子进程设置 rlimit，自限是 Unix 上的最小可行形态；
// provider 的参考实现（cmd/polydb-provider）读取同一约定并自行应用。
func applySandboxPlatform(cmd *exec.Cmd) (func(), error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// Unix 的进程组没有「宿主退出即全灭」的内核机制（Linux 可用
	// prctl PDEATHSIG，但那是 Linux 专属且 Rust/Go 双端语义要一致）；
	// 生命周期绑定由 provider 的父进程自检补齐（§16.16.1 Unix 行）。
	return func() {}, nil
}
