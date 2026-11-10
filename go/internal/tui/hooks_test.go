package tui

import (
	"errors"
	"testing"
)

// TestFactoryRegistryLifecycle 是 PD-09 的回归测试：工厂注册表此前是裸 map，
// 无撤回路径。这里钉住「派发在途时撤回被守卫拒绝、派发结束后可撤回」这条
// `¬ relied_n(γ)` 性质，防止后续改动把它悄悄退化回 last-wins。
func TestFactoryRegistryLifecycle(t *testing.T) {
	const name = "pd09_probe"

	// 干净起点：确保上一次运行没有残留。
	_ = UnregisterFactory(name)
	delete(Factories, name)

	Factories[name] = func(AppContext, string) (Overlay, error) { return nil, nil }

	// 无派发在途时可以撤回。
	if err := UnregisterFactory(name); err != nil {
		t.Fatalf("unregister with no dispatch in flight: %v", err)
	}
	if _, ok := Factories[name]; ok {
		t.Fatal("factory still present after unregister")
	}

	// 派发在途时撤回必须被拒。
	Factories[name] = func(AppContext, string) (Overlay, error) { return nil, nil }
	AcquireFactory(name)
	if !FactoryInUse(name) {
		t.Fatal("FactoryInUse = false while dispatch in flight")
	}
	if got := FactoryReliance(name); got != 1 {
		t.Fatalf("FactoryReliance = %d, want 1", got)
	}
	err := UnregisterFactory(name)
	if !errors.Is(err, ErrFactoryInUse) {
		t.Fatalf("UnregisterFactory during dispatch = %v, want ErrFactoryInUse", err)
	}
	if _, ok := Factories[name]; !ok {
		t.Fatal("factory removed despite in-flight dispatch")
	}
	ReleaseFactory(name)

	// 计数归零后条目被清理，撤回成功。
	if FactoryInUse(name) {
		t.Fatal("FactoryInUse = true after release")
	}
	if err := UnregisterFactory(name); err != nil {
		t.Fatalf("unregister after release: %v", err)
	}
}
