//! 大字段旁路存储（spec/encoding.md §5.2）：`blobs/<uuid>` 一文件一字节串。
//!
//! - TTL = [`BLOB_TTL`]（按文件 mtime 起算），进程启动（app-core 构造）与
//!   每次写入后清扫；无显式删除接口，过期即删。
//! - 并发安全：文件名全局唯一，写读无共享可变状态；清扫与读的竞态由
//!   读侧把 `NotFound` 归一为 [`CoreError::BlobNotFound`] 兜底。

use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use polydb_core::{CoreError, CoreResult};
use polydb_protocol::BLOB_REF_PREFIX;

/// 旁路大字段存活时间（spec/encoding.md §5.2 删除策略）。
pub const BLOB_TTL: Duration = Duration::from_secs(24 * 60 * 60);

pub struct BlobStore {
    dir: PathBuf,
}

impl BlobStore {
    /// 返回 `data_dir` 下 `blobs/` 的存储句柄；目录惰性创建（首次写入）。
    pub fn new(data_dir: impl AsRef<Path>) -> Self {
        Self {
            dir: data_dir.as_ref().join("blobs"),
        }
    }

    /// 存储目录（测试与诊断用）。
    pub fn dir(&self) -> &Path {
        &self.dir
    }

    /// 落盘一份字节串，返回完整引用 ID（`$polydb_blob:<uuid>`）。
    /// 调用方（`db_core::blob_value`）只在超过内联阈值时调用；
    /// 写失败由调用方回落仅 len 占位。
    pub fn put_blob(&self, data: &[u8]) -> CoreResult<String> {
        fs::create_dir_all(&self.dir)
            .map_err(|e| CoreError::Storage(format!("create blob dir: {e}")))?;
        let id = uuid::Uuid::new_v4().to_string();
        fs::write(self.dir.join(&id), data)
            .map_err(|e| CoreError::Storage(format!("write blob: {e}")))?;
        self.sweep();
        Ok(format!("{BLOB_REF_PREFIX}{id}"))
    }

    /// 返回字节长度；未知或已过期报 `BlobNotFound`。
    pub fn size(&self, id: &str) -> CoreResult<u64> {
        let meta = fs::metadata(self.path(id)).map_err(|e| self.not_found(id, e))?;
        Ok(meta.len())
    }

    /// 读取文件区间 `[start, start+length)`；长度由调用方按 `size` 钳位。
    pub fn read_range(&self, id: &str, start: u64, length: u64) -> CoreResult<Vec<u8>> {
        let mut f = fs::File::open(self.path(id)).map_err(|e| self.not_found(id, e))?;
        f.seek(SeekFrom::Start(start))
            .map_err(|e| CoreError::Storage(format!("seek blob: {e}")))?;
        let mut buf = vec![0u8; length as usize];
        f.read_exact(&mut buf)
            .map_err(|e| CoreError::Storage(format!("read blob: {e}")))?;
        Ok(buf)
    }

    /// 返回完整内容（进程内 `AppCore::get_blob` 用例路径，§5.2）。
    pub fn get(&self, id: &str) -> CoreResult<Vec<u8>> {
        fs::read(self.path(id)).map_err(|e| self.not_found(id, e))
    }

    /// 删除超过 [`BLOB_TTL`] 的文件（按 mtime）。进程启动与每次写入后
    /// 调用；目录不存在时为空操作。单文件删除失败（如被占用）跳过，
    /// 下次清扫重试。
    pub fn sweep(&self) {
        let Ok(entries) = fs::read_dir(&self.dir) else {
            return;
        };
        let deadline = SystemTime::now() - BLOB_TTL;
        for entry in entries.flatten() {
            let Ok(meta) = entry.metadata() else { continue };
            match meta.modified() {
                Ok(mtime) if mtime <= deadline => {}
                _ => continue,
            }
            let _ = fs::remove_file(entry.path());
        }
    }

    fn path(&self, id: &str) -> PathBuf {
        self.dir.join(id)
    }

    fn not_found(&self, id: &str, e: std::io::Error) -> CoreError {
        if e.kind() == std::io::ErrorKind::NotFound {
            CoreError::BlobNotFound(id.to_string())
        } else {
            CoreError::Storage(format!("blob io: {e}"))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use polydb_protocol::BLOB_REF_PREFIX;

    fn temp_store() -> (tempfile_dir::TempDirGuard, BlobStore) {
        let guard = tempfile_dir::TempDirGuard::new();
        let store = BlobStore::new(guard.path());
        (guard, store)
    }

    // 轻量临时目录（不引 tempfile 依赖）。
    mod tempfile_dir {
        use std::path::{Path, PathBuf};

        pub struct TempDirGuard(PathBuf);
        impl TempDirGuard {
            pub fn new() -> Self {
                let p = std::env::temp_dir().join(format!("polydb-blob-{}", uuid::Uuid::new_v4()));
                std::fs::create_dir_all(&p).unwrap();
                Self(p)
            }
            pub fn path(&self) -> &Path {
                &self.0
            }
        }
        impl Drop for TempDirGuard {
            fn drop(&mut self) {
                let _ = std::fs::remove_dir_all(&self.0);
            }
        }
    }

    #[test]
    fn put_get_roundtrip() {
        let (_g, store) = temp_store();
        let ref_id = store.put_blob(&[1, 2, 3]).unwrap();
        assert!(ref_id.starts_with(BLOB_REF_PREFIX));
        let id = ref_id.trim_start_matches(BLOB_REF_PREFIX);
        assert!(uuid::Uuid::parse_str(id).is_ok());
        assert_eq!(store.size(id).unwrap(), 3);
        assert_eq!(store.get(id).unwrap(), vec![1, 2, 3]);
        assert_eq!(store.read_range(id, 1, 2).unwrap(), vec![2, 3]);
    }

    #[test]
    fn not_found_maps_to_blob_not_found() {
        let (_g, store) = temp_store();
        let id = uuid::Uuid::new_v4().to_string();
        let err = store.get(&id).unwrap_err();
        assert!(matches!(err, CoreError::BlobNotFound(_)));
        assert!(matches!(
            store.size(&id).unwrap_err(),
            CoreError::BlobNotFound(_)
        ));
        assert!(matches!(
            store.read_range(&id, 0, 1).unwrap_err(),
            CoreError::BlobNotFound(_)
        ));
    }

    #[test]
    fn sweep_removes_expired_keeps_fresh() {
        let (_g, store) = temp_store();
        let stale = store.put_blob(b"old").unwrap();
        let fresh = store.put_blob(b"new").unwrap();
        let stale_id = stale.trim_start_matches(BLOB_REF_PREFIX).to_string();
        let fresh_id = fresh.trim_start_matches(BLOB_REF_PREFIX).to_string();

        // 把 stale 文件 mtime 拨回 TTL 之外。
        let old = SystemTime::now() - BLOB_TTL - Duration::from_secs(60);
        let f = fs::OpenOptions::new()
            .write(true)
            .open(store.path(&stale_id))
            .unwrap();
        f.set_times(fs::FileTimes::new().set_modified(old)).unwrap();

        store.sweep();

        assert!(matches!(
            store.size(&stale_id).unwrap_err(),
            CoreError::BlobNotFound(_)
        ));
        assert!(store.size(&fresh_id).is_ok());
    }

    #[test]
    fn dir_is_lazy() {
        let (_g, store) = temp_store();
        store.sweep(); // 目录未建：空操作不报错
        assert!(!store.dir().exists());
    }
}
