package dbsqlite

import (
	"context"

	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// init 把 SQLite 的打开动作登记进 polydb 的驱动分派注册表（PD-01/PD-02）。
// appcore 不再手写 kind switch，新增引擎退化为「本文件登记一项」。
func init() {
	pdb.RegisterDriver(protocol.DatabaseKindSQLite, openFromInfo)
}

// openFromInfo 由注册表调用：SQLite 无网络地址，database 字段即文件路径，
// 空路径落 :memory:（与原 appcore switch 的行为一致）。
func openFromInfo(ctx context.Context, info protocol.ConnectionInfo, _ string) (pdb.Driver, error) {
	path := info.Database
	if path == "" {
		path = ":memory:"
	}
	c, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return NewProtocolDriver(c), nil
}
