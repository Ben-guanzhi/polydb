package contract

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ignoreKeys 是两端实现必然不同的动态字段：比较前统一替换为占位符，仅校验存在性。
var ignoreKeys = map[string]bool{
	"id": true, "created_at": true, "updated_at": true,
	"execution_time_ms":       true,
	"total_execution_time_ms": true,
	"latency_ms":              true,
}

// normalize 递归归一化响应：统一数值表示、屏蔽动态字段。
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if ignoreKeys[k] {
				if val != nil {
					out[k] = "<dynamic>"
				}
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case uint64:
		if t <= math.MaxInt64 {
			return int64(t)
		}
		return t
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	default:
		return v
	}
}

// keepKeys 递归地把 map 裁剪为仅含指定键（数组逐项应用）。
func keepKeys(v any, keys ...string) any {
	switch t := v.(type) {
	case map[string]any:
		want := map[string]bool{}
		for _, k := range keys {
			want[k] = true
		}
		out := make(map[string]any)
		for k, val := range t {
			if want[k] {
				out[k] = val
			}
		}
		return out
	case []any:
		for i, item := range t {
			t[i] = keepKeys(item, keys...)
		}
		return t
	default:
		return v
	}
}

// collapseSQL 折叠 SQL 中的连续空白，便于比较两端（同一 sqlite 的存储 SQL 一致）。
func collapseSQL(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if sql, ok := m["sql"].(string); ok {
		m["sql"] = strings.Join(strings.Fields(sql), " ")
	}
	return m
}

// onlyColumnNames 把查询结果的列裁剪成只保留 name（Rust 侧列类型固定回退 TEXT，差异不比）。
func onlyColumnNames(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if cols, ok := m["columns"].([]any); ok {
		for i, c := range cols {
			if cm, ok := c.(map[string]any); ok {
				cols[i] = keepKeys(cm, "name")
			}
		}
	}
	return m
}

// dropErrMessages 删除 results 中错误项（含 code 键）的 message，其余保留。
func dropErrMessages(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if res, ok := m["results"].([]any); ok {
		for _, item := range res {
			if im, ok := item.(map[string]any); ok {
				if _, isErr := im["code"]; isErr {
					delete(im, "message")
				}
			}
		}
	}
	return m
}

// diff 子集比较：以 want 为准，want 中的键必须在 got 中取值相等（got 可有多余键）。
func diff(want, got any, path string, out *[]string) {
	switch wt := want.(type) {
	case map[string]any:
		gt, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: type mismatch (want=map got=%T)", path, got))
			return
		}
		for k, wv := range wt {
			gv, inGot := gt[k]
			if !inGot {
				*out = append(*out, fmt.Sprintf("%s.%s: missing in got", path, k))
				continue
			}
			diff(wv, gv, path+"."+k, out)
		}
	case []any:
		gt, ok := got.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: type mismatch (want=slice got=%T)", path, got))
			return
		}
		if len(wt) != len(gt) {
			*out = append(*out, fmt.Sprintf("%s: length differs (want=%d got=%d)", path, len(wt), len(gt)))
			return
		}
		for i := range wt {
			diff(wt[i], gt[i], fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		if fmt.Sprintf("%v", want) != fmt.Sprintf("%v", got) {
			*out = append(*out, fmt.Sprintf("%s: value differs (want=%v got=%v)", path, want, got))
		}
	}
}
