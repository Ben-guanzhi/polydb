package dbsqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbc "github.com/dbcore/dbcore"
)

// §94 probe: does the polydb protocol path preserve the ValTime kind?
func TestProbeTimeKindRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	const ddl = "CREATE TABLE probe_ts (id INTEGER, at TIMESTAMP)"
	if _, err := c.Execute(ctx, ddl); err != nil {
		t.Fatalf("ddl: %v", err)
	}

	want := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if _, err := c.Execute(ctx,
		"INSERT INTO probe_ts(id, at) VALUES (?, ?)",
		dbc.Value{Kind: dbc.ValInt, I: 1},
		dbc.Value{Kind: dbc.ValTime, T: want},
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	res, err := c.Execute(ctx, "SELECT id, at FROM probe_ts")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	got := res.Rows[0][1]
	fmt.Printf("PROBE kind=%v text=%q time=%v\n", got.Kind, got.S, got.T)
}
