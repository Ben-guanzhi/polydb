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
)

// SetBlobSink 注册进程级旁路写入端（appcore.New 调用）；传 nil 关闭，
// 回落 §5.1 的仅 len 占位降级。重复注册以最后一次为准。
func SetBlobSink(s BlobSink) {
	blobSinkMu.Lock()
	blobSink = s
	blobSinkMu.Unlock()
}

// currentBlobSink 返回当前写入端；可能为 nil（未启用旁路）。
func currentBlobSink() BlobSink {
	blobSinkMu.RLock()
	defer blobSinkMu.RUnlock()
	return blobSink
}
