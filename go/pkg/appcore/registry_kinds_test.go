package appcore

import (
	"sort"
	"testing"

	kernel "github.com/dbcore/dbcore"
	pdb "github.com/polydb/polydb/pkg/dbcore"
	// 六个 polydb 驱动包各自 init() 登记 polydb 注册表（RegisterDriver），
	// 同时它们 import 对应的内核引擎包（RegisterFactory），因此这里同时把两端
	// 的 kind 集合都拉起来。PD-01/PD-02：appcore 只按 kind 查 polydb 注册表，
	// 所以两端集合必须一致，否则「内核登记了、协议层没登记」的引擎在
	// Connect 时会落到 NOT_SUPPORTED。
	//
	// 为什么放在 appcore 而不是 pkg/dbcore：pkg/dbcore 是 polydb 的再导出层，
	// 引内核会触发 dbcore 消费契约断言 dbcoreImportDirs 的集合漂移（见
	// WORKSPACE.md §102.3/§102.6：对照测试放"唯一能同时看见两边的包"，比
	// 放宽契约断言正确得多）。appcore 是消费方，且已 import 内核与六个驱动，
	// 不新增任何 import 根。
	_ "github.com/polydb/polydb/pkg/dbmssql"
	_ "github.com/polydb/polydb/pkg/dbmysql"
	_ "github.com/polydb/polydb/pkg/dboracle"
	_ "github.com/polydb/polydb/pkg/dbpostgres"
	_ "github.com/polydb/polydb/pkg/dbredis"
	_ "github.com/polydb/polydb/pkg/dbsqlite"
	"github.com/polydb/polydb/pkg/protocol"
)

// TestRegistryKindsMatchAcrossEnds 是 PD-15 的注册表版回归：把 polydb 协议层
// 注册表（pkg/dbcore.RegisterDriver）的 kind 集合与内核注册表
// （dbcore.RegisterFactory）的 kind 集合钉在一起。新增引擎若只在内核登记而
// 忘了在 polydb 驱动包 init 里 RegisterDriver（或反之），这里即失败。
func TestRegistryKindsMatchAcrossEnds(t *testing.T) {
	want := kernelKinds(t)
	got := polydbKinds(t)
	if len(want) == 0 {
		t.Fatal("kernel dbcore.RegisteredKinds() is empty: kernel engine packages were not imported")
	}
	if !sort.StringsAreSorted(want) {
		t.Fatalf("kernel kinds not sorted: %v", want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("polydb kinds not sorted: %v", got)
	}
	if len(want) != len(got) {
		t.Fatalf("kind count mismatch: kernel=%v polydb=%v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("kind set mismatch at index %d: kernel=%v polydb=%v", i, want, got)
		}
	}
}

func kernelKinds(t *testing.T) []string {
	t.Helper()
	ks := kernel.RegisteredKinds()
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

func polydbKinds(t *testing.T) []string {
	t.Helper()
	ks := pdb.RegisteredDriverKinds()
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// TestRegisteredDriverKindsCoverSpec 把注册表集合再钉一次 spec/schemas/common.json
// 的 DatabaseKind 枚举，避免注册了协议层没有的 kind（或反过来）。
func TestRegisteredDriverKindsCoverSpec(t *testing.T) {
	spec := []string{"sqlite", "mysql", "postgres", "mssql", "oracle", "redis"}
	got := pdb.RegisteredDriverKinds()
	if len(got) != len(spec) {
		t.Fatalf("polydb registered kinds = %v, want %v", got, spec)
	}
	byValue := map[protocol.DatabaseKind]bool{}
	for _, k := range got {
		byValue[k] = true
	}
	for _, s := range spec {
		if !byValue[protocol.DatabaseKind(s)] {
			t.Fatalf("spec kind %q missing from polydb registry %v", s, got)
		}
	}
}
