package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
)

// BlobTTL 是旁路大字段的存活时间（spec/encoding.md §5.2 删除策略）：
// 按文件 mtime 起算，超期文件在进程启动（app-core 构造）与每次写入后的
// 清扫中删除；无显式删除接口，过期即删。
const BlobTTL = 24 * time.Hour

// ErrBlobNotFound 表示引用不存在或已被 TTL 清扫删除。
var ErrBlobNotFound = errors.New("blob not found")

// BlobStore 是数据目录下的大字段旁路存储：blobs/<uuid> 一文件一字节串
// （spec/encoding.md §5.2）。并发安全：文件名全局唯一，写读无共享可变
// 状态；清扫与读的竞态由读侧把 ENOENT 归一为 ErrBlobNotFound 兜底。
type BlobStore struct {
	dir string
}

// NewBlobStore 返回 dataDir 下 blobs/ 的存储句柄；目录惰性创建（首次写入）。
func NewBlobStore(dataDir string) *BlobStore {
	return &BlobStore{dir: filepath.Join(dataDir, "blobs")}
}

// PutBlob 落盘一份字节串，返回完整引用 ID（$polydb_blob:<uuid>）。
// dbcore.BlobValue 只在超过内联阈值时调用；写失败由调用方回落仅 len 占位。
func (s *BlobStore) PutBlob(data []byte) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("create blob dir: %w", err)
	}
	id := uuid.NewString()
	if err := os.WriteFile(filepath.Join(s.dir, id), data, 0o600); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}
	s.Sweep()
	return protocol.BlobRefPrefix + id, nil
}

// Size 返回字节长度；未知或已过期返回 ErrBlobNotFound。
func (s *BlobStore) Size(id string) (int64, error) {
	fi, err := os.Stat(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrBlobNotFound
		}
		return 0, fmt.Errorf("stat blob: %w", err)
	}
	return fi.Size(), nil
}

// ReadRange 读取文件区间 [start, start+length)；长度由调用方按 Size 钳位。
func (s *BlobStore) ReadRange(id string, start, length int64) ([]byte, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("open blob: %w", err)
	}
	defer f.Close()
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, start); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("read blob: %w", err)
	}
	return buf, nil
}

// Get 返回完整内容（进程内 GetBlob 用例路径，spec/encoding.md §5.2）。
func (s *BlobStore) Get(id string) ([]byte, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("read blob: %w", err)
	}
	return data, nil
}

// Sweep 删除超过 BlobTTL 的文件（按 mtime）。进程启动与每次写入后调用；
// 目录不存在时为空操作。单文件删除失败（如被占用）跳过，下次清扫重试。
func (s *BlobStore) Sweep() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	deadline := time.Now().Add(-BlobTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().After(deadline) {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, e.Name()))
	}
}

func (s *BlobStore) path(id string) string {
	return filepath.Join(s.dir, id)
}
