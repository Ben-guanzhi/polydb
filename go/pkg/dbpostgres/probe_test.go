package dbpostgres

// F0.1 PG 方言取证（Go 侧），照 E6.0 的形态（go/pkg/dbsqlite/view_rebuild_probe_test.go）：
// 用真引擎把「规则层要钉进方言口径的事实」一条条探出来，结论回填 docs/plan-compare-sync.md A.26。
// 本文件**不改规则层、不改 spec**——PG 到位前它只会软跳过，到位后一次跑完出真值。
//
// 分档理由（为什么有的点只在一侧探）：
//   - 引擎事实（PG 服务器自己的脾气，与客户端语言无关）：P2–P6、P12 ⇒ 只在 Go 侧探一次，
//     Rust 侧不重复探；同一件引擎事在两端各探一遍，只会制造双实现口径漂移的假象。
//   - 驱动事实（pgx / sqlx 各自把系统目录读成了什么）：P1、P7–P11 ⇒ 两端各探，
//     Rust 侧镜像在 rust/crates/db-postgres/tests/probe.rs。
//
// 每条不预设 PG 结论：观测一律以 `OBS[px]` 前缀打印（跑完 grep 这一条即可收全部真值），
// 断言只打在两类东西上——① 我们自身层的形状不变式（驱动读得对不对、字段填没填），
// ② 该点要钉的那条判据本身（红了就是证据，不是噪声）。纯两可的分支只记不赌。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// probeSchema 专用 schema：探测会在真库上建表建视图，绝不能落在 public 里污染使用者的数据。
const probeSchema = "polydb_probe"

// defaultPGDSN 与 test/contract 同口径（同一环境变量、同一默认串）：两边要么都能连，要么都静默跳过。
const defaultPGDSN = "postgres://postgres@localhost:5432/postgres?sslmode=disable"

func probeDSN() string {
	if dsn := os.Getenv("POLYDB_TEST_PG"); dsn != "" {
		return dsn
	}
	return defaultPGDSN
}

// probeConn 取一个干净的探测现场。连不上即 connected:false 软跳过（与契约测试同一口径）。
func probeConn(t *testing.T) *ProtocolDriver {
	t.Helper()
	dsn := probeDSN()
	c, err := Open(context.Background(), dsn)
	if err != nil {
		t.Skipf("connected:false 无可用 PG（POLYDB_TEST_PG=%s）：%v", dsn, err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	if _, err := c.Execute(ctx, dropProbeSchema); err != nil {
		t.Fatalf("清理旧探测 schema 失败：%v", err)
	}
	if _, err := c.Execute(ctx, "CREATE SCHEMA "+probeSchema); err != nil {
		t.Fatalf("建探测 schema 失败：%v", err)
	}
	t.Cleanup(func() { _, _ = c.Execute(ctx, dropProbeSchema) })
	pd := NewProtocolDriver(c)
	return pd
}

const dropProbeSchema = "DROP SCHEMA IF EXISTS " + probeSchema + " CASCADE"

// mustExec 只用于**前置**建表语句：前置失败说明探测现场没搭起来，直接判失败。
func mustExec(t *testing.T, c *ProtocolDriver, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	for _, q := range stmts {
		if _, err := c.Execute(ctx, q); err != nil {
			t.Fatalf("前置语句失败 %s：%v", q, err)
		}
	}
}

// tryExec 用于**被测**语句：成败都是真值，记成观测串返回，绝不让引擎的脾气直接把测试弄红。
func tryExec(t *testing.T, c *ProtocolDriver, q string) string {
	t.Helper()
	if _, err := c.Execute(context.Background(), q); err != nil {
		return "ERR: " + err.Error()
	}
	return "OK"
}

// scalar 取单值（走驱动自己的 Execute 路径，顺带钉住这条读法在 PG 上通得过）。
func scalar(t *testing.T, c *ProtocolDriver, q string) string {
	t.Helper()
	res, err := c.Execute(context.Background(), q)
	if err != nil {
		return "ERR: " + err.Error()
	}
	return cell(res)
}

// scalarIn 在事务内取单值——用于证明「事务内的 DDL 对本事务可见、对连接池不可见」。
func scalarIn(t *testing.T, c *ProtocolDriver, tx dbcore.SQLTx, q string) string {
	t.Helper()
	res, err := c.ExecuteIn(tx, context.Background(), q)
	if err != nil {
		return "ERR: " + err.Error()
	}
	return cell(res)
}

func cell(res *protocol.QueryResult) string {
	if res == nil || len(res.Rows) == 0 {
		return "<no rows>"
	}
	raw, err := res.Rows[0][0].MarshalJSON()
	if err != nil {
		return "ERR: " + err.Error()
	}
	return strings.Trim(string(raw), `"`)
}

// countObjects 在探测 schema 里按名字数对象，用来把「挡住 vs 静默改掉」两条分支都判得开。
func countObjects(t *testing.T, c *ProtocolDriver, names ...string) string {
	t.Helper()
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, "'"+n+"'")
	}
	return scalar(t, c, "SELECT count(*) FROM pg_class cl"+
		" JOIN pg_namespace n ON n.oid = cl.relnamespace"+
		" WHERE n.nspname = '"+probeSchema+"' AND cl.relname IN ("+strings.Join(quoted, ",")+")")
}

func observe(t *testing.T, key, format string, args ...any) {
	t.Helper()
	t.Logf("OBS["+key+"] "+format, args...)
}

// errOrNone 把 tryExec 的结果压成一句可 grep 的真值。
func okErr(s string) string {
	if strings.HasPrefix(s, "ERR") {
		return s
	}
	return "OK"
}

// P1（驱动事实 + 判据）：DDL 能否进事务并整体回滚。
// 要钉进口径：Script.DdlInTx（现在在 ddl.go:851 / ddl.rs:892 硬编码 true，只被 SQLite 的取证背书过）。
// 判据：事务内建的表，事务内可见；回滚后连接池视角必须彻底消失——
// 只要「回滚后还在」，PG 就不能走单事务分派，DdlInTx 得按方言给 false。
func TestProbeDDLInTransactionRollback(t *testing.T) {
	c := probeConn(t)
	ctx := context.Background()

	tx, err := c.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		t.Fatalf("Begin：%v", err)
	}
	// 注意：事务路径只能是 Begin + ExecuteIn。Execute 与 Begin 都抢 c.mu，
	// 而裸 `BEGIN` 走 Execute 会落在连接池的某一条连接上、把那条连接留在事务态，
	// 后续探测就被污染了——这条是读代码定下的用法约束，不需要引擎验证。
	ddlErr := ""
	if _, err := c.ExecuteIn(tx, ctx, "CREATE TABLE "+probeSchema+".p1 (id bigint)"); err != nil {
		ddlErr = err.Error()
	}
	observe(t, "p1", "ddl_in_tx_err=%s", orNone(ddlErr))

	observe(t, "p1", "visible_inside_tx=%s", scalarIn(t, c, tx,
		"SELECT count(*) FROM pg_class cl JOIN pg_namespace n ON n.oid=cl.relnamespace"+
			" WHERE n.nspname='"+probeSchema+"' AND cl.relname='p1'"))

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback：%v", err)
	}
	after := countObjects(t, c, "p1")
	observe(t, "p1", "ddl_in_tx=%v（回滚后残留=%s）", after == "0", after)
	if after != "0" {
		t.Errorf("DDL 未随事务整体回滚（残留 %s 个对象）⇒ PG 不能声明 DdlInTx=true，"+
			"规则层需按方言落能力位，见 A.26 P1", after)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// P2（引擎事实）：DROP TABLE 被依赖视图挡住，还是静默留下破视图？
// 对照 SQLite 的 G13（E6.0 实测：SQLite 的 DROP 不挡，视图留成坏的，查询期才炸）。
// 要钉进口径：DDL 生成器删表前是否必须先发依赖对象（A.20 B 档的整组语义）。
// 「挡 / 不挡」两条分支都是可用真值，各自决定生成器的一条不同规则，所以本条只记不赌；
// 赌的是下限：CASCADE 这条分支必须能把依赖对象一起带走。
func TestProbeDropTableBlockedByDependentView(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p2 (id bigint, qty bigint NOT NULL)",
		"CREATE VIEW "+probeSchema+".p2_v AS SELECT id, qty FROM "+probeSchema+".p2",
	)

	drop := tryExec(t, c, "DROP TABLE "+probeSchema+".p2")
	observe(t, "p2", "drop_table_without_cascade=%s blocked=%v", okErr(drop), strings.HasPrefix(drop, "ERR"))
	observe(t, "p2", "objects_after_plain_drop=%s", countObjects(t, c, "p2", "p2_v"))

	cascade := tryExec(t, c, "DROP TABLE IF EXISTS "+probeSchema+".p2 CASCADE")
	observe(t, "p2", "drop_cascade=%s", okErr(cascade))
	gone := countObjects(t, c, "p2", "p2_v")
	observe(t, "p2", "objects_after_cascade=%s", gone)
	if gone != "0" {
		t.Errorf("DROP TABLE ... CASCADE 后仍有 %s 个对象残留 ⇒ 依赖目录口径不可信，见 A.26 P2", gone)
	}
}

// P3（引擎事实）：ALTER TABLE ... DROP COLUMN 被视图依赖挡住吗？
// 要钉进口径：drop_column 条目在 PG 上是否需要整组重建（SQLite 侧靠重建表绕过）。
func TestProbeDropColumnBlockedByView(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p3 (id bigint, qty bigint NOT NULL, note text)",
		"CREATE VIEW "+probeSchema+".p3_v AS SELECT id, qty FROM "+probeSchema+".p3",
	)

	dropCol := tryExec(t, c, "ALTER TABLE "+probeSchema+".p3 DROP COLUMN qty")
	observe(t, "p3", "drop_referenced_column=%s", okErr(dropCol))

	// 对照：删一个视图没引用的列，必须成功——否则上面的「挡住」是环境问题而不是依赖判定。
	dropOther := tryExec(t, c, "ALTER TABLE "+probeSchema+".p3 DROP COLUMN note")
	observe(t, "p3", "drop_unreferenced_column=%s", okErr(dropOther))

	cols, err := c.ListColumns(context.Background(), probeSchema, "p3")
	if err != nil {
		t.Fatalf("ListColumns：%v", err)
	}
	observeCols(t, "p3", cols)
	names := map[string]bool{}
	for _, col := range cols {
		names[col.Name] = true
	}
	// 形状不变式：报错的那列还在，成功删掉的那列必须从回读里消失。
	if strings.HasPrefix(dropCol, "ERR") && !names["qty"] {
		t.Errorf("DROP COLUMN 已报错但 qty 不在回读里 ⇒ 目录与错误码口径矛盾，见 A.26 P3")
	}
	if !strings.HasPrefix(dropOther, "ERR") && names["note"] {
		t.Errorf("DROP COLUMN note 成功但 note 仍在回读里 ⇒ 读侧口径有问题，见 A.26 P3")
	}
	if strings.HasPrefix(dropOther, "ERR") {
		t.Errorf("删无人引用的列也失败（%s）⇒ 对照失效，上面的「挡住」不可信，见 A.26 P3", dropOther)
	}
}

func observeCols(t *testing.T, key string, cols []protocol.ColumnInfo) {
	t.Helper()
	for _, col := range cols {
		t.Logf("OBS["+key+"] col %s type=%q nullable=%v default=%v auto=%v pk=%v",
			col.Name, col.DataType, col.Nullable, deref(col.DefaultValue), col.IsAutoIncrement, col.IsPrimaryKey)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// P4（引擎事实）：ALTER TABLE ... RENAME 会不会重解析引用它的视图？
// 对照 SQLite 的 G13（E6.0 实测：会，且因原表已 DROP 而直接在 RENAME 那步失败）。
// 要钉进口径：整表重建（CREATE tmp → INSERT SELECT → DROP → RENAME）在 PG 上能不能走。
func TestProbeRenameReparsesViews(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p4 (id bigint, qty bigint NOT NULL)",
		"INSERT INTO "+probeSchema+".p4 SELECT g, g*10 FROM generate_series(1,3) g",
		"CREATE VIEW "+probeSchema+".p4_v AS SELECT id, qty FROM "+probeSchema+".p4",
	)

	rename := tryExec(t, c, "ALTER TABLE "+probeSchema+".p4 RENAME TO p4_new")
	observe(t, "p4", "rename_with_dependent_view=%s", okErr(rename))

	// 关键判据：改名后视图还能不能查出数据（SQLite 在此处留破视图；PG 若按 OID 绑定则应照旧）。
	observe(t, "p4", "view_still_queryable_after_rename=%s",
		scalar(t, c, "SELECT count(*) FROM "+probeSchema+".p4_v"))
	observe(t, "p4", "viewdef=%s",
		scalar(t, c, "SELECT pg_get_viewdef('"+probeSchema+".p4_v'::regclass, true)"))

	if strings.HasPrefix(rename, "ERR") {
		t.Logf("RENAME 被依赖视图挡住 ⇒ PG 上整表重建不能靠 RENAME 收口（A.26 P4 记为挡住）")
		return
	}
	// 形状不变式：改名后旧名消失、新名存在（这是 RENAME 语义本身，不涉引擎脾气）。
	if old := countObjects(t, c, "p4"); old != "0" {
		t.Errorf("RENAME 成功后旧名 p4 仍可查到 %s 次，见 A.26 P4", old)
	}
	if now := countObjects(t, c, "p4_new"); now != "1" {
		t.Errorf("RENAME 成功后新名 p4_new 回读 %s（应为 1），见 A.26 P4", now)
	}
}

// P5（引擎事实）：ALTER COLUMN ... TYPE 是否需要 USING、是否重写、视图是否挡路。
// 要钉进口径：类型变更条目在 PG 上是直改语句还是必须整表重建。
func TestProbeAlterColumnType(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p5 (id bigint, qty bigint NOT NULL)",
		"INSERT INTO "+probeSchema+".p5 SELECT 1, 100",
	)

	observe(t, "p5", "int8_to_text_without_using=%s",
		okErr(tryExec(t, c, "ALTER TABLE "+probeSchema+".p5 ALTER COLUMN qty TYPE text")))
	withUsing := tryExec(t, c, "ALTER TABLE "+probeSchema+".p5 ALTER COLUMN qty TYPE text USING qty::text")
	observe(t, "p5", "int8_to_text_with_using=%s", okErr(withUsing))

	cols, err := c.ListColumns(context.Background(), probeSchema, "p5")
	if err != nil {
		t.Fatalf("ListColumns：%v", err)
	}
	observeCols(t, "p5", cols)
	if !strings.HasPrefix(withUsing, "ERR") {
		// 形状不变式：直改成功后驱动必须读到新类型，否则 diff 会以为「没变」。
		for _, col := range cols {
			if col.Name == "qty" && !strings.Contains(strings.ToLower(col.DataType), "text") {
				t.Errorf("ALTER TYPE 后回读仍是 %q ⇒ pg 驱动的类型回读口径不可信，见 A.26 P5", col.DataType)
			}
		}
	}

	// 视图引用该列时 ALTER TYPE 挡不挡？（决定「改类型 + 有依赖视图」要不要整组处理）
	mustExec(t, c, "CREATE VIEW "+probeSchema+".p5_v AS SELECT qty FROM "+probeSchema+".p5")
	observe(t, "p5", "alter_type_with_view=%s",
		okErr(tryExec(t, c, "ALTER TABLE "+probeSchema+".p5 ALTER COLUMN qty TYPE varchar(32)")))
}

// P6（引擎事实）：非空表上 ADD COLUMN NOT NULL DEFAULT 是否即时（不重写、不逐行回填）。
// 要钉进口径：SQLite 侧「NOT NULL 无默认值 ⇒ 整表重建」这条规则在 PG 上是否也需要。
// 判据用目录标志位 atthasmissing，而不是量时间——计时在 CI 上不可信。
func TestProbeAddColumnNotNullDefault(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p6 (id bigint)",
		"INSERT INTO "+probeSchema+".p6 SELECT g FROM generate_series(1,3) g",
	)

	add := tryExec(t, c, "ALTER TABLE "+probeSchema+".p6 ADD COLUMN status text NOT NULL DEFAULT 'new'")
	observe(t, "p6", "add_not_null_default_on_nonempty=%s", okErr(add))

	observe(t, "p6", "atthasmissing=%s（true=默认值记在目录里、未逐行回填 ⇒ PG 无需整表重建）",
		scalar(t, c, "SELECT atthasmissing FROM pg_attribute WHERE attrelid="+
			"'"+probeSchema+".p6'::regclass AND attname='status'"))

	reads := scalar(t, c, "SELECT count(*) FROM "+probeSchema+".p6 WHERE status='new'")
	observe(t, "p6", "existing_rows_read_default=%s", reads)
	if !strings.HasPrefix(add, "ERR") && reads != "3" {
		t.Errorf("ADD COLUMN 成功但 %s 行读到默认值（应为 3）⇒ 默认值语义不成立，见 A.26 P6", reads)
	}

	// 对照：非空表上加 NOT NULL 且**无默认值**——这条决定 add_column 是否要拆两支。
	observe(t, "p6", "add_not_null_without_default=%s",
		okErr(tryExec(t, c, "ALTER TABLE "+probeSchema+".p6 ADD COLUMN tag text NOT NULL")))
}

// P7（驱动事实，两端）：identity / serial 的自增回读。
// 对照 SQLite：E3.0 取证 sqlite 侧恒 false（驱动填不出），PG 侧是否不同？
// 要钉进口径：add_column / alter_column 是否把自增列判成「必须重建」。
func TestProbeAutoIncrementReadback(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p7 (id bigint GENERATED BY DEFAULT AS IDENTITY, legacy serial, n bigint)",
	)

	cols, err := c.ListColumns(context.Background(), probeSchema, "p7")
	if err != nil {
		t.Fatalf("ListColumns：%v", err)
	}
	observeCols(t, "p7", cols)
	auto := map[string]bool{}
	for _, col := range cols {
		auto[col.Name] = col.IsAutoIncrement
	}
	// 判据：identity（attidentity）必须被读成自增；读不出来就是驱动缺陷，红得有道理。
	if !auto["id"] {
		t.Errorf("IDENTITY 列 id 回读 IsAutoIncrement=false ⇒ pg 驱动读侧漏了 attidentity，见 A.26 P7")
	}
	// 观测（不赌）：serial 走 nextval 默认值而非 attidentity，驱动当前认不认它。
	observe(t, "p7", "identity_auto=%v serial_auto=%v", auto["id"], auto["legacy"])
	observe(t, "p7", "serial_default_expr=%s",
		scalar(t, c, "SELECT coalesce(pg_get_expr(d.adbin,d.adrelid),'<none>') FROM pg_attrdef d"+
			" JOIN pg_class cl ON cl.oid=d.adrelid"+
			" JOIN pg_namespace n ON n.oid=cl.relnamespace"+
			" WHERE n.nspname='"+probeSchema+"' AND cl.relname='p7' AND d.adnum=2"))
}

// P8（驱动事实，两端）：唯一约束/主键的自动索引名字形状。
// 对照 SQLite：E3.0 取证的 `sqlite_autoindex_*`（名字由引擎生成、不含列名语义）。
// 要钉进口径：diff 用索引名当身份，PG 的名字稳定性直接决定 drop_index/create_index 的判定。
func TestProbeConstraintIndexNames(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p8 (id bigint PRIMARY KEY, code text NOT NULL UNIQUE)",
	)

	idx, err := c.ListIndexes(context.Background(), probeSchema, "p8")
	if err != nil {
		t.Fatalf("ListIndexes：%v", err)
	}
	for _, ix := range idx {
		t.Logf("OBS[p8] index name=%q unique=%v primary=%v cols=%v", ix.Name, ix.Unique, ix.Primary, indexColNames(ix.Columns))
	}
	// 形状不变式：主键与唯一约束各出一条索引，名字非空且互不相同（名字为空 diff 就点不中）。
	if len(idx) != 2 {
		t.Errorf("主键 + 唯一约束应回读到 2 条索引，实得 %d ⇒ pg 索引读侧口径有问题，见 A.26 P8", len(idx))
	}
	seen := map[string]bool{}
	for _, ix := range idx {
		if ix.Name == "" {
			t.Errorf("回读到无名索引，diff 的身份键会失效，见 A.26 P8")
		}
		if seen[ix.Name] {
			t.Errorf("索引名重复 %q，见 A.26 P8", ix.Name)
		}
		seen[ix.Name] = true
	}
	// 观测：同一批名字在 pg_constraint 里也叫这个吗（约束与索引同名 ⇒ 规则层可以共用身份键）。
	observe(t, "p8", "pg_constraint_conname=%s",
		scalar(t, c, "SELECT coalesce(string_agg(conname::text, ',' order by conname),'<none>') FROM pg_constraint"+
			" WHERE conrelid='"+probeSchema+".p8'::regclass"))
}

func indexColNames(cols []protocol.IndexColumn) []string {
	out := make([]string, 0, len(cols))
	for _, x := range cols {
		out = append(out, x.Name)
	}
	return out
}

// P9（驱动事实，两端）：外键 on_update / on_delete 回读（docs/plan-compare-sync.md §8-7 的债）。
// §8-7 定案是「随有实例环境一起补」，理由正是盲写=数字上已实现、实际未验证。
// 本条同时探两头：引擎目录给不给得出、我们驱动读不读——缺口在哪一头，处置完全不同。
func TestProbeForeignKeyActionsReadback(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p9_parent (id bigint PRIMARY KEY)",
		"CREATE TABLE "+probeSchema+".p9_child (id bigint PRIMARY KEY, parent_id bigint"+
			" REFERENCES "+probeSchema+".p9_parent(id) ON UPDATE CASCADE ON DELETE SET NULL)",
	)

	fks, err := c.ListForeignKeys(context.Background(), probeSchema, "p9_child")
	if err != nil {
		t.Fatalf("ListForeignKeys：%v", err)
	}
	for _, fk := range fks {
		t.Logf("OBS[p9] fk name=%q cols=%v ref=%s.%s refcols=%v on_update=%q on_delete=%q",
			fk.Name, fk.Columns, fk.ReferencedSchema, fk.ReferencedTable, fk.ReferencedColumns, fk.OnUpdate, fk.OnDelete)
	}
	// 形状不变式：外键本体必须读到（名字、列、被引用表），否则 diff 的关系项全瞎。
	if len(fks) != 1 {
		t.Fatalf("应回读到 1 条外键，实得 %d ⇒ pg 外键读侧有问题，见 A.26 P9", len(fks))
	}
	if fks[0].ReferencedTable != "p9_parent" || len(fks[0].Columns) != 1 {
		t.Errorf("外键回读形状不符：%+v，见 A.26 P9", fks[0])
	}

	// 引擎那头：目录里到底有没有？有而读不出 = 驱动缺陷；没有 = 方言不支持，规则层要另想口径。
	observe(t, "p9", "pg_action_codes(confupdtype/confdeltype)=%s driver_on_update=%q driver_on_delete=%q",
		scalar(t, c, "SELECT confupdtype::text||'/'||confdeltype::text FROM pg_constraint"+
			" WHERE conrelid='"+probeSchema+".p9_child'::regclass AND contype='f'"),
		fks[0].OnUpdate, fks[0].OnDelete)
}

// P10（驱动事实，两端）：format_type 的文本形状。
// 这是 §8-3「generic_type 归一命名表」的输入——那张表一直没落笔，等的就是可实测的方言实例。
// 本条不猜映射，只把驱动实际交给规则层的原始文本钉下来。
func TestProbeFormatTypeShapes(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p10 ("+
			"a bigint, b integer, c text, d varchar(20), e char(3), f numeric(10,2), g boolean,"+
			" h timestamptz, i date, j jsonb, k bytea, l text[], m uuid, n inet)",
	)

	cols, err := c.ListColumns(context.Background(), probeSchema, "p10")
	if err != nil {
		t.Fatalf("ListColumns：%v", err)
	}
	observeCols(t, "p10", cols)
	// 形状不变式：14 列全读到、类型串非空、OrdinalPosition 与回读序一致（规则层靠它对齐列序）。
	if len(cols) != 14 {
		t.Errorf("应回读到 14 列，实得 %d，见 A.26 P10", len(cols))
	}
	for i, col := range cols {
		if col.DataType == "" {
			t.Errorf("列 %s 的 DataType 为空，见 A.26 P10", col.Name)
		}
		if int(col.OrdinalPosition) != i+1 {
			t.Errorf("列 %s 的 OrdinalPosition=%d 与回读序 %d 不符，见 A.26 P10", col.Name, col.OrdinalPosition, i+1)
		}
	}
	// 观测：长度/精度修饰落在 DataType 文本里，还是落在 MaxLength/Precision/Scale（现在驱动没填后者）。
	for _, col := range cols {
		if col.Name == "d" || col.Name == "f" {
			observe(t, "p10", "%s DataType=%q MaxLength=%s Precision=%s Scale=%s",
				col.Name, col.DataType, ptr32(col.MaxLength), ptr32(col.Precision), ptr32(col.Scale))
		}
	}
}

func ptr32(v *int32) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *v)
}

// P11（驱动事实，两端）：ListTables 是否真的尊重 schema 入参。
// 对照 SQLite：驱动忽略入参（A.9 F6 / §8-6，两文件两连接才勉强可用）。
// 要钉进口径：同库多 schema 的比对（§8-6 的 v1.1）在 PG 上是否可行。
func TestProbeListTablesRespectsSchema(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p11_marker (id bigint)",
		"CREATE VIEW "+probeSchema+".p11_marker_v AS SELECT id FROM "+probeSchema+".p11_marker",
	)

	inProbe, err := c.ListTables(context.Background(), probeSchema)
	if err != nil {
		t.Fatalf("ListTables(探测 schema)：%v", err)
	}
	inPublic, err := c.ListTables(context.Background(), "public")
	if err != nil {
		t.Fatalf("ListTables(public)：%v", err)
	}
	for _, tb := range inProbe {
		t.Logf("OBS[p11] probe_schema entry name=%q type=%q", tb.Name, tb.Type)
	}
	observe(t, "p11", "public_entries=%d probe_entries=%d", len(inPublic), len(inProbe))

	// 判据（两头都成立才算「尊重入参」）：探测 schema 里查得到，public 里查不到。
	if !containsTable(inProbe, "p11_marker") {
		t.Errorf("ListTables(%s) 未返回 p11_marker ⇒ 驱动没按 schema 过滤，见 A.26 P11", probeSchema)
	}
	if containsTable(inPublic, "p11_marker") {
		t.Errorf("ListTables(public) 混进了 %s 的对象 ⇒ 跨 schema 泄漏，§8-6 同库多 schema 不可行，见 A.26 P11", probeSchema)
	}
	// 观测：视图是否带出非 table 的 Type（规则层靠这个把视图只当名字比对）。
	viewType := "<absent>"
	for _, tb := range inProbe {
		if tb.Name == "p11_marker_v" {
			viewType = string(tb.Type)
		}
	}
	observe(t, "p11", "view_type=%q", viewType)
}

func containsTable(list []protocol.TableInfo, name string) bool {
	for _, x := range list {
		if x.Name == name {
			return true
		}
	}
	return false
}

// P12（引擎事实）：pg_depend 能否枚举「哪些视图引用了这张表 / 这一列」。
// 要钉进口径：A.20 的 B 档（删表/改列前先给出依赖对象清单，让用户决定整组）。
// 表级与列级都要探到——只有表级就够不着「DROP COLUMN 被谁挡住」那条规则。
func TestProbeDependencyCatalog(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c,
		"CREATE TABLE "+probeSchema+".p12 (id bigint, qty bigint NOT NULL, note text)",
		"CREATE VIEW "+probeSchema+".p12_v_qty AS SELECT id, qty FROM "+probeSchema+".p12",
		"CREATE VIEW "+probeSchema+".p12_v_id AS SELECT id FROM "+probeSchema+".p12",
		"CREATE MATERIALIZED VIEW "+probeSchema+".p12_mv AS SELECT qty FROM "+probeSchema+".p12",
	)

	// 表级：哪些视图/物化视图依赖这张表。
	tableLevel := scalar(t, c,
		"SELECT coalesce(string_agg(dep.relname, ',' order by dep.relname),'<none>')"+
			" FROM pg_depend d"+
			" JOIN pg_rewrite rw ON rw.oid = d.objid"+
			" JOIN pg_class dep ON dep.oid = rw.ev_class"+
			" WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass"+
			"   AND d.refobjid = '"+probeSchema+".p12'::regclass"+
			"   AND dep.relkind IN ('v','m') AND dep.oid <> d.refobjid")
	observe(t, "p12", "table_level_dependents=%s", tableLevel)
	if strings.HasPrefix(tableLevel, "ERR") || tableLevel == "<none>" {
		t.Errorf("表级依赖枚举不可用（%s）⇒ A.20 B 档在 PG 上没有目录支撑，见 A.26 P12", tableLevel)
	}

	// 列级：refobjsubid 指到具体列，才能把「DROP COLUMN 被谁挡住」讲清楚。
	colLevel := scalar(t, c,
		"SELECT coalesce(string_agg(dep.relname || '.' || att.attname, ',' order by dep.relname),'<none>')"+
			" FROM pg_depend d"+
			" JOIN pg_rewrite rw ON rw.oid = d.objid"+
			" JOIN pg_class dep ON dep.oid = rw.ev_class"+
			" JOIN pg_attribute att ON att.attrelid = d.refobjid AND att.attnum = d.refobjsubid"+
			" WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass"+
			"   AND d.refobjid = '"+probeSchema+".p12'::regclass AND d.refobjsubid > 0"+
			"   AND dep.relkind IN ('v','m')")
	observe(t, "p12", "column_level_dependents=%s", colLevel)
	if strings.HasPrefix(colLevel, "ERR") {
		t.Fatalf("列级依赖查询失败：%s", colLevel)
	}

	// 判据：qty 被两个对象引用（普通视图 + 物化视图），note 无人引用。
	if !strings.Contains(colLevel, "p12_mv.qty") {
		t.Errorf("列级依赖漏掉物化视图对 qty 的引用（实得 %s）⇒ 清单不足以支撑整组勾选，见 A.26 P12", colLevel)
	}
	if strings.Contains(colLevel, "note") {
		t.Errorf("无人引用的 note 出现在依赖清单里（实得 %s）⇒ 清单口径过宽，见 A.26 P12", colLevel)
	}
}
