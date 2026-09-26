package tui

// E6.2 TUI 远程模式向导端到端真跑：基准侧/期望侧各一个 :memory: SQLite，
// 但 TUI 只拿到一个 **Remote** 客户端——比对/生成/执行/回滚全部走真实的
// httptest + HTTP + msgpack 通道，不再走进程内直调。
//
// 为什么要单独跑这一份：E5.2 的按键测试（compare_test.go）用 NewLocal，
// 服务端重算勾选校验、DTO 的 msgpack 编解码、事务端点这三件事**从没被 TUI 打到过**；
// 而 transport 层的 cmpClients 对拍只证明"两个客户端返回一致"，不证明向导按键环路打得通。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/server"
	"github.com/polydb/polydb/pkg/transport"
)

const remoteToken = "e6-2-secret"

// countingHandler 记录每个请求命中的路由模板，用来证明流量真的经过了服务端。
type countingHandler struct {
	inner http.Handler
	mu    sync.Mutex
	paths []string
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

func (c *countingHandler) seq() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

// count 统计命中某子串（区分大小写、子串匹配）的请求数。
func (c *countingHandler) count(sub string) int {
	n := 0
	for _, p := range c.seq() {
		if strings.Contains(p, sub) {
			n++
		}
	}
	return n
}

// setupRemote 把 app 挂到 httptest 服务端（启用 Bearer token），返回带 token 的 Remote 客户端。
func setupRemote(t *testing.T, app *appcore.AppCore) (*transport.Remote, *countingHandler, string) {
	t.Helper()
	inner := server.NewWithToken(app, remoteToken).Handler()
	counter := &countingHandler{inner: inner}
	srv := httptest.NewServer(counter)
	t.Cleanup(srv.Close)
	rem := transport.NewRemote(srv.URL)
	rem.SetToken(remoteToken)
	return rem, counter, srv.URL
}

// remoteDiff 用 HTTP 通道回读差异计划（与 realDiff 走进程内直调互为对照：
// 这条路径上的条目 id 经过了服务端重算与 msgpack 编解码）。
func remoteDiff(t *testing.T, c transport.Client, source, target string) []string {
	t.Helper()
	plan, err := c.SchemaDiff(context.Background(), &protocol.SchemaDiffRequest{
		Source: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(source)},
		Target: protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(target)},
	})
	if err != nil {
		t.Fatalf("remote diff: %v", err)
	}
	ids := []string{}
	for _, e := range plan.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// tableExistsRemote 经 HTTP 回读某张表在不在服务端那一侧。
func tableExistsRemote(t *testing.T, c transport.Client, connID, name string) bool {
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
	return string(raw) == "1"
}

// openTablesRemote 走 HTTP 把模型推进到库表视图（与 openTables 同形，只差客户端）。
func openTablesRemote(t *testing.T, rem *transport.Remote) tea.Model {
	t.Helper()
	var m tea.Model = New(rem)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, m.Init())
	if md(m).view != viewConns || len(md(m).conns) < 1 {
		t.Fatalf("setup: view=%v conns=%d", md(m).view, len(md(m).conns))
	}
	m, cmd := m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).view != viewTables {
		t.Fatalf("setup: view=%v want viewTables (status=%s)", md(m).view, md(m).status)
	}
	return m
}

func TestCompareWizardOverRemoteTransport(t *testing.T) {
	app, baseID := setupApp(t)
	const tgtName = "远程期望库"
	tgtID := addSQLiteConn(t, app, tgtName,
		`CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(30), email TEXT)`,
		`CREATE TABLE new_t(id INTEGER)`)
	const wantJoined = "u//drop_table,new_t//create_table,t/email/add_column,t/name/alter_column"

	rem, counter, url := setupRemote(t, app)
	// 前置：服务端确实启用了鉴权——不带 token 的客户端必须被挡在 401。
	bad := transport.NewRemote(url)
	if _, err := bad.ListConnections(); err == nil {
		t.Fatalf("未带 token 却列出了连接：鉴权没生效，后面的断言都是空的")
	} else if !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("未带 token 的错误不是鉴权错误：%v", err)
	}

	m := openTablesRemote(t, rem)
	m = enterCompare(t, m)
	if got := len(md(m).cmpCandidates); got != 2 {
		t.Fatalf("candidates=%d want 2", got)
	}

	// ① 选期望侧 → Enter 比对（这一脚踩的是 POST /api/schema/diff）
	m, cmd := m.Update(runeKey('j'))
	if got := md(m).connName(md(m).cmpTargetID()); got != tgtName {
		t.Fatalf("cursor on %q want %q", got, tgtName)
	}
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStageDiff {
		t.Fatalf("after diff: stage=%v status=%s", md(m).cmpStage, md(m).status)
	}
	if got := strings.Join(cmpEntryIDs(m), ","); got != wantJoined {
		t.Fatalf("entries=%s want %s", got, wantJoined)
	}
	if n := counter.count("POST /api/schema/diff"); n == 0 {
		t.Fatalf("比对没打到服务端端点：requests=%v", counter.seq())
	}

	// ② 取消勾选一个重建组成员 ⇒ 缺口 ⇒ 服务端在 /api/schema/ddl 上也会拒
	m, _ = m.Update(key(tea.KeyDown))
	m, _ = m.Update(key(tea.KeyDown))
	if got := md(m).cmpEntries[md(m).cmpCur].ID; got != "t/email/add_column" {
		t.Fatalf("cursor on %q", got)
	}
	m, _ = m.Update(key(tea.KeySpace))
	if len(md(m).cmpGaps) == 0 {
		t.Fatalf("unchecking a rebuild entry should open a gap")
	}
	// 绕过客户端提示直接问服务端：陈旧/未整组勾选必须由**服务端**拒（呈现层不算数）。
	if _, err := rem.SchemaDDL(context.Background(), &protocol.SchemaDdlRequest{
		Source:   protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(baseID)},
		Target:   protocol.SchemaDiffTarget{ConnectionID: protocol.ConnectionID(tgtID)},
		Selected: []string{"t/name/alter_column"},
	}); err == nil {
		t.Fatalf("服务端接受了未整组勾选的脚本请求")
	}
	m = fillGapsAndGenerate(t, m)
	s := md(m).cmpScript
	if s == nil || len(s.Statements) == 0 {
		t.Fatalf("script empty: status=%s", md(m).status)
	}
	if !s.DdlInTx || !s.PragmasOutsideTx {
		t.Fatalf("capability bits lost over the wire: ddl_in_tx=%v pragma_outside=%v", s.DdlInTx, s.PragmasOutsideTx)
	}
	if n := counter.count("POST /api/schema/ddl"); n == 0 {
		t.Fatalf("生成脚本没打到服务端端点：requests=%v", counter.seq())
	}

	// ③ x 执行：事务四方法 + 端内语句全部走 HTTP
	m, cmd = m.Update(runeKey('x'))
	m = drive(t, m, cmd)
	if !md(m).cmpApplied {
		t.Fatalf("remote apply failed: status=%s outcome=%+v", md(m).status, md(m).cmpOutcome)
	}
	if o := md(m).cmpOutcome; o == nil || o.Ran != len(s.Statements) {
		t.Fatalf("outcome=%+v want Ran=%d", md(m).cmpOutcome, len(s.Statements))
	}
	if n := counter.count("/api/transactions/"); n < 3 {
		t.Fatalf("事务端点调用过少（begin/execute/commit 至少 3 次）：n=%d requests=%v", n, counter.seq())
	}
	// 落地状态用服务端回读证明（不是本地对象的镜像）
	if got := strings.Join(remoteDiff(t, rem, baseID, tgtID), ","); got != "u//drop_table" {
		t.Fatalf("after remote sync: %s want u//drop_table", got)
	}
	if !tableExistsRemote(t, rem, baseID, "new_t") {
		t.Fatalf("new_t 没在服务端那一侧建出来")
	}
	// 破坏性条目（删表 u）默认不勾选 ⇒ 同步后必须**还在**：
	// 这条断言同时钉住"默认勾选口径"一路穿过 HTTP 到服务端重算都没走样。
	if !tableExistsRemote(t, rem, baseID, "u") {
		t.Fatalf("u 被删了：破坏性条目不该在默认勾选里")
	}

	// r 代跑回滚：确认词走同一个 HTTP 环路
	m, cmd = m.Update(runeKey('r'))
	if !md(m).cmpConfirmOn {
		t.Fatalf("r should open confirm: cmd=%v", cmd)
	}
	m = typeInto(t, m, md(m).cmpBase.Name)
	m, cmd = m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpApplied || !strings.Contains(md(m).status, "回滚脚本已执行") {
		t.Fatalf("rollback status=%q outcome=%+v", md(m).status, md(m).cmpOutcome)
	}
	if got := strings.Join(remoteDiff(t, rem, baseID, tgtID), ","); got != wantJoined {
		t.Fatalf("after remote rollback: %s want %s", got, wantJoined)
	}
	if tableExistsRemote(t, rem, baseID, "new_t") {
		t.Fatalf("回滚后 new_t 还留着：HTTP 事务通道没真的回滚")
	}
}

// TestCompareWizardRemoteReportsServerError 证明服务端错误能穿到向导状态行：
// 期望侧指到一个不存在的连接 ⇒ /api/schema/diff 4xx ⇒ 状态行带服务端消息、停在第①段。
func TestCompareWizardRemoteReportsServerError(t *testing.T) {
	app, _ := setupApp(t)
	rem, _, _ := setupRemote(t, app)
	m := openTablesRemote(t, rem)
	m = enterCompare(t, m)

	md(m).cmpBase = protocol.ConnectionInfo{ID: "ghost-conn", Name: "不存在", Kind: protocol.DatabaseKindSQLite}
	m, cmd := m.Update(key(tea.KeyEnter))
	m = drive(t, m, cmd)
	if md(m).cmpStage != cmpStagePick {
		t.Fatalf("failed diff must stay on stage 1: stage=%v", md(m).cmpStage)
	}
	if !strings.Contains(md(m).status, "比对失败") {
		t.Fatalf("status=%q want 比对失败…", md(m).status)
	}
	// 服务端错误码要能一路传到状态行（不是被吞成"未知错误"）
	if !strings.Contains(md(m).status, "POLYDB_ERR_") {
		t.Fatalf("status lost the error code: %q", md(m).status)
	}
}
