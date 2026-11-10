use polydb_app_core::AppCore;
use polydb_storage::Storage;
use polydb_transport::{LocalTransport, Transport};
use std::sync::Arc;

fn main() {
    // 与 Go 的 os.UserConfigDir() 对齐（dirs::config_dir 等价），此前用
    // data_local_dir() 解析到 ~/.local/share/polydb，与 Go 的 ~/.config/polydb
    // 不同——GUI 与 Go TUI/Web 读写不同的 polydb.db / keyring.bin。
    let data_dir = dirs::config_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("."))
        .join("polydb");
    std::fs::create_dir_all(&data_dir).ok();
    let db_path = data_dir.join("polydb.db");
    let storage = Storage::open(db_path.to_str().unwrap()).expect("failed to open storage");

    let app_core = Arc::new(AppCore::new(storage));
    let transport: Arc<dyn Transport> = Arc::new(LocalTransport::new(app_core));

    polydb_ui_gui::run(transport);
}
