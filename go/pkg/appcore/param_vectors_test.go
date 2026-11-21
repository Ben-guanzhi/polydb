package appcore

// 共享向量对拍（§384）：读 test/param-vectors/*.json，Go 侧内核
// sqltext.ExtractParams/RewritePlaceholders 的输出必须与 expected 逐字段
// 相等——Rust 侧 polydb-params 读同一批文件做同样断言（schema-diff 先例）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Ben-guanzhi/dbcore/sqltext"
)

type paramVector struct {
	Name    string   `json:"name"`
	Note    string   `json:"note"`
	SQL     string   `json:"sql"`
	Labels  []string `json:"labels"`
	Rewrite struct {
		DBType string `json:"db_type"`
		SQL    string `json:"sql"`
	} `json:"rewrite"`
}

func TestParamVectors(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "test", "param-vectors")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("向量语料缺失: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var v paramVector
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			// 语料自检：name 必须等于文件名（去 .json）。
			wantName := filepath.Base(f[:len(f)-len(filepath.Ext(f))])
			if v.Name != wantName {
				t.Errorf("name %q != file %q", v.Name, wantName)
			}

			refs := sqltext.ExtractParams(v.SQL)
			var labels []string
			for _, r := range refs {
				labels = append(labels, r.Label())
			}
			if joinComma(labels) != joinComma(v.Labels) {
				t.Errorf("labels = %v, want %v", labels, v.Labels)
			}

			out := sqltext.RewritePlaceholders(v.SQL, refs, v.Rewrite.DBType)
			if out != v.Rewrite.SQL {
				t.Errorf("rewrite = %q, want %q", out, v.Rewrite.SQL)
			}
		})
	}
}

func joinComma(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
