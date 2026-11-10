package dbredis

import (
	"context"
	"fmt"
	"strconv"

	dbcore "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// init 把 Redis 的打开动作登记进 polydb 的驱动分派注册表（PD-01/PD-02）。
func init() {
	dbcore.RegisterDriver(protocol.DatabaseKindRedis, openFromInfo)
}

// openFromInfo 由注册表调用：地址与 db 编号的解析只存在于本驱动包。
func openFromInfo(ctx context.Context, info protocol.ConnectionInfo, password string) (dbcore.Driver, error) {
	idx, err := dbIndex(info)
	if err != nil {
		return nil, err
	}
	return Open(ctx, addr(info), idx, password)
}

// addr 返回 host:port，缺省 localhost:6379。
func addr(info protocol.ConnectionInfo) string {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	if port == 0 {
		port = 6379
	}
	return host + ":" + strconv.Itoa(port)
}

// dbIndex 用连接信息里的 database 字段存 Redis db 编号（"0".."15"）。
//
// 解析纪律（PD-16，2026-11-09）：**非法输入必须报错，不得静默回落 0**。
// 静默回落会让用户连到 db 0 而不是他自己填的库——数据写进错误的库，且
// 无任何提示。Rust 侧 redis_db_index 同此纪律（两端行为必须一致，
// AGENTS.md §14 红线）。
//
// 规则：空串 → 0；纯十进制数字 → 该值；其余（含空白、正负号、小数点、
// 非 ASCII 数字、超出 u32 上界）→ 报错。超出 u32 上界的输入即便 Redis
// 集群模式允许更大 db 号，本项目两端都用 u32 承载，故同样报错而非回落。
func dbIndex(info protocol.ConnectionInfo) (int, error) {
	raw := info.Database
	if raw == "" {
		return 0, nil
	}
	idx, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, &protocol.PolyDBError{
			Code:    protocol.ErrInvalidParam,
			Message: fmt.Sprintf("invalid redis db number %q (expected 0..4294967295)", raw),
		}
	}
	return int(idx), nil
}
