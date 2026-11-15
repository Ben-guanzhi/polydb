// polydb-provider 是 polydb 的**参考 provider 进程**：一个独立的可执行程序，
// 按 behavior.md §16.12.2 的 stdio JSON-RPC 2.0 协议与宿主通信。
//
// 它演示「进程外驱动」的最小形态：
//   - 启动后先发 provider/hello{key, capabilities, protocol_version} 声明自己
//     是哪个 key、支持哪些能力（§16.13.2）；
//   - 收到 provider/exec{query_id, sql, params} 后执行并回 provider_result；
//   - 收到 provider/cancel{query_id} 时 best-effort 中断（§16.9.3）。
//
// 传输：stdin/stdout 上的 newline-delimited JSON-RPC 2.0，**stdout 只承载协议
// 帧**，日志一律写 stderr（与 cmd/polydb-mcp 同纪律）。
//
// 关键参数：
//
//	-key        本进程提供的 provider key（必填）
//	-cap        声明的能力，逗号分隔（默认 query）
//	-sql        每条 exec 返回的固定 SQL 文本（用于契约测试的确定性结果）
//	-rows       每条 exec 返回的行数（默认 1）
//	-exit-after 处理 N 条 exec 后主动退出（用于契约测试的崩溃场景）
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = 1
	methodHello     = "provider/hello"
	methodExec      = "provider/exec"
	methodCancel    = "provider/cancel"
	methodShutdown  = "provider/shutdown"
)

// rpcMessage 是 JSON-RPC 2.0 的最小形态：请求有 id，响应回同一个 id。
type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// execParams 是 provider/exec 的参数。
type execParams struct {
	QueryID string `json:"query_id"`
	SQL     string `json:"sql"`
	Handle  string `json:"handle,omitempty"`
	Params  []any  `json:"params,omitempty"`
}

// execViaCallback 把 SQL 交回宿主执行（§16.14.2）。
//
// provider 只带**不透明 handle**，看不到 connection_id、主机、端口或凭据；
// 宿主的执行路径复用其 app-core（只读判定、限行、事务全部沿用）。
//
// ctx 由 provider/cancel 触发取消（§16.9.3 best-effort）：被取消的回调请求
// 随即中止，不再占用宿主的执行槽。
//
// 返回值是宿主回的 QueryResult JSON 片段（无外层 result 包装）。
func execViaCallback(ctx context.Context, callbackURL, token string, p execParams) string {
	body := map[string]any{
		"handle": p.Handle,
		"sql":    p.SQL,
		"params": p.Params,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return `{}`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(raw))
	if err != nil {
		return `{}`
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return `{}`
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// 宿主回 {"result": {...}} 或 {"error": {...}}；这里把 result 原样交回，
	// 错误则转成 QueryResult 形态的失败（宿主侧会看到 result 为空）。
	var wrapper struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(out, &wrapper); err == nil && len(wrapper.Result) > 0 {
		return string(wrapper.Result)
	}
	return `{}`
}

func main() {
	key := flag.String("key", "", "provider key（必填）")
	cap := flag.String("cap", "query", "声明的能力，逗号分隔")
	sql := flag.String("sql", "SELECT 1 AS v", "每条 exec 返回的固定 SQL 文本")
	rows := flag.Int("rows", 1, "每条 exec 返回的行数")
	exitAfter := flag.Int("exit-after", 0, "处理 N 条 exec 后主动退出（0 = 不退出）")
	noCallback := flag.Bool("no-callback", false, "禁用回调面：exec 一律回固定结果（用于验证非回调路径）")
	commutative := flag.Bool("commutative", false, "声明 key 可多实例并存（§16.18 witnessed coeffect，Thm 47）——宿主将按轮询负载分担")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "polydb-provider: -key is required")
		os.Exit(2)
	}

	// §16.16：provider 侧的沙箱配合。
	//   - 父进程自检（Unix）：宿主意外终止 → 本进程被 init 收养 → 自行退出。
	//     Windows 上由宿主的 Job Object 兜底（KILL_ON_JOB_CLOSE），无需自检。
	//   - 内存自限（Unix）：RLIMIT_AS 由 provider 自己应用——Go 标准库无法在
	//     fork 时替子进程设置 rlimit。
	selfLimitMemory()

	if parentPID := os.Getenv("POLYDB_PROVIDER_PARENT_PID"); parentPID != "" && runtime.GOOS != "windows" {
		go watchParentPID(parentPID)
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	// stdout 的写锁：exec 并发处理后响应可能乱序到达，必须串行化写入
	//（newline-delimited JSON-RPC 的帧边界靠整行写入保证）。
	var outMu sync.Mutex
	write := func(m rpcMessage) {
		outMu.Lock()
		defer outMu.Unlock()
		_ = writeMsg(out, m)
	}
	// 在途 exec 的可取消 context（provider/cancel 据此中断在途回调，§16.9.3）。
	execCtxs := map[string]context.CancelFunc{}
	var execMu sync.Mutex

	// §16.13.2：启动后先声明自己（key / 能力 / 协议版本）。宿主据此校验并
	// 建立 attached 登记；声明不被接受时宿主会终止本进程。
	hello := map[string]any{
		"key":              *key,
		"capabilities":     splitList(*cap),
		"protocol_version": protocolVersion,
		"commutative":      *commutative,
	}
	helloBytes, _ := json.Marshal(hello)
	_ = writeMsg(out, rpcMessage{JSONRPC: "2.0", Method: "provider/hello", Result: raw(string(helloBytes))})

	processed := 0
	var processedMu sync.Mutex
	// 阶段 9：宿主下发的 provider/ack 带回调端点地址与令牌（§16.14.2）。
	// 拿到后，exec 就经回调让宿主代为执行真实 SQL（provider 不碰凭据）。
	var callbackURL, callbackToken string
	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// 解析错误：回 JSON-RPC 解析错误（与 MCP 同形态），继续读下一行。
			write(rpcMessage{JSONRPC: "2.0", Error: &rpcError{
				Code:    -32700,
				Message: "parse error: " + err.Error(),
			}})
			continue
		}
		if msg.ID == nil {
			// 通知：provider/ack 是宿主的回调面授权（§16.14.2）。
			if msg.Method == "provider/ack" {
				var ack struct {
					CallbackURL   string `json:"callback_url"`
					CallbackToken string `json:"callback_token"`
				}
				if err := json.Unmarshal(*msg.Result, &ack); err == nil {
					callbackURL = ack.CallbackURL
					callbackToken = ack.CallbackToken
				}
			}
			continue
		}
		switch msg.Method {
		case methodHello:
			// 宿主侧确认：忽略参数即可（我们主动声明过）。
			write(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: raw(`{"ok":true}`)})
		case methodExec:
			var p execParams
			_ = json.Unmarshal(msg.Params, &p)
			processedMu.Lock()
			processed++
			processedMu.Unlock()
			// 每条 exec 独立 goroutine：回调执行可能阻塞数秒，串行处理会让
			// 后续 exec 排队（宿主的并发查询全被拖住）。响应可能乱序——
			// 宿主按 query_id 关联，与到达顺序无关。
			ctx, cancel := context.WithCancel(context.Background())
			execMu.Lock()
			execCtxs[p.QueryID] = cancel
			execMu.Unlock()
			id := msg.ID
			url, token := callbackURL, callbackToken
			go func() {
				defer func() {
					execMu.Lock()
					delete(execCtxs, p.QueryID)
					execMu.Unlock()
					cancel()
				}()
				// 有回调面就经回调让宿主执行真实 SQL（§16.14）；否则回固定结果。
				var res string
				if url != "" && p.Handle != "" && !*noCallback {
					res = execViaCallback(ctx, url, token, p)
				} else {
					res = buildResult(*sql, *rows)
				}
				// 响应形状：{"result": <QueryResult>}（§16.13.3 的帧映射）。
				write(rpcMessage{JSONRPC: "2.0", ID: id, Result: raw(`{"result":` + res + `}`)})
				processedMu.Lock()
				done := *exitAfter > 0 && processed >= *exitAfter
				processedMu.Unlock()
				if done {
					// 崩溃场景：直接退出（不响应 shutdown，模拟异常终止）。
					os.Exit(7)
				}
			}()
		case methodCancel:
			// best-effort 补偿（§16.9.3）：真实中断在途回调——被取消的 exec
			// 的回调请求随 ctx 取消而中止，不会再占用宿主的执行槽。
			var cp struct {
				QueryID string `json:"query_id"`
			}
			_ = json.Unmarshal(msg.Params, &cp)
			execMu.Lock()
			cancel, ok := execCtxs[cp.QueryID]
			execMu.Unlock()
			if ok {
				cancel()
			}
			write(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: raw(`{"ok":true}`)})
		case methodShutdown:
			write(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: raw(`{"ok":true}`)})
			outMu.Lock()
			out.Flush()
			outMu.Unlock()
			return
		default:
			write(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{
				Code:    -32601,
				Message: "method not found: " + msg.Method,
			}})
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "polydb-provider: stdin:", err)
		os.Exit(1)
	}
}

// buildResult 构造一条确定性结果（列名 v，单列整数值 1）。
func buildResult(sql string, rows int) string {
	if rows < 0 {
		rows = 0
	}
	values := make([]any, 0, rows)
	for i := 0; i < rows; i++ {
		values = append(values, []any{1})
	}
	res := map[string]any{
		"columns": []any{map[string]any{
			"name":     "v",
			"type":     "INTEGER",
			"nullable": false,
		}},
		"rows":              values,
		"affected_rows":     0,
		"execution_time_ms": 0,
	}
	b, err := json.Marshal(res)
	if err != nil {
		return `{"columns":[],"rows":[],"affected_rows":0,"execution_time_ms":0}`
	}
	return string(b)
}

func raw(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

// splitList 把逗号分隔的能力串拆成数组（去空白、去空项）。
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeMsg(out *bufio.Writer, m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		return err
	}
	return out.Flush()
}
