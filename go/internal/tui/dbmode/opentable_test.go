package dbmode

import (
	"fmt"
	"testing"

	"github.com/polydb/polydb/internal/tui"
)

func TestOpenTableFactoryRequiresConnection(t *testing.T) {
	if _, err := openTableFactory(&fakeCtx{}, "ns\tusers"); err == nil {
		t.Fatal("expected error without a live connection")
	}
}

func TestOpenTableFactoryRequiresTableName(t *testing.T) {
	be := &fakeBackend{}
	if _, err := openTableFactory(&fakeCtx{be: be}, "ns\t "); err == nil {
		t.Fatal("expected error for empty table name")
	}
}

func TestOpenTableFactorySuccess(t *testing.T) {
	frame := stringFrame([]string{"id", "name"}, [][]string{{"1", "ada"}})
	be := &fakeBackend{frame: frame}

	ov, err := openTableFactory(&fakeCtx{be: be}, "public\tusers")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	loader, ok := ov.(*tui.TableLoader)
	if !ok {
		t.Fatalf("factory returned %T, want *tui.TableLoader", ov)
	}

	// Init kicks off the async fetch with the decoded ns/table and the limit.
	msg := runCmd(loader.Init())
	loaded, ok := msg.(tui.TableLoadedMsg)
	if !ok {
		t.Fatalf("Init produced %T, want tui.TableLoadedMsg", msg)
	}
	if be.fetchNS != "public" || be.fetchTable != "users" || be.fetchLimit != tui.TableFetchLimit {
		t.Fatalf("FetchTable(%q, %q, %d), want (public, users, %d)",
			be.fetchNS, be.fetchTable, be.fetchLimit, tui.TableFetchLimit)
	}
	if loaded.Err != nil || loaded.Frame != frame {
		t.Fatalf("unexpected load result: %+v", loaded)
	}

	// The result closes the loader and opens the frame as a new tab.
	if _, cmd := loader.Update(loaded); cmd == nil {
		t.Fatal("loaded message produced no command")
	}
	cmds := loader.ResultCmds(loaded)
	if len(cmds) != 2 {
		t.Fatalf("ResultCmds returned %d commands, want 2", len(cmds))
	}
	if _, ok := runCmd(cmds[0]).(tui.CloseOverlayMsg); !ok {
		t.Fatal("first command must close the loading overlay")
	}
	af, ok := runCmd(cmds[1]).(tui.ApplyFrameMsg)
	if !ok {
		t.Fatal("second command must apply the frame")
	}
	if af.Frame != frame || !af.NewTab || af.TabTitle != "users" || af.Crumb != "table" {
		t.Fatalf("ApplyFrameMsg = %+v, want NewTab users/table", af)
	}
}

func TestOpenTableFactoryFetchError(t *testing.T) {
	be := &fakeBackend{fetchErr: fmt.Errorf("table gone")}
	ov, err := openTableFactory(&fakeCtx{be: be}, "users")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	loader := ov.(*tui.TableLoader)

	loaded := runCmd(loader.Init()).(tui.TableLoadedMsg)
	if loaded.Err == nil {
		t.Fatal("expected fetch error")
	}
	cmds := loader.ResultCmds(loaded)
	if len(cmds) != 2 {
		t.Fatalf("ResultCmds returned %d commands, want 2", len(cmds))
	}
	if _, ok := runCmd(cmds[0]).(tui.CloseOverlayMsg); !ok {
		t.Fatal("first command must close the loading overlay")
	}
	em, ok := runCmd(cmds[1]).(tui.ErrorMsg)
	if !ok || em.Err == nil {
		t.Fatal("second command must surface the error")
	}
}

func TestTableLoaderEscCloses(t *testing.T) {
	loader := tui.NewTableLoader(nil, &fakeBackend{}, "", "users", false)
	_, cmd := loader.Update(keyPress("esc"))
	if _, ok := runCmd(cmd).(tui.CloseOverlayMsg); !ok {
		t.Fatal("esc must close the loading overlay")
	}
}
