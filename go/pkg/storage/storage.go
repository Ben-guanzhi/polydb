// Package storage 提供 polydb 本地元数据持久化（SQLite，WAL 模式）。
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open storage %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
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
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
