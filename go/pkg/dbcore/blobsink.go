package dbcore

import "sync"

// BlobSink 是大字段旁路写入端（spec/encoding.md §5.2）：把超内联阈值的
// 字节串落盘并返回完整引用 ID（$polydb_blob:<uuid>）。storage.BlobStore
// 实现此接口；dbcore 只依赖接口，遵守「driver 层不得依赖 storage」约束。
type BlobSink interface {
	PutBlob(data []byte) (string, error)
}

var (
	blobSinkMu sync.RWMutex
	blobSink   BlobSink
	// count 是引用计数（PD-38，2026-11-11）：每次 SetBlobSink 递增，
	// ClearBlobSink 递减，归零才真正清除。与 Rust 侧 db-core/blobsink.rs
	// 的 SinkState.count 严格镜像——此前 Go 侧无计数，A 关闭会清掉 B 的
	// sink（PD-03 注释说"严格镜像"但实际不一致，§7 红线）。
	count int
)

// SetBlobSink 注册进程级旁路写入端（appcore.New 调用）。
// 递增引用计数；传 nil 直接清除（不递减计数，仅供测试清理用——
// 生产代码应走 ClearBlobSink 以正确递减）。
func SetBlobSink(s BlobSink) {
	blobSinkMu.Lock()
	blobSink = s
	if s != nil {
		count++
	}
	blobSinkMu.Unlock()
}

// ClearBlobSink 递减引用计数；归零时真正清除（appcore.Close 调用）。
// 与 Rust 侧 clear_blob_sink 严格镜像。
func ClearBlobSink() {
	blobSinkMu.Lock()
	if count > 0 {
		count--
		if count == 0 {
			blobSink = nil
		}
	}
	blobSinkMu.Unlock()
}

// currentBlobSink 返回当前写入端；可能为 nil（未启用旁路）。
func currentBlobSink() BlobSink {
	blobSinkMu.RLock()
	defer blobSinkMu.RUnlock()
	return blobSink
}
