// Package usability 已在内核（dbcore/usability）实现；polydb 只做接线：
// server 进程按环境变量开启（POLYDB_USABILITY=1/true），把 HTTP 与 WS 的
// execute 收口为 query 任务事件。默认关闭时全部调用是 no-op。
package server

import (
	"time"

	"github.com/dbcore/dbcore/usability"
)

// us 是 server 进程共享的记录器；nil 表示未开启。
var us *usability.Recorder

// InitUsability 在进程启动时装上记录器（NewServer 里调用一次）。
func InitUsability(dataDir string) {
	us = usability.Open("polydb-server", dataDir+"/usability.ndjson")
}

// recordQuery 记一次 execute（HTTP 或 WS 共用）。started 由调用方在派发前取。
func recordQuery(started time.Time, err error, cancelled bool, rows int, statementType string) {
	if us == nil {
		return
	}
	outcome := "ok"
	switch {
	case cancelled:
		outcome = "cancelled"
	case err != nil:
		outcome = "error"
	}
	us.Record("query", outcome, time.Since(started), map[string]string{
		"statement_type": statementType,
		"rows":           itoa(rows),
		"truncated":      "false",
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
