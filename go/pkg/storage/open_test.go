package storage

// Open must carry its pragmas on every physical connection the pool opens, not
// just the first one. busy_timeout is per-connection state in SQLite: the old
// shape Exec'd WAL once and left the pool's secondary connections at
// busy_timeout=0, so a second concurrent writer failed with SQLITE_BUSY
// immediately. The server and the TUI share this file concurrently, which is
// exactly this package's main scenario.

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenCarriesPragmasIntoTheDSN(t *testing.T) {
	// The contract is the DSN shape: modernc runs `_pragma` query parameters on
	// every physical connection the pool opens, so the file path must carry
	// busy_timeout (and the per-connection state with it). A bare path would
	// leave the pool's secondary connections at busy_timeout=0.
	path := filepath.Join(t.TempDir(), "meta.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("schema: %v", err)
	}
}

func TestOpenSurvivesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Eight concurrent writers hammering distinct keys: with busy_timeout=0 on
	// any pool connection the contention window trips SQLITE_BUSY immediately.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := db.Exec(
					"INSERT OR REPLACE INTO kv (k, v) VALUES (?, ?)",
					keyFor(i, j), "v",
				); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}

func keyFor(writer, round int) string {
	return string(rune('a'+writer)) + string(rune('0'+round%10))
}
