package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/polydb/polydb/pkg/appcore"
	"github.com/polydb/polydb/pkg/protocol"
)

const wsServerVersion = "0.1.0"

// maxWSMessageBytes 是单条 WS 消息的上限。gorilla 缺省不限，恶意客户端可以用
// 超大消息打爆内存；这里与 Rust 侧 axum 的 max_message_size 对齐。WS 消息只装
// 查询文本与参数，正常量级在 KB，16 MiB 留足余量。
//
// gorilla 的 Upgrader 没有消息上限字段（ReadBufferSize 只管 I/O 缓冲，官方注释
// 明说它不限制消息大小），所以上限必须在升级后对每条连接调 Conn.SetReadLimit。
const maxWSMessageBytes = int64(16 << 20)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
}

// checkWSOrigin 防浏览器跨站 WebSocket 劫持（CSWSH）。判定规则与 HTTP CORS 共用
// allowedOrigin，两边白名单不会各写一遍后漂移。
func checkWSOrigin(r *http.Request) bool {
	return allowedOrigin(r.Header.Get("Origin"), r.Host)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, &protocol.PolyDBError{
			Code:    protocol.ErrUnknown,
			Message: "websocket upgrade failed: " + err.Error(),
		})
		return
	}
	defer func() { _ = conn.Close() }()
	// Upgrader 只能设 MsgBufferSize，消息大小上限必须由 Conn.SetReadLimit 给，
	// 否则超大消息仍会整条读进来。设在升级之后，覆盖 runInbound 每条 ReadMessage。
	conn.SetReadLimit(maxWSMessageBytes)

	sess := &wsSession{
		conn:     conn,
		app:      s.app,
		token:    s.token,
		inFlight: make(map[string]chan struct{}),
		writeCh:  make(chan []byte, 32),
		shutdown: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sess.runOutbound() }()
	go func() { defer wg.Done(); sess.runInbound() }()
	wg.Wait()

	sess.cancelAll()
}

type wsSession struct {
	conn   *websocket.Conn
	app    *appcore.AppCore
	token  string
	connID string
	authed bool
	// inFlightMu 只护 inFlight：查询在独立 goroutine 执行后（§6 并发契约），
	// 注册表被入站循环、各查询的完成路径与取消消息并发访问。
	inFlightMu sync.Mutex
	inFlight   map[string]chan struct{}
	writeCh    chan []byte
	shutdown   chan struct{}
}

// wsWriteTimeout bounds a single outbound frame write. Without it, a client
// that stops reading wedges WriteMessage forever: writeCh (cap 32) then fills,
// s.send blocks, handleQuery's synchronous send stalls the inbound loop, and
// no further messages — including query_cancel — are processed until the
// client closes the socket.
const wsWriteTimeout = 30 * time.Second

func (s *wsSession) runOutbound() {
	for {
		select {
		case data := <-s.writeCh:
			_ = s.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := s.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				s.stop()
				return
			}
		case <-s.shutdown:
			return
		}
	}
}

func (s *wsSession) stop() {
	select {
	case <-s.shutdown:
	default:
		close(s.shutdown)
	}
}

// wsSendTimeout bounds how long send waits for a writeCh slot. handleQuery
// calls send synchronously on the inbound loop: an unbounded wait here (a
// client that stopped reading) would stall the loop and no further messages —
// including query_cancel — would be processed.
const wsSendTimeout = 30 * time.Second

func (s *wsSession) send(msg *protocol.ServerMessage) {
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case s.writeCh <- data:
	case <-time.After(wsSendTimeout):
		// The outbound writer is wedged (or gone); the session's read idle
		// reaper will tear it down. Dropping the frame beats blocking the
		// inbound loop forever.
	case <-s.shutdown:
	}
}

func (s *wsSession) runInbound() {
	// Half-open TCP: a client that vanished without FIN leaves ReadMessage
	// blocked forever, and the session — with its in-flight queries and
	// server-side resources — lingers until process exit. A read deadline
	// refreshed by any inbound traffic (incl. the pong our ping elicits)
	// reaps such sessions; healthy clients answer within a tick.
	const readIdle = 60 * time.Second
	const pingEvery = 25 * time.Second
	refreshDeadline := func() {
		_ = s.conn.SetReadDeadline(time.Now().Add(readIdle))
	}
	s.conn.SetPongHandler(func(string) error {
		refreshDeadline()
		return nil
	})
	refreshDeadline()
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				// WriteControl is the only write documented as safe
				// concurrently with the outbound writer — WriteMessage here
				// raced runInbound's frame writes (gorilla panics on
				// concurrent writers) and could corrupt the stream.
				if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					s.stop()
					return
				}
			case <-s.shutdown:
				return
			}
		}
	}()
	for {
		_, data, err := s.conn.ReadMessage()
		refreshDeadline()
		if err != nil {
			s.stop()
			return
		}
		var msg protocol.ClientMessage
		if err := msgpack.NewDecoder(bytes.NewReader(data)).Decode(&msg); err != nil {
			s.stop()
			return
		}
		switch msg.Type {
		case protocol.ClientMsgHello:
			s.handleHello(msg)
		case protocol.ClientMsgQuery:
			// behavior.md §6/§12：同一连接可并发多个 query，取消只作用于对应
			// query_id。此前 Execute 在本 goroutine 同步执行，query_cancel 只能
			// 等查询结束后才被读到——WS 取消因此完全失效（HTTP 侧的后台执行 +
			// registry 是正确范式）。执行移入独立 goroutine，入站循环保持读消息。
			s.handleQuery(msg)
		case protocol.ClientMsgQueryCancel:
			s.handleCancel(msg)
		default:
			s.send(&protocol.ServerMessage{
				Type:  protocol.ServerMsgQueryError,
				Error: &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "unknown message type: " + string(msg.Type)},
			})
		}
	}
}

func (s *wsSession) handleHello(msg protocol.ClientMessage) {
	// behavior.md §12.2：服务端启用 token 时 hello 必须携带匹配的 auth.token，
	// 失败发送 query_error(UNAUTHORIZED) 后关闭连接；未启用时忽略 auth 字段。
	if s.token != "" && (msg.Auth == nil || msg.Auth.Token != s.token) {
		s.send(&protocol.ServerMessage{
			Type:  protocol.ServerMsgQueryError,
			Error: &protocol.PolyDBError{Code: protocol.ErrUnauthorized, Message: "missing or invalid bearer token"},
		})
		s.stop()
		return
	}
	if msg.ConnectionID == "" {
		s.send(&protocol.ServerMessage{
			Type:  protocol.ServerMsgQueryError,
			Error: &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "missing connection_id"},
		})
		return
	}
	if _, err := s.app.GetConnection(msg.ConnectionID); err != nil {
		s.send(&protocol.ServerMessage{
			Type:  protocol.ServerMsgQueryError,
			Error: &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection not found: " + msg.ConnectionID},
		})
		return
	}
	s.connID = msg.ConnectionID
	s.authed = true
	s.send(&protocol.ServerMessage{
		Type:          protocol.ServerMsgHelloAck,
		ServerVersion: wsServerVersion,
	})
}

// wsMaxConcurrentQueries 是单个 WS 会话允许的并发在途查询数。HTTP 每请求一个
// goroutine、由 HTTP 服务器自身的并发面兜底；WS 的会话是长活的，需要一个明确的
// 闸门防失控客户端把 goroutine 堆满。
const wsMaxConcurrentQueries = 16

// registerQuery 把一条查询登记为可取消。返回 false 表示会话的在途查询已到闸门，
// 或该 query_id 已在途——覆盖已存在的 id 会孤儿化先前查询的取消通道，后来的
// query_cancel 关的是新通道，先前的查询永远无法取消。重复 id 由调用方回错误帧。
func (s *wsSession) registerQuery(id string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if _, exists := s.inFlight[id]; exists {
		return false
	}
	if len(s.inFlight) >= wsMaxConcurrentQueries {
		return false
	}
	s.inFlight[id] = make(chan struct{})
	return true
}

// retireQuery 注销一条在途查询并 close 它的取消通道。close 恰好发生一次：
// 完成路径、query_cancel 与会话收尾都可能想关，先删后关（同一把锁内）让
// 三者互斥，第二次 close 不会发生。
func (s *wsSession) retireQuery(id string) bool {
	s.inFlightMu.Lock()
	cancel, ok := s.inFlight[id]
	if ok {
		delete(s.inFlight, id)
	}
	s.inFlightMu.Unlock()
	if ok {
		close(cancel)
	}
	return ok
}

func (s *wsSession) handleQuery(msg protocol.ClientMessage) {
	// behavior.md §12.2：启用 token 时，未通过认证 hello 之前拒绝一切 query，
	// 防止客户端跳过 hello 直接携带 connection_id 执行 SQL。
	if s.token != "" && !s.authed {
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrUnauthorized, Message: "query requires authenticated hello first"},
		})
		s.stop()
		return
	}
	if msg.QueryID == "" {
		s.send(&protocol.ServerMessage{
			Type:  protocol.ServerMsgQueryError,
			Error: &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "missing query_id"},
		})
		return
	}
	connID := s.connID
	if msg.ConnectionID != "" {
		connID = msg.ConnectionID
	}
	if connID == "" {
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "query requires hello handshake first or explicit connection_id"},
		})
		return
	}
	if _, err := s.app.GetConnection(connID); err != nil {
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrConnectionNotFound, Message: "connection not found: " + connID},
		})
		return
	}
	if !s.registerQuery(msg.QueryID) {
		// Either the session's in-flight gate is full, or this query_id is
		// already running — the old overwrite silently orphaned the first
		// query's cancel channel. The caller can tell the cases apart by
		// whether it got a QueryStarted frame for this id.
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: fmt.Sprintf("query_id %s is already in flight, or the session has reached %d concurrent queries", msg.QueryID, wsMaxConcurrentQueries)},
		})
		return
	}

	s.send(&protocol.ServerMessage{
		Type:    protocol.ServerMsgQueryStarted,
		QueryID: msg.QueryID,
	})
	go s.runQuery(msg, connID)
}

// runQuery executes one registered query to completion. It owns the whole
// lifecycle: timeout race (§2.3, same shape as the HTTP handler), cancel via
// the in-flight registry, row cap and the panic guard — panicMiddleware only
// covers HTTP handler goroutines, so a panicking driver on this path would
// otherwise take the process down.
func (s *wsSession) runQuery(msg protocol.ClientMessage, connID string) {
	defer func() {
		if r := recover(); r != nil {
			s.retireQuery(msg.QueryID)
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryError,
				QueryID: msg.QueryID,
				Error:   &protocol.PolyDBError{Code: protocol.ErrUnknown, Message: fmt.Sprintf("internal error: %v", r)},
			})
		}
	}()

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() {
		cancel, ok := s.cancelChanFor(msg.QueryID)
		if !ok {
			// 快查询在 watcher 被调度前已完成并注销（nil 通道永不触发，
			// 否则该 goroutine 要挂到会话关闭——高频短查询下持续累积）。
			return
		}
		select {
		case <-cancel:
			cancelFn()
		case <-s.shutdown:
			cancelFn()
		}
	}()

	var timeoutCh <-chan time.Time
	if msg.TimeoutMs != nil && *msg.TimeoutMs > 0 {
		timer := time.NewTimer(time.Duration(*msg.TimeoutMs) * time.Millisecond)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	type execOutcome struct {
		res *protocol.QueryResult
		err error
	}
	ch := make(chan execOutcome, 1)
	go func() {
		res, err := s.app.Execute(ctx, connID, msg.SQL, msg.Params...)
		ch <- execOutcome{res, err}
	}()

	select {
	case result := <-ch:
		// 先取取消判定再注销：注销会 close 取消通道，watcher 随之触发
		// cancelFn，晚一步读 ctx.Err() 会把正常完成误判成已取消。
		cancelled := ctx.Err() != nil
		s.retireQuery(msg.QueryID)
		if cancelled {
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryCancelled,
				QueryID: msg.QueryID,
			})
			return
		}
		if result.err != nil {
			var pe *protocol.PolyDBError
			if e, ok := result.err.(*protocol.PolyDBError); ok {
				pe = e
			} else {
				pe = &protocol.PolyDBError{Code: protocol.ErrUnknown, Message: result.err.Error()}
			}
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryError,
				QueryID: msg.QueryID,
				Error:   pe,
			})
			return
		}
		// 与 HTTP 同一限行（§5）：无上限的 SELECT 会在序列化进 socket 前先
		// 打爆内存。max_rows 消息字段与 HTTP 的 max_rows 参数同义。
		//
		// PD-11（2026-11-09）：这里是 WS 查询的**发射边界**。§6.1 把「获取」
		// （执行，可被 cancel 撤回）与「发射」（写帧，一旦 WriteMessage 成功
		// 即不可逆）分开：cancel 只能撤回尚未发射的获取，绝不能撤回已经写到
		// socket 的帧。上面的 cancelled 判定发生在 retire 之前，若 cancel 在
		// 判定与发射之间到达，客户端会既收到 cancel 又收到完整结果——边界被
		// 穿透。因此在发射前再判一次 ctx.Err()，把「取消」与「结果发射」收敛到
		// 同一个决策点；已发射的帧仍不可撤回（这正是发射边界的含义），但不会再
		// 出现「先 cancel 后结果」的竞态窗口。
		if ctx.Err() != nil {
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryCancelled,
				QueryID: msg.QueryID,
			})
			return
		}
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryResult,
			QueryID: msg.QueryID,
			Result:  applyMaxRows(result.res, rowLimitFrom(msg.MaxRows)),
		})
	case <-timeoutCh:
		// 到点先回 TIMEOUT，被放弃的执行在后台自然结束（best-effort）。
		s.retireQuery(msg.QueryID)
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrTimeout, Retryable: true, Message: fmt.Sprintf("query timed out: %s", msg.QueryID)},
		})
	}
}

// cancelChanFor snapshots the cancel channel of an in-flight query. The query
// may complete between registration and the watcher's read; a missing entry
// yields nil, whose receive never fires — the watcher then just waits for
// shutdown.
func (s *wsSession) cancelChanFor(id string) (<-chan struct{}, bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	cancel, ok := s.inFlight[id]
	if !ok {
		return nil, false
	}
	return cancel, true
}

func (s *wsSession) handleCancel(msg protocol.ClientMessage) {
	// 已完成（已注销）的查询与 HTTP registry 同语义：回 query not found。
	if s.retireQuery(msg.QueryID) {
		return
	}
	s.send(&protocol.ServerMessage{
		Type:    protocol.ServerMsgQueryError,
		QueryID: msg.QueryID,
		Error:   &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "query not found: " + msg.QueryID},
	})
}

func (s *wsSession) cancelAll() {
	s.inFlightMu.Lock()
	ids := make([]string, 0, len(s.inFlight))
	for id := range s.inFlight {
		ids = append(ids, id)
	}
	s.inFlightMu.Unlock()
	for _, id := range ids {
		s.retireQuery(id)
	}
	s.stop()
}
