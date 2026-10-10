package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
)

func TestBlobPutGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := NewBlobStore(dir)

	ref, err := st.PutBlob([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if !strings.HasPrefix(ref, protocol.BlobRefPrefix) {
		t.Fatalf("ref %q missing prefix %q", ref, protocol.BlobRefPrefix)
	}
	id := strings.TrimPrefix(ref, protocol.BlobRefPrefix)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("ref id not a uuid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs", id)); err != nil {
		t.Fatalf("blob file missing: %v", err)
	}

	got, err := st.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("Get = %v, want [1 2 3]", got)
	}
	size, err := st.Size(id)
	if err != nil || size != 3 {
		t.Fatalf("Size = (%d, %v), want 3", size, err)
	}

	part, err := st.ReadRange(id, 1, 2)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if len(part) != 2 || part[0] != 2 || part[1] != 3 {
		t.Fatalf("ReadRange = %v, want [2 3]", part)
	}
}

func TestBlobNotFound(t *testing.T) {
	st := NewBlobStore(t.TempDir())
	id := uuid.NewString()
	if _, err := st.Get(id); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("Get err = %v, want ErrBlobNotFound", err)
	}
	if _, err := st.Size(id); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("Size err = %v, want ErrBlobNotFound", err)
	}
	if _, err := st.ReadRange(id, 0, 1); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("ReadRange err = %v, want ErrBlobNotFound", err)
	}
}

func TestBlobStoreDirLazy(t *testing.T) {
	st := NewBlobStore(t.TempDir())
	st.Sweep() // 目录未建：空操作不报错
	if _, err := os.Stat(st.dir); !os.IsNotExist(err) {
		t.Fatalf("blobs dir should not exist before first put, stat err = %v", err)
	}
}

func TestBlobSweepRemovesExpired(t *testing.T) {
	st := NewBlobStore(t.TempDir())
	staleRef, err := st.PutBlob([]byte("old"))
	if err != nil {
		t.Fatalf("PutBlob stale: %v", err)
	}
	freshRef, err := st.PutBlob([]byte("new"))
	if err != nil {
		t.Fatalf("PutBlob fresh: %v", err)
	}
	staleID := strings.TrimPrefix(staleRef, protocol.BlobRefPrefix)
	freshID := strings.TrimPrefix(freshRef, protocol.BlobRefPrefix)

	// 把 stale 文件 mtime 拨回 TTL 之外（§5.2 删除策略：按 mtime 起算）。
	old := time.Now().Add(-BlobTTL - time.Hour)
	if err := os.Chtimes(filepath.Join(st.dir, staleID), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	st.Sweep()

	if _, err := st.Size(staleID); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("stale blob should be swept, Size err = %v", err)
	}
	if _, err := st.Size(freshID); err != nil {
		t.Fatalf("fresh blob should survive sweep: %v", err)
	}
}
