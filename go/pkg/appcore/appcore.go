// Package appcore 提供用例编排：连接管理、连通性、查询与元数据内省。
package appcore

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	dbc "github.com/dbcore/dbcore"
	"github.com/google/uuid"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/dbcore/bridge"
	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/sshtunnel"
	"github.com/polydb/polydb/pkg/storage"
	"golang.org/x/sync/singleflight"

	// PD-01/PD-02：各驱动包的 init() 把 opener 登记进 pkg/dbcore 的驱动分派
	// 注册表。这里的空导入就是「按 kind 选择引擎」的机制——与内核
	// `_ "github.com/dbcore/dbcore/<engine>"` 的 opt-in 纪律一致，二进制不含
	// 未使用驱动。
	_ "github.com/polydb/polydb/pkg/dbmssql"
	_ "github.com/polydb/polydb/pkg/dbmysql"
	_ "github.com/polydb/polydb/pkg/dboracle"
	_ "github.com/polydb/polydb/pkg/dbpostgres"
	_ "github.com/polydb/polydb/pkg/dbredis"
	_ "github.com/polydb/polydb/pkg/dbsqlite"
)

// driverSpec holds per-engine metadata: default port, schema, host requirement.
// Adding a new engine means adding one entry to the driverSpecs map.
type driverSpec struct {
	defaultPort   int
	defaultSchema string
	needsHost     bool
}

var driverSpecs = map[protocol.DatabaseKind]driverSpec{
	protocol.DatabaseKindSQLite:   {0, "main", false},
	protocol.DatabaseKindPostgres: {5432, "public", true},
	protocol.DatabaseKindMySQL:    {3306, "", true},
	protocol.DatabaseKindMSSQL:    {1433, "", true},
	protocol.DatabaseKindOracle:   {1521, "", true},
	protocol.DatabaseKindRedis:    {6379, "", true},
}

func driverSpecFor(kind protocol.DatabaseKind) driverSpec {
	return driverSpecs[kind]
}

// 事务注册表在共享内核 dbcore.TxManager（每连接一个）里：commit/rollback 即摘除、
// 执行期摘除再插入以串行化、空闲 30 分钟清扫、容量 1024，DB 调用一律在注册表锁外。
// 该语义曾是本包的手工实现（P0-2），Option B 后收敛回内核单一来源；
// 这里只保留内核 TransactionInfo 不承载的线语义元数据。
type txMeta struct {
	connID         string
	startedAt      time.Time
	isolationLevel protocol.IsolationLevel
}

type AppCore struct {
	repo        *storage.ConnectionRepository
	kr          keyring.Keyring
	mu          sync.RWMutex
	connections map[string]dbcore.Connection
	tunnels     map[string]*sshtunnel.Tunnel
	// txManagers 每个已开连接一个事务注册表；BeginTransaction 懒建，Disconnect 关闭。
	txManagers map[string]*bridge.TxManager
	// txMeta 事务 ID → 线语义元数据（内核 TransactionInfo 只含 ID/Status）。
	txMeta map[string]*txMeta
	// readOnly 记录已打开连接的只读标志（behavior.md §12.3）。
	// Connect 时从 ConnectionInfo.read_only 快照，Disconnect 时清除。
	readOnly map[string]bool
	// userDisconnected 记录用户显式断开的连接（PD-04，2026-11-09）。
	// 心跳（Ping）不得复活它——否则对不可达主机每 30s 重建一次 SSH 隧道+连接；
	// 显式 Connect 清除该意图。execute 的懒连接语义不变（首次查询仍可免显式
	// Connect），与 Rust 侧 user_disconnected 的判定范围严格一致。
	userDisconnected map[string]bool
	// knownHostsPath 为 SSH 主机密钥 known_hosts 文件（TOFU 首用校验，见 docs/ssh-tunnel.md）；
	// 空则不校验主机密钥（M8 原行为）。由宿主（server/tui main）经 SetKnownHostsPath 注入。
	// atomic.Value：宿主在服务启动前写入，Connect 在 HTTP goroutine 里读——
	// 跨 goroutine 的写读需要同步（见 SetKnownHostsPath）。
	knownHostsPath atomic.Value
	// blobs 是大字段旁路存储（spec/encoding.md §5.2）：New 时建句柄、
	// 启动清扫并注册到 dbcore（进程级 sink，覆盖本进程所有入口）。
	blobs *storage.BlobStore
	// connectSF 归并同一连接 id 的并发懒连接：两个 goroutine 同时 miss 时
	// 只让一个跑完整 Connect（含 SSH 隧道 + 拨号 + Ping），其余共享其结果，
	// 避免重复开隧道/连接后又被覆盖 Close（见 ensure）。
	connectSF singleflight.Group
	closeOnce sync.Once
}

func New(db *sql.DB, kr keyring.Keyring) *AppCore {
	blobs := storage.NewBlobStore(storage.DataDir())
	blobs.Sweep()
	dbcore.SetBlobSink(blobs)
	a := &AppCore{
		repo:             storage.NewConnectionRepository(db),
		kr:               kr,
		connections:      make(map[string]dbcore.Connection),
		tunnels:          make(map[string]*sshtunnel.Tunnel),
		txManagers:       make(map[string]*bridge.TxManager),
		txMeta:           make(map[string]*txMeta),
		readOnly:         make(map[string]bool),
		userDisconnected: make(map[string]bool),
		blobs:            blobs,
	}
	// 事务清扫随各连接的 TxManager 自带 sweeper 启动，AppCore 不再全局起 goroutine。
	return a
}

// BlobStore 返回大字段旁路存储（server 的 REST+Range 取回经此读取）。
func (a *AppCore) BlobStore() *storage.BlobStore { return a.blobs }

// GetBlob 是 §5.2 的进程内取回用例：blobID 为引用前缀后的 UUID，
// 非法 UUID 报 INVALID_PARAM，未知/已过期报 BLOB_NOT_FOUND。
func (a *AppCore) GetBlob(blobID string) ([]byte, error) {
	if _, err := uuid.Parse(blobID); err != nil {
		return nil, &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "invalid blob id: " + blobID}
	}
	data, err := a.blobs.Get(blobID)
	if errors.Is(err, storage.ErrBlobNotFound) {
		return nil, &protocol.PolyDBError{Code: protocol.ErrBlobNotFound, Message: "blob not found: " + blobID}
	}
	return data, err
}

// SetKnownHostsPath 指定 SSH known_hosts 文件路径；传空串保持 M8 的不校验行为。
//
// 宿主都在服务启动前调用（写后无并发写），但 Connect 在 HTTP goroutine 里读
// 同一字段——跨 goroutine 的写读是 Go 内存模型意义上的 data race（race
// detector 标记，且架构上允许宿主运行中改路径）。atomic.Value 消掉竞争，
// 语义不变：Set 后新 Connect 生效，进行中的连接不回溯。
func (a *AppCore) SetKnownHostsPath(path string) { a.knownHostsPath.Store(path) }

// KnownHostsPath 返回当前 known_hosts 路径（读侧与 SetKnownHostsPath 配对）。
func (a *AppCore) KnownHostsPath() string {
	p, _ := a.knownHostsPath.Load().(string)
	return p
}

// ─── 连接 CRUD ─────────────────────────────────────────────

func (a *AppCore) CreateConnection(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
	if err := a.storeConnSecrets(req.Password, &req.PasswordRef, "conn"); err != nil {
		return protocol.ConnectionInfo{}, err
	}
	req.Password = ""
	if err := a.storeSshSecrets(req.SSHTunnel); err != nil {
		return protocol.ConnectionInfo{}, err
	}
	return a.repo.Create(req)
}

func (a *AppCore) ListConnections() ([]protocol.ConnectionInfo, error) {
	return a.repo.List()
}

func (a *AppCore) GetConnection(id string) (protocol.ConnectionInfo, error) {
	return a.repo.Get(id)
}

func (a *AppCore) UpdateConnection(id string, req *protocol.UpdateConnectionRequest) (protocol.ConnectionInfo, error) {
	if req.Password != "" {
		ref := ""
		if req.PasswordRef != nil {
			ref = *req.PasswordRef
		}
		if err := a.storeConnSecrets(req.Password, &ref, "conn"); err != nil {
			return protocol.ConnectionInfo{}, err
		}
		req.PasswordRef = &ref
		req.Password = ""
	}
	if err := a.storeSshSecretsUpdate(id, req.SSHTunnel); err != nil {
		return protocol.ConnectionInfo{}, err
	}
	return a.repo.Update(id, req)
}

func (a *AppCore) DeleteConnection(id string) (bool, error) {
	a.Disconnect(id)
	// 先取回 secret 引用再删行：keyring 是独立存储，DELETE 不级联。
	// 不留这一步的话删除连接后 keyring 里仍残留可复用的明文密码
	// （PD-07，2026-11-09），等于把「删除」做成了只删元数据的假删除。
	refs, err := a.secretRefs(id)
	if err != nil {
		return false, err
	}
	ok, err := a.repo.Delete(id)
	if err != nil {
		return false, err
	}
	a.revokeSecrets(refs)
	return ok, nil
}

// secretRefs 收集连接在 keyring 里的全部凭据引用（连接密码 + SSH 密码/私钥口令）。
// 连接不存在时返回已收集到的空集合而不是错误——Delete 对不存在的 id 幂等返回 false。
func (a *AppCore) secretRefs(id string) ([]string, error) {
	var refs []string
	ref, err := a.repo.GetPasswordRef(id)
	switch {
	case err == nil && ref != "":
		refs = append(refs, ref)
	case err != nil && !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}
	info, err := a.repo.Get(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return refs, nil
		}
		return nil, err
	}
	if info.SSHTunnel != nil {
		if info.SSHTunnel.PasswordRef != "" {
			refs = append(refs, info.SSHTunnel.PasswordRef)
		}
		if info.SSHTunnel.PrivateKeyPassphraseRef != "" {
			refs = append(refs, info.SSHTunnel.PrivateKeyPassphraseRef)
		}
	}
	return refs, nil
}

// revokeSecrets 摘除 keyring 里的凭据；删除失败**不**向上传播（不掩盖行删除
// 结果）。凭据残留可被后续对同一 ref 的 Set 覆盖，故这里静默是有意的；
// 调用方已保证 ref 只在此刻之后不再被引用（Create 的一次性明文已清空）。
func (a *AppCore) revokeSecrets(refs []string) {
	for _, r := range refs {
		if r == "" {
			continue
		}
		_ = a.kr.Delete(r)
	}
}

// ─── 连通性 ────────────────────────────────────────────────

func (a *AppCore) Connect(ctx context.Context, id string) error {
	// 显式 connect 清除「用户断开」意图：心跳因此得以恢复工作（PD-04，
	// 与 Rust connect 首行的 user_disconnected.remove 镜像）。
	a.mu.Lock()
	delete(a.userDisconnected, id)
	a.mu.Unlock()
	info, err := a.repo.Get(id)
	if err != nil {
		return err
	}

	// password_ref 不随 ConnectionInfo 下发，单独从存储查询后到 keyring 取明文。
	ref, err := a.repo.GetPasswordRef(id)
	if err != nil {
		return err
	}
	password, err := a.secret(ref)
	if err != nil {
		return err
	}

	connInfo := info
	var tunnel *sshtunnel.Tunnel
	if info.SSHTunnel != nil && driverSpecFor(info.Kind).needsHost {
		sshPwd, err := a.secret(info.SSHTunnel.PasswordRef)
		if err != nil {
			return err
		}
		passphrase, err := a.secret(info.SSHTunnel.PrivateKeyPassphraseRef)
		if err != nil {
			return err
		}
		targetHost, targetPort := targetHostPort(info)
		cfg := *info.SSHTunnel
		cfg.PrivateKeyPassphrase = passphrase
		if cfg.Port == 0 {
			cfg.Port = 22 // OpenSSH 默认端口（known_hosts 校验与拨号一致）
		}
		tunnel, err = sshtunnel.Open(ctx, &cfg, targetHost, targetPort, sshPwd, a.KnownHostsPath())
		if err != nil {
			return &protocol.PolyDBError{Code: protocol.ErrSSHTunnelFailed, Message: err.Error(), Retryable: true}
		}
		host, port, err := net.SplitHostPort(tunnel.LocalAddr())
		if err != nil {
			_ = tunnel.Close()
			return &protocol.PolyDBError{Code: protocol.ErrSSHTunnelFailed, Message: err.Error(), Retryable: true}
		}
		connInfo.Host = host
		connInfo.Port = atoi(port)
	}

	// PD-01/PD-02（2026-11-09）：按 kind 查驱动分派注册表打开连接，删除手写
	// switch。注册表由各驱动包 init() 登记（见 pkg/dbcore/registry.go），新增引擎
	// 退化为「驱动包登记一项 + driverSpecs 补一项元数据」。DSN/地址方言知识已
	// 下沉到各驱动包，appcore 不再持有任何 kind 专属的打开逻辑。
	d, err2 := dbcore.OpenDriver(ctx, info.Kind, connInfo, password)
	if err2 != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		if errors.Is(err2, dbcore.ErrDriverNotRegistered) {
			return &protocol.PolyDBError{Code: protocol.ErrNotSupported, Message: err2.Error()}
		}
		return &protocol.PolyDBError{Code: protocol.ErrConnectionFailed, Message: err2.Error(), Retryable: true}
	}
	if err := d.Ping(ctx); err != nil {
		_ = d.Close()
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return &protocol.PolyDBError{Code: protocol.ErrConnectionFailed, Message: err.Error(), Retryable: true}
	}

	a.mu.Lock()
	// 覆盖写入前必须摘掉被顶替的句柄：并发懒连接时两个 goroutine 都会打开驱动，
	// 谁后写谁留下，前一个若不 Close 就永久泄漏（Go 没有 Drop，Rust 侧靠 insert
	// 覆盖旧值自动释放）。隧道同理——它只服务于那个已关闭的连接。
	// Close 与事务回滚都是 DB I/O（sql.DB.Close 会等在途查询），与 Disconnect
	// 同一纪律：锁内只摘句柄，锁外收尾，否则一条慢查询让所有连接的全部操作
	// 在 a.mu 上排队。被顶替连接的事务注册表一并收掉——TxManager 在创建时把
	// driver 钉死，重连后复用旧注册表会把新事务开在已关闭的驱动上。
	var staleDriver dbcore.Driver
	var staleTunnel *sshtunnel.Tunnel
	var staleMgr *bridge.TxManager
	if prev, ok := a.connections[id]; ok {
		staleDriver = prev.Driver()
	}
	if prevTunnel, ok := a.tunnels[id]; ok && prevTunnel != nil {
		staleTunnel = prevTunnel
		delete(a.tunnels, id)
	}
	if m, ok := a.txManagers[id]; ok {
		staleMgr = m
		delete(a.txManagers, id)
	}
	for tid, m := range a.txMeta {
		if m.connID == id {
			delete(a.txMeta, tid)
		}
	}
	a.connections[id] = dbcore.NewConnection(d)
	a.readOnly[id] = info.ReadOnly != nil && *info.ReadOnly
	if tunnel != nil {
		a.tunnels[id] = tunnel
	}
	a.mu.Unlock()

	if staleMgr != nil {
		staleMgr.RollbackAll()
		staleMgr.Close()
	}
	if staleDriver != nil {
		_ = staleDriver.Close()
	}
	if staleTunnel != nil {
		_ = staleTunnel.Close()
	}
	return nil
}

// isReadOnly 报告连接是否以只读模式打开。未连接的连接按其存储配置判定，
// 这样 execute 前的懒连接路径也能被正确拦截。
func (a *AppCore) isReadOnly(id string) bool {
	a.mu.RLock()
	ro, ok := a.readOnly[id]
	a.mu.RUnlock()
	if ok {
		return ro
	}
	info, err := a.repo.Get(id)
	if err != nil {
		return false
	}
	return info.ReadOnly != nil && *info.ReadOnly
}

// connectionKind 返回连接的方言，用于挑选词法剖面。
//
// 已连接时取驱动上报的 kind（唯一权威）；未连接时取存储配置里的 kind——
// 懒连接路径（ensure 之前）也要能判定方言，否则只读闸门会在未知方言下
// 退化成「取各剖面并集」，把合法的只读查询也误拦。
func (a *AppCore) connectionKind(id string) protocol.DatabaseKind {
	a.mu.RLock()
	c, ok := a.connections[id]
	a.mu.RUnlock()
	if ok {
		return c.Kind()
	}
	info, err := a.repo.Get(id)
	if err != nil {
		return ""
	}
	return info.Kind
}

// rejectWriteOnReadOnly 按 behavior.md §12.3 拦截只读连接上的写语句：
// 对整条语句（含 WITH 子树与多语句）做变更关键字扫描（dbcore.MutatesData），
// 命中即返回 POLYDB_ERR_READ_ONLY。不再复用 DetectStatementType 的判型——
// 数据修改 CTE（WITH…DELETE/UPDATE/INSERT）与 TRUNCATE/GRANT/CALL 等会被
// 判成 select/other 而漏拦。
//
// kind 是连接的方言：词法差异（'#' 行注释、反斜杠转义、PG 的 dollar-quote、
// Oracle 的 q'…'、MySQL 的 /*! … */）决定「字面量在哪结束」。认错方言会把
// 后面的写语句当成字面量吞掉——那是 fail-open，直接丢数据。
func rejectWriteOnReadOnly(sqlText string, kind protocol.DatabaseKind) error {
	if dbcore.MutatesData(sqlText, kind) {
		return &protocol.PolyDBError{Code: protocol.ErrReadOnly, Message: "connection is read-only: write statements are rejected"}
	}
	return nil
}

func (a *AppCore) Disconnect(id string) {
	a.mu.Lock()
	var driver dbcore.Driver
	if c, ok := a.connections[id]; ok {
		driver = c.Driver()
		delete(a.connections, id)
	}
	delete(a.readOnly, id)
	// PD-04：标记用户显式断开。心跳（Ping）据此拒绝复活，避免对不可达主机
	// 周期性重建 SSH 隧道+连接；execute 的懒连接语义不变。
	a.userDisconnected[id] = true
	var tunnel *sshtunnel.Tunnel
	if t, ok := a.tunnels[id]; ok {
		tunnel = t
		delete(a.tunnels, id)
	}
	mgr := a.txManagers[id]
	delete(a.txManagers, id)
	for tid, m := range a.txMeta {
		if m.connID == id {
			delete(a.txMeta, tid)
		}
	}
	a.mu.Unlock()

	// 锁外收尾：回滚该连接上仍活跃的事务（避免 *sql.Tx 泄漏）并停掉其清扫
	// goroutine，随后关驱动与隧道。这些都是 DB I/O（sql.DB.Close 会等在途
	// 查询），不能抱着 AppCore 锁做——这也是 Disconnect 不再用 defer Unlock
	// 的原因。
	if mgr != nil {
		mgr.RollbackAll()
		mgr.Close()
	}
	if driver != nil {
		_ = driver.Close()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
}

// IsConnected 报告指定连接当前是否已打开驱动实例（进程内前端用于避免重复 Connect 重置驱动）。
func (a *AppCore) IsConnected(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.connections[id]
	return ok
}

func (a *AppCore) ConnectionStatus(id string) (protocol.ConnectionStatus, error) {
	if _, err := a.repo.Get(id); err != nil {
		return protocol.ConnectionStatus{}, err
	}
	a.mu.RLock()
	_, connected := a.connections[id]
	a.mu.RUnlock()
	return protocol.ConnectionStatus{ID: id, Connected: connected}, nil
}

func (a *AppCore) Ping(ctx context.Context, id string) error {
	// 心跳是断开连接事实上的重连器：ping 不得复活用户主动断开的连接
	// （PD-04，对不可达主机每 30s 重建一次 SSH 隧道+连接）。显式 Connect 恢复；
	// execute 的懒连接语义保持不变（首次查询仍可免显式 Connect）。
	// 与 Rust ping 首行的 user_disconnected 判定镜像。
	if a.isUserDisconnected(id) {
		return &protocol.PolyDBError{Code: protocol.ErrConnectionFailed, Message: "connection is disconnected"}
	}
	c, err := a.ensure(ctx, id)
	if err != nil {
		return err
	}
	return c.Driver().Ping(ctx)
}

// isUserDisconnected 报告连接是否被用户显式断开且尚未显式重连。
func (a *AppCore) isUserDisconnected(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.userDisconnected[id]
}

// ensure 懒连接：已连接直接返回，否则按保存的连接信息打开驱动。
// 查询/元数据入口都应先经 ensure，保证未显式 /test 也能工作。
//
// 并发首次使用同一 id 时用 singleflight 归并：只让一个 goroutine 跑完整
// Connect（SSH 隧道 + 拨号 + Ping 都是慢操作），其余共享其结果。
// 没有它时两个 goroutine 各开一条隧道/连接，后进者覆盖先到者再 Close，
// 结果虽不泄漏却白做一遍（还会让先到者的调用方拿到"连接失败"的假错误）。
func (a *AppCore) ensure(ctx context.Context, id string) (dbcore.Connection, error) {
	a.mu.RLock()
	c, ok := a.connections[id]
	a.mu.RUnlock()
	if ok {
		return c, nil
	}
	// flight 内二次检查：RLock 未命中之后、进入 Do 之前可能已被别的
	// flight 或显式 Connect 建好。
	_, err, _ := a.connectSF.Do(id, func() (any, error) {
		a.mu.RLock()
		_, ok := a.connections[id]
		a.mu.RUnlock()
		if ok {
			return nil, nil
		}
		return nil, a.Connect(ctx, id)
	})
	if err != nil {
		return dbcore.Connection{}, err
	}
	return a.get(id)
}

func (a *AppCore) get(id string) (dbcore.Connection, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c, ok := a.connections[id]
	if !ok {
		return dbcore.Connection{}, &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection not found or not connected: " + id}
	}
	return c, nil
}

func (a *AppCore) sqlDriver(ctx context.Context, id string) (dbcore.SQLDriver, error) {
	c, err := a.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.AsSQL()
}

// ─── 查询 ──────────────────────────────────────────────────

func (a *AppCore) Execute(ctx context.Context, id, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	if a.isReadOnly(id) {
		if err := rejectWriteOnReadOnly(sql, a.connectionKind(id)); err != nil {
			return nil, err
		}
	}
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.Execute(ctx, sql, args...)
}

// ─── 事务 ──────────────────────────────────────────────────

// sqlTxNotFound 把内核的 ErrTransactionNotFound 统一映射为 protocol 错误码
// （spec §10 与 Rust 侧 TransactionNotFound 一致：查不到就是没有该事务，
// 而非事务执行失败）。
func sqlTxNotFound(txID string) error {
	return &protocol.PolyDBError{Code: protocol.ErrTransactionNotFound, Message: "transaction not found: " + txID}
}

// txContext 取事务的线语义元数据与其所属连接的事务注册表；
// 元数据不存在（未 begin / 已 finalize / 连接已断开）即 NotFound。
func (a *AppCore) txContext(txID string) (*txMeta, *bridge.TxManager, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	meta, ok := a.txMeta[txID]
	if !ok {
		return nil, nil, sqlTxNotFound(txID)
	}
	mgr := a.txManagers[meta.connID]
	if mgr == nil {
		return nil, nil, sqlTxNotFound(txID)
	}
	return meta, mgr, nil
}

// txManagerFor 返回连接的事务注册表，懒建（内核 TxManager 自带清扫 goroutine，
// Disconnect 时 Close）。持有 a.mu 写锁时调用。
func (a *AppCore) txManagerForLocked(connID string, txD dbc.SQLTxDriver) *bridge.TxManager {
	if m, ok := a.txManagers[connID]; ok {
		return m
	}
	m := bridge.NewTxManager(txD)
	a.txManagers[connID] = m
	return m
}

// BeginTransaction 在指定连接上开启一个新的事务。要求对应驱动暴露内核原生
// 事务驱动（bridge.SQLTxProvider，Option B）；注册表是 dbcore.TxManager，
// DB 调用在其注册表锁外执行。
// 返回的 TransactionInfo 是前端后续 ExecuteInTx / CommitTransaction / RollbackTransaction
// 的凭据；进程内 tx 由 TxManager 持有，前端无需理解 *sql.Tx。
//
// 内部 Begin 使用 context.Background() 而非调用方传入的 ctx：事务的生命周期跨越 HTTP
// 请求边界（begin 之后的 execute / commit / rollback 是独立请求），若绑定到 r.Context()
// 则请求一返回 ctx 即被取消，部分驱动（如 modernc.org/sqlite 通过 TxOptions 挂监听）
// 会在 ctx 取消后自动回滚刚建的事务。调用方传入的 ctx 仍用于驱动查找（ensure 可能触发
// 懒连接），但不会绑定到 *sql.Tx 生命周期。
func (a *AppCore) BeginTransaction(ctx context.Context, req *protocol.BeginTransactionRequest) (*protocol.TransactionInfo, error) {
	d, err := a.sqlDriver(ctx, req.ConnectionID)
	if err != nil {
		return nil, err
	}
	prov, ok := d.(bridge.SQLTxProvider)
	if !ok {
		return nil, &protocol.PolyDBError{Code: protocol.ErrNotSupported, Message: "driver does not support transactions for connection " + req.ConnectionID}
	}
	now := time.Now()
	a.mu.Lock()
	mgr := a.txManagerForLocked(req.ConnectionID, prov.SQLTx())
	a.mu.Unlock()
	info, err := mgr.Begin(context.Background(), dbc.TxMode{IsolationLevel: string(req.IsolationLevel)})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	// Begin 在锁外做 DB I/O，期间 Disconnect 可能已把连接收走（注册表已
	// RollbackAll+Close）。txMeta 不能无条件重插——那会留下一条永不清理的
	// 孤儿记录和一个挂在已关驱动上的 *sql.Tx。复查失败就回滚刚开的事务。
	if _, live := a.connections[req.ConnectionID]; !live {
		a.mu.Unlock()
		_ = mgr.Rollback(info.ID)
		return nil, &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection closed while beginning transaction: " + req.ConnectionID}
	}
	a.txMeta[info.ID] = &txMeta{
		connID:         req.ConnectionID,
		startedAt:      now,
		isolationLevel: req.IsolationLevel,
	}
	a.mu.Unlock()
	return &protocol.TransactionInfo{
		ID:             info.ID,
		ConnectionID:   req.ConnectionID,
		Status:         protocol.TxnActive,
		StartedAt:      now,
		IsolationLevel: req.IsolationLevel,
	}, nil
}

// CommitTransaction 提交指定事务；已 finalize 的事务返回 POLYDB_ERR_TRANSACTION_NOT_FOUND。
// 事务对象 finalize 后即从注册表移除，语义与"事务只成功 finalize 一次"一致（见 spec §10）。
// 提交的 DB I/O 在 TxManager 注册表锁外、也在 AppCore 锁外——慢提交不再拖住其他事务
// （这正是把注册表收敛进内核的原因之一）。
func (a *AppCore) CommitTransaction(txID string) (*protocol.TransactionInfo, error) {
	meta, mgr, err := a.txContext(txID)
	if err != nil {
		return nil, err
	}
	if err := mgr.Commit(txID); err != nil {
		if errors.Is(err, dbc.ErrTransactionNotFound) {
			return nil, sqlTxNotFound(txID)
		}
		a.mu.Lock()
		delete(a.txMeta, txID)
		a.mu.Unlock()
		return nil, &protocol.PolyDBError{Code: protocol.ErrTransactionFailed, Message: "commit failed: " + err.Error()}
	}
	a.mu.Lock()
	info := &protocol.TransactionInfo{
		ID:             txID,
		ConnectionID:   meta.connID,
		Status:         protocol.TxnCommitted,
		StartedAt:      meta.startedAt,
		IsolationLevel: meta.isolationLevel,
	}
	delete(a.txMeta, txID)
	a.mu.Unlock()
	return info, nil
}

// RollbackTransaction 回滚指定事务；已 finalize 的事务返回 POLYDB_ERR_TRANSACTION_NOT_FOUND。
func (a *AppCore) RollbackTransaction(txID string) (*protocol.TransactionInfo, error) {
	meta, mgr, err := a.txContext(txID)
	if err != nil {
		return nil, err
	}
	if err := mgr.Rollback(txID); err != nil {
		if errors.Is(err, dbc.ErrTransactionNotFound) {
			return nil, sqlTxNotFound(txID)
		}
		a.mu.Lock()
		delete(a.txMeta, txID)
		a.mu.Unlock()
		return nil, &protocol.PolyDBError{Code: protocol.ErrTransactionFailed, Message: "rollback failed: " + err.Error()}
	}
	a.mu.Lock()
	info := &protocol.TransactionInfo{
		ID:             txID,
		ConnectionID:   meta.connID,
		Status:         protocol.TxnRolledBack,
		StartedAt:      meta.startedAt,
		IsolationLevel: meta.isolationLevel,
	}
	delete(a.txMeta, txID)
	a.mu.Unlock()
	return info, nil
}

// ExecuteInTx 在指定事务内执行一条 SQL。事务必须处于 active 状态；
// 否则返回 POLYDB_ERR_TRANSACTION_NOT_FOUND。
//
// 执行期间的串行化由内核 TxManager 的 remove/reinsert 保证：spec §10 要求事务内
// 查询串行，而 *sql.Tx 底下只有一条连接，两个 goroutine 同时 ExecuteIn 会交错乃至
// 互相踩坏结果。借出后并发的那一次只会拿到 NOT_FOUND，不会再并发驱动同一个句柄。
// SQL 失败事务保持 active，成败都放回注册表。
func (a *AppCore) ExecuteInTx(ctx context.Context, txID, sql string, args ...protocol.Value) (*protocol.QueryResult, error) {
	meta, mgr, err := a.txContext(txID)
	if err != nil {
		return nil, err
	}
	kind := a.connectionKind(meta.connID)
	if a.isReadOnly(meta.connID) {
		if err := rejectWriteOnReadOnly(sql, kind); err != nil {
			return nil, err
		}
	}
	dbArgs := make([]dbc.Value, len(args))
	for i, v := range args {
		dbArgs[i] = bridge.ValueToDB(v)
	}
	start := time.Now()
	res, err := mgr.ExecuteInTx(ctx, txID, sql, dbArgs...)
	if err != nil {
		if errors.Is(err, dbc.ErrTransactionNotFound) {
			return nil, sqlTxNotFound(txID)
		}
		return nil, err
	}
	out := bridge.QueryResultFromDB(res)
	// 内核 QueryResult 不承载线语义字段，边界补齐（与各驱动 protocol 管线一致）。
	out.ExecutionTimeMs = float64(time.Since(start).Microseconds()) / 1000.0
	out.StatementType = dbcore.DetectStatementType(sql, kind)
	return out, nil
}

// Close 停掉所有连接的事务清扫 goroutine（幂等）。宿主（server / tui / mcp main）
// 在进程退出时调用。
func (a *AppCore) Close() {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		managers := make([]*bridge.TxManager, 0, len(a.txManagers))
		for _, m := range a.txManagers {
			managers = append(managers, m)
		}
		a.mu.Unlock()
		for _, m := range managers {
			m.Close()
		}
		// PD-03（2026-11-09）：销毁 AppCore 时摘掉进程级旁路写入端。此前只在
		// New 里 set 从不 clear，销毁后全局 sink 仍指向已释放的 BlobStore（悬垂），
		// 多实例互相覆盖，且新实例的 blob 可能写进旧实例的目录。
		// PD-38（2026-11-11）：改用 ClearBlobSink（递减引用计数，归零才清除），
		// 与 Rust 侧 Drop::drop 的 clear_blob_sink 严格镜像——此前 SetBlobSink(nil)
		// 直接清空，会误清其他存活实例的 sink。
		dbcore.ClearBlobSink()
	})
}

// GetTransactionInfo 返回当前事务的快照（供前端在编辑器上显示状态徽章等）。
func (a *AppCore) GetTransactionInfo(txID string) (*protocol.TransactionInfo, error) {
	meta, mgr, err := a.txContext(txID)
	if err != nil {
		return nil, err
	}
	info, err := mgr.Info(txID)
	if err != nil {
		return nil, sqlTxNotFound(txID)
	}
	return &protocol.TransactionInfo{
		ID:             txID,
		ConnectionID:   meta.connID,
		Status:         protocol.TransactionStatus(info.Status),
		StartedAt:      meta.startedAt,
		IsolationLevel: meta.isolationLevel,
	}, nil
}

func (a *AppCore) ListSchemas(ctx context.Context, id string) ([]protocol.SchemaInfo, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ListSchemas(ctx)
}

func (a *AppCore) ListTables(ctx context.Context, id, schema string) ([]protocol.TableInfo, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ListTables(ctx, schema)
}

func (a *AppCore) ListColumns(ctx context.Context, id, schema, table string) ([]protocol.ColumnInfo, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ListColumns(ctx, schema, table)
}

func (a *AppCore) ListIndexes(ctx context.Context, id, schema, table string) ([]protocol.IndexInfo, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ListIndexes(ctx, schema, table)
}

func (a *AppCore) ListForeignKeys(ctx context.Context, id, schema, table string) ([]protocol.ForeignKeyInfo, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ListForeignKeys(ctx, schema, table)
}

func (a *AppCore) CreateTableSQL(ctx context.Context, id, schema, table string) (string, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return "", err
	}
	return d.CreateTableSQL(ctx, schema, table)
}

// ─── Redis KV ────────────────────────────────────────────────

func (a *AppCore) kvDriver(ctx context.Context, id string) (dbcore.KVDriver, error) {
	c, err := a.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.AsKV()
}

func (a *AppCore) SelectDB(ctx context.Context, id string, index int) error {
	d, err := a.kvDriver(ctx, id)
	if err != nil {
		return err
	}
	return d.SelectDB(ctx, index)
}

func (a *AppCore) ScanKeys(ctx context.Context, id string, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	d, err := a.kvDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.ScanKeys(ctx, cursor, pattern, count)
}

func (a *AppCore) GetValue(ctx context.Context, id, key string) (protocol.RedisValue, error) {
	d, err := a.kvDriver(ctx, id)
	if err != nil {
		return protocol.RedisValue{}, err
	}
	return d.GetValue(ctx, key)
}

func (a *AppCore) SetValue(ctx context.Context, id, key string, value protocol.RedisValue) error {
	if a.isReadOnly(id) {
		return &protocol.PolyDBError{Code: protocol.ErrReadOnly, Message: "connection is read-only: KV writes are rejected"}
	}
	d, err := a.kvDriver(ctx, id)
	if err != nil {
		return err
	}
	return d.SetValue(ctx, key, value)
}

func (a *AppCore) ExecCommand(ctx context.Context, id string, args []string) (protocol.RedisReply, error) {
	if a.isReadOnly(id) {
		return protocol.RedisReply{}, &protocol.PolyDBError{Code: protocol.ErrReadOnly, Message: "connection is read-only: KV commands are rejected"}
	}
	d, err := a.kvDriver(ctx, id)
	if err != nil {
		return protocol.RedisReply{}, err
	}
	return d.ExecCommand(ctx, args)
}

// ─── 表数据浏览（M11，behavior.md §13）───────────────────────

// BrowseRows 按表浏览行。只读操作，不受 read_only 影响。
func (a *AppCore) BrowseRows(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (*protocol.TableRowsResult, error) {
	if req == nil {
		req = &protocol.TableRowsRequest{}
	}
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.BrowseRows(ctx, schema, table, req)
}

// BrowseRowsCount 对同条件执行精确 COUNT(*)。
func (a *AppCore) BrowseRowsCount(ctx context.Context, id, schema, table string, req *protocol.TableRowsRequest) (uint64, error) {
	if req == nil {
		req = &protocol.TableRowsRequest{}
	}
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return 0, err
	}
	return d.BrowseRowsCount(ctx, schema, table, req)
}

// ─── 辅助 ──────────────────────────────────────────────────

// secret 从 keyring 取回机密；ref 为空返回空串（连接可能不需要密码）。
func (a *AppCore) secret(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	s, err := a.kr.Get(ref)
	if err != nil {
		return "", &protocol.PolyDBError{Code: protocol.ErrKeyringFailed, Message: err.Error()}
	}
	return s, nil
}

// storeConnSecrets 把一次性明文密码写入 keyring 并回填 ref（secret 为空则不动 ref）。
func (a *AppCore) storeConnSecrets(secret string, ref *string, scope string) error {
	if secret == "" {
		return nil
	}
	r := *ref
	if r == "" {
		r = keyring.Ref(scope)
	}
	if err := a.kr.Set(r, secret); err != nil {
		return &protocol.PolyDBError{Code: protocol.ErrKeyringFailed, Message: err.Error()}
	}
	*ref = r
	return nil
}

// storeSshSecrets 处理创建请求的 SSH 一次性明文（密码/私钥口令）并清空明文。
func (a *AppCore) storeSshSecrets(ssh *protocol.SshTunnelConfig) error {
	if ssh == nil {
		return nil
	}
	if err := a.storeConnSecrets(ssh.Password, &ssh.PasswordRef, "ssh"); err != nil {
		return err
	}
	ssh.Password = ""
	if err := a.storeConnSecrets(ssh.PrivateKeyPassphrase, &ssh.PrivateKeyPassphraseRef, "ssh-pass"); err != nil {
		return err
	}
	ssh.PrivateKeyPassphrase = ""
	return nil
}

// storeSshSecretsUpdate 处理更新请求的 SSH 一次性明文；复用已有 ref（改密码时保留同一把 key）。
func (a *AppCore) storeSshSecretsUpdate(id string, ssh *protocol.SshTunnelConfig) error {
	if ssh == nil {
		return nil
	}
	existing, err := a.repo.Get(id)
	if err != nil {
		return err
	}
	if ssh.Password != "" {
		ref := ssh.PasswordRef
		if ref == "" && existing.SSHTunnel != nil {
			ref = existing.SSHTunnel.PasswordRef
		}
		if err := a.storeConnSecrets(ssh.Password, &ref, "ssh"); err != nil {
			return err
		}
		ssh.PasswordRef = ref
		ssh.Password = ""
	}
	if ssh.PrivateKeyPassphrase != "" {
		ref := ssh.PrivateKeyPassphraseRef
		if ref == "" && existing.SSHTunnel != nil {
			ref = existing.SSHTunnel.PrivateKeyPassphraseRef
		}
		if err := a.storeConnSecrets(ssh.PrivateKeyPassphrase, &ref, "ssh-pass"); err != nil {
			return err
		}
		ssh.PrivateKeyPassphraseRef = ref
		ssh.PrivateKeyPassphrase = ""
	}
	return nil
}

// targetHostPort 返回隧道目标地址（含各驱动默认端口）。
func targetHostPort(info protocol.ConnectionInfo) (string, int) {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	if port == 0 {
		port = driverSpecFor(info.Kind).defaultPort
	}
	return host, port
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// DSN / 地址的方言知识已下沉到各驱动包的 registry.go（PD-01/PD-02）；
// appcore 只保留与 kind 无关的通用工具。
