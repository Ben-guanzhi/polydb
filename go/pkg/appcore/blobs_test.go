package appcore

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

func newBlobTestApp(t *testing.T) *AppCore {
	t.Helper()
	t.Setenv("POLYDB_DATA_DIR", t.TempDir())
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "blob-test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return New(db, kr)
}

func TestGetBlobInvalidID(t *testing.T) {
	a := newBlobTestApp(t)
	_, err := a.GetBlob("not-a-uuid")
	var pe *protocol.PolyDBError
	if !errors.As(err, &pe) || pe.Code != protocol.ErrInvalidParam {
		t.Fatalf("err = %v, want INVALID_PARAM PolyDBError", err)
	}
}

func TestGetBlobUnknown(t *testing.T) {
	a := newBlobTestApp(t)
	_, err := a.GetBlob(uuid.NewString())
	var pe *protocol.PolyDBError
	if !errors.As(err, &pe) || pe.Code != protocol.ErrBlobNotFound {
		t.Fatalf("err = %v, want BLOB_NOT_FOUND PolyDBError", err)
	}
}

func TestGetBlobRoundtrip(t *testing.T) {
	a := newBlobTestApp(t)
	ref, err := a.BlobStore().PutBlob([]byte("payload"))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	got, err := a.GetBlob(strings.TrimPrefix(ref, protocol.BlobRefPrefix))
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("GetBlob = %q", got)
	}
}
