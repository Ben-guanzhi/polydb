package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

func TestParseByteRange(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		size      int64
		wantStart int64
		wantEnd   int64
		wantMode  int
	}{
		{"no header", "", 100, 0, 99, rangeFull},
		{"simple", "bytes=0-9", 100, 0, 9, rangePartial},
		{"clamped end", "bytes=90-199", 100, 90, 99, rangePartial},
		{"open end", "bytes=50-", 100, 50, 99, rangePartial},
		{"suffix", "bytes=-10", 100, 90, 99, rangePartial},
		{"suffix exceeds size", "bytes=-150", 100, 0, 99, rangePartial},
		{"start beyond size", "bytes=100-", 100, 0, 0, rangeUnsatisfiable},
		{"suffix zero", "bytes=-0", 100, 0, 0, rangeUnsatisfiable},
		{"inverted ignored", "bytes=5-3", 100, 0, 99, rangeFull},
		{"multi range ignored", "bytes=0-1,4-5", 100, 0, 99, rangeFull},
		{"bad syntax ignored", "bytes=abc-def", 100, 0, 99, rangeFull},
		{"other unit ignored", "items=0-9", 100, 0, 99, rangeFull},
		{"garbage ignored", "foo=bar", 100, 0, 99, rangeFull},
		{"empty size full", "", 0, 0, -1, rangeFull},
		{"empty size satisfiable start", "bytes=0-", 0, 0, 0, rangeUnsatisfiable},
		{"empty size suffix", "bytes=-5", 0, 0, 0, rangeUnsatisfiable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, mode := parseByteRange(c.header, c.size)
			if start != c.wantStart || end != c.wantEnd || mode != c.wantMode {
				t.Fatalf("parseByteRange(%q, %d) = (%d, %d, %d), want (%d, %d, %d)",
					c.header, c.size, start, end, mode, c.wantStart, c.wantEnd, c.wantMode)
			}
		})
	}
}

// seedBlob 把字节串写入当前 POLYDB_DATA_DIR 的 blob 存储并返回裸 UUID。
func seedBlob(t *testing.T, data []byte) string {
	t.Helper()
	ref, err := storage.NewBlobStore(storage.DataDir()).PutBlob(data)
	if err != nil {
		t.Fatalf("seed blob: %v", err)
	}
	return strings.TrimPrefix(ref, protocol.BlobRefPrefix)
}

func blobGet(t *testing.T, base, connID, blobID, rng string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/api/connections/"+connID+"/blobs/"+blobID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func decodeErrCode(t *testing.T, res *http.Response) string {
	t.Helper()
	raw, _ := io.ReadAll(res.Body)
	var pe protocol.PolyDBError
	if err := json.Unmarshal(raw, &pe); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	return pe.Code
}

func TestGetBlobServing(t *testing.T) {
	t.Setenv("POLYDB_DATA_DIR", t.TempDir())
	srv := newTestServer(t)
	defer srv.Close()

	data := []byte("0123456789")
	id := seedBlob(t, data)
	connID := uuid.NewString()

	t.Run("full 200", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, id, "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		if got := res.Header.Get("Accept-Ranges"); got != "bytes" {
			t.Fatalf("Accept-Ranges = %q", got)
		}
		if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(res.Body)
		if string(body) != string(data) {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("range 206", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, id, "bytes=0-2")
		if res.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", res.StatusCode)
		}
		if got := res.Header.Get("Content-Range"); got != "bytes 0-2/10" {
			t.Fatalf("Content-Range = %q", got)
		}
		body, _ := io.ReadAll(res.Body)
		if string(body) != "012" {
			t.Fatalf("body = %q, want 012", body)
		}
	})

	t.Run("suffix range 206", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, id, "bytes=-4")
		if res.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", res.StatusCode)
		}
		if got := res.Header.Get("Content-Range"); got != "bytes 6-9/10" {
			t.Fatalf("Content-Range = %q", got)
		}
		body, _ := io.ReadAll(res.Body)
		if string(body) != "6789" {
			t.Fatalf("body = %q, want 6789", body)
		}
	})

	t.Run("unsatisfiable 416", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, id, "bytes=99-")
		if res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("status = %d, want 416", res.StatusCode)
		}
		if got := res.Header.Get("Content-Range"); got != "bytes */10" {
			t.Fatalf("Content-Range = %q", got)
		}
		if code := decodeErrCode(t, res); code != protocol.ErrInvalidParam {
			t.Fatalf("code = %q, want %q", code, protocol.ErrInvalidParam)
		}
	})

	t.Run("unknown 404", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, uuid.NewString(), "")
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.StatusCode)
		}
		if code := decodeErrCode(t, res); code != protocol.ErrBlobNotFound {
			t.Fatalf("code = %q, want %q", code, protocol.ErrBlobNotFound)
		}
	})

	t.Run("bad blob id 400", func(t *testing.T) {
		res := blobGet(t, srv.URL, connID, "not-a-uuid", "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.StatusCode)
		}
		if code := decodeErrCode(t, res); code != protocol.ErrInvalidParam {
			t.Fatalf("code = %q, want %q", code, protocol.ErrInvalidParam)
		}
	})

	t.Run("bad conn id 400", func(t *testing.T) {
		res := blobGet(t, srv.URL, "nope", id, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.StatusCode)
		}
		if code := decodeErrCode(t, res); code != protocol.ErrInvalidParam {
			t.Fatalf("code = %q, want %q", code, protocol.ErrInvalidParam)
		}
	})
}
