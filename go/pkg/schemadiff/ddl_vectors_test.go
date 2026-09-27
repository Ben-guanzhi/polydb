package schemadiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

// 共享 DDL 向量语料：与 Rust 侧 crates/schema-diff/tests/ddl_vectors.rs 读同一批文件。
//
// 流程与真实调用方一致：diff(source, target) → 按 selected 过滤 → generate(同一两侧, 过滤后的条目)。
// selected 为 null 表示「全部条目」，[] 表示「一条都不勾」，非空数组表示勾选子集
// （其中的 id 必须是本次 diff 真实产出的 id，否则测试直接失败，防止语料与 E1 语义脱钩）。

// ddlVector 是一条语料。expected 用指针区分「null」与「空脚本」。
type ddlVector struct {
	Name         string    `json:"name"`
	Note         string    `json:"note"`
	Options      Options   `json:"options"`
	Source       Snapshot  `json:"source"`
	Target       Snapshot  `json:"target"`
	Selected     *[]string `json:"selected"`
	Expected     *Script   `json:"expected"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
}

func ddlVectorsDir() string { return filepath.Join("..", "..", "..", "test", "schema-ddl-vectors") }

func ddlVectorFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(ddlVectorsDir())
	if err != nil {
		t.Fatalf("读取 DDL 向量目录失败：%v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, filepath.Join(ddlVectorsDir(), e.Name()))
		}
	}
	if len(out) < 18 {
		t.Fatalf("DDL 向量语料过少：%d", len(out))
	}
	return out
}

func loadDDLVectors(t *testing.T) []ddlVector {
	t.Helper()
	var out []ddlVector
	for _, p := range ddlVectorFiles(t) {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", p, err)
		}
		var v ddlVector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析 %s 失败：%v", p, err)
		}
		checkVectorName(t, p, v.Name)
		out = append(out, v)
	}
	return out
}

// runDDLVector 跑完整管线：diff → SelectEntries → Generate。
// 三个阶段中任何一个报错，都必须与 error_code / error_message 逐字一致。
func runDDLVector(v *ddlVector) (*Script, error) {
	plan, err := Diff(&v.Source, &v.Target, &v.Options)
	if err != nil {
		return nil, err
	}
	// selected 为 null 表示「全部条目」，因此要显式展开成 plan 里的全部 id。
	var selected []string
	if v.Selected != nil {
		selected = *v.Selected
	} else {
		selected = make([]string, 0, len(plan))
		for _, e := range plan {
			selected = append(selected, e.ID)
		}
	}
	picked, err := SelectEntries(&v.Source, &v.Target, plan, selected)
	if err != nil {
		return nil, err
	}
	return Generate(&v.Source, &v.Target, picked, &v.Options)
}

func errCodeMessage(err error) (string, string) {
	pe, ok := err.(*protocol.PolyDBError)
	if !ok {
		return "<非 PolyDBError>", err.Error()
	}
	return pe.Code, pe.Message
}

func sameStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameStatement 有意把 nil 与空切片视为相等：纯函数层的 Statement 不是线路类型，
// 空集合的两种表示都合法，线路上的 []/null 纪律由 protocol DTO 与契约测试保证。
func sameStatement(a, b Statement) bool {
	return sameStrs(a.EntryIDs, b.EntryIDs) &&
		a.SQL == b.SQL &&
		a.Phase == b.Phase &&
		a.Table == b.Table &&
		sameStrs(a.Warnings, b.Warnings) &&
		a.NonReversible == b.NonReversible
}

func sameStatements(a, b []Statement) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameStatement(a[i], b[i]) {
			return false
		}
	}
	return true
}

func dumpScript(t *testing.T, s *Script) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return string(b)
}

func checkScript(t *testing.T, name string, got, want *Script) {
	t.Helper()
	if !sameStatements(got.Statements, want.Statements) {
		t.Fatalf("%s 正向脚本不符：\n got = %s\nwant = %s", name, dumpScript(t, got), dumpScript(t, want))
	}
	if !sameStatements(got.Rollback, want.Rollback) {
		t.Fatalf("%s 回滚脚本不符：\n got = %s\nwant = %s", name, dumpScript(t, got), dumpScript(t, want))
	}
	if got.PragmasOutsideTx != want.PragmasOutsideTx {
		t.Fatalf("%s pragmas_outside_tx：got=%v want=%v", name, got.PragmasOutsideTx, want.PragmasOutsideTx)
	}
	if got.DdlInTx != want.DdlInTx {
		t.Fatalf("%s ddl_in_tx：got=%v want=%v", name, got.DdlInTx, want.DdlInTx)
	}
}

func TestSharedDDLVectorsMatchExpectedScript(t *testing.T) {
	for _, v := range loadDDLVectors(t) {
		got, err := runDDLVector(&v)
		if v.ErrorCode != "" {
			if err == nil {
				t.Fatalf("%s 应当报错，实得脚本 %s", v.Name, dumpScript(t, got))
			}
			code, msg := errCodeMessage(err)
			if code != v.ErrorCode || msg != v.ErrorMessage {
				t.Fatalf("%s 错误不符：got [%s] %s, want [%s] %s",
					v.Name, code, msg, v.ErrorCode, v.ErrorMessage)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s 意外报错：%v", v.Name, err)
		}
		if v.Expected == nil {
			t.Fatalf("%s 缺 expected（非错误语料必须锁定脚本）", v.Name)
		}
		checkScript(t, v.Name, got, v.Expected)
		// 幂等：同一输入再生成一次必须逐字一致
		again, err := runDDLVector(&v)
		if err != nil || !sameStatements(got.Statements, again.Statements) ||
			!sameStatements(got.Rollback, again.Rollback) {
			t.Fatalf("%s 输出不稳定", v.Name)
		}
	}
}

func TestSharedDDLVectorsCoverEveryOpAndWarning(t *testing.T) {
	ops := map[string]bool{}
	seen := map[string]bool{}
	nr := map[bool]int{}
	pragmas := map[bool]int{}
	for _, v := range loadDDLVectors(t) {
		plan, err := Diff(&v.Source, &v.Target, &v.Options)
		if err != nil {
			continue
		}
		vv := v
		got, err := runDDLVector(&vv)
		if err != nil {
			continue
		}
		if v.Selected == nil {
			for _, e := range plan {
				ops[string(e.Op)] = true
			}
		} else {
			want := map[string]bool{}
			for _, id := range *v.Selected {
				want[id] = true
			}
			for _, e := range plan {
				if want[e.ID] {
					ops[string(e.Op)] = true
				}
			}
		}
		for _, st := range append(append([]Statement{}, got.Statements...), got.Rollback...) {
			for _, w := range st.Warnings {
				seen[w] = true
			}
			nr[st.NonReversible]++
		}
		pragmas[got.PragmasOutsideTx]++
	}
	for _, op := range []Op{
		OpCreateTable, OpDropTable, OpAddColumn, OpDropColumn, OpAlterColumn,
		OpAddIndex, OpDropIndex, OpAddForeignKey, OpDropForeignKey,
	} {
		if !ops[string(op)] {
			t.Fatalf("DDL 语料未覆盖动作：%s", op)
		}
	}
	for _, w := range []string{
		WTriggers, WGeneratedColumns, WCheckConstraints, WAutoincrement,
		WCreateTable, WDropColumnCheck, WAddColumnLimits, WRebuildNewNotNull,
	} {
		if !seen[w] {
			t.Fatalf("DDL 语料未覆盖告警：%s", w)
		}
	}
	// 视图警告（E6.5）是带名字的动态文案，不能按常量逐字比 ⇒ 退一步锁它的两句话头，
	// 完整的逐字一致性由 d24/d25 两份语料的 expected 兜住。
	for _, needle := range []string{"DROP TABLE 不检查依赖", "重建经 RENAME"} {
		hit := false
		for w := range seen {
			if strings.Contains(w, needle) {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("DDL 语料未覆盖视图警告：%s", needle)
		}
	}
	if nr[true] == 0 || nr[false] == 0 {
		t.Fatalf("DDL 语料必须同时覆盖可逆与不可逆语句：%v", nr)
	}
	if pragmas[true] == 0 || pragmas[false] == 0 {
		t.Fatalf("DDL 语料必须同时覆盖含重建与不含重建的脚本：%v", pragmas)
	}
}

// TestWriteDDLVectorExpected 是语料生产器，仅在显式置环境变量时运行：
//
//	POLYDB_DDL_VECTORS_WRITE=1 go test ./pkg/schemadiff -run WriteDDLVectorExpected
//
// 它只替换每个文件里 "expected" 这一个 JSON 值（按字节定位区间、保留原有换行风格），
// 其余内容逐字节不动，从而让 git diff 只反映期望脚本本身。
// 写入后必须人工复核 SQL，再由 Rust 侧加载器做第二实现验证。
func TestWriteDDLVectorExpected(t *testing.T) {
	if os.Getenv("POLYDB_DDL_VECTORS_WRITE") != "1" {
		t.Skip("只读模式：置 POLYDB_DDL_VECTORS_WRITE=1 才回写 expected")
	}
	for _, p := range ddlVectorFiles(t) {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", p, err)
		}
		var v ddlVector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析 %s 失败：%v", p, err)
		}
		got, err := runDDLVector(&v)
		block := "null"
		if v.ErrorCode == "" {
			if err != nil {
				t.Fatalf("%s 生成失败，无法写入期望：%v", v.Name, err)
			}
			block, err = indentJSON(got, "  ")
			if err != nil {
				t.Fatalf("%s 序列化失败：%v", v.Name, err)
			}
		}
		nl := "\n"
		if bytes.Contains(raw, []byte("\r\n")) {
			nl = "\r\n"
		}
		out, err := replaceExpectedValue(raw, block, nl)
		if err != nil {
			t.Fatalf("%s 回写失败：%v", v.Name, err)
		}
		if bytes.Equal(out, raw) {
			continue
		}
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", p, err)
		}
		t.Logf("已回写 %s", p)
	}
}

func indentJSON(s *Script, pad string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	for i := 1; i < len(lines); i++ {
		lines[i] = append([]byte(pad), lines[i]...)
	}
	return string(bytes.Join(lines, []byte("\n"))), nil
}

// replaceExpectedValue 定位 `"expected":` 后的那个 JSON 值并整段替换。
func replaceExpectedValue(raw []byte, replacement, nl string) ([]byte, error) {
	return replaceJSONValue(raw, "expected", replacement, nl)
}

// replaceJSONValue 定位 `"<key>":` 后的那个 JSON 值并整段替换，其余字节逐字不动。
// 语料生产器用它只改期望值，git diff 因而只反映期望本身。
func replaceJSONValue(raw []byte, key, replacement, nl string) ([]byte, error) {
	needle := []byte(`"` + key + `":`)
	i := bytes.Index(raw, needle)
	if i < 0 {
		return nil, fmt.Errorf("文件里没有 %s 字段", key)
	}
	start := i + len(needle)
	for start < len(raw) && (raw[start] == ' ' || raw[start] == '\t') {
		start++
	}
	depth := 0
	end := -1
	inStr := false
	esc := false
	for j := start; j < len(raw); j++ {
		c := raw[j]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
		case depth > 0 && (c == '}' || c == ']'):
			depth--
			if depth == 0 {
				end = j + 1
			}
		case depth == 0 && (c == ',' || c == '\n' || c == '\r'):
			end = j
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("expected 字段值区间解析失败")
	}
	repl := []byte(replacement)
	if nl != "\n" {
		repl = bytes.ReplaceAll(repl, []byte("\n"), []byte(nl))
	}
	out := make([]byte, 0, len(raw)-(end-start)+len(repl))
	out = append(out, raw[:start]...)
	out = append(out, repl...)
	out = append(out, raw[end:]...)
	return out, nil
}
