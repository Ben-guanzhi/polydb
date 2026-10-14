// polydb-server 是 Go 后端的 HTTP 服务入口（M2）。
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	logw := newLogger()
	slog.SetDefault(logw.logger)
	defer func() { _ = logw.Close() }()
	logger := slog.Default()
	logger.Info("polydb-server", "version", Version)

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
	app.SetKnownHostsPath(filepath.Join(dataDir, "known_hosts")) // SSH 主机密钥 TOFU 校验（docs/ssh-tunnel.md）
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

// newLogger 按 AGENTS.md §10/§15 装配：component 名统一 polydb，级别由
// POLYDB_LOG_LEVEL（debug|info|warn|error，默认 info）控制，POLYDB_LOG_FILE
// 非空时写文件（约定路径 ~/.local/state/polydb/polydb.log），否则写 stderr 交
// 给容器日志收集。JSON handler：服务端日志是给人 grep 更是给机器聚合的。
func newLogger() *logWriter {
	return newLoggerTo(os.Stderr)
}

func newLoggerTo(w io.Writer) *logWriter {
	opts := &slog.HandlerOptions{Level: parseLogLevel(os.Getenv("POLYDB_LOG_LEVEL"))}
	var target = w
	var file *os.File
	if p := os.Getenv("POLYDB_LOG_FILE"); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "polydb: cannot create log dir %s: %v, falling back to stderr\n",
				filepath.Dir(p), err)
		} else if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "polydb: cannot open log file %s: %v, falling back to stderr\n",
				p, err)
		} else {
			// Only used to prove the path is writable. Keep it open and the
			// process leaks a descriptor forever and Windows refuses to delete
			// the file; closing it is a no-op for a stdlib handler.
			file = f
			target = f
		}
	}
	return &logWriter{
		logger: slog.New(slog.NewJSONHandler(target, opts)).With("component", "polydb"),
		file:   file,
	}
}

// logWriter 把日志 writer 和它可能持有的文件句柄放在一起，让 Close 能归还
// 句柄——否则进程泄漏一个 descriptor，且 Windows 上无法删除该日志文件。
type logWriter struct {
	logger *slog.Logger
	file   *os.File
}

func (l *logWriter) Debug(msg string, args ...any) { l.logger.Debug(msg, args...) }
func (l *logWriter) Info(msg string, args ...any)  { l.logger.Info(msg, args...) }
func (l *logWriter) Warn(msg string, args ...any)  { l.logger.Warn(msg, args...) }
func (l *logWriter) Error(msg string, args ...any) { l.logger.Error(msg, args...) }
func (l *logWriter) With(args ...any) *slog.Logger { return l.logger.With(args...) }

func (l *logWriter) Close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func parseLogLevel(v string) slog.Level {
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
