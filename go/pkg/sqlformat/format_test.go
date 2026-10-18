package sqlformat

import "testing"

func TestFormatKeywords(t *testing.T) {
	got := Format("select id, name from users where active = 1")
	want := "SELECT id,\nname\nFROM users\nWHERE active = 1"
	if got != want {
		t.Fatalf("keywords:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatJoin(t *testing.T) {
	got := Format("select a.id, b.name from a join b on a.id = b.aid where a.x > 0")
	want := "SELECT a.id,\nb.name\nFROM a\nJOIN b\nON a.id = b.aid\nWHERE a.x > 0"
	if got != want {
		t.Fatalf("join:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatSubquery(t *testing.T) {
	got := Format("select * from (select id from t where x = 1) sub")
	want := "SELECT *\nFROM (\n  SELECT id\n  FROM t\n  WHERE x = 1\n) sub"
	if got != want {
		t.Fatalf("subquery:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatOperatorSpacing(t *testing.T) {
	got := Format("select a from t where x >= 1 and y <> 2 or z <= 3")
	want := "SELECT a\nFROM t\nWHERE x >= 1\nAND y <> 2\nOR z <= 3"
	if got != want {
		t.Fatalf("operators:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatStringsPreserved(t *testing.T) {
	got := Format("select name from t where name = 'Bob Smith' and note = \"hi\"")
	if !contains(got, "'Bob Smith'") || !contains(got, `"hi"`) {
		t.Fatalf("strings not preserved: %q", got)
	}
	if !contains(got, "WHERE name = 'Bob Smith'") {
		t.Fatalf("string spacing wrong: %q", got)
	}
}

func TestFormatCommentPreserved(t *testing.T) {
	got := Format("select 1 -- pick the constant\nfrom dual")
	if !contains(got, "-- pick the constant") {
		t.Fatalf("comment dropped: %q", got)
	}
}

func TestFormatNoopEmpty(t *testing.T) {
	if got := Format("   "); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}

func TestFormatFunctions(t *testing.T) {
	got := Format("select count(*) from t")
	want := "SELECT count (*)\nFROM t"
	if got != want {
		t.Fatalf("functions:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatIdempotent(t *testing.T) {
	in := "select id,name from users where active=1 order by id desc"
	once := Format(in)
	twice := Format(once)
	if once != twice {
		t.Fatalf("format not idempotent:\n once:  %q\n twice: %q", once, twice)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
