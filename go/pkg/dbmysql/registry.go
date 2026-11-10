package dbmysql

import (
	"context"
	"strconv"

	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// init 把 MySQL 的打开动作登记进 polydb 的驱动分派注册表（PD-01/PD-02）。
func init() {
	pdb.RegisterDriver(protocol.DatabaseKindMySQL, openFromInfo)
}

// openFromInfo 由注册表调用：DSN 方言知识只存在于本驱动包（AGENTS.md §12）。
func openFromInfo(ctx context.Context, info protocol.ConnectionInfo, password string) (pdb.Driver, error) {
	c, err := Open(ctx, dsn(info, password))
	if err != nil {
		return nil, err
	}
	return NewProtocolDriver(c), nil
}

// dsn 构造 user:pass@tcp(host:port)/dbname?parseTime=true
//
// `parseTime=true` 是**必须**的：共享内核的 MySQL 连接规范
// （dbcore/mysql/spec.go:56 `cfg.ParseTime = true`；其 spec_test.go:36 还
// 断言了这一条）要求 DATE/DATETIME/TIMESTAMP 列扫成 time.Time。
//
// 本包原先手拼 DSN 时**漏了这一项**，于是 DATETIME 列以 MySQL 原生文本
// 形态（"2026-11-10 07:08:09"）返回——而 Rust 侧 sqlx 给的是类型化的
// NaiveDateTime，两端形态因此不同（PD-26，2026-11-10）。
// 加上后 Go 侧同样走 time.Time 路径，与 Rust 侧同形（PD-20 的 RFC3339）。
//
// 注：内核的 `DSN(spec)` 还设了 ClientFoundRows/超时/TLS 等项；本包
// 目前只补齐 parseTime 以对齐时间列形态，其余项的收口另议。
func dsn(info protocol.ConnectionInfo, password string) string {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	if port == 0 {
		port = 3306
	}
	dbname := info.Database
	if dbname == "" {
		dbname = "mysql"
	}
	return info.Username + ":" + password + "@tcp(" + host + ":" + strconv.Itoa(port) + ")/" + dbname + "?parseTime=true"
}
