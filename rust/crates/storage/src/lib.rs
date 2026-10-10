use std::path::{Path, PathBuf};
use std::sync::Arc;

use parking_lot::Mutex;
use polydb_core::{CoreError, CoreResult};
use rusqlite::Connection as RusqliteConnection;

mod connection_repo;
pub use connection_repo::ConnectionRepository;

pub mod blobs;
pub use blobs::{BlobStore, BLOB_TTL};

pub mod keyring;
pub use keyring::{keyring_ref, FileKeyring, Keyring};

pub struct Storage {
    conn: Arc<Mutex<RusqliteConnection>>,
    /// 库文件所在目录：`blobs/` 等旁路资源的根（spec/encoding.md §5.2）。
    data_dir: PathBuf,
}

impl Storage {
    pub fn open(path: &str) -> CoreResult<Self> {
        let conn = RusqliteConnection::open(path)
            .map_err(|e| CoreError::Storage(format!("failed to open storage: {e}")))?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            .map_err(|e| CoreError::Storage(format!("pragma failed: {e}")))?;
        let data_dir = Path::new(path)
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .unwrap_or(Path::new("."))
            .to_path_buf();
        let storage = Self {
            conn: Arc::new(Mutex::new(conn)),
            data_dir,
        };
        storage.init_schema()?;
        Ok(storage)
    }

    fn init_schema(&self) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS connections (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                kind TEXT NOT NULL,
                host TEXT,
                port INTEGER,
                database TEXT,
                username TEXT,
                password_ref TEXT,
                options TEXT NOT NULL DEFAULT '{}',
                ssh_tunnel TEXT,
                default_schema TEXT,
                read_only INTEGER NOT NULL DEFAULT 0,
                \"group\" TEXT,
                \"color\" TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );",
        )
        .map_err(|e| CoreError::Storage(format!("schema init failed: {e}")))?;
        // M10 迁移：老库没有 read_only 列。列已存在时报错属预期，忽略。
        if let Err(e) = conn.execute_batch(
            "ALTER TABLE connections ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;",
        ) {
            if !e.to_string().contains("duplicate column") {
                return Err(CoreError::Storage(format!("migrate read_only failed: {e}")));
            }
        }
        // M15 迁移：老库没有 group/color 列。
        for col in ["\"group\"", "color"] {
            let stmt = format!("ALTER TABLE connections ADD COLUMN {col} TEXT;");
            if let Err(e) = conn.execute_batch(&stmt) {
                if !e.to_string().contains("duplicate column") {
                    return Err(CoreError::Storage(format!("migrate {col} failed: {e}")));
                }
            }
        }
        Ok(())
    }

    /// 库文件所在目录（`BlobStore` 定位用；`:memory:` 等无父目录时为 `.`）。
    pub fn data_dir(&self) -> &Path {
        &self.data_dir
    }

    pub fn connections(&self) -> ConnectionRepository {
        ConnectionRepository::new(self.conn.clone())
    }
}

static STORAGE: std::sync::OnceLock<Storage> = std::sync::OnceLock::new();

pub fn init_storage(path: &str) -> CoreResult<()> {
    let storage = Storage::open(path)?;
    STORAGE
        .set(storage)
        .map_err(|_| CoreError::Storage("storage already initialized".into()))?;
    Ok(())
}

pub fn get_storage() -> CoreResult<&'static Storage> {
    STORAGE
        .get()
        .ok_or_else(|| CoreError::Storage("storage not initialized".into()))
}
