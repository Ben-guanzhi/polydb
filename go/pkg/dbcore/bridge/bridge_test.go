package bridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
	dbsqlite "github.com/dbcore/dbcore/sqlite"
	"github.com/polydb/polydb/pkg/protocol"
)

func boolPtr(b bool) *bool { return &b }

// 同一引擎的时间戳在 wire 上必须只有一个拼写：事务路径 (本包 ValueFromDB 的
// ValTime 分支) 与直查路径 (dbcore.DriverToValue 的 time.Time 分支) 各自转换
// 一次。这里锁定 bridge 侧产出内核 Value.String 的同式拼写；直查侧的字面量
// 对照在 valueconv_test（本包是唯一能同时看见两边的位置）。
func TestValueFromDBTimeSpellingMatchesDriverPath(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	got := ValueFromDB(dbcore.Value{Kind: dbcore.ValTime, T: ts})
	s, ok := protocol.TryGetString(got)
	if !ok {
		t.Fatalf("ValTime must convert to a string value, got %+v", got)
	}
	if s != "2026-01-02T03:04:05.123456Z" {
		t.Fatalf("bridge timestamp spelling = %q, want RFC3339Nano", s)
	}
	// 整秒对齐。
	whole := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got2 := ValueFromDB(dbcore.Value{Kind: dbcore.ValTime, T: whole})
	s2, _ := protocol.TryGetString(got2)
	if s2 != "2026-01-02T03:04:05Z" {
		t.Fatalf("whole-second spelling = %q", s2)
	}
}

func TestValueRoundTrip(t *testing.T) {
	cases := []dbcore.Value{
		{Kind: dbcore.ValNull},
		{Kind: dbcore.ValInt, I: 42},
		{Kind: dbcore.ValFloat, F: 3.14},
		{Kind: dbcore.ValText, S: "hello"},
		{Kind: dbcore.ValBool, Bool: true},
	}
	for _, v := range cases {
		got := ValueToDB(ValueFromDB(v))
		if got.Kind != v.Kind {
			t.Fatalf("kind changed %v -> %v", v.Kind, got.Kind)
		}
		switch v.Kind {
		case dbcore.ValNull:
			if got.Kind != dbcore.ValNull {
				t.Fatalf("null round-trip lost")
			}
		case dbcore.ValInt:
			if got.I != v.I {
				t.Fatalf("int round-trip %d -> %d", v.I, got.I)
			}
		case dbcore.ValFloat:
			if got.F != v.F {
				t.Fatalf("float round-trip %v -> %v", v.F, got.F)
			}
		case dbcore.ValText:
			if got.S != v.S {
				t.Fatalf("text round-trip %q -> %q", v.S, got.S)
			}
		case dbcore.ValBool:
			if got.Bool != v.Bool {
				t.Fatalf("bool round-trip %v -> %v", v.Bool, got.Bool)
			}
		}
	}
}

func TestValueToDBFromProtocol(t *testing.T) {
	// protocol->dbcore direction must preserve types.
	if v := ValueToDB(protocol.NewIntValue(7)); v.Kind != dbcore.ValInt || v.I != 7 {
		t.Fatalf("int via protocol = %+v", v)
	}
	if v := ValueToDB(protocol.NewStringValue("x")); v.Kind != dbcore.ValText || v.S != "x" {
		t.Fatalf("text via protocol = %+v", v)
	}
	if v := ValueToDB(protocol.NewNullValue()); v.Kind != dbcore.ValNull {
		t.Fatalf("null via protocol = %+v", v)
	}
	// Integral floats stay int.
	if v := ValueToDB(protocol.NewFloatValue(2.0)); v.Kind != dbcore.ValInt || v.I != 2 {
		t.Fatalf("integral float should become ValInt, got %+v", v)
	}
	// Integers past 2^53 keep their low bits: the old float64 decode rounded
	// 9007199254740993 to 9007199254740992 before the int64 cast.
	const big = int64(9007199254740993)
	if v := ValueToDB(protocol.NewIntValue(big)); v.Kind != dbcore.ValInt || v.I != big {
		t.Fatalf("bigint parameter lost precision: %+v (want %d)", v, big)
	}
}

func TestQueryResultConvert(t *testing.T) {
	in := &protocol.QueryResult{
		Columns: []protocol.ResultColumn{
			{Name: "id", DataType: "INTEGER", Nullable: boolPtr(false)},
			{Name: "name", DataType: "TEXT", Nullable: boolPtr(true)},
		},
		Rows: [][]protocol.Value{
			{protocol.NewIntValue(1), protocol.NewStringValue("a")},
		},
		AffectedRows: 0,
	}

	dbr := QueryResultToDB(in)
	if len(dbr.Columns) != 2 || dbr.Columns[0].Name != "id" || dbr.Columns[0].Type != "INTEGER" {
		t.Fatalf("dbcore columns wrong: %+v", dbr.Columns)
	}
	if len(dbr.Rows) != 1 || dbr.Rows[0][0].Kind != dbcore.ValInt || dbr.Rows[0][0].I != 1 {
		t.Fatalf("dbcore rows wrong: %+v", dbr.Rows)
	}

	back := QueryResultFromDB(dbr)
	if len(back.Columns) != 2 {
		t.Fatalf("protocol columns lost: %+v", back.Columns)
	}
	if back.Columns[1].DataType != "TEXT" || back.Columns[1].Nullable == nil || !*back.Columns[1].Nullable {
		t.Fatalf("nullable/type lost: %+v", back.Columns[1])
	}
	if ValueToDB(back.Rows[0][0]).I != 1 {
		t.Fatalf("row value lost: %+v", back.Rows[0][0])
	}
	if ValueToDB(back.Rows[0][1]).S != "a" {
		t.Fatalf("row value lost: %+v", back.Rows[0][1])
	}
}

// TestDbCoreContractViaPolydb proves polydb, as a consumer of the shared dbcore
// module, can run dbcore's engine-agnostic contract suite against dbcore's own
// SQLite driver. (DB-CORE-CONTRACT.md §6: the gate every engine driver must pass.)
func TestDbCoreContractViaPolydb(t *testing.T) {
	drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			d, err := dbsqlite.Open(":memory:")
			if err != nil {
				return nil, err
			}
			s, ok := d.AsSQL()
			if !ok {
				return nil, fmt.Errorf("sqlite driver is not SQL")
			}
			txd, ok := s.(dbcore.SQLTxDriver)
			if !ok {
				return nil, fmt.Errorf("sqlite driver is not a SQLTxDriver")
			}
			return txd, nil
		},
		Schema:      "main",
		Placeholder: func(int) string { return "?" },
	}.Run(t)
}
