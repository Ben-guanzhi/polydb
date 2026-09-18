package keyring

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestFileKeyring(t *testing.T) *FileKeyring {
	t.Helper()
	dir := t.TempDir()
	k, err := NewFileKeyring(dir, "master-pw")
	if err != nil {
		t.Fatalf("NewFileKeyring: %v", err)
	}
	return k
}

func TestFileKeyringSetGetDelete(t *testing.T) {
	k := newTestFileKeyring(t)
	if err := k.Set("a", "secret1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := k.Set("b", "secret2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := k.Get("a")
	if err != nil || got != "secret1" {
		t.Fatalf("Get(a) = %q, %v; want secret1", got, err)
	}
	got, err = k.Get("b")
	if err != nil || got != "secret2" {
		t.Fatalf("Get(b) = %q, %v; want secret2", got, err)
	}
	if _, err := k.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(nope) err = %v; want ErrNotFound", err)
	}
	if err := k.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v; want ErrNotFound", err)
	}
	if err := k.Delete("a"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestFileKeyringPersistence(t *testing.T) {
	dir := t.TempDir()
	k1, err := NewFileKeyring(dir, "pw")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := k1.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	k2, err := NewFileKeyring(dir, "pw")
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	got, err := k2.Get("k")
	if err != nil || got != "v" {
		t.Fatalf("Get after reopen = %q, %v; want v", got, err)
	}
}

func TestFileKeyringWrongMasterPassword(t *testing.T) {
	dir := t.TempDir()
	k1, err := NewFileKeyring(dir, "right")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := k1.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := NewFileKeyring(dir, "wrong"); err == nil {
		t.Fatal("expected error with wrong master password")
	}
}

func TestFileKeyringCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.bin")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyring(dir, "pw"); err == nil {
		t.Fatal("expected error on corrupt file")
	}
}

func TestRefFormat(t *testing.T) {
	r := Ref("conn")
	if len(r) < len("polydb:conn:") {
		t.Fatalf("ref too short: %q", r)
	}
}
