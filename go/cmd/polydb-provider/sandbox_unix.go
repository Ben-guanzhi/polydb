//go:build !windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// selfLimitMemory 自限地址空间（RLIMIT_AS，§16.16.2 Unix 行）。
// 宿主通过 POLYDB_PROVIDER_MEM_MB 下发上限；provider 自己应用——这是 Unix 上
// 「fork 时替子进程设 rlimit」不可用时的最小可行形态。
func selfLimitMemory() {
	mb := os.Getenv("POLYDB_PROVIDER_MEM_MB")
	if mb == "" {
		mb = "512"
	}
	n, err := strconv.Atoi(mb)
	if err != nil || n <= 0 {
		return
	}
	limit := syscall.Rlimit{Cur: uint64(n) << 20, Max: uint64(n) << 20}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &limit); err != nil {
		// 自限失败不阻断启动（沙箱尽力而为，§16.16.3）。
		fmt.Fprintln(os.Stderr, "polydb-provider: setrlimit:", err)
	}
}

// watchParentPID 自检父进程（§16.16.1 Unix 行）：宿主死亡后本进程被 init
// 收养（getppid 变化）→ 自行退出。轮询 2s 一次，足够及时且开销可忽略。
func watchParentPID(parentPID string) {
	want, err := strconv.Atoi(parentPID)
	if err != nil {
		return
	}
	for {
		if os.Getppid() != want {
			fmt.Fprintln(os.Stderr, "polydb-provider: host exited, exiting")
			os.Exit(0)
		}
	}
}
