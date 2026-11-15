//go:build windows

package server

import (
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sandboxCmd 是沙箱需要的最小命令面（避免直接依赖 *exec.Cmd，便于测试）。
type sandboxCmd interface {
	Process() *os.Process
}

// applySandboxPlatform 把 provider 进程加入 Windows Job Object：
//
//   - JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE：宿主进程退出（含被杀）→ Job 句柄
//     关闭 → OS 强制终止 Job 内的全部进程（§16.16.1 生命周期绑定）。
//   - JOB_OBJECT_LIMIT_PROCESS_MEMORY：内存上限（§16.16.2）。
//
// 返回的 cleanup 释放 Job 句柄（进程退出后由宿主调用；KILL_ON_JOB_CLOSE
// 意味着句柄关闭本身就会终止 provider，显式 Kill 是兜底）。
func applySandboxPlatform(cmd *exec.Cmd) (func(), error) {
	if cmd.Process == nil {
		return func() {}, fmt.Errorf("sandbox: process not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
				windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY,
		},
		ProcessMemoryLimit: uintptr(providerMemLimitBytes()),
	}
	if _, err := windows.SetInformationJobObject(
		job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), 0,
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	if err := windows.AssignProcessToJobObject(windows.Handle(job), windows.Handle(cmd.Process.Pid)); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign to job object: %w", err)
	}
	cleanup := func() {
		// KILL_ON_JOB_CLOSE：句柄关闭即终止 Job 内全部进程。显式 Terminate
		// 是兜底（宿主还活着、但 provider 卡死时由 §16.13.4 的回收路径调用）。
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	return cleanup, nil
}
