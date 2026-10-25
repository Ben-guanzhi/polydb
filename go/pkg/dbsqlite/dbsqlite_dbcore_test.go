package dbsqlite

import (
	"context"
	"fmt"
	"testing"

	dbc "github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestDBCoreContractViaConn proves the cutover: *Conn itself is now a dbcore.SQLTxDriver
// (Option B), so it runs the shared github.com/dbcore/dbcore contract directly — no
// adapter shim. This replaces the earlier DBCoreDriver pilot wrapper.
func TestDBCoreContractViaConn(t *testing.T) {
	drivertest.Harness{
		Make: func(ctx context.Context) (dbc.SQLTxDriver, error) {
			return Open(ctx, ":memory:")
		},
		Schema:      "main",
		Placeholder: func(int) string { return "?" },
	}.Run(t)
}

// TestDBCoreBrowseTranslation exercises the dbcore↔polydb request translation that the
// empty-path contract test does not cover: paging + sort + filter, and verifies the
// Total backfill respects filters (and that BrowseRowsCount applies no pagination LIMIT).
func TestDBCoreBrowseTranslation(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	d := c // *Conn is now a dbc.SQLTxDriver directly

	if _, err := d.Execute(ctx, "CREATE TABLE t(id INTEGER, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := d.Execute(ctx, "INSERT INTO t(id,name) VALUES (?,?)",
			dbc.Value{Kind: dbc.ValInt, I: int64(i)},
			dbc.Value{Kind: dbc.ValText, S: fmt.Sprintf("n%d", i)},
		); err != nil {
			t.Fatal(err)
		}
	}

	res, err := d.BrowseRows(ctx, "main", "t", &dbc.TableRowsRequest{
		Page:     1,
		PageSize: 2,
		Sort:     []dbc.SortKey{{Column: "id", Desc: true}},
		Filter:   []dbc.FilterExpr{{Column: "id", Op: dbc.FilterGt, Value: dbc.Value{Kind: dbc.ValInt, I: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("paged rows = %d, want 2", len(res.Rows))
	}
	if res.Total != 4 {
		t.Fatalf("total = %d, want 4 (id > 1)", res.Total)
	}
	if id, ok := dbc.TryGetInt(res.Rows[0][0]); !ok || id != 5 {
		t.Fatalf("first row id = %v (%v), want 5 (desc sort)", id, ok)
	}

	cnt, err := d.BrowseRowsCount(ctx, "main", "t", &dbc.TableRowsRequest{
		Filter: []dbc.FilterExpr{{Column: "id", Op: dbc.FilterGt, Value: dbc.Value{Kind: dbc.ValInt, I: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 4 {
		t.Fatalf("filtered count = %d, want 4", cnt)
	}

	// Unfiltered count must ignore any PageSize the caller passed.
	all, err := d.BrowseRowsCount(ctx, "main", "t", &dbc.TableRowsRequest{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if all != 5 {
		t.Fatalf("unfiltered count = %d, want 5 (PageSize must not limit COUNT)", all)
	}
}
