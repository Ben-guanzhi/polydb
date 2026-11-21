package storage

// 片段收藏 CRUD 的端到端（§387）：内存 meta 库 → Save → List → Delete。

import (
	"context"
	"testing"
)

func TestSnippetCRUD(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewSnippetRepository(db)
	ctx := context.Background()

	if err := r.SaveSnippet(ctx, Snippet{ID: "s1", Name: "活跃用户", SQL: "SELECT * FROM users WHERE active=1", Group: "常用"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveSnippet(ctx, Snippet{ID: "s2", Name: "订单", SQL: "SELECT * FROM orders", Group: "报表"}); err != nil {
		t.Fatal(err)
	}
	all, err := r.ListSnippets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("list = %d, want 2", len(all))
	}
	// 排序：group 字典序（常用 < 报表 按 UTF-8 字节序未必——只断言总数与内容）。
	found := false
	for _, s := range all {
		if s.ID == "s1" && s.SQL == "SELECT * FROM users WHERE active=1" && s.Group == "常用" {
			found = true
		}
	}
	if !found {
		t.Fatalf("s1 not found: %+v", all)
	}

	// 更新（同 id）。
	if err := r.SaveSnippet(ctx, Snippet{ID: "s1", Name: "活跃用户V2", SQL: "SELECT 1", Group: "常用"}); err != nil {
		t.Fatal(err)
	}
	all, _ = r.ListSnippets(ctx)
	if len(all) != 2 {
		t.Fatalf("upsert created duplicate: %d", len(all))
	}

	if err := r.DeleteSnippet(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	all, _ = r.ListSnippets(ctx)
	if len(all) != 1 || all[0].ID != "s2" {
		t.Fatalf("after delete = %+v", all)
	}
}
