package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

// 大字段旁路取回（spec/encoding.md §5.2）：
// GET /api/connections/{id}/blobs/{blob_id}，支持单段 Range。

// Range 解析结果：rangeFull 忽略 Range 按 200 全量；
// rangePartial 206；rangeUnsatisfiable 416。
const (
	rangeFull = iota
	rangePartial
	rangeUnsatisfiable
)

// parseByteRange 解析单段 Range 头（spec/encoding.md §5.2 取回表）。
// 非 bytes= 前缀、多段（含逗号）、语法错误、a > b 一律忽略（rangeFull）；
// a ≥ size 或 bytes=-0 不可满足（rangeUnsatisfiable）；b 超界钳到 size-1。
func parseByteRange(h string, size int64) (start, end int64, mode int) {
	full := func() (int64, int64, int) { return 0, size - 1, rangeFull }
	if h == "" {
		return full()
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return full()
	}
	if strings.HasPrefix(spec, "-") {
		n, err := strconv.ParseInt(spec[1:], 10, 64)
		if err != nil || n < 0 {
			return full()
		}
		if n == 0 || size == 0 {
			return 0, 0, rangeUnsatisfiable
		}
		if n >= size {
			return 0, size - 1, rangePartial
		}
		return size - n, size - 1, rangePartial
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return full()
	}
	a, err := strconv.ParseInt(spec[:dash], 10, 64)
	if err != nil || a < 0 {
		return full()
	}
	if a >= size {
		return 0, 0, rangeUnsatisfiable
	}
	bStr := spec[dash+1:]
	if bStr == "" {
		return a, size - 1, rangePartial
	}
	b, err := strconv.ParseInt(bStr, 10, 64)
	if err != nil || b < a {
		return full()
	}
	if b >= size {
		b = size - 1
	}
	return a, b, rangePartial
}

// getBlob 取回旁路大字段：连接 id 按既有约定只校验格式（connID），
// blob_id 须为合法 UUID（非法 400），未知/过期 404，Range 按 §5.2 表格。
func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	if _, ok := connID(w, r); !ok {
		return
	}
	blobID := r.PathValue("blob_id")
	if _, err := uuid.Parse(blobID); err != nil {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "invalid blob id: " + blobID})
		return
	}
	st := s.app.BlobStore()
	size, err := st.Size(blobID)
	if err != nil {
		writeBlobError(w, err, blobID)
		return
	}
	start, end, mode := parseByteRange(r.Header.Get("Range"), size)

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	if mode == rangeUnsatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		writeJSON(w, http.StatusRequestedRangeNotSatisfiable,
			&protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "requested range not satisfiable"})
		return
	}
	length := end - start + 1
	if length < 0 {
		length = 0
	}
	data, err := st.ReadRange(blobID, start, length)
	if err != nil {
		writeBlobError(w, err, blobID)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if mode == rangePartial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.Write(data)
}

// writeBlobError 把存储读错误映射为 HTTP：过期/未知 404 BLOB_NOT_FOUND，
// 其余走通用 writeError。
func writeBlobError(w http.ResponseWriter, err error, blobID string) {
	if errors.Is(err, storage.ErrBlobNotFound) {
		writeError(w, &protocol.PolyDBError{Code: protocol.ErrBlobNotFound, Message: "blob not found: " + blobID})
		return
	}
	writeError(w, err)
}
