//! 大字段旁路 sink（spec/encoding.md §5.2）。
//!
//! 驱动层用 [`blob_value`] 编码二进制单元格：≤[`MAX_INLINE_BLOB_BYTES`] 走
//! 内联 base64（§5.1）；超过阈值改发旁路引用
//! `{"$blob": {"len": N, "ref": "$polydb_blob:<uuid>"}}`，由 app-core 注入的
//! [`BlobSink`] 落盘。无 sink 或落盘失败时回落仅 `len` 占位（§5.2 降级）。
//!
//! sink 为进程级全局：驱动实例由连接参数构造，无注入点，故在 `AppCore`
//! 构造期一次性设置（与 Go `dbcore.SetBlobSink` 对齐）。

use std::collections::HashMap;
use std::sync::{Arc, RwLock};

use polydb_protocol::{blob_value as inline_blob_value, Value, MAX_INLINE_BLOB_BYTES};

/// 大字段落盘端。实现方为 `storage::BlobStore`（经 app-core 包装）。
pub trait BlobSink: Send + Sync {
    /// 落盘并返回完整引用 ID（`$polydb_blob:<uuid>`）；失败返回 `None`，
    /// 由 [`blob_value`] 回落仅长度占位。
    fn put_blob(&self, data: &[u8]) -> Option<String>;
}

static BLOB_SINK: RwLock<Option<Arc<dyn BlobSink>>> = RwLock::new(None);

/// 注入落盘端（`AppCore` 构造期调用；重复调用以最后一次为准）。
pub fn set_blob_sink(sink: Arc<dyn BlobSink>) {
    *BLOB_SINK.write().unwrap_or_else(|e| e.into_inner()) = Some(sink);
}

/// 清除落盘端（测试隔离用；生产路径只在 `AppCore` 构造时设置一次）。
pub fn clear_blob_sink() {
    *BLOB_SINK.write().unwrap_or_else(|e| e.into_inner()) = None;
}

/// sink 感知的二进制单元格编码——**驱动层唯一入口**。
/// 与 Go `dbcore.BlobValue` 对齐：小值内联，大值旁路，降级回占位。
pub fn blob_value(bytes: &[u8]) -> Value {
    if bytes.len() <= MAX_INLINE_BLOB_BYTES {
        return inline_blob_value(bytes);
    }
    let mut inner = HashMap::new();
    inner.insert("len".to_string(), Value::Integer(bytes.len() as i64));
    {
        let sink = BLOB_SINK.read().unwrap_or_else(|e| e.into_inner());
        if let Some(id) = sink.as_ref().and_then(|s| s.put_blob(bytes)) {
            inner.insert("ref".to_string(), Value::String(id));
        }
    }
    let mut outer = HashMap::new();
    outer.insert("$blob".to_string(), Value::Object(inner));
    Value::Object(outer)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    /// 全局 sink 在并行测试间共享：整组用例串行进出。
    static LOCK: Mutex<()> = Mutex::new(());

    struct FakeSink {
        stored: Mutex<Vec<Vec<u8>>>,
        fail: bool,
    }

    impl FakeSink {
        fn ok() -> Self {
            Self {
                stored: Mutex::new(Vec::new()),
                fail: false,
            }
        }
        fn failing() -> Self {
            Self {
                stored: Mutex::new(Vec::new()),
                fail: true,
            }
        }
    }

    impl BlobSink for FakeSink {
        fn put_blob(&self, data: &[u8]) -> Option<String> {
            if self.fail {
                return None;
            }
            let mut stored = self.stored.lock().unwrap();
            stored.push(data.to_vec());
            Some(format!("$polydb_blob:fake-{}", stored.len()))
        }
    }

    fn blob_fields(v: &Value) -> (&HashMap<String, Value>, Option<&HashMap<String, Value>>) {
        let Value::Object(outer) = v else {
            panic!("not an object")
        };
        let Some(Value::Object(inner)) = outer.get("$blob") else {
            panic!("no $blob")
        };
        (outer, Some(inner))
    }

    fn run_with_sink(sink: Option<Arc<dyn BlobSink>>, f: impl FnOnce()) {
        let _g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        match sink {
            Some(s) => set_blob_sink(s),
            None => clear_blob_sink(),
        }
        f();
        clear_blob_sink();
    }

    #[test]
    fn inline_below_threshold_is_unaffected_by_sink() {
        run_with_sink(Some(Arc::new(FakeSink::failing())), || {
            let v = blob_value(b"small");
            let (outer, inner) = blob_fields(&v);
            let inner = inner.unwrap();
            assert_eq!(inner["len"], Value::Integer(5));
            assert!(inner.contains_key("base64"));
            assert!(!inner.contains_key("ref"));
            assert_eq!(outer.len(), 1);
        });
    }

    #[test]
    fn over_threshold_routes_to_sink() {
        let sink = Arc::new(FakeSink::ok());
        let payload = vec![7u8; MAX_INLINE_BLOB_BYTES + 1];
        let sink2 = sink.clone();
        run_with_sink(Some(sink2), || {
            let v = blob_value(&payload);
            let (outer, inner) = blob_fields(&v);
            let inner = inner.unwrap();
            assert_eq!(
                inner["len"],
                Value::Integer((MAX_INLINE_BLOB_BYTES + 1) as i64)
            );
            assert!(!inner.contains_key("base64"));
            let Value::String(ref_id) = &inner["ref"] else {
                panic!("no ref")
            };
            assert!(ref_id.starts_with("$polydb_blob:"));
            assert_eq!(outer.len(), 1);
        });
        {
            let stored = sink.stored.lock().unwrap();
            assert_eq!(stored.len(), 1);
            assert_eq!(stored[0], payload);
        }
    }

    #[test]
    fn over_threshold_without_sink_emits_len_only() {
        run_with_sink(None, || {
            let v = blob_value(&vec![0u8; MAX_INLINE_BLOB_BYTES + 1]);
            let (_, inner) = blob_fields(&v);
            let inner = inner.unwrap();
            assert_eq!(inner.len(), 1);
            assert!(inner.contains_key("len"));
            assert!(!inner.contains_key("ref"));
        });
    }

    #[test]
    fn over_threshold_sink_failure_falls_back_to_len_only() {
        run_with_sink(Some(Arc::new(FakeSink::failing())), || {
            let v = blob_value(&vec![0u8; MAX_INLINE_BLOB_BYTES + 1]);
            let (_, inner) = blob_fields(&v);
            let inner = inner.unwrap();
            assert_eq!(inner.len(), 1);
            assert!(!inner.contains_key("ref"));
        });
    }
}
