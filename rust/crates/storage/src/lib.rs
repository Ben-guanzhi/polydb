use std::sync::Arc;

use parking_lot::Mutex;
use polydb_core::{CoreError, CoreResult};
use rusqlite::Connection as RusqliteConnection;

mod connection_repo;
pub use connection_repo::ConnectionRepository;

pub mod keyring;
pub use keyring::{keyring_ref, FileKeyring, Keyring};

pub struct Storage {
    conn: Arc<Mutex<RusqliteConnection>>,
}

impl Storage {
    pub fn open(path: &str) -> CoreResult<Self> {
        let conn = RusqliteConnection::open(path)
            .map_err(|e| CoreError::Storage(format!("failed to open storage: {e}")))?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            .map_err(|e| CoreError::Storage(format!("pragma failed: {e}")))?;
        let storage = Self {
            conn: Arc::new(Mutex::new(conn)),
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
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );",
        )
        .map_err(|e| CoreError::Storage(format!("schema init failed: {e}")))?;
        Ok(())
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
