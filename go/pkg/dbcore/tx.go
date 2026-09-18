package dbcore

import (
	"context"
	"database/sql"
)

// SQLTx 抽象 database/sql 的事务执行器，屏蔽 *sql.Tx 与 *sql.DB 的具体类型。
// *sql.DB 与 *sql.Tx 都满足该接口，因此 driver 的 ExecuteIn 可以统一在两种执行器上工作。
type SQLTx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// TxMode 是 SQLTxDriver.Begin 的选项。IsolationLevel 为空表示使用驱动默认的隔离级别。
// 支持的枚举字符串与 protocol.IsolationLevel 保持一致；这里刻意用 string 避免 dbcore
// 依赖 protocol 的循环。
type TxMode struct {
	IsolationLevel string
}

// TxOptionsFromMode 把 TxMode 翻译成 *sql.TxOptions。
// 无法映射到 database/sql 标准枚举的级别（如 MSSQL 的快照隔离、Oracle 的 serializable 特殊实现）
// 会退化为 LevelDefault，由驱动自己再用 SET SESSION 之类的语句补充设置。
func TxOptionsFromMode(mode TxMode) *sql.TxOptions {
	opts := &sql.TxOptions{}
	switch mode.IsolationLevel {
	case "read_uncommitted":
		opts.Isolation = sql.LevelReadUncommitted
	case "read_committed":
		opts.Isolation = sql.LevelReadCommitted
	case "repeatable_read":
		opts.Isolation = sql.LevelRepeatableRead
	case "serializable":
		opts.Isolation = sql.LevelSerializable
	}
	return opts
}
