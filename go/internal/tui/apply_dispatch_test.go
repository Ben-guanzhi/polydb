package tui

// E6.1 执行分派的应用环路取证（真 :memory: SQLite + 真实 Local 客户端，外面套一层调用录音）。
//
// commands.go 的 applyScriptSync 有两条分派，此前只被"计数对得上"意义的测试覆盖过：
// 1. pragmas_outside_tx=false + ddl_in_tx=true ⇒ **整份脚本**包进同一个事务。
//    老用例只在"首条就炸"的形状上碰过它，从没证明过**已经落地的语句会被事务撤销**；
// 2. ddl_in_tx=false ⇒ 逐条跑，失败留下中间态。老用例断言了 Ran/Skipped，
//    但没断言"第一条的效果此时**必须还在库里**"（这正是 A.15 G14 的灾难形状）。
// 所以这里既断言**调用序列**（PRAGMA 到底在不在事务里），也断言**库里的最终状态**
// （事务回滚 vs 中间态残留）——两组断言互斥，互为对照。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

// recorder 包装真实客户端，只记录调用序列，其余一律透传——不 mock 数据库。
type recorder struct {
	transport.Client
	calls []string
}

func shortSQL(sql string) string {
	r := []rune(strings.TrimSpace(sql))
	if len(r) > 34 {
		return string(r[:34]) + "…"
	}
	return string(r)
}

func (r *recorder) Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	r.calls = append(r.calls, "exec:"+shortSQL(sql))
	return r.Client.Execute(ctx, id, sql, args...)
}

func (r *recorder) BeginTransaction(ctx context.Context, id string) (*protocol.TransactionInfo, error) {
	r.calls = append(r.calls, "begin")
	return r.Client.BeginTransaction(ctx, id)
}

func (r *recorder) ExecuteInTx(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	r.calls = append(r.calls, "tx:"+shortSQL(sql))
	return r.Client.ExecuteInTx(ctx, txID, sql, args...)
}

func (r *recorder) CommitTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	r.calls = append(r.calls, "commit")
	return r.Client.CommitTransaction(ctx, txID)
}

func (r *recorder) RollbackTransaction(ctx context.Context, txID string) (*protocol.TransactionInfo, error) {
	r.calls = append(r.calls, "rollback")
	return r.Client.RollbackTransaction(ctx, txID)
}

func (r *recorder) seq() string { return strings.Join(r.calls, " | ") }

// tableCount 用真引擎回读：某张表在不在（0 / 1）。走未包装的客户端，不污染录音序列。
func tableCount(t *testing.T, c transport.Client, connID, name string) int {
	t.Helper()
	res, err := c.Execute(context.Background(), connID,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='"+name+"'")
	if err != nil {
		t.Fatalf("回读 %s：%v", name, err)
	}
	raw, err := json.Marshal(res.Rows[0][0])
	if err != nil {
		t.Fatalf("编码回读值：%v", err)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("回读 %s 不是整数：%s", name, raw)
	}
	return n
}

// 分派一：pragmas_outside_tx=false ⇒ 整份脚本一个事务；中途失败时**已落地的语句必须被撤销**。
func TestApplyScriptSyncWholeTxRollsBackLandedStatements(t *testing.T) {
	app, id := setupApp(t)
	local := transport.NewLocal(app)
	rec := &recorder{Client: local}

	out := applyScriptSync(rec, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `CREATE TABLE e6_tx(a INTEGER);`, Phase: 50, Table: "e6_tx"},
			{SQL: `CREATE TABLE e6_tx(b INTEGER);`, Phase: 50, Table: "e6_tx"}, // 重名 ⇒ 必炸
			{SQL: `CREATE TABLE e6_after(b INTEGER);`, Phase: 50, Table: "e6_after"},
		},
		DdlInTx:          true,
		PragmasOutsideTx: false,
	}, cmpRunForward)

	want := "begin | tx:CREATE TABLE e6_tx(a INTEGER); | tx:CREATE TABLE e6_tx(b INTEGER); | rollback"
	if rec.seq() != want {
		t.Fatalf("调用序列不对：\n got %s\nwant %s", rec.seq(), want)
	}
	if out.Ran != 1 || len(out.Failed) != 1 || !out.RolledBack || out.Skipped != 1 {
		t.Fatalf("编排结果不对：%+v", out)
	}
	// 关键断言：Ran=1 只表示"执行过没报错"，事务回滚后**库里必须什么都没有了**。
	if n := tableCount(t, local, id, "e6_tx"); n != 0 {
		t.Fatalf("事务分支必须撤销已落地的建表，实际 sqlite_master 里还有 %d 条", n)
	}
	if n := tableCount(t, local, id, "e6_after"); n != 0 {
		t.Fatalf("失败之后的语句不该跑：%d", n)
	}
}

// 分派二：ddl_in_tx=false ⇒ 不开事务、逐条跑，失败**必须留下中间态**。
// 与上一份断言互斥：同一条已落地语句在这里反过来要求"还在"。A.15 G14 就是这个形状。
func TestApplyScriptSyncNonTxBranchLeavesIntermediateState(t *testing.T) {
	app, id := setupApp(t)
	local := transport.NewLocal(app)
	rec := &recorder{Client: local}

	out := applyScriptSync(rec, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `CREATE TABLE e6_seq(a INTEGER);`, Phase: 50, Table: "e6_seq"},
			{SQL: `CREATE TABLE e6_seq(b INTEGER);`, Phase: 50, Table: "e6_seq"},
			{SQL: `CREATE TABLE e6_seq2(c INTEGER);`, Phase: 50, Table: "e6_seq2"},
		},
		DdlInTx:          false,
		PragmasOutsideTx: false,
	}, cmpRunForward)

	want := "exec:CREATE TABLE e6_seq(a INTEGER); | exec:CREATE TABLE e6_seq(b INTEGER);"
	if rec.seq() != want {
		t.Fatalf("逐条分支不得开事务，序列应为 exec→exec 后停住：\n got %s\nwant %s", rec.seq(), want)
	}
	if out.Ran != 1 || len(out.Failed) != 1 || out.RolledBack || out.Skipped != 1 {
		t.Fatalf("编排结果不对：%+v", out)
	}
	if n := tableCount(t, local, id, "e6_seq"); n != 1 {
		t.Fatalf("无事务分支下已落地语句必须留在库里（中间态），实际 %d", n)
	}
}

// 分派一的另一种形状：pragmas_outside_tx=true ⇒ 首尾 PRAGMA 必须**在事务外**（A.11 G6），
// 主体两条共享**一个**事务。老用例只有 Ran==3 的成功断言，看不见 PRAGMA 落在哪。
func TestApplyScriptSyncPragmasStayOutsideTheTx(t *testing.T) {
	app, id := setupApp(t)
	local := transport.NewLocal(app)
	rec := &recorder{Client: local}

	out := applyScriptSync(rec, id, &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `PRAGMA foreign_keys=OFF;`, Phase: 0, Table: ""},
			{SQL: `CREATE TABLE e6_w(a INTEGER);`, Phase: 50, Table: "e6_w"},
			{SQL: `CREATE INDEX ix_e6_w ON e6_w(a);`, Phase: 80, Table: "e6_w"},
			{SQL: `PRAGMA foreign_keys=ON;`, Phase: 255, Table: ""},
		},
		DdlInTx:          true,
		PragmasOutsideTx: true,
	}, cmpRunForward)

	want := "exec:PRAGMA foreign_keys=OFF; | begin | tx:CREATE TABLE e6_w(a INTEGER); | " +
		"tx:CREATE INDEX ix_e6_w ON e6_w(a); | commit | exec:PRAGMA foreign_keys=ON;"
	if rec.seq() != want {
		t.Fatalf("调用序列不对：\n got %s\nwant %s", rec.seq(), want)
	}
	if out.Ran != 4 || len(out.Failed) != 0 || out.RolledBack {
		t.Fatalf("编排结果不对：%+v", out)
	}
	if strings.Count(rec.seq(), "begin") != 1 {
		t.Fatalf("主体两条必须共享一个事务：%s", rec.seq())
	}
	if n := tableCount(t, local, id, "e6_w"); n != 1 {
		t.Fatalf("成功分支必须真把表建出来，实际 %d", n)
	}
}

// 回滚方向复用同一分派：跑 rollback 那一份语句、能力位与正向共享（A.11 规则 7），
// 且首尾 PRAGMA 照样留在事务外。
func TestApplyScriptSyncRollbackRunReusesSameDispatch(t *testing.T) {
	app, id := setupApp(t)
	local := transport.NewLocal(app)
	if _, err := local.Execute(context.Background(), id, "CREATE TABLE e6_rb(a INTEGER)"); err != nil {
		t.Fatalf("预置表：%v", err)
	}
	rec := &recorder{Client: local}

	script := &protocol.DdlScript{
		Statements: []protocol.DdlStatement{
			{SQL: `PRAGMA foreign_keys=OFF;`, Phase: 0},
			{SQL: `DROP TABLE e6_missing;`, Phase: 40}, // 正向那份故意给一条必炸的语句
			{SQL: `PRAGMA foreign_keys=ON;`, Phase: 255},
		},
		Rollback: []protocol.DdlStatement{
			{SQL: `PRAGMA foreign_keys=OFF;`, Phase: 0},
			{SQL: `DROP TABLE e6_rb;`, Phase: 40, Table: "e6_rb"},
			{SQL: `PRAGMA foreign_keys=ON;`, Phase: 255},
		},
		DdlInTx:          true,
		PragmasOutsideTx: true,
	}
	out := applyScriptSync(rec, id, script, cmpRunRollback)

	want := "exec:PRAGMA foreign_keys=OFF; | begin | tx:DROP TABLE e6_rb; | " +
		"commit | exec:PRAGMA foreign_keys=ON;"
	if rec.seq() != want {
		t.Fatalf("回滚方向必须跑 rollback 那份且保持同一分派：\n got %s\nwant %s", rec.seq(), want)
	}
	if out.Ran != 3 || len(out.Failed) != 0 {
		t.Fatalf("回滚编排结果不对：%+v", out)
	}
	if n := tableCount(t, local, id, "e6_rb"); n != 0 {
		t.Fatalf("回滚分支必须真把表删掉，实际还剩 %d", n)
	}
}
