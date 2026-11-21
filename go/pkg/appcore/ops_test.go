package appcore

// 运维四件套的端到端（§376）：SQLite 无进程/变量/用户体系——诚实报错
// 路径钉住；造数在 SQLite 上真跑（内核 GenerateInserts + Execute 闸门）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func TestOpsSQLiteHonestErrors(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	for name, fn := range map[string]func() error{
		"会话": func() error { _, err := a.ListServerProcesses(ctx, id); return err },
		"变量": func() error { _, err := a.ListServerVariables(ctx, id); return err },
		"用户": func() error { _, err := a.ListServerUsers(ctx, id); return err },
		"用户DDL": func() error {
			_, err := a.GenerateUserDDL(ctx, id, protocol.UserOpsRequest{Action: "create", User: "u"})
			return err
		},
	} {
		err := fn()
		if err == nil || !strings.Contains(err.Error(), "不支持") {
			t.Errorf("%s: want 诚实报错（不支持…），got %v", name, err)
		}
	}
}

func TestOpsGenDataSQLite(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()

	if _, err := a.Execute(ctx, id, `CREATE TABLE things (id INTEGER PRIMARY KEY, label TEXT, score REAL)`); err != nil {
		t.Fatal(err)
	}
	n, err := a.GenerateTestData(ctx, id, protocol.GenDataRequest{Table: "things", Rows: 5, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("executed = %d, want 5", n)
	}
	// 行真的进了表。
	res, err := a.Execute(ctx, id, "SELECT COUNT(*) FROM things")
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := json.Unmarshal(res.Rows[0][0].Raw(), &count); err != nil {
		t.Fatalf("parse count: %v", err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
}
