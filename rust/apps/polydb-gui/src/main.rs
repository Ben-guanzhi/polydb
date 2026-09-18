use polydb_app_core::AppCore;
use polydb_storage::Storage;
use polydb_transport::LocalTransport;
use std::sync::Arc;

fn main() {
    let data_dir = dirs::data_local_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("."))
        .join("polydb");
    std::fs::create_dir_all(&data_dir).ok();
    let db_path = data_dir.join("polydb.db");
    let storage = Storage::open(db_path.to_str().unwrap()).expect("failed to open storage");

    let app_core = Arc::new(AppCore::new(storage));
    let transport = Arc::new(LocalTransport::new(app_core));

    polydb_ui_gui::run(transport);
}
