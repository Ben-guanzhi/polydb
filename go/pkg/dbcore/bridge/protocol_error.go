package bridge

// 内核错误 → 协议错误码的统一出口。此前五引擎的错误一律包成
// POLYDB_ERR_QUERY_FAILED + 裸 message，protocol/error.go 里定义的
// DUPLICATE_KEY/DEADLOCK/TIMEOUT 等码永远产不出来，Rust 侧拿不到可编程的
// 错误分类。分类本体在内核 dberror（三层证据：context / SQLState / 错误链
// 文本），这里只做码面映射与 SQLState 透传。

import (
	"encoding/json"
	"errors"

	"github.com/dbcore/dbcore/dberror"
	"github.com/polydb/polydb/pkg/protocol"
)

// sqlStater 与内核 dberror 的同名未导出接口同形：pgx/lib/pq 的错误带着
// SQLState，结构化分类时顺路透传给协议面。
type sqlStater interface {
	SQLState() string
}

// ErrorFromKernel 把内核/驱动错误映射为协议错误。Unknown 归位为
// QUERY_FAILED——即不认识的原样透出，与既有行为一致；认识的可行动错误
// （唯一冲突、死锁、断连、超时……）给出协议码、可重试位与 SQLState。
func ErrorFromKernel(err error) *protocol.PolyDBError {
	pe := &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: err.Error()}
	switch dberror.Classify(err) {
	case dberror.UniqueViolation:
		pe.Code = protocol.ErrDuplicateKey
	case dberror.ForeignKeyViolation, dberror.NotNullViolation, dberror.CheckViolation:
		pe.Code = protocol.ErrConstraintViolation
	case dberror.Deadlock:
		pe.Code = protocol.ErrDeadlock
		pe.Retryable = true
	case dberror.SerializationFailure:
		pe.Code = protocol.ErrTransactionFailed
		pe.Retryable = true
	case dberror.ConnectionBroken:
		pe.Code = protocol.ErrConnectionFailed
		pe.Retryable = true
	case dberror.Timeout:
		pe.Code = protocol.ErrTimeout
		pe.Retryable = true
	case dberror.Canceled:
		pe.Code = protocol.ErrCancelled
	}
	var st sqlStater
	if errors.As(err, &st) {
		if state := st.SQLState(); state != "" {
			if raw, err := json.Marshal(map[string]string{"sqlstate": state}); err == nil {
				pe.Detail = raw
			}
		}
	}
	return pe
}
