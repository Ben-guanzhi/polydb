// polydb-mcp 把 polydb 的元数据浏览 + 只读查询暴露为 MCP（stdio JSON-RPC）工具，
// 供 Cursor / Claude Desktop 等 MCP 客户端连接（M17）。
//
// 与 polydb-server 相同装配本地存储/密钥环；只读红线：run_readonly_query 仅放行
// SELECT/EXPLAIN/WITH。凭据（连接密码/SSH）走 keyring，绝不随 MCP 响应返回。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/mcp"
	"github.com/polydb/polydb/pkg/storage"
)

func main() {
	var dataDirFlag string
	flag.StringVar(&dataDirFlag, "data-dir", "", "数据目录（缺省取 POLYDB_DATA_DIR 或用户配置目录下的 polydb/）")
	flag.Parse()

	logger := slog.Default()

	dataDir := dataDirFlag
	if dataDir == "" {
		dataDir = storage.DataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		logger.Error("create data dir", "error", err, "dir", dataDir)
		os.Exit(1)
	}
	db, err := storage.Open(filepath.Join(dataDir, "polydb.db"))
	if err != nil {
		logger.Error("open storage", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	kr, err := keyring.Open(dataDir)
	if err != nil {
		logger.Error("open keyring", "error", err)
		os.Exit(1)
	}

	app := appcore.New(db, kr)
	app.SetKnownHostsPath(filepath.Join(dataDir, "known_hosts"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := mcp.New(app)
	logger.Info("polydb-mcp listening on stdio", "data_dir", dataDir)
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		logger.Error("mcp server stopped", "error", err)
	}
}
