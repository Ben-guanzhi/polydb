// polydb-tui 是 polydb 的终端客户端（M4，bubbletea）。
// 两种模式（均只经 transport 访问数据，符合 AGENTS.md 铁律 2）：
//   - 默认（本机）：与 server 共用同一份连接元数据存储（storage.DataDir），进程内直连 app-core；
//   - 远程（-server http://host:port）：通过 REST 连远程 polydb-server，适合多人共享服务端。
package main

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

func main() {
	server := flag.String("server", os.Getenv("POLYDB_SERVER"), "远程 polydb-server 地址（如 http://127.0.0.1:8080）；缺省为本机直连模式")
	token := flag.String("token", os.Getenv("POLYDB_SERVER_TOKEN"), "远程服务端 Bearer token（服务端启用 POLYDB_SERVER_TOKEN 时必填）")
	flag.Parse()

	var client transport.Client
	if *server != "" {
		// 远程模式：无需本地存储/密钥环（连接与密码都归属服务端）。
		remote := transport.NewRemote(*server)
		remote.SetToken(*token)
		client = remote
	} else {
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
		client = transport.NewLocal(appcore.New(db, kr))
	}

	p := tea.NewProgram(tui.New(client))
	if _, err := p.Run(); err != nil {
		slog.Error("tui stopped", "error", err)
		os.Exit(1)
	}
}
