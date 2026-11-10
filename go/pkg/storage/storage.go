// Package storage 提供 polydb 本地元数据持久化（SQLite，WAL 模式）。
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DataDir 返回本地数据目录：POLYDB_DATA_DIR 优先，否则用户配置目录下的 polydb/。
// server 与 TUI 共用同一份连接元数据，必须解析到相同路径。
func DataDir() string {
	if v := os.Getenv("POLYDB_DATA_DIR"); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "polydb")
}

// Open 打开（必要时创建）本地元数据库并初始化 schema。
//
// Pragmas 走 DSN 的 `_pragma` 参数而非 Exec：modernc 只在**每条**物理连接上
// 跑 DSN 参数。此前 journal_mode/WAL 用 Exec 设置了一次——它写进库文件会持
// 久化，但 busy_timeout 是**每连接状态**，连接池的第二条连接 busy_timeout=0，
// 并发写立刻 SQLITE_BUSY（用户库层 dbsqlite.Open 修过同一个问题，storage 层
// 漏掉了；server 与 TUI 共库并发写正是这个包的主场景）。
func Open(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open storage %s: %w", path, err)
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS connections (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	kind          TEXT NOT NULL,
	host          TEXT,
	port          INTEGER,
	database      TEXT,
	username      TEXT,
	password_ref  TEXT,
	options       TEXT NOT NULL DEFAULT '{}',
	ssh_tunnel    TEXT,
	default_schema TEXT,
	read_only     INTEGER NOT NULL DEFAULT 0,
	"group"       TEXT,
	color         TEXT,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	// M10 迁移：老库没有 read_only 列。列已存在时报错属预期，忽略。
	if _, err := db.Exec("ALTER TABLE connections ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate read_only: %w", err)
	}
	// M15 迁移：老库没有 group/color 列。
	for _, col := range []string{`"group" TEXT`, "color TEXT"} {
		if _, err := db.Exec("ALTER TABLE connections ADD COLUMN " + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate %s: %w", col, err)
		}
	}
	return nil
}
