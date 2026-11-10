package tui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
	"github.com/polydb/polydb/pkg/transport"
)

// TestLocalFrameInfersColumnTypesWhenGenericTypeEmpty is the end-to-end half of
// §94: through the real Go data plane (transport → appcore → bridge), the
// protocol result's generic_type is always empty (bridge.QueryResultFromDB
// leaves it zero and v1 drivers don't fill it), yet the TUI frame must still
// type its columns from the sample values instead of labelling everything "str".
func TestLocalFrameInfersColumnTypesWhenGenericTypeEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "polydb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	kr, err := keyring.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cli := transport.NewLocal(appcore.New(db, kr))

	shop := filepath.Join(dir, "shop.db")
	info, err := cli.CreateConnection(&protocol.CreateConnectionRequest{
		Name: "shop", Kind: protocol.DatabaseKindSQLite, Database: shop,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Connect(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	defer cli.Disconnect(info.ID)

	ad := backend.NewAdapter(cli, info)
	if _, err := ad.Run("CREATE TABLE t (id INTEGER, at TIMESTAMP, name TEXT, score REAL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Run("INSERT INTO t VALUES (1, '2026-10-24 12:00:00', 'x', 9.5)"); err != nil {
		t.Fatal(err)
	}

	res, err := cli.Execute(ctx, info.ID, "SELECT id, at, name, score FROM t")
	if err != nil {
		t.Fatal(err)
	}
	// Root cause: the protocol data plane never fills generic_type.
	for _, c := range res.Columns {
		if c.GenericType != "" {
			t.Errorf("column %q generic_type = %q, want empty (pins the §94 root cause)", c.Name, c.GenericType)
		}
	}

	f, err := ad.Run("SELECT id, at, name, score FROM t")
	if err != nil || f.Frame == nil {
		t.Fatalf("Run SELECT = %+v, %v", f, err)
	}
	want := []tuidata.DType{
		tuidata.TypeInt, tuidata.TypeDatetime, tuidata.TypeString, tuidata.TypeFloat,
	}
	for i, col := range f.Frame.Columns {
		if col.Type != want[i] {
			t.Errorf("column %q type = %v, want %v", col.Name, col.Type, want[i])
		}
	}
}
