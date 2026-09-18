//! polydb-server 二进制入口（M2）。配置目录/环境变量约定见 AGENTS.md §6。

use std::path::PathBuf;
use std::sync::Arc;

use polydb_app_core::AppCore;
use polydb_server::router;
use polydb_storage::Storage;

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt::init();

    let addr = std::env::var("POLYDB_ADDR").unwrap_or_else(|_| "127.0.0.1:8080".into());
    let data_dir = data_dir();
    let db_path = data_dir.join("polydb.db");

    if let Err(e) = std::fs::create_dir_all(&data_dir) {
        tracing::error!(error = %e, dir = %data_dir.display(), "create data dir failed");
        std::process::exit(1);
    }
    let storage = match Storage::open(db_path.to_str().unwrap()) {
        Ok(s) => s,
        Err(e) => {
            tracing::error!(error = %e, "open storage failed");
            std::process::exit(1);
        }
    };

    let app = Arc::new(AppCore::new(storage));
    let listener = match tokio::net::TcpListener::bind(&addr).await {
        Ok(l) => l,
        Err(e) => {
            tracing::error!(error = %e, addr = %addr, "bind failed");
            std::process::exit(1);
        }
    };

    tracing::info!(addr = %addr, data_dir = %data_dir.display(), "polydb-server listening");
    if let Err(e) = axum::serve(listener, router(app)).await {
        tracing::error!(error = %e, "server stopped");
        std::process::exit(1);
    }
}

fn data_dir() -> PathBuf {
    if let Ok(d) = std::env::var("POLYDB_DATA_DIR") {
        return PathBuf::from(d);
    }
    // Windows: APPDATA；Unix: HOME（简化，与 Go 实现一致）。
    let base = std::env::var("APPDATA")
        .or_else(|_| std::env::var("HOME"))
        .unwrap_or_else(|_| ".".into());
    PathBuf::from(base).join("polydb")
}
