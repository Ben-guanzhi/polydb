package popup

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/polydb/polydb/internal/tuidata"
)

func qxIOTestFrame() *tuidata.Frame {
	return &tuidata.Frame{Columns: []tuidata.Column{
		tuidata.NewColumn("id", tuidata.TypeInt, []any{int64(1), int64(2), nil}),
		tuidata.NewColumn("name", tuidata.TypeString, []any{"alice", "bob", nil}),
		tuidata.NewColumn("score", tuidata.TypeFloat, []any{1.5, 2.5, 3.5}),
	}}
}

func TestFrameCSV_RoundTrip(t *testing.T) {
	f := qxIOTestFrame()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := qxFrameToCSV(w, f); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	got, err := qxParseCSV(buf.String())
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if got.NumRows() != 3 || got.NumCols() != 3 {
		t.Fatalf("shape mismatch: %dx%d", got.NumRows(), got.NumCols())
	}
	if got.ColumnNames()[0] != "id" || got.ColumnNames()[1] != "name" {
		t.Fatalf("headers: %v", got.ColumnNames())
	}
	// id column inferred int, nil preserved.
	if v := got.Cell(0, 0); v != int64(1) {
		t.Fatalf("id[0] = %#v, want 1", v)
	}
	if v := got.Cell(2, 0); v != nil {
		t.Fatalf("id[2] = %#v, want nil", v)
	}
	// name column string.
	if v := got.Cell(1, 1); v != "bob" {
		t.Fatalf("name[1] = %#v, want bob", v)
	}
	// score column inferred float.
	if v := got.Cell(0, 2); v != 1.5 {
		t.Fatalf("score[0] = %#v, want 1.5", v)
	}
}

func TestFrameJSON_RoundTrip(t *testing.T) {
	f := qxIOTestFrame()
	b, err := qxFrameToJSON(f)
	if err != nil {
		t.Fatalf("to json: %v", err)
	}
	got, err := qxParseJSON(b)
	if err != nil {
		t.Fatalf("from json: %v", err)
	}
	if got.NumRows() != 3 {
		t.Fatalf("rows = %d, want 3", got.NumRows())
	}
	// JSON object key order is not guaranteed; resolve columns by name.
	idCol := got.ColumnIndex("id")
	nameCol := got.ColumnIndex("name")
	scoreCol := got.ColumnIndex("score")
	if idCol < 0 || nameCol < 0 || scoreCol < 0 {
		t.Fatalf("missing columns: %v", got.ColumnNames())
	}
	if v := got.Cell(2, idCol); v != nil {
		t.Fatalf("id[2] = %#v, want nil", v)
	}
	if v := got.Cell(0, nameCol); v != "alice" {
		t.Fatalf("name[0] = %#v, want alice", v)
	}
	if v := got.Cell(0, scoreCol); v != 1.5 {
		t.Fatalf("score[0] = %#v, want 1.5", v)
	}
}

func TestFrameToSQL(t *testing.T) {
	f := qxIOTestFrame()
	sql := qxFrameToSQL(f, "people", "postgres")
	for _, want := range []string{
		`INSERT INTO "people"`,
		`"id"`, `"name"`, `"score"`,
		`(1, 'alice', 1.5)`,
		`(2, 'bob', 2.5)`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("sql missing %q:\n%s", want, sql)
		}
	}
}

func TestParseExportArg(t *testing.T) {
	// Default format inferred from extension.
	format, path, err := qxParseExportArg("/tmp/out.json")
	if err != nil || format != "json" || path != "/tmp/out.json" {
		t.Fatalf("got (%q,%q,%v)", format, path, err)
	}
	// Explicit format + path.
	format, path, err = qxParseExportArg("sql /tmp/out.sql")
	if err != nil || format != "sql" || path != "/tmp/out.sql" {
		t.Fatalf("got (%q,%q,%v)", format, path, err)
	}
	// Unknown format errors.
	if _, _, err := qxParseExportArg("xml /tmp/x.xml"); err == nil {
		t.Fatalf("expected error for unknown format")
	}
}

func TestParseFile_Unsupported(t *testing.T) {
	if _, err := qxParseFile("/tmp/x.txt"); err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}
