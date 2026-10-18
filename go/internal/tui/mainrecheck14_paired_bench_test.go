package tui

import (
	"strings"
	"testing"
)

// mainrecheck14_paired_bench_test.go —— 主会话补测件（第十轮判据复核），不是 ②a 的交付物。
//
// 为什么要这件：⑤-3 的"两进程同侧"是拿**跨进程**的改前/改后绝对中位比降幅，而本机同码
// 跨进程的摆幅已经大过这道门——① 段在零产品改动下测到 view 2,274,608.5｜2,145,582 ns，
// ②a 的"改前"是同一份 pristine 码、测到 1,773,470｜1,568,250 ns ⇒ 同码摆 ±40%，门是 12%。
// 这把尺把"命中"与"每帧强制失效（回落老路）"放进**同一进程、相邻臂**：热漂移对两臂同等作用，
// miss/hit 的比值才是机制自己的价格。**阈值一字不动**（仍是 ≥12%），换的只是仪器的分辨率。
//
// 口径：与 BenchmarkTC14CachePrice/render 同一套生产形状——p.Table.Render(父帧, E 档 opts)，
// compact 一支，稳态每帧一次 naturalWidthsFor。两臂各带计数器性质针（不是只计时）。
var paired14Keep byte

func BenchmarkR14Paired(b *testing.B) {
	f := benchFrame(p14Rows, p14ParentCols, p14Kind)
	cols := []int{0, 1}
	a := benchApp(f, p14WinW, p14WinH, cols)
	p := a.pane()
	if p == nil {
		b.Fatal("pane 必须存在")
	}
	if p.Table.Expanded() {
		b.Fatal("E 档应走 compact 一支")
	}
	o := RenderOpts{
		Width:          p14WinW,
		Height:         bodyHFor(p14WinH, false),
		Theme:          a.th,
		ShowBorders:    a.showBorders,
		ShowRowNumbers: a.showRowNumbers,
		HeaderCur:      p.Table.ColCursor(),
		TableCols:      p.TableCols,
		NoEllipsis:     len(p.TableCols) > 0,
		HScroll:        p.HScroll,
		HighlightQuery: a.searchQuery(),
		HighlightCols:  p.TableCols,
	}
	// 预算针：E 档稳态一遍量宽＝400 次格读，两臂都靠它换算 builds/computed。
	cellBudget := sampleRows * len(cols)
	if cellBudget != 400 {
		b.Fatalf("格读预算 %d ≠ 400", cellBudget)
	}

	for _, arm := range []string{"miss", "hit"} {
		arm := arm
		b.Run(arm, func(b *testing.B) {
			tv := &p.Table
			tv.tcWidths = tableColsWidthCache{}
			s0 := tv.Render(f, o) // 暖：modeSet 落下、首帧表建好
			if n := strings.Count(s0, "\n") + 1; n != o.Height {
				b.Fatalf("暖帧行数 %d ≠ %d", n, o.Height)
			}
			buildsWarm, hitsWarm, compWarm := tv.tcWidths.builds, tv.tcWidths.hits, tv.tcWidths.computed
			if buildsWarm != 1 {
				b.Fatalf("暖帧 builds=%d，期望 1", buildsWarm)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if arm == "miss" {
					tv.tcWidths.valid = false // 整遍重算＝改前那条路（buf 仍常驻 ⇒ 不额外多分配）
				}
				s := tv.Render(f, o)
				if n := strings.Count(s, "\n") + 1; n != o.Height {
					b.Fatalf("%s 臂第 %d 帧行数 %d ≠ %d", arm, i, n, o.Height)
				}
				paired14Keep = s[0]
			}
			b.StopTimer()
			switch arm {
			case "hit":
				if tv.tcWidths.builds != buildsWarm {
					b.Fatalf("hit 臂 builds %d→%d ⇒ 稳态根本没命中，读数无意义", buildsWarm, tv.tcWidths.builds)
				}
				if tv.tcWidths.computed != compWarm {
					b.Fatalf("hit 臂 computed %d→%d ⇒ 命中仍在量宽", compWarm, tv.tcWidths.computed)
				}
				if d := tv.tcWidths.hits - hitsWarm; d < b.N {
					b.Fatalf("hit 臂 hits 增量=%d < b.N=%d", d, b.N)
				}
			case "miss":
				if d := tv.tcWidths.builds - buildsWarm; d != b.N {
					b.Fatalf("miss 臂 builds 增量=%d ≠ b.N=%d ⇒ 强制失效没生效", d, b.N)
				}
				if d := tv.tcWidths.computed - compWarm; d != b.N*cellBudget {
					b.Fatalf("miss 臂 computed 增量=%d ≠ %d", d, b.N*cellBudget)
				}
			}
			b.ReportMetric(float64(tv.tcWidths.builds-buildsWarm), "dbuilds")
			b.ReportMetric(float64(tv.tcWidths.computed-compWarm), "dcomputed")
		})
	}
}

// TestR14PairedPremises 独立钉住这件仪器的前提（不依赖 bench 跑过）：
// 稳态每帧一次量宽调用、命中不回表以外之路、且两臂渲染字节必须**逐字节相同**。
func TestR14PairedPremises(t *testing.T) {
	f := benchFrame(p14Rows, p14ParentCols, p14Kind)
	cols := []int{0, 1}
	a := benchApp(f, p14WinW, p14WinH, cols)
	p := a.pane()
	tv := &p.Table
	o := RenderOpts{
		Width:          p14WinW,
		Height:         bodyHFor(p14WinH, false),
		Theme:          a.th,
		ShowBorders:    a.showBorders,
		ShowRowNumbers: a.showRowNumbers,
		HeaderCur:      tv.ColCursor(),
		TableCols:      p.TableCols,
		NoEllipsis:     len(p.TableCols) > 0,
		HScroll:        p.HScroll,
		HighlightQuery: a.searchQuery(),
		HighlightCols:  p.TableCols,
	}
	tv.tcWidths = tableColsWidthCache{}
	_ = tv.Render(f, o)
	if tv.tcWidths.builds != 1 {
		t.Fatalf("暖帧 builds=%d ≠ 1", tv.tcWidths.builds)
	}
	var outHit []string
	for i := 0; i < 3; i++ {
		outHit = append(outHit, tv.Render(f, o))
	}
	hitBuilds, hitHits, hitComp := tv.tcWidths.builds, tv.tcWidths.hits, tv.tcWidths.computed
	if hitBuilds != 1 || hitComp != 400 {
		t.Fatalf("连渲三帧后 builds=%d computed=%d，期望 1/400", hitBuilds, hitComp)
	}
	tv.tcWidths = tableColsWidthCache{}
	_ = tv.Render(f, o)
	var outMiss []string
	for i := 0; i < 3; i++ {
		tv.tcWidths.valid = false
		outMiss = append(outMiss, tv.Render(f, o))
	}
	missBuilds, missComp := tv.tcWidths.builds, tv.tcWidths.computed
	if missBuilds != 4 || missComp != 1600 {
		t.Fatalf("失配三帧后 builds=%d computed=%d，期望 4/1600", missBuilds, missComp)
	}
	for i := range outHit {
		if outHit[i] != outMiss[i] {
			t.Fatalf("第 %d 帧命中形与失配形不等（%d B vs %d B）⇒ 两臂量的不是同一件事",
				i, len(outHit[i]), len(outMiss[i]))
		}
	}
	t.Logf("R14-PAIRED: hit builds=%d hits=%d computed=%d / miss builds=%d computed=%d（渲染逐帧相同）",
		hitBuilds, hitHits, hitComp, missBuilds, missComp)
}
