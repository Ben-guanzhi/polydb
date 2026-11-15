//go:build windows

package main

// selfLimitMemory 在 Windows 上由宿主的 Job Object 内存限制兜底
// （§16.16.2 Windows 行），provider 自身无需自限。
func selfLimitMemory() {}

// watchParentPID 在 Windows 上由 Job Object 的 KILL_ON_JOB_CLOSE 兜底
// （§16.16.1 Windows 行）：宿主退出 → Job 句柄关闭 → OS 强制终止本进程。
func watchParentPID(string) {}
