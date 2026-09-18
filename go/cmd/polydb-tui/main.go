// polydb-tui 是 polydb 的终端客户端（M4，bubbletea）。
// 与 server 共用同一份连接元数据存储（storage.DataDir），进程内直连 app-core。
package main

import (
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/storage"
)

func main() {
	dataDir := storage.DataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		slog.Error("create data dir", "error", err, "dir", dataDir)
		os.Exit(1)
	}
	db, err := storage.Open(filepath.Join(dataDir, "polydb.db"))
	if err != nil {
		slog.Error("open storage", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	kr, err := keyring.Open(dataDir)
	if err != nil {
		slog.Error("open keyring", "error", err)
		os.Exit(1)
	}

	p := tea.NewProgram(tui.New(appcore.New(db, kr)))
	if _, err := p.Run(); err != nil {
		slog.Error("tui stopped", "error", err)
		os.Exit(1)
	}
}
