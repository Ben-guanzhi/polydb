package popup

// compare 向导的远端证明。此前该向导只有 local 测试（compare_test.go 用
// transport.NewLocal），而它的数据面实际跑在 HTTP + msgpack + Bearer 上 ——
// 这条路径一旦回归，local 测试是发现不了的。
//
// 搭法与 backend/adapter_remote_test.go 同源：httptest 起真服务端 + 真 token，
// 客户端走 transport.NewRemote，端到端把向导推到 diff 阶段。

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

func TestCompareWizardOverRemote(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "compare-remote")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	srv := httptest.NewServer(server.NewWithToken(appcore.New(db, kr), "secret-token").Handler())
	t.Cleanup(srv.Close)

	cli := transport.NewRemote(srv.URL)
	cli.SetToken("secret-token")

	ctx := context.Background()
	src, err := cli.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "src", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	tgt, err := cli.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "tgt", Kind: protocol.DatabaseKindSQLite, Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create tgt: %v", err)
	}

	// 源只有 id/name，目标多一列 age —— 这样 diff 必然有内容可出。
	seed := func(id string, extra bool) {
		tx, err := cli.BeginTransaction(ctx, id)
		if err != nil {
			t.Fatalf("begin %s: %v", id, err)
		}
		if _, err := cli.ExecuteInTx(ctx, tx.ID, "CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
			t.Fatalf("create table %s: %v", id, err)
		}
		if extra {
			if _, err := cli.ExecuteInTx(ctx, tx.ID, "ALTER TABLE people ADD COLUMN age INTEGER"); err != nil {
				t.Fatalf("alter %s: %v", id, err)
			}
		}
		if _, err := cli.CommitTransaction(ctx, tx.ID); err != nil {
			t.Fatalf("commit %s: %v", id, err)
		}
	}
	seed(src.ID, false)
	seed(tgt.ID, true)

	o := &cmpOverlay{ctx: &cmpTestCtx{client: cli}, stage: cmpStageLoading}
	ov, _ := o.Update(o.Init()())
	o = ov.(*cmpOverlay)
	if o.stage != cmpStageSrcConn {
		t.Fatalf("expected srcConn stage, got %d (err=%v)", o.stage, o.err)
	}
	if len(o.conns) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(o.conns))
	}

	// 选源 → 默认 schema → 选目标 → 默认 schema → 出 diff。
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageSrcSchema {
		t.Fatalf("expected srcSchema stage, got %d (err=%v)", o.stage, o.err)
	}
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageTgtConn {
		t.Fatalf("expected tgtConn stage, got %d (err=%v)", o.stage, o.err)
	}
	o = o.press(tea.KeyDown, "")
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageTgtSchema {
		t.Fatalf("expected tgtSchema stage, got %d (err=%v)", o.stage, o.err)
	}
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageDiff {
		t.Fatalf("expected diff stage, got %d (err=%v)", o.stage, o.err)
	}
	if o.err != nil {
		t.Fatalf("diff over remote failed: %v", o.err)
	}
}
