package appcore

// E6.4 快照采集扇出量化（§8-5 的"表数上限"事实订正）。
//
// 此前文档里的说法是"GUI 现有整 schema 外键扫描硬上限 200 表，diff 场景动辄上千表，
// 要不要一并放宽/分页"——这句话把**两件事混成了一件**：
//   - 200 是 GUI「关系」轨道列外键时的 `.take(200)`（rust/crates/ui-gui/src/app.rs），
//     跟结构比对**没有任何关系**；
//   - 比对路径（collect_snapshot）两端都**没有**表数上限，代价是每表 3 次元数据查询串行往返。
//
// 本文件不测 GUI，只把比对路径的真实形状钉成数字：调用次数用计数装饰器精确断言，
// 耗时用真 :memory: SQLite 实测。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

// fanCounter 包住真实驱动，只数 List* 各被叫了几次，其余一律透传。
type fanCounter struct {
	dbcore.SQLDriver
	tables  int
	columns int
	indexes int
	fks     int
}

func (c *fanCounter) AsSQL() (dbcore.SQLDriver, bool) { return c, true }
func (c *fanCounter) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (c *fanCounter) ListTables(ctx context.Context, schema string) ([]protocol.TableInfo, error) {
	c.tables++
	return c.SQLDriver.ListTables(ctx, schema)
}

func (c *fanCounter) ListColumns(ctx context.Context, schema, table string) ([]protocol.ColumnInfo, error) {
	c.columns++
	return c.SQLDriver.ListColumns(ctx, schema, table)
}

func (c *fanCounter) ListIndexes(ctx context.Context, schema, table string) ([]protocol.IndexInfo, error) {
	c.indexes++
	return c.SQLDriver.ListIndexes(ctx, schema, table)
}

func (c *fanCounter) ListForeignKeys(ctx context.Context, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	c.fks++
	return c.SQLDriver.ListForeignKeys(ctx, schema, table)
}

// newSQLiteApp 开一个 app-core + 一个 SQLite 连接（:memory:），返回 app 与连接 id。
func newSQLiteApp(t *testing.T) (*AppCore, string) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	a := New(db, kr)
	conn, err := a.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "fanout",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	return a, conn.ID
}

// createTables 在真库里建 n 张表，每张 4 列 + 1 索引。
func createTables(t *testing.T, a *AppCore, id string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		name := fmt.Sprintf("t%d", i)
		stmt := fmt.Sprintf(`CREATE TABLE %s(id INTEGER PRIMARY KEY, a TEXT, b REAL, c TEXT DEFAULT 'x');`, name)
		if _, err := a.Execute(ctx, id, stmt); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := a.Execute(ctx, id, fmt.Sprintf(`CREATE INDEX ix_%s_a ON %s(a);`, name, name)); err != nil {
			t.Fatalf("index on %s: %v", name, err)
		}
	}
}

// installCounter 把已打开连接的驱动换成计数装饰器（测试在包内，直接替换 connections）。
func installCounter(t *testing.T, a *AppCore, id string) *fanCounter {
	t.Helper()
	c, err := a.get(id)
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	drv, ok := c.Driver().AsSQL()
	if !ok {
		t.Fatalf("connection is not a SQL driver")
	}
	cnt := &fanCounter{SQLDriver: drv}
	a.connections[id] = dbcore.NewConnection(cnt)
	return cnt
}

// TestSnapshotFanOutIsThreeQueriesPerTable 把"每表 3 次串行元数据查询"钉成精确计数，
// 并证明**视图一次都不查**（§14.1 的"快照看不见视图"在调用次数上的形状）。
func TestSnapshotFanOutIsThreeQueriesPerTable(t *testing.T) {
	a, id := newSQLiteApp(t)
	const n = 12
	createTables(t, a, id, n)
	ctx := context.Background()
	// 再塞 5 张视图：它们会出现在 ListTables 结果里，然后被 table_type 过滤掉。
	for i := range 5 {
		v := fmt.Sprintf("v%d", i)
		if _, err := a.Execute(ctx, id, fmt.Sprintf(`CREATE VIEW %s AS SELECT id FROM t0;`, v)); err != nil {
			t.Fatalf("create view %s: %v", v, err)
		}
	}

	cnt := installCounter(t, a, id)
	snap, err := a.collectSnapshot(ctx, protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(id)})
	if err != nil {
		t.Fatalf("collect snapshot: %v", err)
	}
	if len(snap.Tables) != n {
		t.Fatalf("snapshot tables=%d want %d（视图不该进快照）", len(snap.Tables), n)
	}
	if cnt.tables != 1 {
		t.Fatalf("ListTables 调用 %d 次，want 1", cnt.tables)
	}
	if cnt.columns != n || cnt.indexes != n || cnt.fks != n {
		t.Fatalf("每表元数据查询次数 columns=%d indexes=%d fks=%d want 各 %d",
			cnt.columns, cnt.indexes, cnt.fks, n)
	}
	total := cnt.tables + cnt.columns + cnt.indexes + cnt.fks
	if total != 1+3*n {
		t.Fatalf("总元数据查询 = %d，want 1+3n = %d", total, 1+3*n)
	}
	t.Logf("实测：n=%d 表 ⇒ 元数据查询 %d 次（1 次 ListTables + 每表 3 次），视图 5 张贡献 0 次", n, total)
}

// TestSnapshotNoTableCountCap 直接证伪"比对路径有 200 表上限"：
// 1000 张表照采照比，一个字节的上限代码都没有。
func TestSnapshotNoTableCountCap(t *testing.T) {
	a, id := newSQLiteApp(t)
	const n = 1000
	createTables(t, a, id, n)
	ctx := context.Background()

	cnt := installCounter(t, a, id)
	start := time.Now()
	snap, err := a.collectSnapshot(ctx, protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(id)})
	cost := time.Since(start)
	if err != nil {
		t.Fatalf("collect snapshot: %v", err)
	}
	if len(snap.Tables) != n {
		t.Fatalf("tables=%d want %d（若真有 200 上限这里会截断）", len(snap.Tables), n)
	}
	t.Logf("实测：单次快照采集 %d 表（含 %d 次元数据查询）耗时 %v，即每表 %v",
		n, cnt.tables+cnt.columns+cnt.indexes+cnt.fks, cost.Round(time.Millisecond),
		cost/time.Duration(n))
	if cost > 30*time.Second {
		t.Fatalf("采集 %d 表慢到不可用：%v", n, cost)
	}
}

// BenchmarkCollectSnapshot 给出 50/200/1000 表三档的可比数字（每档一个子基准，库只建一次）。
func BenchmarkCollectSnapshot(b *testing.B) {
	for _, n := range []int{50, 200, 1000} {
		b.Run(fmt.Sprintf("tables=%d", n), func(b *testing.B) {
			a, id := newSQLiteAppB(b)
			createTablesB(b, a, id, n)
			ctx := context.Background()
			b.ResetTimer()
			for range b.N {
				snap, err := a.collectSnapshot(ctx, protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(id)})
				if err != nil {
					b.Fatalf("collect snapshot: %v", err)
				}
				if len(snap.Tables) != n {
					b.Fatalf("tables=%d want %d", len(snap.Tables), n)
				}
			}
			b.ReportMetric(float64(1+3*n), "queries/op")
		})
	}
}

func newSQLiteAppB(b *testing.B) (*AppCore, string) {
	b.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		b.Fatalf("open storage: %v", err)
	}
	kr, err := keyring.NewFileKeyring(b.TempDir(), "test")
	if err != nil {
		b.Fatalf("open keyring: %v", err)
	}
	a := New(db, kr)
	conn, err := a.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "bench",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
	})
	if err != nil {
		b.Fatalf("create connection: %v", err)
	}
	return a, conn.ID
}

func createTablesB(b *testing.B, a *AppCore, id string, n int) {
	b.Helper()
	ctx := context.Background()
	for i := range n {
		name := fmt.Sprintf("t%d", i)
		if _, err := a.Execute(ctx, id, fmt.Sprintf(`CREATE TABLE %s(id INTEGER PRIMARY KEY, a TEXT, b REAL);`, name)); err != nil {
			b.Fatalf("create %s: %v", name, err)
		}
	}
}
