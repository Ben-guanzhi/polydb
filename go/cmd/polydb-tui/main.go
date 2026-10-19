// polydb-tui 是 polydb 的终端客户端（bubbletea v2）。
// 两种模式（均只经 transport 访问数据，符合 AGENTS.md 铁律 2）：
//   - 默认（本机）：与 server 共用同一份连接元数据存储（storage.DataDir），进程内直连 app-core；
//   - 远程（-server http://host:port）：通过 REST 连远程 polydb-server，适合多人共享服务端。
package main

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/dbmode"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	server := flag.String("server", os.Getenv("POLYDB_SERVER"), "远程 polydb-server 地址（如 http://127.0.0.1:8080）；缺省为本机直连模式")
	token := flag.String("token", os.Getenv("POLYDB_SERVER_TOKEN"), "远程服务端启用 POLYDB_SERVER_TOKEN 时必填")
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
		app := appcore.New(db, kr)
		app.SetKnownHostsPath(filepath.Join(dataDir, "known_hosts"))
		defer app.Close() // 退出时停止事务清扫 goroutine
		client = transport.NewLocal(app)
	}

	// 数据库模式：注册 dbmode 的 overlay 工厂，启动时压栈连接选择器
	//（saved connections → 连接表单 → SetBackendMsg{Adapter} → schema 浏览）。
	dbmode.RegisterFactories()
	app := tui.New(tui.Options{Client: client})
	app.PushOverlay(dbmode.NewConnectOverlay(app))

	p := tea.NewProgram(app)
	if _, err := p.Run(); err != nil {
		slog.Error("tui stopped", "error", err)
		os.Exit(1)
	}
}
