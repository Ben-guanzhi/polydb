package appcore

// 数据字典导出的端到端（§373）：SQLite 真库 → dictionarySource 适配 →
// 内核 ExportDictionary 的 Markdown 产物——表/列/PK/FK 齐备。

import (
	"context"
	"strings"
	"testing"
)

func TestExportDictionarySQLite(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	// 建两张表 + 一条外键（SQLite 的 REFERENCES 写法）。
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id), total REAL)`,
	} {
		if _, err := a.Execute(ctx, id, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	md, err := a.ExportDictionary(ctx, id, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"users", "orders", "name", "user_id", "FK→users", "PK"} {
		if !strings.Contains(md, want) {
			t.Errorf("dictionary missing %q in:\n%.400s", want, md)
		}
	}
	if !strings.HasPrefix(md, "# ") {
		t.Errorf("dictionary should start with a title, got %.80s", md)
	}
}
