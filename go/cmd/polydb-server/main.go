// polydb-server 是 Go 后端的 HTTP 服务入口（M2）。
package main

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
)

func main() {
	logger := slog.Default()

	dataDir := storage.DataDir()
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
	addr := envOr("POLYDB_ADDR", "127.0.0.1:8080")
	token := os.Getenv("POLYDB_SERVER_TOKEN")

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.NewWithToken(app, token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info("polydb-server listening", "addr", addr, "data_dir", dataDir, "auth", token != "")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
