//! `AppCore::get_blob` 用例（spec/encoding.md §5.2 进程内取回），
//! 对拍 Go `pkg/appcore/blobs_test.go` 的三个用例。

use polydb_app_core::AppCore;
use polydb_protocol::{codes, BLOB_REF_PREFIX};
use polydb_storage::{FileKeyring, Storage};

fn setup() -> AppCore {
    let dir = std::env::temp_dir().join(format!("polydb-blob-app-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    AppCore::with_keyring(storage, std::sync::Arc::new(kr))
}

#[test]
fn get_blob_invalid_id_is_invalid_param() {
    let app = setup();
    let err = app.get_blob("not-a-uuid").unwrap_err();
    assert_eq!(err.to_protocol().code, codes::INVALID_PARAM);
}

#[test]
fn get_blob_unknown_is_blob_not_found() {
    let app = setup();
    let err = app.get_blob(&uuid::Uuid::new_v4().to_string()).unwrap_err();
    assert_eq!(err.to_protocol().code, codes::BLOB_NOT_FOUND);
}

#[test]
fn get_blob_roundtrip() {
    let app = setup();
    let ref_id = app.blob_store().put_blob(b"payload").unwrap();
    let id = ref_id.strip_prefix(BLOB_REF_PREFIX).unwrap();
    let got = app.get_blob(id).unwrap();
    assert_eq!(got, b"payload".to_vec());
}
