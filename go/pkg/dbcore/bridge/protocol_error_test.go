package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// stateErr 与 pgx/lib/pq 的错误同形（带 SQLState），让分类走 SQLSTATE 证据链。
type stateErr struct{ state string }

func (e stateErr) Error() string    { return "constraint violated" }
func (e stateErr) SQLState() string { return e.state }

// 五引擎的错误此前一律 QUERY_FAILED，协议码面（DUPLICATE_KEY/DEADLOCK/…）
// 永远产不出；这里钉住分类映射与 SQLState 透传。
func TestErrorFromKernelMapsClasses(t *testing.T) {
	pe := ErrorFromKernel(stateErr{state: "23505"})
	if pe.Code != protocol.ErrDuplicateKey {
		t.Fatalf("unique 23505 → %s, want %s", pe.Code, protocol.ErrDuplicateKey)
	}
	var detail map[string]string
	if err := json.Unmarshal(pe.Detail, &detail); err != nil || detail["sqlstate"] != "23505" {
		t.Fatalf("detail = %s, want sqlstate 23505", pe.Detail)
	}

	pe = ErrorFromKernel(stateErr{state: "23503"})
	if pe.Code != protocol.ErrConstraintViolation {
		t.Fatalf("FK 23503 → %s, want %s", pe.Code, protocol.ErrConstraintViolation)
	}

	pe = ErrorFromKernel(fmt.Errorf("execute: %w", context.DeadlineExceeded))
	if pe.Code != protocol.ErrTimeout || !pe.Retryable {
		t.Fatalf("deadline → %s retryable=%v, want TIMEOUT retryable", pe.Code, pe.Retryable)
	}

	pe = ErrorFromKernel(errors.New("some driver noise"))
	if pe.Code != protocol.ErrQueryFailed || pe.Detail != nil {
		t.Fatalf("unknown → %s detail=%s, want QUERY_FAILED without detail", pe.Code, pe.Detail)
	}
}
