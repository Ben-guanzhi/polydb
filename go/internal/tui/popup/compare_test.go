package popup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// cmpTestCtx is a minimal AppContext that only wires a real transport client;
// compare never touches the backend, so the rest are zero values.
type cmpTestCtx struct{ client transport.Client }

func (c *cmpTestCtx) CurrentFrame() *tuidata.Frame      { return nil }
func (c *cmpTestCtx) CurrentRow() int                   { return 0 }
func (c *cmpTestCtx) SheetFieldCursor() int             { return 0 }
func (c *cmpTestCtx) SheetFilter() string             { return "" }
func (c *cmpTestCtx) CurrentTableNamespace() string     { return "" }
func (c *cmpTestCtx) BaseCrumb() string                 { return "" }
func (c *cmpTestCtx) Crumbs() []string                  { return nil }
func (c *cmpTestCtx) ColumnNames() []string             { return nil }
func (c *cmpTestCtx) TableNames() []string              { return nil }
func (c *cmpTestCtx) Backend() backend.Backend          { return nil }
func (c *cmpTestCtx) KV() backend.KVBackend             { return nil }
func (c *cmpTestCtx) Theme() *theme.Theme               { return nil }
func (c *cmpTestCtx) ThemeName() string                 { return "" }
func (c *cmpTestCtx) ShowBorders() bool                 { return false }
func (c *cmpTestCtx) ShowRowNumbers() bool              { return false }
func (c *cmpTestCtx) Tabs() []tui.TabInfo               { return nil }
func (c *cmpTestCtx) ActiveTab() int                    { return 0 }
func (c *cmpTestCtx) ActivePaneID() int                 { return 0 }
func (c *cmpTestCtx) PendingEdit() *tui.PendingEdit     { return nil }
func (c *cmpTestCtx) PendingDelete() *tui.PendingDelete { return nil }
func (c *cmpTestCtx) Client() transport.Client          { return c.client }

// press feeds a key and resolves any command the overlay returns (schema/diff
// loads), advancing the wizard one step.
func (o *cmpOverlay) press(code rune, text string) *cmpOverlay {
	ov, cmd := o.Update(tea.KeyPressMsg{Code: code, Text: text})
	if cmd != nil {
		ov, _ = ov.Update(cmd())
	}
	return ov.(*cmpOverlay)
}

func TestCompareWizardEndToEnd(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "compare-test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	loc := transport.NewLocal(appcore.New(db, kr))
	ctx := context.Background()

	// Two independent in-memory SQLite databases: source has one table, target
	// has the same table plus an extra column.
	src, err := loc.CreateConnection(&protocol.CreateConnectionRequest{Name: "src", Kind: protocol.DatabaseKindSQLite, Database: ":memory:"})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	tgt, err := loc.CreateConnection(&protocol.CreateConnectionRequest{Name: "tgt", Kind: protocol.DatabaseKindSQLite, Database: ":memory:"})
	if err != nil {
		t.Fatalf("create tgt: %v", err)
	}

	seed := func(id string, extra bool) {
		tx, err := loc.BeginTransaction(ctx, id)
		if err != nil {
			t.Fatalf("begin %s: %v", id, err)
		}
		if _, err := loc.ExecuteInTx(ctx, tx.ID, "CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
			t.Fatalf("create table %s: %v", id, err)
		}
		if extra {
			if _, err := loc.ExecuteInTx(ctx, tx.ID, "ALTER TABLE people ADD COLUMN age INTEGER"); err != nil {
				t.Fatalf("alter %s: %v", id, err)
			}
		}
		if _, err := loc.CommitTransaction(ctx, tx.ID); err != nil {
			t.Fatalf("commit %s: %v", id, err)
		}
	}
	seed(src.ID, false)
	seed(tgt.ID, true)

	o := &cmpOverlay{ctx: &cmpTestCtx{client: loc}, stage: cmpStageLoading}
	o = o.press(0, "") // no-op at loading
	if o.stage != cmpStageLoading {
		t.Fatalf("expected loading stage before Init")
	}
	// Kick off Init's connection load and apply the result.
	ov, _ := o.Update(o.Init()())
	o = ov.(*cmpOverlay)
	if o.stage != cmpStageSrcConn {
		t.Fatalf("expected srcConn stage, got %d", o.stage)
	}
	if len(o.conns) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(o.conns))
	}

	// Pick source (index 0).
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageSrcSchema {
		t.Fatalf("expected srcSchema stage, got %d", o.stage)
	}
	// Accept default schema.
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageTgtConn {
		t.Fatalf("expected tgtConn stage, got %d", o.stage)
	}
	// Pick target (index 1).
	o = o.press(tea.KeyDown, "")
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageTgtSchema {
		t.Fatalf("expected tgtSchema stage, got %d", o.stage)
	}
	// Accept default schema → triggers diff.
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageDiff {
		t.Fatalf("expected diff stage, got %d (err=%v)", o.stage, o.err)
	}
	if o.err != nil {
		t.Fatalf("diff error: %v", o.err)
	}
	if len(o.plan.Entries) == 0 {
		t.Fatalf("expected at least one diff entry (add_column age)")
	}
	found := false
	for _, e := range o.plan.Entries {
		if e.Op == protocol.OpAddColumn && e.Table == "people" && e.Object == "age" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected add_column people.age in plan, got %+v", o.plan.Entries)
	}

	// Generate the sync script.
	o = o.press(tea.KeyEnter, "")
	if o.stage != cmpStageDDL {
		t.Fatalf("expected ddl stage, got %d (err=%v)", o.stage, o.err)
	}
	if o.err != nil {
		t.Fatalf("ddl error: %v", o.err)
	}
	if len(o.ddl.Statements) == 0 {
		t.Fatalf("expected a non-empty forward script")
	}
	joined := cmpScriptText(o.ddl.Statements)
	if !strings.Contains(joined, "age") {
		t.Fatalf("forward script missing age column: %s", joined)
	}

	// Back navigation works from DDL to diff.
	o = o.press(tea.KeyEscape, "")
	if o.stage != cmpStageDiff {
		t.Fatalf("expected back to diff, got %d", o.stage)
	}
}

func TestCompareWizardNoClient(t *testing.T) {
	o := &cmpOverlay{ctx: &cmpTestCtx{client: nil}, stage: cmpStageLoading}
	if cmd := o.Init(); cmd != nil {
		t.Fatalf("Init should return nil cmd when no client")
	}
	if o.err == nil {
		t.Fatalf("expected error recorded when no client")
	}
}

// cmpLocal builds a Local transport client backed by a throwaway app-core.
func cmpLocal(t *testing.T) transport.Client {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "polydb.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "cmp-local")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return transport.NewLocal(appcore.New(db, kr))
}
