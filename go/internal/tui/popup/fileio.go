package popup

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
)

// Frame (de)serialization helpers shared by the export / import / edit
// commands. They speak the three formats Beekeeper Studio supports for
// everyday data movement: CSV, JSON, and SQL INSERT.

// qxFrameToCSV writes the frame as CSV (header row + one row per record).
func qxFrameToCSV(w *csv.Writer, f *tuidata.Frame) error {
	if err := w.Write(f.ColumnNames()); err != nil {
		return err
	}
	for r := 0; r < f.NumRows(); r++ {
		row := make([]string, f.NumCols())
		for c := 0; c < f.NumCols(); c++ {
			row[c] = f.CellString(r, c)
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// qxFrameToJSON marshals the frame to a JSON array of row objects. time.Time
// values are rendered as RFC3339 strings; everything else keeps its native
// JSON representation.
func qxFrameToJSON(f *tuidata.Frame) ([]byte, error) {
	rows := make([]map[string]any, f.NumRows())
	for r := 0; r < f.NumRows(); r++ {
		m := make(map[string]any, f.NumCols())
		for c := 0; c < f.NumCols(); c++ {
			m[f.Columns[c].Name] = qxCellForJSON(f.Cell(r, c))
		}
		rows[r] = m
	}
	return json.MarshalIndent(rows, "", "  ")
}

func qxCellForJSON(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.Format(time.RFC3339)
	}
	return v
}

// qxFrameToSQL emits INSERT statements (batched 500 rows) for the frame. The
// table name is the caller-supplied label; identifiers are quoted per the
// backend dialect (changes.QuoteIdent), values via qxSQLLiteral.
func qxFrameToSQL(f *tuidata.Frame, table, kind string) string {
	quote := func(s string) string {
		if kind != "" {
			return changes.QuoteIdent(protocol.DatabaseKind(kind), s)
		}
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	cols := f.ColumnNames()
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quote(c)
	}
	colList := strings.Join(quoted, ", ")
	tableQ := quote(table)

	var b strings.Builder
	const batch = 500
	for start := 0; start < f.NumRows(); start += batch {
		end := start + batch
		if end > f.NumRows() {
			end = f.NumRows()
		}
		b.WriteString("INSERT INTO ")
		b.WriteString(tableQ)
		b.WriteString(" (")
		b.WriteString(colList)
		b.WriteString(") VALUES\n")
		for r := start; r < end; r++ {
			vals := make([]string, f.NumCols())
			for c := 0; c < f.NumCols(); c++ {
				vals[c] = qxSQLLiteral(f.Cell(r, c))
			}
			b.WriteString("  (")
			b.WriteString(strings.Join(vals, ", "))
			b.WriteString(")")
			if r < end-1 {
				b.WriteString(",\n")
			} else {
				b.WriteString(";\n")
			}
		}
	}
	return b.String()
}

// qxSQLLiteral renders a cell value as a SQL literal.
func qxSQLLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case time.Time:
		return "'" + x.Format("2006-01-02 15:04:05") + "'"
	default:
		return "'" + fmt.Sprint(x) + "'"
	}
}

// qxParseFile reads a CSV or JSON file (by extension) into a Frame. Unknown
// extensions error out so the import command fails loudly rather than
// silently.
func qxParseFile(path string) (*tuidata.Frame, error) {
	ext := strings.ToLower(filepath.Ext(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch ext {
	case ".json":
		return qxParseJSON(data)
	case ".csv", "":
		return qxParseCSV(string(data))
	default:
		return nil, fmt.Errorf("unsupported import format %q (use .csv or .json)", ext)
	}
}

// qxParseCSV builds a Frame from CSV text. The first row is the header;
// column types are inferred from the data (int > float > string), with
// empty strings treated as NULL.
func qxParseCSV(text string) (*tuidata.Frame, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return &tuidata.Frame{}, nil
	}
	headers := recs[0]
	ncols := len(headers)
	raw := make([][]any, ncols)
	for i := range raw {
		raw[i] = make([]any, 0, len(recs)-1)
	}
	for _, rec := range recs[1:] {
		for c := 0; c < ncols; c++ {
			s := ""
			if c < len(rec) {
				s = rec[c]
			}
			if s == "" {
				raw[c] = append(raw[c], nil)
			} else {
				raw[c] = append(raw[c], s)
			}
		}
	}
	types := make([]tuidata.DType, ncols)
	for c := 0; c < ncols; c++ {
		types[c] = qxInferCSVType(raw[c])
	}
	cols := make([]tuidata.Column, ncols)
	for c := 0; c < ncols; c++ {
		cols[c] = tuidata.NewColumn(headers[c], types[c], qxConvertCSV(raw[c], types[c]))
	}
	return &tuidata.Frame{Columns: cols}, nil
}

func qxInferCSVType(vals []any) tuidata.DType {
	sawInt, sawFloat := true, true
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			continue
		}
		sawInt = false
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			continue
		}
		return tuidata.TypeString
	}
	if sawInt {
		return tuidata.TypeInt
	}
	if sawFloat {
		return tuidata.TypeFloat
	}
	return tuidata.TypeString
}

func qxConvertCSV(vals []any, t tuidata.DType) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		s, ok := v.(string)
		if !ok || s == "" {
			out[i] = nil
			continue
		}
		switch t {
		case tuidata.TypeInt:
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				out[i] = n
			} else {
				out[i] = s
			}
		case tuidata.TypeFloat:
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				out[i] = f
			} else {
				out[i] = s
			}
		default:
			out[i] = s
		}
	}
	return out
}

// qxParseJSON builds a Frame from a JSON array of objects. Column order
// follows first-seen key order across all rows; missing keys are NULL.
func qxParseJSON(data []byte) (*tuidata.Frame, error) {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &tuidata.Frame{}, nil
	}
	var names []string
	seen := make(map[string]bool, len(rows[0]))
	for _, r := range rows {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}
	ncols := len(names)
	raw := make([][]any, ncols)
	for i := range raw {
		raw[i] = make([]any, len(rows))
	}
	for ri, r := range rows {
		for ci, name := range names {
			raw[ci][ri] = qxJSONCell(r[name])
		}
	}
	types := make([]tuidata.DType, ncols)
	for c := 0; c < ncols; c++ {
		types[c] = qxInferJSONType(raw[c])
	}
	cols := make([]tuidata.Column, ncols)
	for c := 0; c < ncols; c++ {
		cols[c] = tuidata.NewColumn(names[c], types[c], raw[c])
	}
	return &tuidata.Frame{Columns: cols}, nil
}

func qxJSONCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case float64, int64, bool:
		return x
	default:
		return v
	}
}

func qxInferJSONType(vals []any) tuidata.DType {
	allNum, allBool := true, true
	for _, v := range vals {
		switch v.(type) {
		case nil:
		case float64, int64:
			allBool = false
		case bool:
			allNum = false
		default:
			allNum = false
			allBool = false
		}
	}
	if allBool {
		return tuidata.TypeBool
	}
	if allNum {
		return tuidata.TypeFloat
	}
	return tuidata.TypeString
}
