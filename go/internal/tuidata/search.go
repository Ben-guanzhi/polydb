package tuidata

import (
	"strings"
	"unicode"
)

// SearchExact returns the indices of rows where at least one cell's display
// string contains needle (case-sensitive). An empty needle matches every row.
func SearchExact(f *Frame, needle string) []int {
	rows := f.NumRows()
	out := make([]int, 0, rows)
	for r := 0; r < rows; r++ {
		for c := range f.Columns {
			if strings.Contains(f.CellString(r, c), needle) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// SearchExactCols returns the indices of rows where at least one of the given
// column cells contains needle (case-insensitive). Use this when you want to
// restrict search to a known subset of columns (e.g. only the visible ones).
func SearchExactCols(f *Frame, cols []int, needle string) []int {
	return SearchExactColsFold(f, cols, needle, nil)
}

// FoldCache 复用"每格折叠成小写"的结果，键＝帧指针 + 行数 + 列数 + 被搜索的列集合。
//
// 搜索框每敲一个可打印键都会把同一帧重扫一遍，而折叠只与帧有关、与 needle 无关：
// 第一键付全价，之后的键跳过 Θ(行×列) 的 ToLower 遍历。中文格尤其贵——ToLower 对
// 非 ASCII 要逐 rune 查 unicode 表（实测约 22 ns/rune；只有折叠出变化时才分配）。
//
// 键里没有每格内容：帧的不可变由类型系统强制（Column.cells 非导出，见 frame.go 顶部
// 注释），包外改不了格值；改一格走 WithCell 换新帧，换页/换结果也都是新帧；append 会
// 改行数（AppendRow 是构建期工具，发布后的帧不再追加）。
type FoldCache struct {
	f      *Frame
	rows   int
	ncols  int
	cols   []int
	vals   [][]string
	builds int // 建了几次；测试用它钉"必须命中"这条性质
}

// foldCacheMaxCells 是缓存的规模上限（格数）。reader 侧没有行数上限，
// 超限的大帧按老路走：不缓存也不会比改前慢。
const foldCacheMaxCells = 200_000

// lookup 键相符时返回缓存的折叠结果；键变了就把旧料放掉（避免大帧滞留）。
func (c *FoldCache) lookup(f *Frame, cols []int) [][]string {
	if c == nil || c.vals == nil {
		return nil
	}
	if c.f != f || c.rows != f.NumRows() || c.ncols != len(f.Columns) || !sameCols(c.cols, cols) {
		*c = FoldCache{builds: c.builds}
		return nil
	}
	return c.vals
}

// build 扫一遍被搜索的列，存下每格的折叠结果。
func (c *FoldCache) build(f *Frame, cols []int) [][]string {
	if c == nil || f == nil {
		return nil
	}
	rows := f.NumRows()
	vals := make([][]string, len(cols))
	for i, col := range cols {
		if col >= len(f.Columns) {
			continue
		}
		v := make([]string, rows)
		for r := 0; r < rows; r++ {
			v[r] = strings.ToLower(f.CellString(r, col))
		}
		vals[i] = v
	}
	*c = FoldCache{f: f, rows: rows, ncols: len(f.Columns), cols: append([]int(nil), cols...), vals: vals, builds: c.builds + 1}
	return vals
}

func sameCols(a, b []int) bool {
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

// SearchExactColsFold 是 SearchExactCols 的带缓存版本：cache 为 nil 或帧过大时
// 逐格现算（＝改前行为）。
func SearchExactColsFold(f *Frame, cols []int, needle string, cache *FoldCache) []int {
	lower := strings.ToLower(needle)
	rows := f.NumRows()
	vals := cache.lookup(f, cols)
	if vals == nil && rows*len(cols) <= foldCacheMaxCells {
		vals = cache.build(f, cols)
	}
	out := make([]int, 0, rows)
	for r := 0; r < rows; r++ {
		for i, c := range cols {
			if c >= len(f.Columns) {
				continue
			}
			var cell string
			if vals != nil {
				cell = vals[i][r]
			} else {
				cell = strings.ToLower(f.CellString(r, c))
			}
			if strings.Contains(cell, lower) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// SearchFuzzy returns the indices of rows whose joined display string fuzzily
// matches needle, in frame order. An empty needle matches every row. The cache
// is nil here: single-shot callers only. Repeated calls over the same frame
// (one per keystroke in a search session) must use SearchFuzzyFold, which
// avoids rebuilding the haystack and skips the scoring that was never used.
func SearchFuzzy(f *Frame, needle string) []int {
	return SearchFuzzyFold(f, needle, nil)
}

// CellCache caches the per-cell display string WITHOUT folding, for the
// case-sensitive exact-search session: the plain search path recomputes
// every cell's CellString on every keystroke (the per-cell strconv/format is
// the residue the fold caches kill on their paths, but only ToLower-ed
// values - the plain path must keep case-sensitive matching, so it gets its
// own cache of the raw display). Keying and the size cap follow FoldCache
// exactly; over the cap the caller computes on the fly, which is what
// SearchExact does today and cannot be slower.
type CellCache struct {
	f      *Frame
	rows   int
	ncols  int
	vals   [][]string
	builds int // 建了几次；测试用它钉"必须命中"这条性质
}

// lookup 键相符时返回缓存的显示串；键变了就把旧料放掉（避免大帧滞留）。
func (c *CellCache) lookup(f *Frame) [][]string {
	if c == nil || c.vals == nil {
		return nil
	}
	if c.f != f || c.rows != f.NumRows() || c.ncols != len(f.Columns) {
		*c = CellCache{builds: c.builds}
		return nil
	}
	return c.vals
}

// build 扫一遍全部列，存下每格的显示串（不折叠，大小写语义归调用方）。
func (c *CellCache) build(f *Frame) [][]string {
	if c == nil || f == nil {
		return nil
	}
	rows := f.NumRows()
	vals := make([][]string, len(f.Columns))
	for ci := range f.Columns {
		v := make([]string, rows)
		for r := 0; r < rows; r++ {
			v[r] = f.CellString(r, ci)
		}
		vals[ci] = v
	}
	*c = CellCache{f: f, rows: rows, ncols: len(f.Columns), vals: vals, builds: c.builds + 1}
	return vals
}

// SearchExactCached is the cached version of SearchExact with the exact
// same case-sensitive semantics: cache nil or the frame too large computes
// every cell on the fly (＝ SearchExact). The frame must be the immutable
// one the session started on - WithCell / repage / new result all hand out
// new frames, which invalidate the cache by pointer.
func SearchExactCached(f *Frame, needle string, cache *CellCache) []int {
	rows := f.NumRows()
	if needle == "" {
		out := make([]int, 0, rows)
		for r := 0; r < rows; r++ {
			out = append(out, r)
		}
		return out
	}
	vals := cache.lookup(f)
	if vals == nil && rows*len(f.Columns) <= foldCacheMaxCells {
		vals = cache.build(f)
	}
	out := make([]int, 0, rows)
	for r := 0; r < rows; r++ {
		for ci := range f.Columns {
			cell := f.CellString(r, ci)
			if vals != nil {
				cell = vals[ci][r]
			}
			if strings.Contains(cell, needle) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// FuzzyCache 复用模糊匹配按行拼出的整行显示串，键＝帧指针＋行数＋列数。
//
// fuzzy 恒定拼全部列，所以键里不带列集合；也不带每格内容——帧的不可变由类型
// 系统强制（同 FoldCache：Column.cells 非导出，改一格走 WithCell 换新帧，
// 换页/换结果也都是新帧）。
//
// 第一键付全价，之后的键跳过 Θ(行×列) 的 CellString＋Join。中文格尤其贵——
// CellString 的 strconv 与 Join 的分配都要付，量尺见 search_fuzzy_bench_test.go。
type FuzzyCache struct {
	f      *Frame
	rows   int
	ncols  int
	hay    []string
	builds int // 建了几次；测试用它钉"必须命中"这条性质
}

// lookup 键相符时返回缓存的 haystack；键变了就把旧料放掉（避免大帧滞留）。
func (c *FuzzyCache) lookup(f *Frame) []string {
	if c == nil || c.hay == nil {
		return nil
	}
	if c.f != f || c.rows != f.NumRows() || c.ncols != len(f.Columns) {
		*c = FuzzyCache{builds: c.builds}
		return nil
	}
	return c.hay
}

// build 拼一遍全部行的整行显示串并入库。调用方已确认不超 foldCacheMaxCells。
func (c *FuzzyCache) build(f *Frame) []string {
	hay := joinFrameRows(f)
	*c = FuzzyCache{f: f, rows: f.NumRows(), ncols: len(f.Columns), hay: hay, builds: c.builds + 1}
	return hay
}

// joinFrameRows 拼出每行的整行显示串，不入库。超限的帧走这条（＝改前行为）。
func joinFrameRows(f *Frame) []string {
	rows := f.NumRows()
	hay := make([]string, rows)
	parts := make([]string, len(f.Columns))
	for r := 0; r < rows; r++ {
		for c := range f.Columns {
			parts[c] = f.CellString(r, c)
		}
		hay[r] = strings.Join(parts, " ")
	}
	return hay
}

// SearchFuzzyFold 是 SearchFuzzy 的带缓存版本：cache 为 nil 或帧过大时不缓存
// （haystack 仍照拼，＝改前行为），判定本身逐次重算。
func SearchFuzzyFold(f *Frame, needle string, cache *FuzzyCache) []int {
	rows := f.NumRows()
	if needle == "" {
		all := make([]int, rows)
		for i := range all {
			all[i] = i
		}
		return all
	}
	hay := cache.lookup(f)
	if hay == nil {
		if cache != nil && rows*len(f.Columns) <= foldCacheMaxCells {
			hay = cache.build(f)
		} else {
			hay = joinFrameRows(f)
		}
	}
	return fuzzyMatchIndices(needle, hay)
}

// fuzzyMatchIndices 按行判定"中/不中"，按帧序返回索引。判据与 fuzzy.Find 逐位
// 等价（同一行的中/不中一致），但不打分、不排序——SearchFuzzy 只用索引，打分
// 与 fuzzy.Find 的末尾排序全程是白付的。等价性由 TestSearchFuzzyFoldMatches
// 差分锁定，库升级改判据时那条测试会先炸。
func fuzzyMatchIndices(needle string, haystack []string) []int {
	pat := []rune(needle)
	if len(pat) == 0 {
		return nil
	}
	// fuzzy 的 equalFold 等价于"落在同一条 unicode.SimpleFold 轨道上"，这里
	// 先把每个模式 rune 的整条轨道展开成集合，匹配时只做整数比较。注意
	// SimpleFold('k') 是 U+212A 而非 'K'——轨道是三元环，不能用 ToLower 近似
	//（ToLower('İ') 是 'i'，会误命中 "elvin" 里的 i）。
	pi := make([]map[rune]struct{}, len(pat))
	for i, p := range pat {
		m := map[rune]struct{}{p: {}}
		cur := unicode.SimpleFold(p)
		for n := 0; n < 64 && cur != p; n++ {
			m[cur] = struct{}{}
			cur = unicode.SimpleFold(cur)
		}
		pi[i] = m
	}
	out := make([]int, 0)
	for i, s := range haystack {
		// fuzzy 也把 haystack 截到首个 NUL：多付一次 IndexByte 换来整行不拆。
		if j := strings.IndexByte(s, 0); j >= 0 {
			s = s[:j]
		}
		pidx := 0
		for _, ch := range s {
			if _, ok := pi[pidx][ch]; ok {
				pidx++
				if pidx == len(pat) {
					out = append(out, i)
					break
				}
			}
		}
	}
	return out
}
