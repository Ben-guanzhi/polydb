package changes

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func TestBuildPKStatementsWithDefaultPK(t *testing.T) {
	q := New()
	q.AddCellChange("r1", "name", sv("a"), sv("b"), mkRow(cell("id", iv(1)), cell("name", sv("a"))))
	q.AddDelete("r2", mkRow(cell("id", iv(2)), cell("name", sv("c"))))
	q.AddInsert("r3", mkRow(cell("id", iv(3)), cell("name", sv("d"))))
	q.AddInsert("r4", mkRow(cell("id", sv("")), cell("name", sv("e"))))
	pk := []string{"id"}
	stmts := BuildStatements(q.All(), "main", "t", pk, protocol.DatabaseKindSQLite)
	if len(stmts) != 4 {
		t.Fatalf("statements = %d, want 4", len(stmts))
	}
	if stmts[0].SQL != `UPDATE "main"."t" SET "name" = 'b' WHERE "id" = 1` {
		t.Errorf("update sql: %s", stmts[0].SQL)
	}
	if stmts[0].ParamSQL != `UPDATE "main"."t" SET "name" = ? WHERE "id" = ?` {
		t.Errorf("update param sql: %s", stmts[0].ParamSQL)
	}
	if len(stmts[0].Params) != 2 || !ValuesEqual(stmts[0].Params[0], sv("b")) || !ValuesEqual(stmts[0].Params[1], iv(1)) {
		t.Errorf("update params: %+v", stmts[0].Params)
	}
	if !strings.Contains(stmts[1].SQL, `DELETE FROM "main"."t" WHERE "id" = 2`) {
		t.Errorf("delete sql: %s", stmts[1].SQL)
	}
	if stmts[2].SQL != `INSERT INTO "main"."t" ("id", "name") VALUES (3, 'd')` {
		t.Errorf("insert sql: %s", stmts[2].SQL)
	}
	if stmts[3].SQL != `INSERT INTO "main"."t" ("id", "name") VALUES (DEFAULT, 'e')` {
		t.Errorf("default-pk insert sql: %s", stmts[3].SQL)
	}
	if len(stmts[3].Params) != 1 || !ValuesEqual(stmts[3].Params[0], sv("e")) {
		t.Errorf("default-pk insert params: %+v", stmts[3].Params)
	}
	if !stmts[0].AllowZero {
		t.Error("update should allow zero")
	}
	if stmts[1].AllowZero {
		t.Error("delete should not allow zero")
	}
}

func TestMySQLAndMSSQLDialects(t *testing.T) {
	if QuoteIdent(protocol.DatabaseKindMySQL, "my table") != "`my table`" {
		t.Errorf("mysql ident: %s", QuoteIdent(protocol.DatabaseKindMySQL, "my table"))
	}
	if QuoteIdent(protocol.DatabaseKindMSSQL, "x") != "[x]" {
		t.Errorf("mssql ident: %s", QuoteIdent(protocol.DatabaseKindMSSQL, "x"))
	}
	if ValueToLiteral(protocol.DatabaseKindMySQL, sv("a'b")) != "'a''b'" {
		t.Errorf("mysql string literal: %s", ValueToLiteral(protocol.DatabaseKindMySQL, sv("a'b")))
	}
	if got := ValueToLiteral(protocol.DatabaseKindSQLite, protocol.NewNullValue()); got != "NULL" {
		t.Errorf("null literal: %s", got)
	}
}

func TestRowCountValidation(t *testing.T) {
	del := Statement{ExpectAffected: 1, AllowZero: false}
	upd := Statement{ExpectAffected: 1, AllowZero: true}
	if !RowsMismatch(del, 0) || !RowsMismatch(del, 2) || RowsMismatch(del, 1) {
		t.Error("delete mismatch logic wrong")
	}
	if RowsMismatch(upd, 0) || RowsMismatch(upd, 1) || !RowsMismatch(upd, 2) {
		t.Error("keyed-update mismatch logic wrong")
	}
}

// NeedsRowProbe / ProbeAllowsCommit 是 R2-02 守卫的**策略**部分，下沉到内核
// 后各前端只提供探针执行（LENS-04）。这里钉住三分归因：
// 行数不符 → 冲突；keyed 语句 0 行 → 需探针；探针后行在且 AllowZero 才放行。
func TestNeedsRowProbeAndVerdict(t *testing.T) {
	keyed := Statement{AllowZero: true, ExpectAffected: 1, Table: "t", Where: "id = ?"}
	if !NeedsRowProbe(keyed, 0) {
		t.Error("keyed zero-row update must be probed (same-value no-op vs vanished row)")
	}
	if NeedsRowProbe(keyed, 1) {
		t.Error("an exact one-row keyed update needs no probe")
	}
	if !NeedsRowProbe(keyed, 2) {
		t.Error("a count above the expectation is a conflict and must be probed")
	}
	insert := Statement{AllowZero: false, ExpectAffected: 1, Table: "t"}
	if !NeedsRowProbe(insert, 0) {
		t.Error("an insert shortfall is a conflict and must be probed")
	}

	if !ProbeAllowsCommit(keyed, 0, true) {
		t.Error("row still there + allow-zero must commit the no-op")
	}
	if ProbeAllowsCommit(keyed, 0, false) {
		t.Error("a vanished row must not commit")
	}
	if ProbeAllowsCommit(insert, 0, true) {
		t.Error("a statement without allow-zero must never commit a shortfall")
	}
	if ProbeAllowsCommit(keyed, 2, true) {
		t.Error("a count above the expectation must not commit, even when the row exists")
	}
}
