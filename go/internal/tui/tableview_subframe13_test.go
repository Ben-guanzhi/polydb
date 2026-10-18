package tui

// 第八轮 ② 段（报告 §13）：限定列那一支的子帧**只物化渲染行窗口**（候选 D）＋ 顺手带上
// 候选 C 的 `make([]any, 0, win)` 预分配。机制原文＝报告 §12.6-1 的推荐候选；选型与定价在
// D:\tmp\round9_subframe\SELECT13.md（① 段，只量不改）。
//
// 这一轮的机制和上一轮**不同**：§11/§12 动的是宽度缓存（键/那块 buf），这一轮动的是
// "子帧到底有多少行"。所以测试的形状也必须换：上一轮钉"命中路径存在"，这一轮钉
// **"渲染输出逐字节不变"**（金样）＋ **"子帧行数必须等于窗口而不是父帧"**（构造性）。
//
// 金样纪律（§4(a)）：期望串不是手写的，是**改码之前**在 pristine 的 tableview.go
// （md5 ba750de06c862fc46cdd2f557cd21eac / 926 行）上渲染出来、写到磁盘的。
// 所以期望的权威在**盘上的文件**里，不在本文件的字面量里；本文件一个期望串都不带。
// 金样目录空着 ⇒ 比对测试必须**失败**，且失败原因由跑测试的人通过环境变量递进来，
// 会原样出现在 `go test` 的输出里（这条设计是为了让"期望还没取"在闸门日志里可见，
// 而不是被读成一次通过）。
//
// 变异电池（§4(e)）的七臂见 mkgate13.sh；每条臂的锚串都印在 mutlog_round13.txt 里。

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/internal/theme"
)

const (
	// 金样目录（仓库内唯一权威，随代码一起版本化，CI 才跑得起来）。仓库相对路径，
	// 绝不写 /tmp、%TEMP% 或绝对路径：本机上一轮写在那里的产物已被清空，而绝对路径
	// 只在作者本机存在，CI 的 Linux 上根本不存在（那会让 38/38 份金样全部读不到）。
	p13bGoldenDir = "testdata/golden-subframe13"
	// 期望还没取时，跑测试的人把原因放进这个环境变量；compare 测试把它打进输出。
	p13bReasonEnv = "SUBFRAME13_GOLDEN_REASON"
	// 写了金样才允许为 1——平时它是关的，compare 测试才是常跑的那一个。
	p13bWriteEnv = "SUBFRAME13_GOLDEN_WRITE"
)

// --- 合成料：手搭，不用 tuidata.New，这样父帧与"窗口子帧"的 CellString / Cell
// 两条读路逐格同数据（§12 的 A-4 教训：只喂一条读路的料测不到另一条）。

func p13bValue(r, c int, long bool, cjk bool) string {
	var b strings.Builder
	if cjk {
		b.WriteString("客户名称测试数据")
		b.WriteString(strconv.Itoa((r + c) % 8))
	} else {
		b.WriteString("customer name here ")
		b.WriteString(strconv.Itoa((r*7 + c*3) % 89))
	}
	if long {
		b.WriteString(" tail tail tail tail tail tail tail tail tail tail")
	}
	return b.String()
}

// p13bFrame 手搭一个 rows×cols 的帧。outOfRange 只影响某列 cells 的**长度**
// （那一列少一格）——它专门给"窗口外必 panic"那条性质用，金样用例一律传 -1。
func p13bFrame(rows, cols int, long bool, outOfRange int, cjk bool) *tuidata.Frame {
	f := &tuidata.Frame{Columns: make([]tuidata.Column, cols)}
	for c := 0; c < cols; c++ {
		n := rows
		if outOfRange >= 0 && c == outOfRange {
			n = rows - 1
		}
		cells := make([]any, n)
		for r := 0; r < n; r++ {
			cells[r] = p13bValue(r, c, long, cjk)
		}
		f.Columns[c] = tuidata.NewColumn("c"+strconv.Itoa(c), tuidata.TypeString, cells)
	}
	return f
}

// --- 用例矩阵
//
// 字段顺序（每条 17 个值）：
// id, rows, cols, colsSel, w, h, borders, rowNumbers, hscroll, sel,
// severity, highlight, noEllipsis, long, outOfRange, cjk, expandedFirst

type p13bCase struct {
	id            string
	rows, cols    int
	colsSel       []int
	w, h          int
	borders       bool
	rowNumbers    bool
	hscroll       int
	sel           int // tv.sel（Render 自己会钳；-1＝不动 sel）
	severity      string
	highlight     string
	noEllipsis    bool
	long          bool
	outOfRange    int
	cjk           bool
	expandedFirst bool // 首帧强制 expanded ⇒ 走 :311 那一支
}

func p13bNth(n int) []int {
	a := make([]int, n)
	for i := range a {
		a[i] = i
	}
	return a
}

func p13bCases() []p13bCase {
	return []p13bCase{
		// E 档形状（判据基准同一形状：50000 行 / 20 列 / cols={0,1} / 120x40）
		{"E_cols2_base0", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, -1, "", "", false, false, -1, false, false},
		{"E_cols2_base0_borders", 50000, 20, []int{0, 1}, 120, 40, true, true, 0, -1, "", "", false, false, -1, false, false},
		{"E_cols2_base300", 50000, 20, []int{0, 1}, 120, 40, false, true, 7, 300, "", "", false, false, -1, false, false},
		{"E_cols2_base_tail", 50000, 20, []int{0, 1}, 120, 40, true, true, 0, 49999, "", "", false, false, -1, false, false},
		{"E_cols2_hscroll", 50000, 20, []int{0, 1}, 120, 40, false, false, 13, 500, "", "", false, false, -1, false, false},
		{"E_cols2_highlight", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, 700, "", "name here 1", false, false, -1, false, false},
		{"E_cols2_severity", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, 111, "c3", "", false, false, -1, false, false},
		{"E_cols2_longcells", 50000, 20, []int{0, 1}, 120, 40, true, true, 0, 900, "", "", true, true, -1, false, false},
		{"E_cols2_noellipsis", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, 1200, "", "", true, false, -1, false, false},
		{"E_cols2_cjk", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, 1500, "", "", false, true, -1, false, false},
		{"E_cols2_expanded", 50000, 20, []int{0, 1}, 120, 40, false, false, 0, 33, "", "", false, false, -1, true, true},
		{"E_allcols", 50000, 20, p13bNth(20), 120, 40, true, true, 0, 2500, "", "", false, false, -1, false, false},
		{"E_cols3_reverse", 50000, 20, []int{4, 2, 0}, 120, 40, false, false, 0, 250, "", "", false, false, -1, false, false},
		{"E_outofrange_col", 50000, 20, []int{0, 1, 99}, 120, 40, false, false, 0, 260, "", "", false, false, -1, false, false},
		{"E_single_col", 50000, 20, []int{1}, 120, 40, false, true, 0, 270, "", "", false, false, -1, false, false},
		{"E_dup_cols", 50000, 20, []int{0, 0, 1}, 120, 40, false, false, 0, 280, "", "", false, false, -1, false, false},

		// 窗口边界形状
		{"rows39_body39", 39, 4, []int{0, 2}, 100, 40, false, false, 0, -1, "", "", false, false, -1, false, false},
		{"rows40_body40", 40, 4, []int{0, 2}, 100, 41, false, true, 0, -1, "", "", false, false, -1, false, false},
		{"rows41_body41", 41, 4, []int{0, 2}, 100, 42, true, true, 0, -1, "", "", false, false, -1, false, false},
		{"rows3_body1", 3, 4, []int{0, 2}, 100, 2, false, true, 0, -1, "", "", false, false, -1, false, false},
		{"rows50000_body1", 50000, 4, []int{0, 2}, 100, 2, false, true, 0, 40000, "", "", false, false, -1, false, false},
		{"rows200_sample", 200, 4, []int{0, 2}, 100, 40, false, false, 0, -1, "", "", false, false, -1, false, false},
		{"rows201_sample", 201, 4, []int{0, 2}, 100, 40, false, false, 0, -1, "", "", false, false, -1, false, false},
		{"rows200_base200", 200, 4, []int{0, 2}, 100, 40, false, true, 0, 199, "", "", false, false, -1, false, false},
		{"rows201_base200", 201, 4, []int{0, 2}, 100, 40, false, true, 0, 200, "", "", false, false, -1, false, false},
		{"rows1_base1", 1, 4, []int{0, 2}, 100, 40, false, true, 0, -1, "", "", false, false, -1, false, false},
		{"rows0_empty", 0, 4, []int{0, 2}, 100, 40, false, true, 0, -1, "", "", false, false, -1, false, false},
		{"rows0_onecol", 0, 1, []int{0}, 100, 40, false, false, 0, -1, "", "", false, false, -1, false, false},
		{"cols1_onecol", 500, 1, []int{0}, 100, 40, false, false, 0, 499, "", "", false, false, -1, false, false},

		// 窄屏 / 极端宽度（触发 :382 的头行截断与 avail 钳位）
		{"narrow_w8", 5000, 6, []int{0, 3}, 8, 24, true, true, 0, 120, "", "", false, false, -1, false, false},
		{"narrow_w3", 5000, 6, []int{0, 3}, 3, 24, true, true, 0, -1, "", "", false, false, -1, false, false},
		{"narrow_w1", 5000, 6, []int{0, 3}, 1, 24, false, false, 0, -1, "", "", false, false, -1, false, false},

		// 非限定列对照（TableCols==nil ⇒ 路径必须一字不变）
		{"ctrl_nocols_4rows", 4, 5, nil, 120, 40, false, true, 0, 2, "", "", false, false, -1, false, false},
		{"ctrl_nocols_bignums", 900, 5, nil, 120, 40, true, true, 0, 812, "c1", "", false, false, -1, false, false},
		{"ctrl_nocols_cjk", 5000, 5, nil, 120, 40, false, false, 0, -1, "", "客户", false, true, -1, false, false},
		{"ctrl_nocols_long", 5000, 5, nil, 120, 40, false, false, 5, -1, "", "", true, false, -1, false, false},
		{"ctrl_nocols_expanded", 5000, 5, nil, 120, 40, false, false, 0, -1, "", "", false, false, -1, true, true},

		// 同一 tv 连渲三帧（mode/游标状态跨帧的形状；D 之后必须仍与 pristine 同路）
		{"seq_same_tv", 5000, 6, []int{0, 2}, 120, 40, false, true, 0, 4999, "", "", false, false, -1, false, false},
	}
}

func p13bRenderCase(t *testing.T, c p13bCase) string {
	t.Helper()
	f := p13bFrame(c.rows, c.cols, c.long, c.outOfRange, c.cjk)
	tv := &TableView{}
	if c.expandedFirst {
		tv.expanded = true
	}
	if c.sel >= 0 {
		tv.JumpTo(c.sel, f.NumRows())
	}
	o := RenderOpts{
		Width:          c.w,
		Height:         c.h,
		Theme:          theme.Default(),
		ShowBorders:    c.borders,
		ShowRowNumbers: c.rowNumbers,
		TableCols:      c.colsSel,
		HScroll:        c.hscroll,
		SeverityCol:    c.severity,
		HighlightQuery: c.highlight,
		NoEllipsis:     c.noEllipsis,
		HeaderCur:      -1,
	}
	if c.id == "seq_same_tv" {
		// 第一帧定 mode，第二帧清 severity，第三帧挪光标——三帧共用同一块 tv 状态。
		out := tv.Render(f, o)
		o.SeverityCol = ""
		out2 := tv.Render(f, o)
		tv.Move(3, f.NumRows())
		return out + "\n===\n" + out2 + "\n===\n" + tv.Render(f, o)
	}
	return tv.Render(f, o)
}

func p13bGoldenPath(id string) string {
	return filepath.Join(p13bGoldenDir, id+".golden")
}

// TestSubFrame13GoldenWrite 只在显式开启时跑，且**必须**跑在改码之前。
func TestSubFrame13GoldenWrite(t *testing.T) {
	if os.Getenv(p13bWriteEnv) != "1" {
		t.Skip("金样写入需 SUBFRAME13_GOLDEN_WRITE=1（必须在改 tableview.go 之前跑一次）")
	}
	if err := os.MkdirAll(p13bGoldenDir, 0o755); err != nil {
		t.Fatalf("建金样目录：%v", err)
	}
	cases := p13bCases()
	for _, c := range cases {
		out := p13bRenderCase(t, c)
		if err := os.WriteFile(p13bGoldenPath(c.id), []byte(out), 0o644); err != nil {
			t.Fatalf("写金样 %s：%v", c.id, err)
		}
	}
	t.Logf("写了 %d 份金样到 %s", len(cases), p13bGoldenDir)
}

// TestSubFrame13GoldenCompare 逐字节比对。金样缺失 ⇒ 失败，并把递进来的原因原样打进输出。
func TestSubFrame13GoldenCompare(t *testing.T) {
	cases := p13bCases()
	missing := 0
	for _, c := range cases {
		if _, err := os.Stat(p13bGoldenPath(c.id)); err != nil {
			missing++
		}
	}
	if missing > 0 {
		reason := os.Getenv(p13bReasonEnv)
		if reason == "" {
			reason = "未提供原因（先跑 TestSubFrame13GoldenWrite，且必须在改 tableview.go 之前）"
		}
		t.Errorf("GOLDEN-MISSING-REASON: 缺 %d/%d 份金样，原因=%s", missing, len(cases), reason)
		t.FailNow()
	}
	n := 0
	for _, c := range cases {
		c := c
		t.Run(c.id, func(t *testing.T) {
			want, err := os.ReadFile(p13bGoldenPath(c.id))
			if err != nil {
				t.Fatalf("读金样：%v", err)
			}
			got := p13bRenderCase(t, c)
			if len(got) == 0 || len(want) == 0 {
				t.Fatalf("空输出 got=%d want=%d（空串会让比对失去意义）", len(got), len(want))
			}
			if got != string(want) {
				t.Fatalf("输出与金样不逐字节相等：len(got)=%d len(golden)=%d md5(got)=%s md5(golden)=%s\n--- got 前 400 字节 ---\n%s\n--- want 前 400 字节 ---\n%s",
					len(got), len(want), p13bHexMd5(got), p13bHexMd5(string(want)),
					p13bClip(got), p13bClip(string(want)))
			}
			n++
		})
	}
	t.Logf("COMPARE-OK: %d/%d 份金样逐字节相等", n, len(cases))
}

func p13bHexMd5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func p13bClip(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// TestSubFrame13GoldenManifest 把盘上金样的三元组打进测试输出（给 GOLDEN.txt 用，不写死数字）。
func TestSubFrame13GoldenManifest(t *testing.T) {
	if os.Getenv(p13bWriteEnv) != "manifest" {
		t.Skip("清单需要 SUBFRAME13_GOLDEN_WRITE=manifest")
	}
	entries, err := os.ReadDir(p13bGoldenDir)
	if err != nil {
		t.Fatalf("读目录：%v", err)
	}
	names := make([]string, 0, len(entries))
	total := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".golden") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var b strings.Builder
	for _, nm := range names {
		bs, err := os.ReadFile(filepath.Join(p13bGoldenDir, nm))
		if err != nil {
			t.Fatalf("读 %s：%v", nm, err)
		}
		total += len(bs)
		mm := md5.Sum(bs)
		fmt.Fprintf(&b, "%-26s %9d bytes md5 %s\n", nm, len(bs), hex.EncodeToString(mm[:]))
	}
	t.Logf("GOLDEN-MANIFEST-START\n%sGOLDEN-MANIFEST-END: %d files, %d bytes total", b.String(), len(names), total)
}

// --- (b) 构造性合同：子帧行数 == 窗口，不是父帧

func TestSubFrame13WindowContract(t *testing.T) {
	const rows, ncols = 10000, 7
	f := p13bFrame(rows, ncols, false, -1, false)
	colsSel := []int{0, 3, 6}
	shapes := []struct{ base, win int }{
		{0, 200}, {1, 200}, {37, 39}, {9961, 39}, {rows, 0}, {rows - 1, 1}, {5000, 200},
		{9990, 500}, // win 越出 rows-base ⇒ 必须被钳到 10 行（电池第⑧臂的靶）
	}
	for _, s := range shapes {
		want := s.win
		if want > rows-s.base {
			want = rows - s.base
		}
		if s.base > rows {
			want = 0
		}
		sub := tableColsSubFrameWindow(f, colsSel, s.base, s.win)
		if sub.NumRows() != want {
			t.Errorf("base=%d win=%d：子帧 NumRows()=%d，应为 %d（== 钳过的窗口，不是父帧的 %d）",
				s.base, s.win, sub.NumRows(), want, rows)
		}
		if sub.NumCols() != len(colsSel) {
			t.Errorf("base=%d win=%d：子帧 NumCols()=%d，应为 %d", s.base, s.win, sub.NumCols(), len(colsSel))
		}
		for j := 0; j < sub.NumRows(); j++ {
			for i, c := range colsSel {
				got := sub.CellString(j, i)
				want2 := f.CellString(s.base+j, c) // 父帧**绝对行**
				if got != want2 {
					t.Fatalf("base=%d win=%d 第 %d 行列 %d：got=%q want(绝对行 %d)=%q ⇒ off-by-base",
						s.base, s.win, j, i, got, s.base+j, want2)
				}
			}
		}
	}
}

// --- (b2) C 的那两行必须真的在：窗口子帧的分配次数是常数，不随行数生长
//
// 这条是电池第⑥臂（去掉 cap 预分配）的**唯一**杀手——去掉 cap 后行数、内容、渲染输出
// 全对，只有分配账变（AppendRow 一路 realloc）。所以拿 AllocsPerRun 钉它，不靠金样。
func TestSubFrame13WindowPreallocCost(t *testing.T) {
	f := p13bFrame(50000, 20, false, -1, false)
	colsSel := []int{0, 1}
	const win = 200
	n := testing.AllocsPerRun(50, func() {
		sub := tableColsSubFrameWindow(f, colsSel, 300, win)
		if sub.NumRows() != win {
			panic("窗口行数不对")
		}
	})
	// 带 cap：&Frame + []Column + names + 2 份 Cells ＝ 5 次（±2 容忍实现细节）。
	// 不带 cap：每列从 0 长到 200 要 realloc ~8 次 ⇒ ≥ 19 次。
	if n > 8 {
		t.Errorf("窗口子帧每次 %v 次分配，应 ≤ 8 ⇒ C 的 cap 预分配没了（AppendRow 在 realloc）", n)
	}
	t.Logf("PREALLOC-ALLOCS=%.0f", n)
}

// --- (c) 前缀等价：窗口子帧 == 全量子帧的行前缀（在本文件里**独立**钉一次，不只引用 ① 段探针）

func TestSubFrame13WindowIsRowPrefixOfFull(t *testing.T) {
	cases := []struct {
		rows, ncols, base, win int
		colsSel                []int
	}{
		{1000, 5, 0, 200, []int{0, 2, 4}},
		{1000, 5, 400, 200, []int{0, 2, 4}},
		{1000, 5, 37, 39, []int{4, 0, 2}},
		{1000, 5, 961, 39, []int{1, 1, 3}},
		{1000, 5, 0, 1000, []int{0, 99}}, // 列下标越界：名字/类型/格子都得对得上
		{0, 5, 0, 0, []int{0, 2}},
	}
	for _, c := range cases {
		f := p13bFrame(c.rows, c.ncols, false, -1, false)
		full := tableColsSubFrame(f, c.colsSel)
		win := tableColsSubFrameWindow(f, c.colsSel, c.base, c.win)
		if full.NumCols() != win.NumCols() || full.NumCols() != len(c.colsSel) {
			t.Fatalf("rows=%d base=%d win=%d：列数 full=%d win=%d want=%d",
				c.rows, c.base, c.win, full.NumCols(), win.NumCols(), len(c.colsSel))
		}
		for i := 0; i < win.NumCols(); i++ {
			if full.Columns[i].Name != win.Columns[i].Name {
				t.Errorf("列 %d 名字不等：full=%q win=%q", i, full.Columns[i].Name, win.Columns[i].Name)
			}
			if full.Columns[i].Type != win.Columns[i].Type {
				t.Errorf("列 %d 类型不等：full=%v win=%v", i, full.Columns[i].Type, win.Columns[i].Type)
			}
		}
		if win.NumRows() > full.NumRows() {
			t.Fatalf("窗口比全量还长：win=%d full=%d", win.NumRows(), full.NumRows())
		}
		for j := 0; j < win.NumRows(); j++ {
			// 窗口第 j 行 ＝ **全量子帧**第 base+j 行（全量子帧与父帧同行号）。
			for i := 0; i < win.NumCols(); i++ {
				got, want := win.Cell(j, i), full.Cell(c.base+j, i)
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("base=%d win=%d：窗口第 %d 行第 %d 列 got=%v want(全量子帧第 %d 行)=%v",
						c.base, c.win, j, i, got, c.base+j, want)
				}
			}
		}
	}
}

// --- (d) fail-loud：窗口外的绝对行号读必须 panic（不是返回空串）

func p13bDidPanic(fn func()) (panicked bool, msg string) {
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			msg = fmt.Sprint(rec)
		}
	}()
	fn()
	return false, ""
}

func TestSubFrame13WindowOutsidePanics(t *testing.T) {
	const rows = 50000
	f := p13bFrame(rows, 20, false, -1, false)
	colsSel := []int{0, 1}
	const base, win = 300, 200
	sub := tableColsSubFrameWindow(f, colsSel, base, win)
	if sub.NumRows() != win {
		t.Fatalf("窗口行数 %d != win %d ⇒ 下面那两条 panic 断言失去意义", sub.NumRows(), win)
	}
	for i := range sub.Columns {
		if got := sub.Columns[i].Len(); got != win {
			t.Errorf("列 %d 的 Cells 长度 %d != win %d ⇒ 绝对行号可能落在界内被静默读到", i, got, win)
		}
	}
	for _, abs := range []int{base + win, rows - 1, 400, 5000} {
		abs := abs
		ok, msg := p13bDidPanic(func() { _ = sub.Cell(abs, 0) })
		if !ok {
			t.Errorf("拿绝对父帧行号 %d 读窗口子帧没有 panic ⇒ fail-loud 这条挂了", abs)
		} else if msg == "" {
			t.Errorf("panic 但消息为空")
		}
	}
	// 反面对照：窗口内的坐标必须能读，否则上面那条 panic 是"整个子帧坏了"的假绿。
	for j := 0; j < win; j++ {
		if sub.Cell(j, 0) == nil {
			t.Fatalf("窗口内第 %d 格是 nil ⇒ panic 断言成了假绿", j)
		}
	}
	// windowRowBounds 自己的钳位：base 越界不许 panic（产品路径靠 tv.rowOff 钳住，这里钉函数合同）。
	for _, b := range []int{-1, 0, rows, rows + 1} {
		st, ln := windowRowBounds(rows, b, 200)
		if st < 0 || st+ln > rows {
			t.Errorf("windowRowBounds(rows=%d, base=%d, 200) = (%d,%d) 越出 [0,%d]", rows, b, st, ln, rows)
		}
	}
}

// --- (d2) 窗口必须同时包住绘制界与采样界，且渲染全程不许 panic

func TestSubFrame13WindowCoversReadSets(t *testing.T) {
	shapes := []struct {
		rows, h int
		sel     int
		borders bool
	}{
		{50000, 40, -1, false}, {50000, 40, 300, false}, {50000, 40, 49999, true},
		{50000, 300, 20000, false}, // 真实终端高于 200 行那一档（① 段 U-2）
		{50000, 260, 49700, true},  // body=255 > sampleRows
		{201, 40, 200, false}, {200, 40, 199, false}, {3, 2, -1, false},
	}
	for _, s := range shapes {
		f := p13bFrame(s.rows, 6, false, -1, false)
		tv := &TableView{}
		if s.sel >= 0 {
			tv.JumpTo(s.sel, f.NumRows())
		}
		body := s.h - 1
		if s.borders {
			body = s.h - 4
		}
		if body < 1 {
			body = 1
		}
		base := tv.rowOff // Render 之前是 0，钳位在 Render 内部；下面取钳后的值
		var rendered string
		panicked, _ := p13bDidPanic(func() {
			rendered = tv.Render(f, RenderOpts{Width: 120, Height: s.h, Theme: theme.Default(), ShowBorders: s.borders, ShowRowNumbers: true, TableCols: []int{0, 3}})
		})
		if panicked {
			t.Errorf("rows=%d h=%d sel=%d：渲染 panic ⇒ 窗口没盖住读集", s.rows, s.h, s.sel)
			continue
		}
		_ = base
		base = tv.rowOff // 钳后
		win := min(s.rows-base, max(body, sampleRows))
		hiMinusBase := min(s.rows, base+sampleRows) - base
		if win < body {
			t.Errorf("rows=%d h=%d：win=%d < body=%d ⇒ 绘制界掉到窗口外", s.rows, s.h, win, body)
		}
		if win < hiMinusBase {
			t.Errorf("rows=%d h=%d：win=%d < hi-base=%d ⇒ 采样窗口跑到界外", s.rows, s.h, win, hiMinusBase)
		}
		if len(rendered) == 0 {
			t.Errorf("rows=%d h=%d：渲染空串", s.rows, s.h)
		}
	}
}

// --- (e) 两条道各自的状态：非限定列那一支仍接缓存、限定列那一支仍不接（⇒ 缓存那块没被动）

func TestSubFrame13CachePathUntouched(t *testing.T) {
	f := p13bFrame(5000, 8, false, -1, false)
	tv := &TableView{}
	_ = tv.Render(f, RenderOpts{Width: 120, Height: 40, Theme: theme.Default(), ShowRowNumbers: true})
	if tv.widths.f != f {
		t.Errorf("非限定列那一支缓存的帧指针 == %p，应为父帧 %p ⇒ 那条道路径变了", tv.widths.f, f)
	}
	if tv.widths.builds < 1 {
		t.Errorf("非限定列那一支 builds=%d，应 ≥ 1 ⇒ 缓存没接上（本轮之外的旧债不许动）", tv.widths.builds)
	}
	tv2 := &TableView{}
	_ = tv2.Render(f, RenderOpts{Width: 120, Height: 40, Theme: theme.Default(), TableCols: []int{0, 3}})
	if tv2.widths.builds != 0 || tv2.widths.f != nil {
		t.Errorf("限定列那一支竟然建了缓存（builds=%d f=%p）⇒ :296-298 那一段被扰动", tv2.widths.builds, tv2.widths.f)
	}
}

// --- (e2) 与判据基准的正式版本在文件末尾（第一份草稿在这里重复声明过一次，已删；登记为 B2-1）

// --- 金样本身的形状自证：每条用例的输出必须非空（防空跑假绿）

func TestSubFrame13GoldenNonEmptyShape(t *testing.T) {
	n := 0
	for _, c := range p13bCases() {
		bs, err := os.ReadFile(p13bGoldenPath(c.id))
		if err != nil {
			t.Fatalf("金样 %s 读不到：%v（先跑 Write，且必须在改码之前）", c.id, err)
		}
		if len(bs) < 20 {
			t.Errorf("金样 %s 只有 %d 字节 ⇒ 可疑（空串比对无意义）", c.id, len(bs))
		}
		if os.Getenv(p13bWriteEnv) == "1" {
			continue
		}
		n++
	}
	t.Logf("P13B-GOLDEN-ASSERTED: %d 份金样非空", n)
	if n != len(p13bCases()) {
		t.Errorf("P13B-GOLDEN-ASSERTED: %d != %d", n, len(p13bCases()))
	}
}

// --- (e2) 走真实渲染路的分配预算：窗口必须真的盖住
//
// 这条是电池第②臂（`win` 退回全部行）的**唯一**杀手——那条臂的输出、行数合同全对
// （合同测的是构造函数，不是渲染路），只有分配账变。
// pristine（① 段日志 subframe13_price_mem_c6_P1.txt）的整帧 view 是 8,793,910 B／1,514 次，
// 其中子帧那份 8,688,474 B／52 次。D 之后整条路应当落在 1 MB 以下。
// 阈值 1,000,000 B：比 pristine 低 8.8 倍，比实测余量高一个数量级 ⇒ 不是为过判据调的数。
func TestSubFrame13RenderAllocBudget(t *testing.T) {
	f := p13bFrame(50000, 20, false, -1, false)
	tv := &TableView{}
	o := RenderOpts{Width: 120, Height: 40, Theme: theme.Default(), TableCols: []int{0, 1}}
	_ = tv.Render(f, o) // 预热：首帧要定 mode，那份一次性分配不算在本帧账上
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	out := tv.Render(f, o)
	runtime.ReadMemStats(&m1)
	delta := int64(m1.TotalAlloc) - int64(m0.TotalAlloc)
	if len(out) < 200 {
		t.Fatalf("渲染输出只有 %d 字节 ⇒ 量尺空转，预算断言失去意义", len(out))
	}
	const budget = 1_000_000
	if delta > budget {
		t.Errorf("一次限定列渲染分配 %d B，预算 %d B ⇒ 窗口没盖住渲染路（还在拷整帧？）", delta, budget)
	}
	t.Logf("P13B-RENDER-ALLOC-DELTA=%d bytes（pristine=8,793,910）", delta)
}

// --- 判据基准：同进程相邻臂才算倍数
//
// pfFull13 ＝ pristine 的那条道（每帧把 50,000×2 格全拷一遍），期望 8,688,473 B／52 allocs
// （＝① 段 prod 臂同形状）；pfWin13 ＝ D 的那条道（只拷窗口 200 行），期望 ~7,032 B／4 allocs。
// winNoCap / winNoClamp 是**构造层**的反事实（测试内的镜像写法，产品码里没有对应路径）：
// 前者去掉 C 的两行，后者不走 windowRowBounds ⇒ 用它证明"钳位是免费的"。
// 渲染层的反事实（②③④⑤⑦）不在这里定价——它们靠电池那五臂被测试杀掉，价格由①段 §4 的表推。
func BenchmarkSubFrame13Price(b *testing.B) {
	f := p13bFrame(50000, 20, false, -1, false)
	colsSel := []int{0, 1}
	const base, win = 300, 200
	for _, arm := range []string{"pfFull13", "pfWin13", "winNoCap", "winNoClamp"} {
		arm := arm
		b.Run(arm, func(b *testing.B) {
			b.ReportAllocs()
			var sink *tuidata.Frame
			for i := 0; i < b.N; i++ {
				switch arm {
				case "pfFull13":
					sink = tableColsSubFrame(f, colsSel)
				case "pfWin13":
					sink = tableColsSubFrameWindow(f, colsSel, base, win)
				case "winNoCap":
					sink = p13bSubFrameNoCap(f, colsSel, base, win)
				case "winNoClamp":
					sink = p13bSubFrameNoClamp(f, colsSel, base, win)
				}
			}
			if sink == nil {
				b.Fatalf("臂 %s 没产出", arm)
			}
			switch arm {
			case "pfFull13":
				if sink.NumRows() != 50000 || sink.NumCols() != 2 {
					b.Fatalf("pfFull13 形状 %d×%d，应为 50000×2", sink.NumRows(), sink.NumCols())
				}
			default:
				if sink.NumRows() != win || sink.NumCols() != 2 {
					b.Fatalf("臂 %s 形状 %d×%d，应为 %d×2", arm, sink.NumRows(), sink.NumCols(), win)
				}
				if got, want := sink.CellString(win-1, 1), f.CellString(base+win-1, colsSel[1]); got != want {
					b.Fatalf("臂 %s 尾行错值 got=%q want=%q", arm, got, want)
				}
			}
		})
	}
}

// p13bSubFrameNoCap ＝ D 减去 C 的那两行（不预分配 cap，AppendRow 一路 realloc）。
func p13bSubFrameNoCap(f *tuidata.Frame, cols []int, base, limit int) *tuidata.Frame {
	out := p13bSubFrameShell(f, cols, 0)
	start, length := windowRowBounds(f.NumRows(), base, limit)
	for r := start; r < start+length; r++ {
		out.AppendRow(p13bSubFrameRow(f, cols, r))
	}
	return out
}

// p13bSubFrameNoClamp ＝ 不经 windowRowBounds 的同一件事（证明"钳位是免费的"）。
func p13bSubFrameNoClamp(f *tuidata.Frame, cols []int, base, limit int) *tuidata.Frame {
	out := p13bSubFrameShell(f, cols, limit)
	for r := base; r < base+limit; r++ {
		out.AppendRow(p13bSubFrameRow(f, cols, r))
	}
	return out
}

func p13bSubFrameShell(f *tuidata.Frame, cols []int, capHint int) *tuidata.Frame {
	names := make([]string, len(cols))
	for i, c := range cols {
		if c < f.NumCols() {
			names[i] = f.Columns[c].Name
		}
	}
	out := tuidata.New(names...)
	for i, c := range cols {
		if c < f.NumCols() {
			out.Columns[i].Type = f.Columns[c].Type
		}
	}
	if capHint > 0 {
		out.PreallocRows(capHint)
	}
	return out
}

func p13bSubFrameRow(f *tuidata.Frame, cols []int, r int) []any {
	row := make([]any, len(cols))
	for i, c := range cols {
		if c < f.NumCols() {
			row[i] = f.Cell(r, c)
		}
	}
	return row
}
