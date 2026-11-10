package keyring

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Save must replace the file atomically (temp + rename), not overwrite it in
// place: a crash mid-write used to leave the ciphertext truncated or half
// written, and GCM then failed on every later open — the whole stored secret
// set was lost at once. The observable contract: after every successful Set,
// the file at keyring.bin parses, and no temp file lingers.
func TestFileKeyringSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeyring(dir, "pw")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := k.Set(fmt.Sprintf("k%d", i), "v"); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
		// The file on disk must be the complete new ciphertext: reopen from
		// scratch and read everything written so far.
		k2, err := NewFileKeyring(dir, "pw")
		if err != nil {
			t.Fatalf("reopen after Set %d: %v (a torn write would fail here)", i, err)
		}
		for j := 0; j <= i; j++ {
			if _, err := k2.Get(fmt.Sprintf("k%d", j)); err != nil {
				t.Fatalf("Get k%d after Set %d: %v", j, i, err)
			}
		}
	}
	// No temp residue: a successful rename consumed the .tmp file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestRefFormat(t *testing.T) {
	r := Ref("conn")
	if len(r) < len("polydb:conn:") {
		t.Fatalf("ref too short: %q", r)
	}
}
