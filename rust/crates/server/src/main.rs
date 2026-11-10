//! polydb-server 二进制入口（M2）。配置目录/环境变量约定见 AGENTS.md §6。

use std::path::PathBuf;
use std::sync::Arc;

use polydb_app_core::AppCore;
use polydb_server::router_with_token;
use polydb_storage::Storage;

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt::init();

    let addr = std::env::var("POLYDB_ADDR").unwrap_or_else(|_| "127.0.0.1:8080".into());
    let token = std::env::var("POLYDB_SERVER_TOKEN")
        .ok()
        .filter(|t| !t.is_empty());
    let data_dir = data_dir();
    let db_path = data_dir.join("polydb.db");

    if let Err(e) = std::fs::create_dir_all(&data_dir) {
        tracing::error!(error = %e, dir = %data_dir.display(), "create data dir failed");
        std::process::exit(1);
    }
    let db_path_str = match db_path.to_str() {
        Some(s) => s,
        None => {
            tracing::error!("db path is not valid UTF-8: {}", db_path.display());
            std::process::exit(1);
        }
    };
    let storage = match Storage::open(db_path_str) {
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

    tracing::info!(
        addr = %addr,
        data_dir = %data_dir.display(),
        auth = token.is_some(),
        "polydb-server listening"
    );
    if let Err(e) = axum::serve(listener, router_with_token(app, token)).await {
        tracing::error!(error = %e, "server stopped");
        std::process::exit(1);
    }
}

fn data_dir() -> PathBuf {
    if let Ok(d) = std::env::var("POLYDB_DATA_DIR") {
        return PathBuf::from(d);
    }
    // 与 Go 的 os.UserConfigDir() 对齐：Linux ~/.config、macOS
    // ~/Library/Application Support、Windows %APPDATA%。此前用
    // APPDATA/HOME 直接拼 "polydb"，Linux 下解析到 ~/polydb，与 Go 的
    // ~/.config/polydb 不同——Rust server 与 Go TUI/Web 会读写不同的
    // polydb.db / keyring.bin / blobs/，连接与密码静默分叉。
    dirs::config_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("polydb")
}

#[cfg(test)]
mod tests {
    use super::data_dir;

    /// F1 回归：server 的 data_dir 必须与 Go 的 os.UserConfigDir()/polydb
    /// 解析一致。此前用 APPDATA/HOME 直接拼 "polydb"，Linux 下解析到
    /// ~/polydb（Go 解析到 ~/.config/polydb），Rust server 与 Go TUI/Web
    /// 读写不同的 polydb.db / keyring.bin，连接与密码静默分叉。
    #[test]
    fn data_dir_uses_config_dir_not_home() {
        std::env::set_var("POLYDB_DATA_DIR", "/tmp/polydb-test");
        assert_eq!(data_dir(), std::path::PathBuf::from("/tmp/polydb-test"));
        std::env::remove_var("POLYDB_DATA_DIR");

        let expected = dirs::config_dir()
            .expect("dirs::config_dir should resolve on all platforms")
            .join("polydb");
        assert_eq!(data_dir(), expected);
    }
}
