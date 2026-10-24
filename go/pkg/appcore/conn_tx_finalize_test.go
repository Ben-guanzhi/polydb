package appcore

// 第二轮分析 §6 的两项 P1 回归测试：
//   - Connect 覆盖注册表时必须关掉被顶替的驱动句柄（并发懒连接会漏一个）；
//   - 事务 finalize 后移出注册表、事务内查询不得并发驱动同一个 *sql.Tx、
//     查不到事务的 error code 是 TRANSACTION_NOT_FOUND。
//
// 语义基线是 spec/behavior.md §10 与 Rust 侧 rust/crates/app-core/src/lib.rs
// （commit/rollback 用 remove 且不回插；execute_in_transaction 先 remove 后回插）。
// Option B 之后注册表本体是 dbcore.TxManager（每连接一个，见 appcore.go）；
// 本文件钉的是 AppCore 接线层的行为，内核语义由 dbcore/drivertest 覆盖。

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbc "github.com/dbcore/dbcore"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/dbcore/bridge"
	"github.com/polydb/polydb/pkg/dbsqlite"
	"github.com/polydb/polydb/pkg/protocol"
)

// codeOf 取出 PolyDBError 的码；非 PolyDBError 返回空串。可在 goroutine 里用
// （不含 t.Fatalf）。
func codeOf(err error) string {
	var pe *protocol.PolyDBError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// closeTracker 包住真实驱动，只数 Close 被调用了几次，其余透传。
type closeTracker struct {
	dbcore.Driver
	closed atomic.Int32
}

func (t *closeTracker) Close() error {
	t.closed.Add(1)
	return t.Driver.Close()
}

// swapDriver 用 wrap 替换注册表里已打开连接的驱动（测试在包内，仍需拿锁）。
func swapDriver(t *testing.T, a *AppCore, id string, wrap func(dbcore.Driver) dbcore.Driver) {
	t.Helper()
	c, err := a.get(id)
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	a.mu.Lock()
	a.connections[id] = dbcore.NewConnection(wrap(c.Driver()))
	a.mu.Unlock()
}

// TestConnectClosesSupersededDriver 钉住"覆盖即关闭"：同一 id 第二次 Connect 时，
// 前一个驱动句柄必须被 Close。Go 没有 Drop，不关就是永久泄漏一个连接。
func TestConnectClosesSupersededDriver(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("connect: %v", err)
	}

	var tracker *closeTracker
	swapDriver(t, a, id, func(d dbcore.Driver) dbcore.Driver {
		tracker = &closeTracker{Driver: d}
		return tracker
	})
	if got := tracker.closed.Load(); got != 0 {
		t.Fatalf("装饰阶段就关掉了驱动（closed=%d），测试前提不成立", got)
	}

	if err := a.Connect(ctx, id); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if got := tracker.closed.Load(); got != 1 {
		t.Fatalf("被顶替的驱动 Close 次数 = %d，want 1（漏关即连接句柄泄漏）", got)
	}
	c, err := a.get(id)
	if err != nil {
		t.Fatalf("get after reconnect: %v", err)
	}
	if c.Driver() == tracker {
		t.Fatalf("重连后注册表里还是旧驱动")
	}
}

// TestFinalizeRemovesTransaction 钉住"事务只成功 finalize 一次"：commit 成功后
// 条目移出注册表，第二次 commit、之后的 rollback、GetTransactionInfo 全部返回
// POLYDB_ERR_TRANSACTION_NOT_FOUND（修复前最后一项返回的是 TRANSACTION_FAILED）。
func TestFinalizeRemovesTransaction(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()
	info, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	committed, err := a.CommitTransaction(info.ID)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if committed.Status != protocol.TxnCommitted {
		t.Fatalf("commit 返回 status=%v，want committed", committed.Status)
	}

	for _, fn := range []func() error{
		func() error { _, err := a.CommitTransaction(info.ID); return err },
		func() error { _, err := a.RollbackTransaction(info.ID); return err },
		func() error { _, err := a.GetTransactionInfo(info.ID); return err },
	} {
		err := fn()
		if code := codeOf(err); code != protocol.ErrTransactionNotFound {
			t.Fatalf("finalize 后再操作错误码 = %q，want %s（err=%v）", code, protocol.ErrTransactionNotFound, err)
		}
	}

	// rollback 分支同样只成功一次。
	second, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin(2): %v", err)
	}
	if _, err := a.RollbackTransaction(second.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	err = func() error { _, err := a.RollbackTransaction(second.ID); return err }()
	if code := codeOf(err); code != protocol.ErrTransactionNotFound {
		t.Fatalf("第二次 rollback 错误码 = %q，want %s", code, protocol.ErrTransactionNotFound)
	}
}

// overlapTracker 记录内核 ExecuteIn 的历史最大并发数（Option B：装饰在内核
// 原生 dbc.SQLTxDriver 上，TxManager 直接驱动它）。
type overlapTracker struct {
	dbc.SQLTxDriver
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (o *overlapTracker) ExecuteIn(exec dbc.SQLTx, ctx context.Context, stmt string, args ...dbc.Value) (*dbc.QueryResult, error) {
	cur := o.inFlight.Add(1)
	for {
		peak := o.peak.Load()
		if cur <= peak || o.peak.CompareAndSwap(peak, cur) {
			break
		}
	}
	// 放大窗口：并发驱动同一事务时几乎必然在这里重叠。
	time.Sleep(10 * time.Millisecond)
	res, err := o.SQLTxDriver.ExecuteIn(exec, ctx, stmt, args...)
	o.inFlight.Add(-1)
	return res, err
}

// overlapConn 在 protocol 驱动之上把 SQLTx 指到装饰器。覆盖 AsSQL 让
// Connection.AsSQL 返回 overlapConn 自身（提升方法只会返回内嵌驱动自己），
// 这样 BeginTransaction 的 bridge.SQLTxProvider 断言拿到的是装饰器。
type overlapConn struct {
	*dbsqlite.ProtocolDriver
	tx dbc.SQLTxDriver
}

func (o *overlapConn) AsSQL() (dbcore.SQLDriver, bool) { return o, true }
func (o *overlapConn) SQLTx() dbc.SQLTxDriver          { return o.tx }

// TestExecuteInTxNeverConcurrentOnSameHandle 钉住 spec §10.2「同一事务内的查询
// 必须串行、不允许并发 execute」：同一事务上的并发 execute 不得同时驱动同一个
// *sql.Tx；被借出期间进来的那次返回 TRANSACTION_NOT_FOUND；跑完之后事务仍在
// 注册表里且能正常 commit（即 execute 后回插）。
func TestExecuteInTxNeverConcurrentOnSameHandle(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()
	if _, err := a.Execute(ctx, id, "CREATE TABLE t(v INTEGER)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	c, err := a.get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	pd, ok := c.Driver().(*dbsqlite.ProtocolDriver)
	if !ok {
		t.Fatalf("sqlite 连接的驱动不是 dbsqlite.ProtocolDriver")
	}
	overlap := &overlapTracker{SQLTxDriver: pd.SQLTx()}
	a.mu.Lock()
	a.connections[id] = dbcore.NewConnection(&overlapConn{ProtocolDriver: pd, tx: overlap})
	a.mu.Unlock()

	info, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	var okCount, borrowed atomic.Int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.ExecuteInTx(ctx, info.ID, "INSERT INTO t VALUES (1)")
			switch {
			case err == nil:
				okCount.Add(1)
			case codeOf(err) == protocol.ErrTransactionNotFound:
				borrowed.Add(1)
			default:
				t.Errorf("并发 execute 返回意外错误: %v", err)
			}
		}()
	}
	wg.Wait()

	if okCount.Load() == 0 {
		t.Fatalf("并发 execute 全部失败（ok=0, borrowed=%d）", borrowed.Load())
	}
	if peak := overlap.peak.Load(); peak != 1 {
		t.Fatalf("同一事务上 ExecuteIn 的历史最大并发 = %d，want 1（spec §10.2 禁止并发 execute）", peak)
	}
	if got := okCount.Load() + borrowed.Load(); got != n {
		t.Fatalf("分类计数 %d ≠ 请求数 %d", got, n)
	}

	// 事务必须还活着（execute 完回插），并且能正常收尾。
	if _, err := a.ExecuteInTx(ctx, info.ID, "SELECT COUNT(*) FROM t"); err != nil {
		t.Fatalf("并发之后的顺序 execute 失败（entry 可能没回插）: %v", err)
	}
	if _, err := a.CommitTransaction(info.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows, err := a.Execute(ctx, id, "SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	count, ok := protocol.TryGetInt(rows.Rows[0][0])
	if !ok {
		t.Fatalf("回读 COUNT 失败: %+v", rows.Rows[0][0])
	}
	if count != int64(okCount.Load()) {
		t.Fatalf("提交的行数 = %d，want %d（被借出挡掉的那几条不应落库）", count, okCount.Load())
	}
}

// txManagerOf 取连接的事务注册表（BeginTransaction 懒建）。
func txManagerOf(t *testing.T, a *AppCore, id string) *bridge.TxManager {
	t.Helper()
	a.mu.RLock()
	defer a.mu.RUnlock()
	mgr, ok := a.txManagers[id]
	if !ok {
		t.Fatalf("连接 %s 的事务注册表不存在", id)
	}
	return mgr
}

// TestSweepReclaimsIdleTransaction 钉住 P0-2 残留修复：begin 后未 finalize 的事务必须有
// 主动回收，否则注册表与底层 *sql.Tx（占用连接池一条连接）会随进程常驻无限增长。
// 把该连接 TxManager 的空闲阈值调成负值并立即清扫，断言条目被移出注册表、
// 其 *sql.Tx 被回滚（连接归还池），且后续操作返回 NOT_FOUND。
func TestSweepReclaimsIdleTransaction(t *testing.T) {
	a, id := newSQLiteApp(t)
	t.Cleanup(a.Close)
	ctx := context.Background()
	info, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// begin 后事务应当可见
	if _, err := a.GetTransactionInfo(info.ID); err != nil {
		t.Fatalf("begin 后事务应当可见: %v", err)
	}

	mgr := txManagerOf(t, a, id)
	mgr.SetIdleTTL(-time.Second) // 任何已开始的 tx 都算"空闲超时"
	mgr.SweepNow()

	// 1) 注册表已摘除
	if _, err := a.GetTransactionInfo(info.ID); codeOf(err) != protocol.ErrTransactionNotFound {
		t.Fatalf("sweep 后事务仍可见（err=%v），空闲回收未生效", err)
	}
	// 2) 后续操作必须拿到 NOT_FOUND（证明 entry 已不存在，*sql.Tx 已被回滚释放）
	if _, err := a.ExecuteInTx(ctx, info.ID, "SELECT 1"); codeOf(err) != protocol.ErrTransactionNotFound {
		t.Fatalf("sweep 后 ExecuteInTx 错误码=%q，want %s", codeOf(err), protocol.ErrTransactionNotFound)
	}
}

// TestSweepEnforcesMaxEntries 钉住容量上限：总数超过上限时，按 lastActive 升序
// 回收最旧的若干，防止突发 begin 把注册表推到无界。
//
// 两条真实 *sql.Tx 需要两条池连接，:memory: SQLite 被限成单连接会 deadlock，
// 所以这条测试用文件库目标连接（dbsqlite 不对文件库限连接数）。
func TestSweepEnforcesMaxEntries(t *testing.T) {
	a, _ := newSQLiteApp(t)
	t.Cleanup(a.Close)
	conn, err := a.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "cap",
		Kind:     protocol.DatabaseKindSQLite,
		Database: filepath.Join(t.TempDir(), "cap.db"),
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	id := conn.ID
	ctx := context.Background()

	first, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin(1): %v", err)
	}
	second, err := a.BeginTransaction(ctx, &protocol.BeginTransactionRequest{ConnectionID: id})
	if err != nil {
		t.Fatalf("begin(2): %v", err)
	}

	mgr := txManagerOf(t, a, id)
	mgr.SetIdleTTL(30 * time.Minute) // 这一轮不让空闲命中，专测容量分支
	mgr.SetMaxEntries(1)
	mgr.SweepNow()

	// 总数 2 > 上限 1，按 lastActive 升序回收最旧的一条；剩一条仍存活。
	if _, err := a.GetTransactionInfo(first.ID); codeOf(err) != protocol.ErrTransactionNotFound {
		t.Fatalf("最旧的事务未被容量回收（err=%v）", err)
	}
	if _, err := a.GetTransactionInfo(second.ID); err != nil {
		t.Fatalf("较新的事务不该被回收: %v", err)
	}
	// 存活的事务仍可正常 finalize（回收只动超额条目）。
	if _, err := a.CommitTransaction(second.ID); err != nil {
		t.Fatalf("commit surviving tx: %v", err)
	}
	// 先断开释放文件句柄：Windows 上 TempDir 清理删不掉仍被连接池占着的 cap.db。
	a.Disconnect(id)
}
