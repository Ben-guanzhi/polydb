package dbcore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// fakeSink 记录调用并按脚本返回，用于钉住 §5.2 旁路分支。
type fakeSink struct {
	data  []byte
	err   error
	calls int
}

func (f *fakeSink) PutBlob(b []byte) (string, error) {
	f.calls++
	f.data = append([]byte(nil), b...)
	if f.err != nil {
		return "", f.err
	}
	return protocol.BlobRefPrefix + "00000000-0000-4000-8000-000000000000", nil
}

func blobFields(t *testing.T, v protocol.Value) map[string]any {
	t.Helper()
	raw, err := v.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	inner, ok := obj["$blob"].(map[string]any)
	if !ok {
		t.Fatalf("$blob missing or not object: %s", raw)
	}
	return inner
}

func TestBlobValueInlineUnaffectedBySink(t *testing.T) {
	sink := &fakeSink{}
	SetBlobSink(sink)
	defer SetBlobSink(nil)

	inner := blobFields(t, BlobValue([]byte{1, 2}))
	if inner["len"] != float64(2) {
		t.Fatalf("len = %v, want 2", inner["len"])
	}
	if inner["base64"] != "AQI=" {
		t.Fatalf("base64 = %v, want AQI=", inner["base64"])
	}
	if _, hasRef := inner["ref"]; hasRef {
		t.Fatalf("inline value must not carry ref: %v", inner)
	}
	if sink.calls != 0 {
		t.Fatalf("sink called %d times for inline value", sink.calls)
	}
}

func TestBlobValueRefWhenSinkEnabled(t *testing.T) {
	sink := &fakeSink{}
	SetBlobSink(sink)
	defer SetBlobSink(nil)

	big := make([]byte, MaxInlineBlobBytes+1)
	inner := blobFields(t, BlobValue(big))
	if inner["len"] != float64(MaxInlineBlobBytes+1) {
		t.Fatalf("len = %v, want %d", inner["len"], MaxInlineBlobBytes+1)
	}
	ref, _ := inner["ref"].(string)
	if ref == "" || ref[:len(protocol.BlobRefPrefix)] != protocol.BlobRefPrefix {
		t.Fatalf("ref = %v, want prefix %q", inner["ref"], protocol.BlobRefPrefix)
	}
	if _, hasB64 := inner["base64"]; hasB64 {
		t.Fatalf("ref value must not carry base64: %v", inner)
	}
	if sink.calls != 1 || len(sink.data) != MaxInlineBlobBytes+1 {
		t.Fatalf("sink calls = %d, data len = %d", sink.calls, len(sink.data))
	}
}

func TestBlobValueSinkErrorFallsBackToPlaceholder(t *testing.T) {
	SetBlobSink(&fakeSink{err: errors.New("disk full")})
	defer SetBlobSink(nil)

	inner := blobFields(t, BlobValue(make([]byte, MaxInlineBlobBytes+1)))
	if len(inner) != 1 {
		t.Fatalf("fallback fields = %v, want only len", inner)
	}
	if _, ok := inner["len"]; !ok {
		t.Fatalf("len missing: %v", inner)
	}
}

func TestBlobValueNoSinkKeepsPlaceholder(t *testing.T) {
	SetBlobSink(nil)
	inner := blobFields(t, BlobValue(make([]byte, MaxInlineBlobBytes+1)))
	if len(inner) != 1 {
		t.Fatalf("no-sink fields = %v, want only len", inner)
	}
}
