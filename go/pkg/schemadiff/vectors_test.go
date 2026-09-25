package schemadiff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 共享向量对拍：与 Rust 侧 crates/schema-diff/tests/vectors.rs 读同一批语料，
// 两端必须产出逐字段一致的计划（含中文 reason）。
type vector struct {
	Name         string   `json:"name"`
	Options      Options  `json:"options"`
	Source       Snapshot `json:"source"`
	Target       Snapshot `json:"target"`
	Expected     []Entry  `json:"expected"`
	ErrorCode    string   `json:"error_code"`
	ErrorMessage string   `json:"error_message"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "test", "schema-diff-vectors")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取向量目录失败：%v", err)
	}
	var out []vector
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", e.Name(), err)
		}
		var v vector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析 %s 失败：%v", e.Name(), err)
		}
		out = append(out, v)
	}
	if len(out) < 15 {
		t.Fatalf("向量语料过少：%d", len(out))
	}
	return out
}

func TestSharedVectorsMatchExpectedPlan(t *testing.T) {
	for _, v := range loadVectors(t) {
		got, err := Diff(&v.Source, &v.Target, &v.Options)
		if v.ErrorCode != "" {
			if err == nil {
				t.Fatalf("%s 应当报错，实得 %d 条计划", v.Name, len(got))
			}
			pe, ok := err.(*protocol.PolyDBError)
			if !ok {
				t.Fatalf("%s 错误类型不符：%#v", v.Name, err)
			}
			if pe.Code != v.ErrorCode || pe.Message != v.ErrorMessage {
				t.Fatalf("%s 错误不符：got [%s] %s, want [%s] %s",
					v.Name, pe.Code, pe.Message, v.ErrorCode, v.ErrorMessage)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s 意外报错：%v", v.Name, err)
		}
		if len(got) != len(v.Expected) {
			t.Fatalf("%s 条目数不符：got %d want %d\n got = %s\nwant = %s",
				v.Name, len(got), len(v.Expected), dump(t, got), dump(t, v.Expected))
		}
		for i := range got {
			if !reflect.DeepEqual(got[i], v.Expected[i]) {
				t.Fatalf("%s 第 %d 条不符：\n got = %s\nwant = %s",
					v.Name, i, dump(t, []Entry{got[i]}), dump(t, []Entry{v.Expected[i]}))
			}
		}
		again, err := Diff(&v.Source, &v.Target, &v.Options)
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatalf("%s 输出不稳定", v.Name)
		}
	}
}

func TestSharedVectorsCoverEveryOp(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range loadVectors(t) {
		for _, e := range v.Expected {
			seen[string(e.Op)] = true
		}
	}
	for _, op := range []Op{
		OpCreateTable, OpDropTable, OpAddColumn, OpDropColumn, OpAlterColumn,
		OpAddIndex, OpDropIndex, OpAddForeignKey, OpDropForeignKey,
	} {
		if !seen[string(op)] {
			t.Fatalf("向量语料未覆盖 op：%s", op)
		}
	}
}

func dump(t *testing.T, entries []Entry) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return string(b)
}
