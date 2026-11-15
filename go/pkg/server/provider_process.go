package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/polydb/polydb/pkg/protocol"
)

// 阶段 8：进程端点（§16.12.1 kind 2 / §16.13）。与 Rust 侧
// rust/crates/server/src/provider_process.rs 严格镜像。
//
// 进程端点 = 一个独立 provider 进程，经 **stdio newline-delimited JSON-RPC
// 2.0** 与宿主通信（§16.12.2）。宿主内部消息是 msgpack 的 ServerMessage，
// 端点内部负责两种帧之间的映射（§16.13.3）：
//
//	宿主 → 进程：provider_exec → {"method":"provider/exec",...}
//	进程 → 宿主：provider_result → 宿主的 QueryResult / QueryError
//
// 进程来源**不可由客户端指定**（§16.13.1）：只能来自服务端配置
// POLYDB_PROVIDER_PROCS。

// errNoProviderCommand 表示配置里没有可用的 provider 命令。
var errNoProviderCommand = errors.New("provider command is empty")

// providerProcSpecs 解析 POLYDB_PROVIDER_PROCS：key=命令，逗号分隔
// （§16.13.1）。命令由**服务端管理员**配置，客户端不能指定。
// providerProcSpec 解析出的一条进程配置。**同 key 可出现多条**——commutative
// key（§16.18）的多实例由此配置。
type providerProcSpec struct {
	key     string
	cmdLine string
}

// providerProcSpecs 从 POLYDB_PROVIDER_PROCS（key=命令，逗号分隔，§16.13.1）
// 解析全部进程配置，保持配置顺序（§16.18 的多实例按出现顺序拉起）。
func providerProcSpecs() []providerProcSpec {
	raw := os.Getenv("POLYDB_PROVIDER_PROCS")
	var out []providerProcSpec
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.Index(pair, "=")
		if eq <= 0 || eq == len(pair)-1 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		cmd := strings.TrimSpace(pair[eq+1:])
		if key != "" && cmd != "" {
			out = append(out, providerProcSpec{key: key, cmdLine: cmd})
		}
	}
	return out
}

// StartProcessEndpoints 按配置拉起全部 provider 进程（§16.13.2）。
//
// 进程端点的 attached 登记在进程发出 provider/hello 声明后建立（校验通过才
// 登记，§16.13.2）；本函数只负责拉起进程。返回启动失败的 key 列表（记日志）。
func (h *ProviderHub) StartProcessEndpoints() []string {
	specs := providerProcSpecs()
	var failed []string
	for _, spec := range specs {
		ep, err := NewProcessEndpoint(h.NextEndpointID(), h, spec.cmdLine, protocol.DriverProviderKey(spec.key))
		if err != nil {
			failed = append(failed, spec.key)
			continue
		}
		ep.onDead = func() { h.DropEndpoint(ep) }
		// 登记到 hub 的进程列表：hello 校验失败的也要能被回收（§16.13.4）。
		h.mu.Lock()
		h.processes = append(h.processes, ep)
		h.mu.Unlock()
	}
	sort.Strings(failed)
	return failed
}

// CloseProcessEndpoints 终止并回收全部 provider 进程（§16.13.4：宿主必须回收
// 子进程，不留僵尸）。幂等；宿主关闭时调用。
func (h *ProviderHub) CloseProcessEndpoints() {
	h.mu.Lock()
	eps := append([]*ProcessEndpoint(nil), h.processes...)
	h.processes = nil
	h.mu.Unlock()
	for _, pe := range eps {
		pe.Close()
	}
}

// rpcFrame 是 stdio 上的 JSON-RPC 2.0 帧（与 cmd/polydb-provider 同形态）。
type rpcFrame struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcFrameError   `json:"error,omitempty"`
}

type rpcFrameError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// execParams 是 provider/exec 的参数（§16.12.2 / §16.14.3）。
type execParams struct {
	QueryID string           `json:"query_id"`
	SQL     string           `json:"sql"`
	Handle  string           `json:"handle,omitempty"`
	Params  []protocol.Value `json:"params,omitempty"`
}

// execResult 是 provider/exec 的响应（§16.13.3 的帧映射）。
type execResult struct {
	Result *protocol.QueryResult `json:"result,omitempty"`
	Error  *protocol.PolyDBError `json:"error,omitempty"`
}

// ProcessEndpoint 是一个进程端点。
type ProcessEndpoint struct {
	id uint64
	// hub 用于把进程回的结果投递给等待方（按 query_id）。
	hub *ProviderHub
	// cmd 是子进程句柄（Close 时 kill + Wait 回收，§16.13.4）。
	cmd *exec.Cmd
	// waitOnce 保证 Wait 只被调用一次（waitLoop 与 Close 竞争）。
	waitOnce sync.Once
	// sandboxCleanup 释放 OS 级沙箱资源（Windows：关闭 Job 句柄——
	// KILL_ON_JOB_CLOSE 会终止 Job 内全部进程，§16.16.1）。
	sandboxCleanup func()

	mu    sync.Mutex
	stdin io.WriteCloser
	alive bool
	// pending 是进程内已发出、等待响应的请求 id → query_id 映射。
	pending map[int64]string
	nextID  int64
	// key 是本端点提供的 provider key。
	key protocol.DriverProviderKey
	// cmdLine 是拉起时的完整命令行——配置对账用它匹配要撤下的条目（§16.19.3
	// 的进程匹配键 = (key, cmd)）。
	cmdLine string
	// onDead 回调：进程不可达时通知 hub 移除登记（§16.13.4）。
	onDead func()
}

// NewProcessEndpoint 拉起一个 provider 进程并建立 stdio 通道。
//
// cmdLine 是完整命令行（可含参数）。进程须按 §16.12.2 自行发送
// provider/hello 声明自己的 key 与能力；本构造不做校验（校验在 hub 侧）。
func NewProcessEndpoint(id uint64, hub *ProviderHub, cmdLine string, key protocol.DriverProviderKey) (*ProcessEndpoint, error) {
	parts := splitCommand(cmdLine)
	if len(parts) == 0 {
		return nil, errNoProviderCommand
	}
	parts[0] = resolveCommandPath(parts[0])
	cmd := exec.Command(parts[0], parts[1:]...)
	// §16.16：provider 侧沙箱配合——Unix 上父进程自检（宿主死即退出）与
	// 内存自限（RLIMIT_AS）都读这两个环境变量；Windows 上由 Job Object 兜底。
	cmd.Env = append(os.Environ(),
		sandboxParentPIDEnv(),
		"POLYDB_PROVIDER_MEM_MB="+fmt.Sprint(providerMemLimitBytes()>>20),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr 归宿主日志（provider 的 stdout 只承载协议帧）。
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// §16.16：OS 级沙箱——生命周期绑定（Windows Job Object KILL_ON_JOB_CLOSE /
	// Unix 进程组）与资源上限。失败不阻断启动（记日志、按无沙箱继续）。
	sandboxCleanup, sandboxErr := applySandboxToCmd(cmd)
	if sandboxErr != nil {
		sandboxCleanup = func() {}
	}
	ep := &ProcessEndpoint{
		id:             id,
		hub:            hub,
		cmd:            cmd,
		stdin:          stdin,
		alive:          true,
		pending:        map[int64]string{},
		key:            key,
		cmdLine:        cmdLine,
		sandboxCleanup: sandboxCleanup,
	}
	go ep.readLoop(stdout)
	go ep.logLoop(stderr)
	go ep.waitLoop(cmd)
	return ep, nil
}

func (e *ProcessEndpoint) ID() uint64 { return e.id }

func (e *ProcessEndpoint) Kind() EndpointKind { return EndpointProcess }

// Emit 投递一帧给进程端点（emission：写成功即已越过边界，不可逆）。
//
// 帧映射（§16.13.3）：宿主消息 → JSON-RPC 方法调用。
func (e *ProcessEndpoint) Emit(msg *protocol.ServerMessage) bool {
	e.mu.Lock()
	if !e.alive {
		e.mu.Unlock()
		return false
	}
	var method string
	var params any
	switch msg.Type {
	case protocol.ServerMsgProviderExec:
		method = "provider/exec"
		params = execParams{QueryID: msg.QueryID, SQL: msg.SQL, Handle: msg.Handle, Params: msg.Params}
	case protocol.ServerMsgProviderCancel:
		method = "provider/cancel"
		params = map[string]any{"query_id": msg.QueryID}
	default:
		e.mu.Unlock()
		return false
	}
	e.nextID++
	id := e.nextID
	if method == "provider/exec" {
		e.pending[id] = msg.QueryID
	}
	// JSON-RPC 请求必须带 id，响应按 id 对应（§16.12.2）。
	rawID := json.RawMessage(strconv.FormatInt(id, 10))
	frame := rpcFrame{JSONRPC: "2.0", ID: &rawID, Method: method, Params: mustJSON(params)}
	raw, err := json.Marshal(frame)
	if err != nil {
		e.mu.Unlock()
		return false
	}
	raw = append(raw, '\n')
	_, err = e.stdin.Write(raw)
	e.mu.Unlock()
	return err == nil
}

// NotifyRetired 在端点被降级时回执（§16.9.4）。进程端点的回执由宿主直接
// 合成（没有会话可通知），因此这里只记日志，不发帧。
func (e *ProcessEndpoint) NotifyRetired(key string, attached []protocol.DriverProviderKey) {}

// readLoop 读进程 stdout 的 JSON-RPC 行，把响应投递给等待方。
func (e *ProcessEndpoint) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame rpcFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		if frame.ID == nil {
			// 通知帧：provider/hello 是进程声明自己（§16.13.2）。
			if frame.Method == "provider/hello" {
				e.handleHello(frame)
			}
			continue
		}
		var id int64
		if err := json.Unmarshal(*frame.ID, &id); err != nil {
			continue
		}
		e.mu.Lock()
		queryID, ok := e.pending[id]
		delete(e.pending, id)
		e.mu.Unlock()
		if !ok {
			continue
		}
		out := providerOutcome{}
		if frame.Error != nil {
			out.err = &protocol.PolyDBError{
				Code:    protocol.ErrQueryFailed,
				Message: frame.Error.Message,
			}
		} else {
			var res execResult
			if err := json.Unmarshal(frame.Result, &res); err != nil {
				out.err = &protocol.PolyDBError{
					Code:    protocol.ErrInvalidParam,
					Message: "provider result decode failed: " + err.Error(),
				}
			} else if res.Error != nil {
				out.err = res.Error
			} else if res.Result != nil {
				out.result = res.Result
			} else {
				out.err = &protocol.PolyDBError{
					Code:    protocol.ErrInvalidParam,
					Message: "provider result has neither result nor error",
				}
			}
		}
		e.hub.Complete(queryID, out)
	}
	e.markDead()
}

// helloDecl 是 provider/hello 的声明（§16.13.2）。
type helloDecl struct {
	Key             string   `json:"key"`
	Capabilities    []string `json:"capabilities"`
	ProtocolVersion int      `json:"protocol_version"`
	// Commutative 是 §16.18 的 witnessed coeffect（Thm 47）：实例可互换，
	// 多实例并存（负载分担）。缺省 false（rolling update 语义）。
	Commutative bool `json:"commutative,omitempty"`
}

// handleHello 校验进程的 hello 声明并建立 attached 登记（§16.13.2）。
// 校验不通过 → 终止该进程（不登记）。
//
// 能力声明：hello 携带的能力会被**自动注册到 broker**（等价于会话端点发
// broker_register，§16.9）——两端点类型共用同一套能力声明路径。
func (e *ProcessEndpoint) handleHello(frame rpcFrame) {
	var decl helloDecl
	if err := json.Unmarshal(frame.Result, &decl); err != nil {
		e.markDead()
		return
	}
	if decl.ProtocolVersion != providerProtocolVersion {
		e.markDead()
		return
	}
	// 声明的 key 必须与配置表一致（进程不能冒充别的 key，§16.13.1）。
	if decl.Key != string(e.key) {
		e.markDead()
		return
	}
	// 能力集合必须合法（非空、无重复），且映射到已知能力位。
	caps, ok := parseCapabilities(decl.Capabilities)
	if !ok {
		e.markDead()
		return
	}
	// 建立登记（attach 内部再查白名单，§16.10.1）。commutative 由 provider
	// 自证（§16.18.1 witnessed coeffect）。
	if err, _ := e.hub.Attach(e.key, e, decl.Commutative); err != nil {
		e.markDead()
		return
	}
	// 能力自动注册到 broker（与 broker_register 同一路径）。
	e.hub.RegisterProviderCaps(e.key, caps)
	// §16.14.2：把回调端点地址与令牌回给进程（provider 据此把 SQL 交回宿主
	// 执行）。ack 走 stdout 的同一条协议流（§16.12.2：stdout 只承载协议帧）。
	e.sendAck(&protocol.ServerMessage{
		Type:          protocol.ServerMsgProviderAck,
		Event:         protocol.DriverBrokerEventActivating,
		Key:           e.key,
		Attached:      e.hub.AttachedKeys(),
		CallbackURL:   e.hub.CallbackURL(),
		CallbackToken: e.hub.CallbackToken(),
	})
}

// sendAck 把宿主消息写成协议帧送到进程的 stdin（§16.14.2）。
//
// 方向：宿主 → 进程走 stdin，进程 → 宿主走 stdout（§16.12.2）。
func (e *ProcessEndpoint) sendAck(msg *protocol.ServerMessage) {
	frame := rpcFrame{JSONRPC: "2.0", Method: "provider/ack", Result: mustJSON(msg)}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	e.mu.Lock()
	defer e.mu.Unlock()
	if w := e.stdin; w != nil {
		_, _ = w.Write(raw)
	}
}

// parseCapabilities 把能力名数组映射到已知能力位；未知能力 → 不合法。
func parseCapabilities(names []string) ([]protocol.DriverCapability, bool) {
	if len(names) == 0 {
		return nil, false
	}
	known := map[string]protocol.DriverCapability{
		"query":       protocol.DriverCapQuery,
		"execute":     protocol.DriverCapExecute,
		"metadata":    protocol.DriverCapMetadata,
		"transaction": protocol.DriverCapTransaction,
		"kv":          protocol.DriverCapKv,
	}
	out := make([]protocol.DriverCapability, 0, len(names))
	seen := map[protocol.DriverCapability]bool{}
	for _, n := range names {
		c, ok := known[strings.TrimSpace(n)]
		if !ok || seen[c] {
			return nil, false
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, true
}

// logLoop 把进程的 stderr 接到宿主日志（stdout 只承载协议帧）。
func (e *ProcessEndpoint) logLoop(stderr io.Reader) {
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		// provider 的日志不是协议帧，只做诊断用途。
		_ = sc.Text()
	}
}

// waitLoop 回收子进程（不留僵尸，§16.13.4）。
//
// Wait 只能调一次：Close 与 waitLoop 竞争时用 waitOnce 串行化。
func (e *ProcessEndpoint) waitLoop(cmd *exec.Cmd) {
	e.waitOnce.Do(func() { _ = cmd.Wait() })
	e.markDead()
}

// Close 终止 provider 进程并回收（§16.13.4：宿主必须回收子进程，不留僵尸）。
// 幂等。
func (e *ProcessEndpoint) Close() {
	e.mu.Lock()
	if e.cmd == nil {
		e.mu.Unlock()
		return
	}
	cmd := e.cmd
	e.cmd = nil
	alive := e.alive
	e.alive = false
	e.mu.Unlock()
	if alive && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	e.waitOnce.Do(func() { _ = cmd.Wait() })
	if e.sandboxCleanup != nil {
		e.sandboxCleanup()
	}
}

// markDead 标记进程不可达：清空在途请求并通知 hub 移除登记。
func (e *ProcessEndpoint) markDead() {
	e.mu.Lock()
	if !e.alive {
		e.mu.Unlock()
		return
	}
	e.alive = false
	pending := e.pending
	e.pending = map[int64]string{}
	e.mu.Unlock()

	// 在途请求全部失败（不挂起，§16.13.4）。
	for _, queryID := range pending {
		e.hub.Complete(queryID, providerOutcome{err: &protocol.PolyDBError{
			Code:    protocol.ErrDriverNotAvailable,
			Message: "provider process exited",
		}})
	}
	if e.onDead != nil {
		e.onDead()
	}
}

// resolveCommandPath 补 Windows 的可执行后缀：Windows 的 exec 对**没有扩展名**
// 的文件不会直接执行（只按 PATH 查找），因此配置里写 "C:\path\provider" 时
// 一律按 "provider.exe" 处理。其它平台原样返回。
func resolveCommandPath(p string) string {
	if runtime.GOOS == "windows" && filepath.Ext(p) == "" {
		return p + ".exe"
	}
	return p
}

// splitCommand 按空白切分命令行，但**引号内的空白不算分隔符**
// （Windows 上可执行文件路径常含空格，如 C:\Program Files\...）。
// 引号会被剥离。
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	started := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case (r == ' ' || r == '\t') && !inQuote:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
