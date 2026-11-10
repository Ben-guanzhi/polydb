package dbmssql

import (
	"context"
	"net/url"
	"strconv"

	pdb "github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// init 把 SQL Server 的打开动作登记进 polydb 的驱动分派注册表（PD-01/PD-02）。
func init() {
	pdb.RegisterDriver(protocol.DatabaseKindMSSQL, openFromInfo)
}

// openFromInfo 由注册表调用：DSN 方言知识只存在于本驱动包（AGENTS.md §12）。
func openFromInfo(ctx context.Context, info protocol.ConnectionInfo, password string) (pdb.Driver, error) {
	c, err := Open(ctx, dsn(info, password))
	if err != nil {
		return nil, err
	}
	return NewProtocolDriver(c), nil
}

// dsn 构造 sqlserver://user:pass@host:port?database=...。
func dsn(info protocol.ConnectionInfo, password string) string {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	if port == 0 {
		port = 1433
	}
	u := &url.URL{Scheme: "sqlserver", Host: host + ":" + strconv.Itoa(port)}
	if info.Username != "" {
		userinfo := url.User(info.Username)
		if password != "" {
			userinfo = url.UserPassword(info.Username, password)
		}
		u.User = userinfo
	}
	if info.Database != "" {
		q := u.Query()
		q.Set("database", info.Database)
		u.RawQuery = q.Encode()
	}
	return u.String()
}
