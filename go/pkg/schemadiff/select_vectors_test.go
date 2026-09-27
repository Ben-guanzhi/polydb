package schemadiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 共享"勾选校验"向量语料：与 Rust 侧 crates/schema-diff/tests/select_vectors.rs 读同一批文件。
//
// 与 DDL 语料的分工：DDL 语料是"路过"select_entries（只断言最终脚本），
// 这一份直接锁定 picked 的 **id 序列**、条目本体不被篡改、以及两条选择规则的逐字文案。
// 计划一律由双端各自跑 Diff 现算 ⇒ 同一条用例同时钉住"计划序"与"勾选结果"。
//
// selected 的口径与 DDL 语料一致：null 表示「全部条目」，[] 表示「一条都不勾」，非空数组是勾选子集
// （**其中的顺序刻意可以与计划序不同**，用来证明输出只认计划序）。

type selectVector struct {
	Name         string    `json:"name"`
	Note         string    `json:"note"`
	Options      Options   `json:"options"`
	Source       Snapshot  `json:"source"`
	Target       Snapshot  `json:"target"`
	Selected     *[]string `json:"selected"`
	Expected     *[]string `json:"expected"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
}

func selectVectorsDir() string {
	return filepath.Join("..", "..", "..", "test", "schema-select-vectors")
}

func selectVectorFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(selectVectorsDir())
	if err != nil {
		t.Fatalf("读取勾选向量目录失败：%v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, filepath.Join(selectVectorsDir(), e.Name()))
		}
	}
	if len(out) < 10 {
		t.Fatalf("勾选向量语料过少：%d", len(out))
	}
	return out
}

func loadSelectVectors(t *testing.T) []selectVector {
	t.Helper()
	var out []selectVector
	for _, p := range selectVectorFiles(t) {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", p, err)
		}
		var v selectVector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析 %s 失败：%v", p, err)
		}
		checkVectorName(t, p, v.Name)
		out = append(out, v)
	}
	return out
}

// selectedOf 把 null 展开成计划里的全部 id（口径同 DDL 语料）。
func selectedOf(v *selectVector, plan []Entry) []string {
	if v.Selected != nil {
		return *v.Selected
	}
	out := make([]string, 0, len(plan))
	for _, e := range plan {
		out = append(out, e.ID)
	}
	return out
}

// runSelectVector 返回 (计划, picked, 错误)。Diff 阶段报错视为语料写坏了，直接失败。
func runSelectVector(t *testing.T, v *selectVector) ([]Entry, []Entry, error) {
	t.Helper()
	plan, err := Diff(&v.Source, &v.Target, &v.Options)
	if err != nil {
		t.Fatalf("%s 的两侧快照跑不出计划（语料写坏）：%v", v.Name, err)
	}
	picked, err := SelectEntries(&v.Source, &v.Target, plan, selectedOf(v, plan))
	return plan, picked, err
}

func idsOf(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

func sameIDs(a, b []string) bool {
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

func dumpIDs(t *testing.T, ids []string) string {
	t.Helper()
	b, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return string(b)
}

func TestSharedSelectVectorsMatchExpectedPicked(t *testing.T) {
	for _, v := range loadSelectVectors(t) {
		plan, picked, err := runSelectVector(t, &v)
		if v.ErrorCode != "" {
			if err == nil {
				t.Fatalf("%s 应当报错，实得 picked %s", v.Name, dumpIDs(t, idsOf(picked)))
			}
			code, msg := errCodeMessage(err)
			if code != v.ErrorCode || msg != v.ErrorMessage {
				t.Fatalf("%s 错误不符：got [%s] %s, want [%s] %s",
					v.Name, code, msg, v.ErrorCode, v.ErrorMessage)
			}
			if len(picked) != 0 {
				t.Fatalf("%s 报错时不得返回条目：%s", v.Name, dumpIDs(t, idsOf(picked)))
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s 意外报错：%v", v.Name, err)
		}
		if v.Expected == nil {
			t.Fatalf("%s 缺 expected（非错误语料必须锁定 picked）", v.Name)
		}
		got := idsOf(picked)
		// nil 与空数组在纯函数层等价（线路上的 []/null 纪律由 DTO 与契约保证）。
		if len(got) != len(*v.Expected) || !sameIDs(got, *v.Expected) {
			t.Fatalf("%s picked 不符：\n got = %s\nwant = %s",
				v.Name, dumpIDs(t, got), dumpIDs(t, *v.Expected))
		}
		// picked 必须是计划的**子序列**（顺序单调），且条目本体逐字段未被改写。
		prev := -1
		for i := range picked {
			at := -1
			for j := range plan {
				if plan[j].ID == picked[i].ID {
					at = j
					break
				}
			}
			if at < 0 {
				t.Fatalf("%s picked 第 %d 条 %s 不在计划里", v.Name, i, picked[i].ID)
			}
			if at <= prev {
				t.Fatalf("%s picked 顺序不是计划序（第 %d 条落在计划位置 %d）", v.Name, i, at)
			}
			prev = at
			if !reflect.DeepEqual(picked[i], plan[at]) {
				t.Fatalf("%s picked 第 %d 条被改写：got %+v, plan %+v", v.Name, i, picked[i], plan[at])
			}
		}
		// 幂等：同一输入再选一次必须一模一样。
		_, again, err := runSelectVector(t, &v)
		if err != nil || !sameIDs(idsOf(again), got) {
			t.Fatalf("%s 输出不稳定", v.Name)
		}
	}
}

// 两条勾选文案的固定前缀（与 ddl.go::SelectEntries 逐字对应，也是覆盖度分类的依据）。
const (
	stalePrefix = "勾选的条目不在当前比对计划里："
	groupPrefix = "需要整表重建，同表变更必须整组勾选："
)

// TestSharedSelectVectorsCoverage 是覆盖度门禁：勾选校验的每条分支都得有语料打到，
// 且必须存在"勾选顺序 ≠ 计划顺序"的用例，否则那条关键断言会静默失效。
func TestSharedSelectVectorsCoverage(t *testing.T) {
	cov := map[string]int{}
	for _, v := range loadSelectVectors(t) {
		plan, picked, err := runSelectVector(t, &v)
		switch {
		case v.Selected == nil:
			cov["selected_null"]++
		case len(*v.Selected) == 0:
			cov["selected_empty"]++
		}
		for i := range v.Source.Tables {
			if isView(&v.Source.Tables[i]) {
				cov["view_present"]++
			}
		}
		// picked 非空但少于计划 ⇒ 真子集勾选；picked 里含重建条目且仍成功 ⇒ 整组勾全通过。
		if len(picked) > 0 && len(picked) < len(plan) {
			cov["subset_pass"]++
		}
		if err == nil && v.Selected != nil && len(picked) < len(*v.Selected) {
			// 勾选给定的 id 数多于产出条目数 ⇒ 重复 id 被并掉。
			cov["duplicate_ids"]++
		}
		if v.Selected != nil && len(*v.Selected) > 0 && len(picked) == 0 && err == nil {
			// 勾了 id 却一条没中且不报错：只有「勾中的全不在计划里」才会这样，而那种情况必须先报 stale。
			t.Fatalf("%s 勾了非空 id 却既没产出也没报错", v.Name)
		}
		if err == nil {
			if hasRebuildEntry(&v, picked) {
				cov["group_whole_pass"]++
			}
			// 计划序不等于 id 字典序 ⇒ 「按计划序输出」不是某种顺带排序的结果。
			lex := append([]string{}, idsOf(picked)...)
			sort.Strings(lex)
			if len(picked) >= 2 && !sameIDs(lex, idsOf(picked)) {
				cov["plan_order_not_lexicographic"]++
			}
		}
		if err != nil {
			code, msg := errCodeMessage(err)
			switch {
			case strings.HasPrefix(msg, stalePrefix):
				cov["stale"]++
				if strings.Contains(msg, "、") {
					cov["stale_multi"]++
				}
				// 显示大小写的 id 被判过期 ⇒ 「勾选层不做大小写兜底」的护栏。
				if strings.Contains(msg, "Foo/") {
					cov["stale_display_casing"]++
				}
				// 分支优先级：把过期 id 摘掉后重跑一次，若改报缺组 ⇒ 这次勾选同时踩中两个分支，
				// 而实际报的是 stale（只有 s14 会走到）。
				if _, e2 := SelectEntries(&v.Source, &v.Target, plan, selectedInPlan(&v, plan)); e2 != nil &&
					strings.Contains(errMsgOf(e2), groupPrefix) {
					cov["stale_beats_group"]++
				}
			case strings.Contains(msg, groupPrefix):
				cov["group"]++
				if strings.Contains(msg, "、") {
					cov["group_multi_missing"]++
				}
				// 文案里的表名用原样大小写、id 用小写形式。
				if strings.Contains(msg, "表 Foo") {
					cov["group_mixed_case_display"]++
				}
			default:
				t.Fatalf("%s 勾选错误文案不在已知分支里：[%s] %s", v.Name, code, msg)
			}
			continue
		}
		if v.Selected == nil || len(picked) < 2 {
			continue
		}
		// 勾选数组自身给定的次序（去重、去掉幽灵 id）与 picked 次序不同 ⇒ 证明输出只认计划序。
		if !sameIDs(seqAsGiven(*v.Selected, idsOf(picked)), idsOf(picked)) {
			cov["unordered_selected"]++
		}
	}
	for _, key := range []string{
		"selected_null", "selected_empty", "subset_pass", "duplicate_ids", "unordered_selected",
		"view_present", "plan_order_not_lexicographic",
		"stale", "stale_multi", "stale_display_casing", "stale_beats_group",
		"group", "group_multi_missing", "group_mixed_case_display", "group_whole_pass",
	} {
		if cov[key] == 0 {
			t.Fatalf("勾选语料缺少分支覆盖：%s（计数 %v）", key, cov)
		}
	}
	if n := len(loadSelectVectors(t)); n < 10 {
		t.Fatalf("勾选语料份数过少：%d", n)
	}
}

// selectedInPlan 只保留确实落在计划里的 id（null 展开成全部），用于复算
// 「若这次勾选没有幽灵 id，它本会撞上哪个分支」。
func selectedInPlan(v *selectVector, plan []Entry) []string {
	if v.Selected == nil {
		return idsOf(plan)
	}
	in := map[string]bool{}
	for i := range plan {
		in[plan[i].ID] = true
	}
	var out []string
	for _, id := range *v.Selected {
		if in[id] {
			out = append(out, id)
		}
	}
	return out
}

// hasRebuildEntry 用同包的 needsRebuild 复核 picked 里是否真有重建条目。
func hasRebuildEntry(v *selectVector, picked []Entry) bool {
	src, tgt := tableMap(&v.Source), tableMap(&v.Target)
	for i := range picked {
		e := &picked[i]
		if needsRebuild(e, src[lowerASCII(e.Table)], tgt[lowerASCII(e.Table)]) {
			return true
		}
	}
	return false
}

func errMsgOf(err error) string {
	_, msg := errCodeMessage(err)
	return msg
}

// seqAsGiven 把 want 里"确实被勾选进结果"的那些 id 按其**给出次序**列出（首个出现胜出）。
func seqAsGiven(want, got []string) []string {
	inGot := map[string]bool{}
	for _, id := range got {
		inGot[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range want {
		if inGot[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// TestWriteSelectVectorExpected 是语料生产器，仅在显式置环境变量时运行：
//
//	POLYDB_SELECT_VECTORS_WRITE=1 go test ./pkg/schemadiff -run WriteSelectVectorExpected
//
// 只替换 expected / error_code / error_message 三个字段的值，其余字节逐字不动。
// 写入后必须人工复核（尤其错误文案里点名的表与缺组顺序），再由 Rust 侧加载器做第二实现验证。
func TestWriteSelectVectorExpected(t *testing.T) {
	if os.Getenv("POLYDB_SELECT_VECTORS_WRITE") != "1" {
		t.Skip("只读模式：置 POLYDB_SELECT_VECTORS_WRITE=1 才回写期望")
	}
	for _, p := range selectVectorFiles(t) {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", p, err)
		}
		var v selectVector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析 %s 失败：%v", p, err)
		}
		_, picked, err := runSelectVector(t, &v)
		out := raw
		nl := "\n"
		if bytes.Contains(raw, []byte("\r\n")) {
			nl = "\r\n"
		}
		write := func(key, block string) {
			var e error
			out, e = replaceJSONValue(out, key, block, nl)
			if e != nil {
				t.Fatalf("%s 回写 %s 失败：%v", v.Name, key, e)
			}
		}
		if err != nil {
			code, msg := errCodeMessage(err)
			write("expected", "null")
			write("error_code", fmt.Sprintf("%q", code))
			write("error_message", quoteJSON(msg))
		} else {
			block := "[]"
			if len(picked) > 0 {
				block = indentIDs(idsOf(picked), "  ")
			}
			write("expected", block)
			write("error_code", `""`)
			write("error_message", `""`)
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

// quoteJSON 用 UTF-8 原样写中文（%q 会转义成 \u，git diff 读不动）。
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func indentIDs(ids []string, pad string) string {
	var sb bytes.Buffer
	sb.WriteString("[\n")
	for i, id := range ids {
		sb.WriteString(pad)
		sb.WriteString(quoteJSON(id))
		if i < len(ids)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(pad[:len(pad)-2] + "]")
	return sb.String()
}
