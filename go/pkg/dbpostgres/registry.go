package dbpostgres

import (
	"context"
	"net/url"
	"strconv"

	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// init 把 PostgreSQL 的打开动作登记进 polydb 的驱动分派注册表（PD-01/PD-02）。
func init() {
	pdb.RegisterDriver(protocol.DatabaseKindPostgres, openFromInfo)
}

// openFromInfo 由注册表调用：DSN 方言知识只存在于本驱动包（AGENTS.md §12）。
func openFromInfo(ctx context.Context, info protocol.ConnectionInfo, password string) (pdb.Driver, error) {
	c, err := Open(ctx, dsn(info, password))
	if err != nil {
		return nil, err
	}
	return NewProtocolDriver(c), nil
}

// dsn 构造 postgres://user:pass@host:port/dbname?sslmode=disable。
func dsn(info protocol.ConnectionInfo, password string) string {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	if port == 0 {
		port = 5432
	}
	dbname := info.Database
	if dbname == "" {
		dbname = "postgres"
	}
	userinfo := url.User(info.Username)
	if password != "" {
		userinfo = url.UserPassword(info.Username, password)
	}
	return "postgres://" + userinfo.String() + "@" + host + ":" + strconv.Itoa(port) + "/" + dbname + "?sslmode=disable"
}
