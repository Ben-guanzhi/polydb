package dbmysql

// F0.4 MySQL 方言取证（Go 侧），照 A.26/F0.1 的形态：用真引擎把「规则层要钉进方言口径的事实」
// 一条条探出来，结论回填 docs/plan-compare-sync.md A.27。
// 本文件**不改规则层、不改 spec**——拿不到凭据时它只会软跳过，拿到后一次跑完出真值。
//
// 与 PG 批的两点形态差异（都是实情的披露，不是偷懒）：
//  1. **只探 Go 单侧**。PG 批的分档是"引擎事实只探一次、驱动事实两端各探"；MySQL 这台引擎在 WSL 里，
//     Windows 侧连不到（WSL2 NAT），而 WSL 内没有 cargo/rustc，Rust 侧交叉编译需要目标平台链接器
//     ⇒ 驱动事实只能先在 Go 侧取值，Rust 侧的差异**由读代码定位到 file:line**（见 A.27 的漂移表），
//     等 Windows 上有可直连的 MySQL 时补双端对拍。
//  2. **专用库是预建的两个**（polydb_probe / polydb_probe_b），探测账号只拿到这两个库的权限，
//     所以本文件不会 CREATE/DROP DATABASE；清理现场靠"每点自己的 m<N>_ 前缀对象 + 开头 DROP IF EXISTS"。
//     默认 DSN 也刻意**不**沿用 test/contract 的 `mysql://root@localhost:3306/mysql`——
//     那会把探测对象建进 `mysql` 系统库。
//
// 断言只打在两类东西上：① 自身层的形状不变式（目录说没了就不能还在、改完类型必须读得回新形状）；
// ② 该点要钉的判据本身（M1 的回滚留存、M11 的两头、M13 的"无依赖目录"）。
// 纯两可的分支（DROP TABLE 挡不挡、MODIFY 挡不挡、INSTANT 收不收）一律只记不赌。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

const (
	// probeDB 主探测库；probeDBAlt 只服务 M11 的"两头判据"（同库多 schema 在 MySQL 里=多库）。
	probeDB    = "polydb_probe"
	probeDBAlt = "polydb_probe_b"
	// defaultDSN 用经典形态（user@tcp(host:port)/db），不用 `mysql://…` URI 形态——
	// 实测 URI 形态会把发过去的用户名解析成 scheme 名（'mysql'），密码给对也连不上。
	// 也刻意不沿用 test/contract 的 `mysql://root@localhost:3306/mysql`：那会把探测对象建进 `mysql` 系统库。
	defaultDSN = "polydb_probe@tcp(127.0.0.1:3306)/polydb_probe"
	sysSchema  = "information_schema"
)

func probeDSN() string {
	if dsn := os.Getenv("POLYDB_TEST_MYSQL"); dsn != "" {
		return dsn
	}
	return defaultDSN
}

// probeConn 取一个可用的 MySQL 连接。连不上即 connected:false 软跳过（与契约测试同一口径）。
func probeConn(t *testing.T) *Conn {
	t.Helper()
	dsn := probeDSN()
	c, err := Open(context.Background(), dsn)
	if err != nil {
		t.Skipf("connected:false 无可用 MySQL（POLYDB_TEST_MYSQL=%s）：%v", dsn, err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// 前置条件不是"能不能连"，而是"两个预建探测库在不在权限范围内"——
	// 探测账号刻意只拿到这两库、不给 CREATE DATABASE，所以缺库要现读出来。
	// 这里必须用 Skipf 而不是 Fatalf：引擎在、只是现场没搭齐，报红会把"缺凭据"和"缺引擎"混成一类。
	visible := scalar(t, c, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.SCHEMATA WHERE schema_name IN ('%s','%s')",
		sysSchema, probeDB, probeDBAlt))
	if visible != "2" {
		t.Skipf("connected:false 探测库不齐（需要 %s 与 %s 两库且账号可见，当前读到 %s；POLYDB_TEST_MYSQL=%s）",
			probeDB, probeDBAlt, visible, dsn)
	}
	observe(t, "server", "VERSION()=%s sql_mode=%s",
		scalar(t, c, "SELECT VERSION()"), scalar(t, c, "SELECT @@sql_mode"))
	return c
}

// ─── 小工具 ────────────────────────────────────────────────

func mustExec(t *testing.T, c *Conn, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := c.Execute(context.Background(), q); err != nil {
			t.Fatalf("前置语句失败 %s：%v", q, err)
		}
	}
}

func mustExecIn(t *testing.T, c *Conn, tx dbcore.SQLTx, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := c.ExecuteIn(tx, context.Background(), q); err != nil {
			t.Fatalf("事务内前置语句失败 %s：%v", q, err)
		}
	}
}

// tryExec 跑一条**被测**语句：失败不当红，把错误原文记进观测。
func tryExec(t *testing.T, c *Conn, q string) string {
	t.Helper()
	if _, err := c.Execute(context.Background(), q); err != nil {
		return "ERR: " + err.Error()
	}
	return "OK"
}

func scalar(t *testing.T, c *Conn, q string) string {
	t.Helper()
	res, err := c.Execute(context.Background(), q)
	if err != nil {
		return "ERR: " + err.Error()
	}
	return cell(res)
}

func scalarIn(t *testing.T, c *Conn, tx dbcore.SQLTx, q string) string {
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
		return "<marshal: " + err.Error() + ">"
	}
	return strings.Trim(string(raw), `"`)
}

// countTables 数某个库里叫这些名字的在册对象（information_schema 视角，不看权限也不看引擎脸色）。
func countTables(t *testing.T, c *Conn, schema string, names ...string) string {
	t.Helper()
	q := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.TABLES WHERE table_schema='%s' AND table_name IN (%s)",
		sysSchema, schema, quoteList(names))
	return scalar(t, c, q)
}

// countTablesOfType 数某个库里指定 table_type 的对象数（M13 用来钉"MySQL 没有物化视图"）。
func countTablesOfType(t *testing.T, c *Conn, schema, tableType string) string {
	t.Helper()
	q := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.TABLES WHERE table_schema='%s' AND table_type='%s'",
		sysSchema, schema, tableType)
	return scalar(t, c, q)
}

// countObjectsInTx 与 countTables 同一条查询，但走事务执行器——M1 要的就是"另一个视角看不看得到"。
func countObjectsInTx(t *testing.T, c *Conn, tx dbcore.SQLTx, names ...string) string {
	t.Helper()
	q := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.TABLES WHERE table_schema='%s' AND table_name IN (%s)",
		sysSchema, probeDB, quoteList(names))
	return scalarIn(t, c, tx, q)
}

func quoteList(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = "'" + n + "'"
	}
	return strings.Join(parts, ",")
}

func observe(t *testing.T, key, format string, args ...any) {
	t.Helper()
	t.Logf("OBS["+key+"] "+format, args...)
}

// colLine 把一份 ColumnInfo 打成一行观测文本，专供看"驱动填没填"。
func colLine(col protocol.ColumnInfo) string {
	return fmt.Sprintf("%s | data_type=%s | generic=%q | nullable=%v | default=%s | maxLen=%s prec=%s scale=%s | pk=%v ai=%v | comment=%q | pos=%d",
		col.Name, col.DataType, string(col.GenericType), col.Nullable, orNone(col.DefaultValue),
		ptrText(col.MaxLength), ptrText(col.Precision), ptrText(col.Scale),
		col.IsPrimaryKey, col.IsAutoIncrement, col.Comment, col.OrdinalPosition)
}

func indexLine(idx protocol.IndexInfo) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = fmt.Sprintf("%s#%d", c.Name, c.Position)
	}
	return fmt.Sprintf("%s | unique=%v primary=%v type=%q cols=[%s]",
		idx.Name, idx.Unique, idx.Primary, string(idx.Type), strings.Join(cols, ","))
}

func fkLine(fk protocol.ForeignKeyInfo) string {
	return fmt.Sprintf("%s | cols=[%s] ref=%s.%s[%s] on_update=%q on_delete=%q",
		fk.Name, strings.Join(fk.Columns, ","), fk.ReferencedSchema, fk.ReferencedTable,
		strings.Join(fk.ReferencedColumns, ","), string(fk.OnUpdate), string(fk.OnDelete))
}

func orNone(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return `"` + *s + `"`
}

func ptrText(v *int32) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *v)
}

func tableNames(tables []protocol.TableInfo) map[string]protocol.TableType {
	out := make(map[string]protocol.TableType, len(tables))
	for _, tb := range tables {
		out[tb.Name] = tb.Type
	}
	return out
}

// ─── M1 DDL 隐式提交（本批头牌：ddl_in_tx=false 的第一支真值）─────

// TestProbeDDLImplicitCommit 探的是 §7 ① 里那句"SQLite 结构上产不出 ddl_in_tx=false"。
// MySQL 的 DDL 隐式提交是文档口径，但文档不是证据：判据本体是"回滚之后表还在不在"。
// 红了（表没了）意味着能力位该写 true、且 A.16 的"逐条执行"分派在 MySQL 上依然用不上——
// 那才是需要回去改文档的情形，不是噪声。
func TestProbeDDLImplicitCommit(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m1_kept")

	tx, err := c.Begin(context.Background(), dbcore.TxMode{})
	if err != nil {
		t.Fatalf("Begin 失败：%v", err)
	}
	// 零 TxMode 在 Go 侧不发隔离级别语句（dbcore/tx.go:25 造的是空 TxOptions），
	// 而 Rust 侧 begin_tx 对四种隔离级别**全部**返回 Some（db-mysql/src/lib.rs:138-151），
	// 恒发 `BEGIN ISOLATION LEVEL READ COMMITTED`。这里把 Go 侧真值取出来，供 A.27 的漂移表引用。
	observe(t, "m1.tx_isolation", "零 TxMode 的事务内 @@transaction_isolation=%s",
		scalarIn(t, c, tx, "SELECT @@transaction_isolation"))

	mustExecIn(t, c, tx,
		"CREATE TABLE m1_kept (id INT PRIMARY KEY, v VARCHAR(8))",
		"INSERT INTO m1_kept (id, v) VALUES (1, 'aa')")
	observe(t, "m1.in_tx", "事务内 表在册=%s 行=%s",
		countObjectsInTx(t, c, tx, "m1_kept"), scalarIn(t, c, tx, "SELECT COUNT(*) FROM m1_kept"))

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback 失败：%v", err)
	}
	after := countTables(t, c, probeDB, "m1_kept")
	observe(t, "m1.after_rollback", "回滚后 表在册=%s 行=%s（DDL 隐式提交 ⇒ 结构留存、事务前的 DML 也一起提交）",
		after, scalar(t, c, "SELECT COUNT(*) FROM m1_kept"))

	if after != "1" {
		t.Fatalf("M1 判据：MySQL 探得 DDL 可被回滚撤销（表在册=%s）⇒ ddl_in_tx 应为 true，与文档假设相反", after)
	}
	mustExec(t, c, "DROP TABLE IF EXISTS m1_kept")
}

// ─── M2 DROP TABLE 被依赖视图挡住吗 ────────────────────────

// TestProbeViewBlocksDropTable 对照 SQLite 的 G16（静默删表、留下破视图）。
// 挡与不挡各决定一条生成器规则，所以两种结果都只记；断言打在**目录自洽**上：
// 若目录说视图还在，查它就必须报错——目录撒谎比行为两可贵得多。
func TestProbeViewBlocksDropTable(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP VIEW IF EXISTS m2_v", "DROP TABLE IF EXISTS m2_t")
	mustExec(t, c,
		"CREATE TABLE m2_t (id INT PRIMARY KEY, qty INT)",
		"CREATE VIEW m2_v AS SELECT id, qty FROM m2_t")

	observe(t, "m2.drop_table", "DROP TABLE（被视图引用）= %s", tryExec(t, c, "DROP TABLE m2_t"))
	tbl := countTables(t, c, probeDB, "m2_t")
	view := countTables(t, c, probeDB, "m2_v")
	observe(t, "m2.catalog", "删表尝试后目录：表=%s 视图=%s", tbl, view)

	if tbl == "0" && view == "1" {
		sel := tryExec(t, c, "SELECT id FROM m2_v")
		observe(t, "m2.broken_view_select", "表没了、视图仍在目录 ⇒ 查它：%s", sel)
		if !strings.HasPrefix(sel, "ERR") {
			t.Fatalf("M2 目录自洽断言红：视图指向不存在的表却能查出结果（%s）⇒ information_schema 在撒谎", sel)
		}
	}
	mustExec(t, c, "DROP VIEW IF EXISTS m2_v", "DROP TABLE IF EXISTS m2_t")
}

// ─── M3 DROP COLUMN 被依赖视图挡住吗 ──────────────────────

// TestProbeViewBlocksDropColumn 决定 drop_column 在 MySQL 上要不要走整表重建
// （SQLite 靠重建绕过，A.11/A.15 的口径）。对照项是**同表未被引用的列**——
// 那条必须删得掉，否则"挡不挡"这个问法本身不成立（这是判据，不是赌注）。
func TestProbeViewBlocksDropColumn(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP VIEW IF EXISTS m3_v", "DROP TABLE IF EXISTS m3_t")
	mustExec(t, c,
		"CREATE TABLE m3_t (id INT PRIMARY KEY, qty INT, note VARCHAR(16))",
		"CREATE VIEW m3_v AS SELECT id, qty FROM m3_t")

	blocked := tryExec(t, c, "ALTER TABLE m3_t DROP COLUMN qty")
	free := tryExec(t, c, "ALTER TABLE m3_t DROP COLUMN note")
	observe(t, "m3.drop_column", "被视图引用的列=%s | 未被引用的列=%s", blocked, free)

	if free != "OK" {
		t.Fatalf("M3 对照项红：未被视图引用的列也删不掉（%s）⇒ 本点的「挡/不挡」分档不成立，探测设计要改", free)
	}
	cols, err := c.ListColumns(context.Background(), probeDB, "m3_t")
	if err != nil {
		t.Fatalf("ListColumns 回读失败：%v", err)
	}
	observe(t, "m3.readback", "删列后驱动读回 %d 列：%s", len(cols), strings.Join(colNames(cols), ", "))
	mustExec(t, c, "DROP VIEW IF EXISTS m3_v", "DROP TABLE IF EXISTS m3_t")
}

func colNames(cols []protocol.ColumnInfo) []string {
	out := make([]string, len(cols))
	for i, col := range cols {
		out[i] = col.Name
	}
	return out
}

// ─── M4 整表重建四步在 MySQL 上走不走得通 ──────────────────

// TestProbeRebuildFourStepsSurviveViews 直接照 A.15 的四个动作模拟一遍
// （CREATE tmp → INSERT SELECT → DROP 原表 → RENAME tmp 回原名）。
// SQLite 上正是死在最后一步之前的依赖上（G13），所以这条不是演示题：
// 它决定"重建触发条件"能不能按方言放开。每一步的结果都记，断言只打在
// "四步若全报 OK，则原名字必须恢复且行数守恒"这条自洽上。
func TestProbeRebuildFourStepsSurviveViews(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP VIEW IF EXISTS m4_v", "DROP TABLE IF EXISTS m4_tmp", "DROP TABLE IF EXISTS m4_t", "DROP TABLE IF EXISTS m4_t2")
	mustExec(t, c,
		"CREATE TABLE m4_t (id INT PRIMARY KEY, qty INT)",
		"INSERT INTO m4_t (id, qty) VALUES (1, 10), (2, 20)",
		"CREATE VIEW m4_v AS SELECT id, qty FROM m4_t")

	observe(t, "m4.step1_create_like", "%s", tryExec(t, c, "CREATE TABLE m4_tmp (LIKE m4_t)"))
	observe(t, "m4.step2_insert_select", "%s", tryExec(t, c, "INSERT INTO m4_tmp (id, qty) SELECT id, qty FROM m4_t"))
	observe(t, "m4.step3_drop_original", "%s", tryExec(t, c, "DROP TABLE m4_t"))
	observe(t, "m4.step4_rename_back", "%s", tryExec(t, c, "RENAME TABLE m4_tmp TO m4_t"))

	back := countTables(t, c, probeDB, "m4_t")
	if back == "1" {
		rows := scalar(t, c, "SELECT COUNT(*) FROM m4_t")
		observe(t, "m4.rows_after_rebuild", "原名字已恢复，行数=%s", rows)
		if rows != "2" {
			t.Fatalf("M4 自洽断言红：四步都走完了但数据不守恒（期望 2 行，读回 %s）", rows)
		}
	}
	sel := tryExec(t, c, "SELECT id FROM m4_v")
	observe(t, "m4.view_after_rebuild", "重建后查依赖视图 m4_v：%s", sel)

	mustExec(t, c, "DROP VIEW IF EXISTS m4_v", "DROP TABLE IF EXISTS m4_tmp", "DROP TABLE IF EXISTS m4_t", "DROP TABLE IF EXISTS m4_t2")
}

// ─── M5 改列类型：挡不挡、静不静截断 ───────────────────────

// TestProbeAlterColumnTypeNarrowing 有两个赌注之外的硬事实要钉：
// ① 改完**驱动必须读得出新形状**，否则 diff 会判"没变"（这是自身层的不变式）；
// ② 收窄成功但数据被截，规则层就不能把类型收窄当无损动作（destructive 标记的方言依据）。
// 视图挡不挡 MODIFY 只记不赌。
func TestProbeAlterColumnTypeNarrowing(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP VIEW IF EXISTS m5_v", "DROP TABLE IF EXISTS m5_t")
	mustExec(t, c,
		"CREATE TABLE m5_t (id INT PRIMARY KEY, name VARCHAR(10)) ENGINE=InnoDB",
		"INSERT INTO m5_t (id, name) VALUES (1, 'abcdefghij')")

	narrow := tryExec(t, c, "ALTER TABLE m5_t MODIFY name VARCHAR(3)")
	observe(t, "m5.narrow", "VARCHAR(10)→VARCHAR(3)（存 10 字符）= %s", narrow)
	if narrow == "OK" {
		v := scalar(t, c, "SELECT name FROM m5_t")
		observe(t, "m5.value_after_narrow", "存下来的值=%q（长度 %d）", v, len([]rune(v)))
		if len([]rune(v)) > 3 {
			t.Fatalf("M5 目录/实际不一致：列已改到 VARCHAR(3)，却仍读回 %d 字符 ⇒ 读侧或引擎有一边在撒谎", len([]rune(v)))
		}
	}
	cols, err := c.ListColumns(context.Background(), probeDB, "m5_t")
	if err != nil {
		t.Fatalf("ListColumns 回读失败：%v", err)
	}
	observe(t, "m5.readback", "改类型后驱动读回：%s", colLine(findCol(cols, "name")))
	mustExec(t, c, "CREATE VIEW m5_v AS SELECT name FROM m5_t")
	observe(t, "m5.modify_through_view", "有依赖视图时放宽回 VARCHAR(50)：%s",
		tryExec(t, c, "ALTER TABLE m5_t MODIFY name VARCHAR(50)"))

	mustExec(t, c, "DROP VIEW IF EXISTS m5_v", "DROP TABLE IF EXISTS m5_t")
}

func findCol(cols []protocol.ColumnInfo, name string) protocol.ColumnInfo {
	for _, col := range cols {
		if col.Name == name {
			return col
		}
	}
	return protocol.ColumnInfo{Name: "<缺失:" + name + ">"}
}

// ─── M6 非空表 ADD COLUMN NOT NULL DEFAULT 是否即时 ────────

// TestProbeInstantAddColumn 不量时间（PG 批用 atthasmissing，MySQL 的对应判据更硬）：
// 直接把 `ALGORITHM=INSTANT` 写进语句里要——引擎接受即证明"不重建表"，拒绝就报出原因。
// 剩下的赌注一条没有，只钉一条自洽：接受之后既有行必须真的带上了默认值。
func TestProbeInstantAddColumn(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m6_t")
	mustExec(t, c,
		"CREATE TABLE m6_t (id INT PRIMARY KEY, v INT)",
		"INSERT INTO m6_t (id, v) VALUES (1, 1), (2, 2), (3, 3)")

	instant := tryExec(t, c, "ALTER TABLE m6_t ADD COLUMN qty INT NOT NULL DEFAULT 7, ALGORITHM=INSTANT")
	observe(t, "m6.add_with_default", "非空表 ADD NOT NULL DEFAULT + ALGORITHM=INSTANT：%s", instant)
	if instant == "OK" {
		rows := scalar(t, c, "SELECT COUNT(*) FROM m6_t WHERE qty=7")
		if rows != "3" {
			t.Fatalf("M6 自洽断言红：引擎收下 INSTANT 加列，既有行却没带上默认值（qty=7 的行数=%s）", rows)
		}
		observe(t, "m6.rows_carry_default", "既有 3 行全部读到 qty=7")
	}
	observe(t, "m6.add_not_null_no_default", "对照：非空表 ADD NOT NULL 无默认：%s",
		tryExec(t, c, "ALTER TABLE m6_t ADD COLUMN tag INT NOT NULL, ALGORITHM=INSTANT"))
	observe(t, "m6.add_plain", "对照：非空表 ADD 可空列：%s",
		tryExec(t, c, "ALTER TABLE m6_t ADD COLUMN memo VARCHAR(8) NULL"))

	cols, err := c.ListColumns(context.Background(), probeDB, "m6_t")
	if err != nil {
		t.Fatalf("ListColumns 回读失败：%v", err)
	}
	observe(t, "m6.readback", "加列后驱动读回列形状：")
	for _, col := range cols {
		observe(t, "m6.readback.line", "  %s", colLine(col))
	}
	mustExec(t, c, "DROP TABLE IF EXISTS m6_t")
}

// ─── M7 自增回读（AUTO_INCREMENT / SERIAL）────────────────

// TestProbeAutoIncrementReadback 钉的是"is_auto_increment 填不填得出"：
// SQLite 侧恒 false（A.11 G3 实测），判不出自增就会把 id 当普通数据列参与重建，
// 生成器随之写出 INSERT ... (id) VALUES (...) 这种把自增列当数据的语句。
func TestProbeAutoIncrementReadback(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m7_a", "DROP TABLE IF EXISTS m7_b")
	mustExec(t, c,
		"CREATE TABLE m7_a (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, v INT)",
		"CREATE TABLE m7_b (id SERIAL PRIMARY KEY, v INT)")

	for _, name := range []string{"m7_a", "m7_b"} {
		cols, err := c.ListColumns(context.Background(), probeDB, name)
		if err != nil {
			t.Fatalf("ListColumns(%s) 失败：%v", name, err)
		}
		id := findCol(cols, "id")
		observe(t, "m7."+name, "驱动读回：%s", colLine(id))
		extra := scalar(t, c, fmt.Sprintf(
			"SELECT EXTRA FROM %s.COLUMNS WHERE table_schema='%s' AND table_name='%s' AND column_name='id'",
			sysSchema, probeDB, name))
		if !strings.Contains(strings.ToLower(extra), "auto_increment") {
			t.Fatalf("M7 判据红：目录里 EXTRA=%q 不含 auto_increment，但本列是按 AUTO_INCREMENT/SERIAL 建的", extra)
		}
		if !id.IsAutoIncrement {
			t.Fatalf("M7 判据红：目录 EXTRA=%q 而驱动 IsAutoIncrement=false ⇒ 读侧漏填，重建会当普通列", extra)
		}
	}
	observe(t, "m7.serial_shape", "SERIAL 的 COLUMN_TYPE 原文=%s（别名展开成什么，生成器得按这个写）",
		scalar(t, c, fmt.Sprintf(
			"SELECT COLUMN_TYPE FROM %s.COLUMNS WHERE table_schema='%s' AND table_name='m7_b' AND column_name='id'",
			sysSchema, probeDB)))
	mustExec(t, c, "DROP TABLE IF EXISTS m7_a", "DROP TABLE IF EXISTS m7_b")
}

// ─── M8 索引名的形状：跨表撞车 ────────────────────────────

// TestProbeIndexNameCollisionAcrossTables 打在 diff 的身份键口径上（A.5/A.11 拿索引名当身份键，
// SQLite 侧是 sqlite_autoindex_ 带表名、PG 侧由约束名派生）。
// MySQL 的主键索引名**恒为 'PRIMARY'** ⇒ 同名跨表必然撞；身份键要是不带表限定就是错的。
// 这条不预设结果，用两张结构完全相同的表实测交集。
func TestProbeIndexNameCollisionAcrossTables(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m8_a", "DROP TABLE IF EXISTS m8_b")
	mustExec(t, c,
		"CREATE TABLE m8_a (id INT PRIMARY KEY, x INT, UNIQUE (x))",
		"CREATE TABLE m8_b (id INT PRIMARY KEY, x INT, UNIQUE (x))")

	var shared []string
	perTable := map[string][]protocol.IndexInfo{}
	for _, name := range []string{"m8_a", "m8_b"} {
		idx, err := c.ListIndexes(context.Background(), probeDB, name)
		if err != nil {
			t.Fatalf("ListIndexes(%s) 失败：%v", name, err)
		}
		perTable[name] = idx
		observe(t, "m8."+name, "索引共 %d 个", len(idx))
		for _, one := range idx {
			observe(t, "m8."+name+".line", "  %s", indexLine(one))
		}
	}
	for _, one := range perTable["m8_a"] {
		for _, other := range perTable["m8_b"] {
			if one.Name == other.Name {
				shared = append(shared, one.Name)
			}
		}
	}
	observe(t, "m8.cross_table_collision", "两表索引名交集=%v", shared)

	primaryCount := 0
	for _, one := range perTable["m8_a"] {
		if one.Primary {
			primaryCount++
			if one.Name != "PRIMARY" {
				t.Fatalf("M8 判据红：Primary=true 的索引名字不是 'PRIMARY'（实际 %q）⇒ 驱动里 name==\"PRIMARY\" 那条硬判断要改", one.Name)
			}
		}
	}
	if primaryCount != 1 {
		t.Fatalf("M8 形状不变式红：一张表读回 %d 个 Primary 索引（应为 1）", primaryCount)
	}
	mustExec(t, c, "DROP TABLE IF EXISTS m8_a", "DROP TABLE IF EXISTS m8_b")
}

// ─── M9 外键动作回读（§8-7 的债在 MySQL 上同形）───────────

// TestProbeForeignKeyActionsReadback 要钉的是"目录给不给得出"这另一半：
// Go 的 ListForeignKeys 只查 KEY_COLUMN_USAGE（dbmysql.go:269），Rust 同形（lib.rs:506）
// ⇒ 两端的 on_update/on_delete 都恒空，而 MySQL 的 REFERENTIAL_CONSTRAINTS 明摆着有
// UPDATE_RULE/DELETE_RULE。断言打在"目录给得出"上（给不出我才要慌），驱动回读只记。
func TestProbeForeignKeyActionsReadback(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m9_c", "DROP TABLE IF EXISTS m9_p")
	mustExec(t, c,
		"CREATE TABLE m9_p (id INT PRIMARY KEY)",
		"CREATE TABLE m9_c (id INT PRIMARY KEY, p INT, CONSTRAINT m9_fk FOREIGN KEY (p) REFERENCES m9_p(id) ON UPDATE CASCADE ON DELETE SET NULL)")

	catalog := scalar(t, c, fmt.Sprintf(
		"SELECT CONCAT(UPDATE_RULE,'/',DELETE_RULE) FROM %s.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA='%s' AND CONSTRAINT_NAME='m9_fk'",
		sysSchema, probeDB))
	if strings.HasPrefix(catalog, "ERR") || catalog == "<no rows>" {
		t.Fatalf("M9 判据红：REFERENTIAL_CONSTRAINTS 查不到这条外键（%s）⇒ on_update/on_delete 不是读侧的锅", catalog)
	}
	observe(t, "m9.catalog", "目录原值 UPDATE_RULE/DELETE_RULE=%s", catalog)

	fks, err := c.ListForeignKeys(context.Background(), probeDB, "m9_c")
	if err != nil {
		t.Fatalf("ListForeignKeys 失败：%v", err)
	}
	if len(fks) != 1 {
		t.Fatalf("M9 形状不变式红：驱动读回 %d 条外键（应为 1）", len(fks))
	}
	observe(t, "m9.driver", "驱动读回：%s", fkLine(fks[0]))
	if fks[0].OnUpdate != "" || fks[0].OnDelete != "" {
		observe(t, "m9.driver.note", "驱动已填动作 ⇒ §8-7 的读侧缺口在本版本已补，A.27 的记录要翻")
	}
	mustExec(t, c, "DROP TABLE IF EXISTS m9_c", "DROP TABLE IF EXISTS m9_p")
}

// ─── M10 COLUMN_TYPE 的形状 + precision/scale 空槽 ─────────

// TestProbeColumnTypeShapes 是 §8-3 归一命名表的**第二份输入**（PG 那份是 P10 的 format_type）。
// 两端都读 COLUMN_TYPE（带 unsigned/长度/枚举值的原文）而不是 DATA_TYPE，
// 且两端都不填 MaxLength/Precision/Scale/GenericType/Comment（dbmysql.go:190、lib.rs:405）
// ⇒ 类型收窄的判断没有数值依据，只能去解析原文。这条把"原文长什么样"和"槽位是不是空"一起钉住。
func TestProbeColumnTypeShapes(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m10_t")
	mustExec(t, c,
		"CREATE TABLE m10_t ("+
			"id BIGINT PRIMARY KEY, b BIGINT UNSIGNED, sm SMALLINT, ti TINYINT, "+
			"de DECIMAL(10,2), fl FLOAT, do DOUBLE, "+
			"ch CHAR(8), vc VARCHAR(255), tx TEXT, bl BLOB, "+
			"dt DATE, tm TIME, ts DATETIME, js JSON, en ENUM('a','b'), "+
			"gen INT AS (id * 2) STORED)")

	cols, err := c.ListColumns(context.Background(), probeDB, "m10_t")
	if err != nil {
		t.Fatalf("ListColumns 失败：%v", err)
	}
	const wantCols = 17
	if len(cols) != wantCols {
		t.Fatalf("M10 形状不变式红：建了 %d 列，驱动读回 %d 列", wantCols, len(cols))
	}
	emptySlots := 0
	for i, col := range cols {
		if col.DataType == "" {
			t.Fatalf("M10 形状不变式红：第 %d 列 %s 的 DataType 是空串", i+1, col.Name)
		}
		if int(col.OrdinalPosition) != i+1 {
			t.Fatalf("M10 形状不变式红：%s 的 OrdinalPosition=%d，与列序 %d 不符", col.Name, col.OrdinalPosition, i+1)
		}
		if col.MaxLength == nil && col.Precision == nil && col.Scale == nil {
			emptySlots++
		}
		observe(t, "m10."+col.Name, "%s", colLine(col))
	}
	observe(t, "m10.empty_slots", "三个尺寸槽位全空的列数=%d/%d（收窄判断没有数值依据，只能解析 COLUMN_TYPE 原文）",
		emptySlots, len(cols))
	observe(t, "m10.gen_column", "生成列 gen 的目录 EXTRA=%q（DTO 里没有 is_generated 槽位，驱动把它当普通列）",
		scalar(t, c, fmt.Sprintf(
			"SELECT EXTRA FROM %s.COLUMNS WHERE table_schema='%s' AND table_name='m10_t' AND column_name='gen'",
			sysSchema, probeDB)))
	observe(t, "m10.enum_raw", "枚举列原文=%q（生成器要把它带引号原样写回去）",
		scalar(t, c, fmt.Sprintf(
			"SELECT COLUMN_TYPE FROM %s.COLUMNS WHERE table_schema='%s' AND table_name='m10_t' AND column_name='en'",
			sysSchema, probeDB)))
	observe(t, "m10.no_collation_slot", "字符列的 COLLATION_NAME=%q，而 ColumnInfo 没有任何字符集/排序规则字段",
		scalar(t, c, fmt.Sprintf(
			"SELECT COLLATION_NAME FROM %s.COLUMNS WHERE table_schema='%s' AND table_name='m10_t' AND column_name='vc'",
			sysSchema, probeDB)))
	mustExec(t, c, "DROP TABLE IF EXISTS m10_t")
}

// ─── M11 ListTables 尊重 schema 入参吗（两头判据）─────────

// TestProbeListTablesRespectsSchema 与 PG 的 P11 同形：SQL 里都有 WHERE TABLE_SCHEMA = ?
// （dbmysql.go:149、lib.rs:368），但"同库多 schema 可行"要两头成立——
// 标记对象在**自己的**库里查得到、在**对方的**库里查不到。
// MySQL 的 schema 就是 database，所以这一条同时回答 §8-6 的 v1.1：多库并行是 MySQL 的日常而非 exotic。
func TestProbeListTablesRespectsSchema(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m11_in_probe", "DROP TABLE IF EXISTS m11_in_probe_b")
	mustExec(t, c,
		"CREATE TABLE "+probeDB+".m11_in_probe (id INT PRIMARY KEY)",
		"CREATE TABLE "+probeDBAlt+".m11_in_probe_b (id INT PRIMARY KEY)")
	t.Cleanup(func() {
		_, _ = c.Execute(context.Background(), "DROP TABLE IF EXISTS m11_in_probe")
		_, _ = c.Execute(context.Background(), "DROP TABLE IF EXISTS "+probeDBAlt+".m11_in_probe_b")
	})

	inA, err := c.ListTables(context.Background(), probeDB)
	if err != nil {
		t.Fatalf("ListTables(%s) 失败：%v", probeDB, err)
	}
	inB, err := c.ListTables(context.Background(), probeDBAlt)
	if err != nil {
		t.Fatalf("ListTables(%s) 失败：%v", probeDBAlt, err)
	}
	namesA, namesB := tableNames(inA), tableNames(inB)
	observe(t, "m11.counts", "ListTables(%s) 返回 %d 项，ListTables(%s) 返回 %d 项",
		probeDB, len(inA), probeDBAlt, len(inB))

	if _, ok := namesA["m11_in_probe"]; !ok {
		t.Fatalf("M11 两头判据红：库 %s 里建的表没在自己库的 ListTables 里出现", probeDB)
	}
	if _, leaked := namesA["m11_in_probe_b"]; leaked {
		t.Fatalf("M11 两头判据红：%s 的标记表串进了 %s 的结果 ⇒ schema 入参没生效", probeDBAlt, probeDB)
	}
	if _, ok := namesB["m11_in_probe_b"]; !ok {
		t.Fatalf("M11 两头判据红：库 %s 里建的表没在自己库的 ListTables 里出现", probeDBAlt)
	}
	if _, leaked := namesB["m11_in_probe"]; leaked {
		t.Fatalf("M11 两头判据红：%s 的标记表串进了 %s 的结果 ⇒ schema 入参没生效", probeDB, probeDBAlt)
	}
	schemas, err := c.ListSchemas(context.Background())
	if err != nil {
		t.Fatalf("ListSchemas 失败：%v", err)
	}
	observe(t, "m11.schemas", "ListSchemas 返回 %d 个库，含两个探测库=%v/%v",
		len(schemas), hasName(schemas, probeDB), hasName(schemas, probeDBAlt))
}

func hasName(schemas []protocol.SchemaInfo, name string) bool {
	for _, s := range schemas {
		if s.Name == name {
			return true
		}
	}
	return false
}

// ─── M12 驱动读法漂移：三处（Go 侧取值，Rust 侧按代码引用）─

// TestProbeDriverReadDrifts 把三处"同一件事两端读得不一样"钉成可复现的观测：
//  1. ListSchemas——Go 滤掉四个系统库（dbmysql.go:128），Rust 一条 WHERE 都没有（lib.rs:353）；
//  2. ListIndexes——Go 的 SQL 不查 INDEX_TYPE，IndexInfo.Type 恒空（dbmysql.go:228），
//     Rust 查了并映射成 btree/hash/fulltext/spatial（lib.rs:444）；
//  3. CreateTableSQL——Go 用反引号拼标识符（dbmysql.go:317），
//     Rust 把 `schema.table` 当参数绑进 `SHOW CREATE TABLE ??`（lib.rs:557）。
//
// 前两条能在 Go 侧实测出后果（差几项 / 槽位是否空）；第三条只能证明 Go 自己通得过，
// Rust 那侧要等有 Windows 直连（或 WSL 内有 toolchain）时才算双端对拍。
func TestProbeDriverReadDrifts(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP TABLE IF EXISTS m12_t")
	mustExec(t, c,
		"CREATE TABLE m12_t (id INT PRIMARY KEY, x INT, KEY ix_x (x))",
		"INSERT INTO m12_t (id, x) VALUES (1, 1)")

	driftSchemas(t, c)
	driftIndexType(t, c)
	driftCreateTableSQL(t, c)

	mustExec(t, c, "DROP TABLE IF EXISTS m12_t")
}

func driftSchemas(t *testing.T, c *Conn) {
	t.Helper()
	raw := scalar(t, c, fmt.Sprintf("SELECT COUNT(*) FROM %s.SCHEMATA", sysSchema))
	schemas, err := c.ListSchemas(context.Background())
	if err != nil {
		t.Fatalf("ListSchemas 失败：%v", err)
	}
	filtered := 0
	for _, s := range schemas {
		if s.Name == "mysql" || s.Name == sysSchema || s.Name == "performance_schema" || s.Name == "sys" {
			filtered++
		}
	}
	observe(t, "m12.schemas", "目录里库数=%s，Go 的 ListSchemas 返回=%d，其中系统库出现次数=%d"+
		"（Go 滤 4 个、Rust 不滤 ⇒ 同一实例两端元数据树差 %s 项）",
		raw, len(schemas), filtered, subtractNumbers(t, raw, fmt.Sprint(len(schemas))))
	if filtered != 0 {
		t.Fatalf("M12 形状不变式红：Go 的 ListSchemas 里出现了 %d 个系统库，与 dbmysql.go:128 的 WHERE 不符", filtered)
	}
}

// subtractNumbers 只为把 OBS 写得能口算，不参与断言。
func subtractNumbers(t *testing.T, a, b string) string {
	t.Helper()
	var ai, bi int
	if _, err := fmt.Sscanf(a, "%d", &ai); err != nil {
		return "?"
	}
	if _, err := fmt.Sscanf(b, "%d", &bi); err != nil {
		return "?"
	}
	return fmt.Sprintf("%d", ai-bi)
}

func driftIndexType(t *testing.T, c *Conn) {
	t.Helper()
	idx, err := c.ListIndexes(context.Background(), probeDB, "m12_t")
	if err != nil {
		t.Fatalf("ListIndexes 失败：%v", err)
	}
	raw := scalar(t, c, fmt.Sprintf(
		"SELECT GROUP_CONCAT(DISTINCT INDEX_TYPE) FROM %s.STATISTICS WHERE table_schema='%s' AND table_name='m12_t'",
		sysSchema, probeDB))
	empty := 0
	for _, one := range idx {
		observe(t, "m12.index", "%s", indexLine(one))
		if one.Type == "" {
			empty++
		}
	}
	observe(t, "m12.index_type", "目录里 INDEX_TYPE=%q，而驱动读回 Type 为空的索引数=%d/%d"+
		"（Rust 侧同一张表会填成 btree ⇒ 契约对拍一旦真跑必差）", raw, empty, len(idx))
}

func driftCreateTableSQL(t *testing.T, c *Conn) {
	t.Helper()
	ddl, err := c.CreateTableSQL(context.Background(), probeDB, "m12_t")
	if err != nil {
		t.Fatalf("CreateTableSQL 失败：%v", err)
	}
	if !strings.Contains(strings.ToUpper(ddl), "CREATE TABLE") {
		t.Fatalf("M12 形状不变式红：CreateTableSQL 回的不是 DDL 文本：%q", ddl)
	}
	observe(t, "m12.create_table_sql", "Go 侧（反引号拼接）通得过，长度 %d：%s", len(ddl), oneline(ddl))
}

func oneline(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ─── M13 依赖目录有无 + 视图定义文本（§8-8 B 档的方言结论）──

// TestProbeDependencyCatalog 回答 A.20 B 档在 MySQL 上到底可不可行：
// PG 有 pg_depend（P12 在探），SQLite 没有（B 档判死）。MySQL 若也没有 per-object 依赖目录，
// 那"依赖可见"就是**按方言分级**的能力位，而不是全库盲区。
// 判据本体是"这些目录表不存在"——查到了才要改 §8-8。
func TestProbeDependencyCatalog(t *testing.T) {
	c := probeConn(t)
	mustExec(t, c, "DROP VIEW IF EXISTS m13_v", "DROP TABLE IF EXISTS m13_t")
	mustExec(t, c,
		"CREATE TABLE m13_t (id INT PRIMARY KEY, qty INT)",
		"CREATE VIEW m13_v AS SELECT id, qty FROM m13_t")

	cat := scalar(t, c, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.TABLES WHERE table_schema='%s' AND table_name IN "+
			"('VIEW_TABLE_USAGE','DEPENDENCIES','OBJECT_DEPENDENCIES','DEPENDS_ON')",
		sysSchema, sysSchema))
	observe(t, "m13.dependency_catalog_tables", "information_schema 里疑似依赖目录的表数=%s", cat)
	if cat != "0" {
		t.Fatalf("M13 判据红：MySQL %s 里查到了 %s 张疑似依赖目录表 ⇒ §8-8 的\"B 档在 MySQL 上判死\"要翻",
			sysSchema, cat)
	}

	def := scalar(t, c, fmt.Sprintf(
		"SELECT VIEW_DEFINITION FROM %s.VIEWS WHERE table_schema='%s' AND table_name='m13_v'",
		sysSchema, probeDB))
	observe(t, "m13.view_definition", "VIEWS.VIEW_DEFINITION 原文=%q（B 档若要在 MySQL 上做，只有解析这一条路）", def)
	observe(t, "m13.show_create_view", "SHOW CREATE VIEW 通得过=%v",
		!strings.HasPrefix(scalar(t, c, "SHOW CREATE VIEW m13_v"), "ERR"))

	mv := countTablesOfType(t, c, probeDB, "MATERIALIZED VIEW")
	observe(t, "m13.materialized", "物化视图表数=%s（Go 的 ListTables 把非 'VIEW' 一律映射成 'table'，dbmysql.go:147）", mv)
	observe(t, "m13.table_type_domain", "本库在册 table_type 域=%s", scalar(t, c, fmt.Sprintf(
		"SELECT GROUP_CONCAT(DISTINCT TABLE_TYPE) FROM %s.TABLES WHERE table_schema='%s'", sysSchema, probeDB)))

	mustExec(t, c, "DROP VIEW IF EXISTS m13_v", "DROP TABLE IF EXISTS m13_t")
}
