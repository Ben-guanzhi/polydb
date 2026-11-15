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
		conn:       conn,
		app:        s.app,
		token:      s.token,
		broker:     s.broker,
		hub:        s.hub,
		endpointID: s.hub.NextEndpointID(),
		inFlight:   make(map[string]*querySignals),
		writeCh:    make(chan []byte, 32),
		outDone:    make(chan struct{}),
		shutdown:   make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer close(sess.outDone); sess.runOutbound() }()
	go func() { defer wg.Done(); sess.runInbound() }()
	wg.Wait()

	sess.cancelAll()
	// behavior.md §16.8：会话卸载 = 应用本会话贡献的 accumulator。
	// cancelAll 只是发取消通知；必须等在途 runQuery 全部走完出口并 Release
	// 完成（queryWG），再逐个 Revoke 本会话成功注册的 keys——否则 reliance
	// 仍 >0，守卫拒绝，key 反而永久泄漏。只撤自己的 key，故 recovery
	// exactness（Thm 68）不受其它会话影响。
	sess.queryWG.Wait()
	for _, key := range sess.registeredKeys {
		_ = sess.broker.Revoke(key)
	}
	// 阶段 4：断连自动 detach（§16.9.6）。只移除本会话登记的 keys。
	_ = sess.hub.DropEndpoint(NewSessionEndpoint(sess.endpointID, sess))
}

type wsSession struct {
	conn  *websocket.Conn
	app   *appcore.AppCore
	token string
	// broker 是进程级共享视图（Server.broker），非 per-session；会话结束只释放
	// 对它的引用，进程级依赖图不随连接销毁（behavior.md §16.6）。
	broker *appcore.DriverBroker
	// hub 是阶段 4 的进程级桥接（attached + pending），跨会话共享。
	hub *ProviderHub
	// endpointID 是本会话的 provider 端点 id（§16.12.1 的归属判定：detach /
	// 断连 / 降级只移除自己的登记）。会话端点用它作 ProviderEndpoint.ID()。
	endpointID uint64
	connID     string
	authed     bool
	// inFlightMu 只护 inFlight：查询在独立 goroutine 执行后（§6 并发契约），
	// 注册表被入站循环、各查询的完成路径与取消消息并发访问。
	inFlightMu sync.Mutex
	inFlight   map[string]*querySignals
	// registeredKeys 是本会话**成功注册**的 broker keys（behavior.md §16.8）。
	// 连接关闭时逐个 Revoke，应用该会话贡献的 accumulator。只记自己的 key，
	// 保证 recovery exactness（Thm 68）。只由 runInbound goroutine 写、
	// wg.Wait() 之后读（happens-before），无需额外锁。
	registeredKeys []protocol.DriverProviderKey
	// queryWG 等在途 runQuery 全部走完出口并 Release 完成。wg.Wait() 只等
	// runInbound/runOutbound 两个 goroutine，不等 runQuery；若此处不等待就
	// 立刻 Revoke，reliance 仍 >0 → 守卫拒绝 → 本会话的 key 反而永久泄漏，
	// 与「断连自动回收」目标相反。
	queryWG  sync.WaitGroup
	writeCh  chan []byte
	shutdown chan struct{}
	// outDone 在 runOutbound 退出后关闭：stop() 排干 writeCh 前必须等它，
	// 否则 close 帧会抢在队列里的 query_error 之前落盘（乱序）。
	outDone chan struct{}
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
	// 拒绝后必须立刻解除对端的阻塞：只 close(shutdown) 时 runInbound 仍阻塞在
	// ReadMessage（60s idle deadline），ServeWS 的 wg.Wait() 等着它 → conn.Close
	// 不执行 → 客户端等 TCP FIN 等满自己的读死线（测试实测 3s+；生产里就是拒绝
	// 后连接滞留到 idle reaper）。
	//
	// 顺序敏感：stop() 通常紧跟着 send()（拒绝即停），而 send 只是把消息排进
	// writeCh —— 若这里直接 WriteControl close 帧，会抢在 runOutbound 把队列里
	// 的 query_error 落盘之前，客户端先收到关闭帧、丢掉错误详情。所以先等
	// runOutbound 退出（close(shutdown) 已让它从 select 退出；正在写的帧落完），
	// 再排干队列里剩余的帧，然后发 CloseMessage 握手，最后 conn.Close 兜底半开
	// 场景。对已停会话全部幂等（返回 error 忽略即可）。
	<-s.outDone
	for {
		select {
		case data := <-s.writeCh:
			_ = s.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			_ = s.conn.WriteMessage(websocket.BinaryMessage, data)
		default:
			goto drained
		}
	}
drained:
	_ = s.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rejected"),
		time.Now().Add(2*time.Second))
	_ = s.conn.Close()
}

// wsSendTimeout bounds how long send waits for a writeCh slot. handleQuery
// calls send synchronously on the inbound loop: an unbounded wait here (a
// client that stopped reading) would stall the loop and no further messages —
// including query_cancel — would be processed.
const wsSendTimeout = 30 * time.Second

// enqueue 把一帧排进发送队列，返回是否成功入队（§16.9 的 emission 判定点：
// false = 目标会话已关闭或背压超时，帧未进入发送通道）。跨 session 发送
// （provider_exec / provider_cancel）必须用它拿 bool；本会话的既有 send
// 调用点保持 void 语义零改动。
func (s *wsSession) enqueue(msg *protocol.ServerMessage) bool {
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return false
	}
	select {
	case s.writeCh <- data:
		return true
	case <-time.After(wsSendTimeout):
		// The outbound writer is wedged (or gone); the session's read idle
		// reaper will tear it down. Dropping the frame beats blocking the
		// inbound loop forever.
		return false
	case <-s.shutdown:
		return false
	}
}

func (s *wsSession) send(msg *protocol.ServerMessage) {
	_ = s.enqueue(msg)
}

// sendBrokerTransition 按 behavior.md §16.5 的「统一 2 帧响应」发消息：
// 先 broker_state（操作后终态视图；失败时为操作前视图），再 broker_event
// （notify_d 分类 + 请求 key；失败时带 reason）。与 Rust 侧
// receive_loop 的 broker arm 严格镜像。
func (s *wsSession) sendBrokerTransition(key string, tr appcore.BrokerTransition) {
	reason := ""
	if tr.Error != nil {
		reason = tr.Error.Message
	}
	s.send(&protocol.ServerMessage{
		Type:      protocol.ServerMsgBrokerState,
		Providers: tr.State,
		Error:     tr.Error,
	})
	s.send(&protocol.ServerMessage{
		Type:   protocol.ServerMsgBrokerEvent,
		Event:  tr.Event,
		Key:    protocol.DriverProviderKey(key),
		Reason: reason,
	})
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
		case protocol.ClientMsgBrokerRegister:
			// behavior.md §16.5：不受 §12.2 的 query 认证门禁约束（只登记
			// key/capabilities，不接触用户数据、不执行 SQL）。
			if msg.Provider == nil {
				s.sendBrokerTransition("", appcore.BrokerTransition{
					State: s.broker.Snapshot(),
					Event: protocol.DriverBrokerEventNeutral,
					Error: &protocol.PolyDBError{
						Code:    protocol.ErrInvalidParam,
						Message: "broker_register requires provider",
					},
				})
				break
			}
			key := string(msg.Provider.Key)
			tr := s.broker.Register(*msg.Provider)
			// 只有成功登记才记入本会话的 keys（失败不产生转移，Def 19）。
			if tr.Error == nil {
				s.registeredKeys = append(s.registeredKeys, msg.Provider.Key)
			}
			s.sendBrokerTransition(key, tr)
		case protocol.ClientMsgBrokerRevoke:
			key := string(msg.Key)
			tr := s.broker.Revoke(msg.Key)
			// 撤回成功则从本会话清单移除，避免连接关闭时重复 Revoke。
			if tr.Error == nil {
				filtered := s.registeredKeys[:0]
				for _, k := range s.registeredKeys {
					if k != msg.Key {
						filtered = append(filtered, k)
					}
				}
				s.registeredKeys = filtered
			}
			s.sendBrokerTransition(key, tr)
		case protocol.ClientMsgBrokerSetGrant:
			// 阶段 5：能力衰减的 wire 入口（§16.10.2）。只能收紧不能放宽；
			// 成功属 notify_d 的 neutral 分类（不 reload、依赖图不动）。
			tr := s.broker.SetGranted(msg.Key, msg.GrantedCapabilities)
			s.sendBrokerTransition(msg.Key, tr)
		case protocol.ClientMsgProviderAttach:
			// §16.12.1：会话端点是执行端抽象的一个实现。
			// §16.18：commutative 由 attach 方自证（witnessed，Thm 47）。
			ep := NewSessionEndpoint(s.endpointID, s)
			err, attached := s.hub.Attach(msg.Key, ep, msg.Commutative)
			event := protocol.DriverBrokerEventActivating
			if err != nil {
				event = protocol.DriverBrokerEventNeutral
			}
			s.send(&protocol.ServerMessage{
				Type:     protocol.ServerMsgProviderAck,
				Event:    event,
				Key:      msg.Key,
				Attached: attached,
				Error:    err,
			})
		case protocol.ClientMsgProviderDetach:
			ep := NewSessionEndpoint(s.endpointID, s)
			err, attached := s.hub.Detach(msg.Key, ep)
			event := protocol.DriverBrokerEventDeactivating
			if err != nil {
				event = protocol.DriverBrokerEventNeutral
			}
			s.send(&protocol.ServerMessage{
				Type:     protocol.ServerMsgProviderAck,
				Event:    event,
				Key:      msg.Key,
				Attached: attached,
				Error:    err,
			})
		case protocol.ClientMsgProviderResult:
			// 迟到结果（pending 已被 cancel/timeout 清除）→ Complete 返回 false，
			// 静默扣住（withhold，§16.9.2 step 5）。已发射的帧不撤回。
			out := providerOutcome{result: msg.Result, err: msg.Error}
			if msg.Result == nil && msg.Error == nil {
				out.err = &protocol.PolyDBError{
					Code:    protocol.ErrInvalidParam,
					Message: "provider_result has neither result nor error",
				}
			}
			_ = s.hub.Complete(msg.QueryID, out)
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
// querySignals 是一次在途查询的**成对信号**（behavior.md §16.15）：正常完成
// 只关 done（不触发取消），真取消才关 cancel——两者若共用一个通道，完成时的
// 关闭会被 watcher 误判为取消，正常完成的查询会被吞掉。
type querySignals struct {
	cancel chan struct{}
	done   chan struct{}
}

func (s *wsSession) registerQuery(id string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if _, exists := s.inFlight[id]; exists {
		return false
	}
	if len(s.inFlight) >= wsMaxConcurrentQueries {
		return false
	}
	// 成对信号（§16.15）：正常完成只关 done，真取消才关 cancel。
	s.inFlight[id] = &querySignals{
		cancel: make(chan struct{}),
		done:   make(chan struct{}),
	}
	return true
}

// finishQuery 正常完成：从在途表注销并关闭 done 通道（**不**触发取消）。
// watcher 看到 done 直接退出，查询上下文不被污染——正常完成的结果得以发射
// （§16.15 完成与取消信号分离）。
func (s *wsSession) finishQuery(id string) bool {
	return s.retireQuerySignal(id, false)
}

// retireQuery 真取消：从在途表注销并关闭 cancel 通道（watcher 据此中断执行）。
// 调用者：handleCancel（客户端 query_cancel）、cancelAll（会话收尾）、
// timeout/panic 分支（放弃执行）。
func (s *wsSession) retireQuery(id string) bool {
	return s.retireQuerySignal(id, true)
}

// retireQuerySignal 是 finishQuery / retireQuery 的公共实现：先删后关
// （同一把锁内删，锁外关）让重复调用互斥，close 恰好发生一次。
func (s *wsSession) retireQuerySignal(id string, cancel bool) bool {
	s.inFlightMu.Lock()
	sig, ok := s.inFlight[id]
	if ok {
		delete(s.inFlight, id)
	}
	s.inFlightMu.Unlock()
	if !ok {
		return false
	}
	if cancel {
		close(sig.cancel)
	} else {
		close(sig.done)
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
	// behavior.md §16.8：可选的 broker 绑定。CheckAndAcquire 在同一临界区原子
	// 完成「能力校验 + acquire」，避免 revoke 插进校验与绑定之间（Thm 71）。
	// 失败时不发 QueryStarted、不进 inFlight、不 acquire。
	if msg.ProviderKey != "" {
		if pe := s.broker.CheckAndAcquire(protocol.DriverProviderKey(msg.ProviderKey)); pe != nil {
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryError,
				QueryID: msg.QueryID,
				Error:   pe,
			})
			return
		}
		// §16.9.2 step 2：必须在 registerQuery / QueryStarted **之前**查
		// attached 表。未 attach 时立刻归还 reliance（CheckAndAcquire 已
		// 成功），不进 inFlight、不发 QueryStarted。
		if s.hub.ProviderEndpoint(protocol.DriverProviderKey(msg.ProviderKey)) == nil {
			s.broker.Release(protocol.DriverProviderKey(msg.ProviderKey))
			s.send(&protocol.ServerMessage{
				Type:    protocol.ServerMsgQueryError,
				QueryID: msg.QueryID,
				Error: &protocol.PolyDBError{
					Code:    protocol.ErrDriverNotAvailable,
					Message: "no attached provider for key: " + msg.ProviderKey,
				},
			})
			return
		}
	}
	if !s.registerQuery(msg.QueryID) {
		// CheckAndAcquire 已成功但查询没进 inFlight：必须立刻归还，避免
		// reliance 泄漏（Rust 侧由 BrokerRelianceGuard 的 Drop 兜同一情形）。
		if msg.ProviderKey != "" {
			s.broker.Release(protocol.DriverProviderKey(msg.ProviderKey))
		}
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
	// queryWG.Add 必须在 go 之前：断连收尾靠它等在途查询 Release 完成。
	s.queryWG.Add(1)
	go s.runQuery(msg, connID)
}

// runQuery executes one registered query to completion. It owns the whole
// lifecycle: timeout race (§2.3, same shape as the HTTP handler), cancel via
// the in-flight registry, row cap and the panic guard — panicMiddleware only
// covers HTTP handler goroutines, so a panicking driver on this path would
// otherwise take the process down.
func (s *wsSession) runQuery(msg protocol.ClientMessage, connID string) {
	// behavior.md §16.8：reliance 绑定覆盖**所有**出口（正常完成 / 执行错误 /
	// cancel / timeout / 二次 cancel 竞态 / panic）。用最外层合成 defer 而非
	// 逐个出口手写 Release，避免遗漏造成守卫永久不放开。顺序钉死为
	// Release→queryWG.Done：若反过来，断连收尾的 queryWG.Wait() 会在
	// reliance 归零前返回，Revoke 被守卫拒绝、key 永久泄漏。
	// 与 Rust 侧 BrokerRelianceGuard 的 Drop 严格镜像。
	defer func() {
		if msg.ProviderKey != "" {
			s.broker.Release(protocol.DriverProviderKey(msg.ProviderKey))
		}
		s.queryWG.Done()
	}()
	// S-01：任务起点——WS 与 HTTP 的 recordQuery 共用，四态收口见 select。
	started := time.Now()
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
		sig, ok := s.cancelSignalsFor(msg.QueryID)
		if !ok {
			// 快查询在 watcher 被调度前已完成并注销（nil 通道永不触发，
			// 否则该 goroutine 要挂到会话关闭——高频短查询下持续累积）。
			return
		}
		select {
		// §16.15：done = 正常完成（**不**取消——否则完成会被误判为取消，
		// 正常查询被吞掉）；cancel = 真取消；shutdown = 会话收尾（等同取消）。
		case <-sig.done:
			return
		case <-sig.cancel:
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
		// panicMiddleware 只覆盖 HTTP handler goroutine；执行 goroutine 若 panic
		// 会绕过 runQuery 的 defer 链、崩进程并永久泄漏 reliance。转成错误送进
		// ch，走 runQuery 的正常错误出口（B），使合成 defer 仍能 Release。
		defer func() {
			if r := recover(); r != nil {
				ch <- execOutcome{err: fmt.Errorf("internal error: %v", r)}
			}
		}()
		// §16.9：带 provider_key 时把内置执行换成「发 provider_exec + 等
		// provider_result」；不带时逐字保留内置驱动路径（§16.9 硬要求）。
		if msg.ProviderKey != "" {
			// §16.18.2：分派（轮询）+ 在途计数 + 登记**一次**完成——分开做
			// 会让轮询推进两次，exec 与计数落在不同实例上。
			wait, ep := s.hub.RegisterPending(msg.QueryID, protocol.DriverProviderKey(msg.ProviderKey))
			if ep == nil {
				ch <- execOutcome{err: &protocol.PolyDBError{
					Code:    protocol.ErrDriverNotAvailable,
					Message: "provider detached before exec: " + msg.ProviderKey,
				}}
				return
			}
			// 进程端点走回调面：把 connection_id 换成**不透明 handle**，provider
			// 因此永远看不到连接元数据与凭据（§16.14.1）。会话端点不需要。
			handle := ""
			if ep.Kind() == EndpointProcess {
				handle = s.hub.HandleFor(connID)
				if handle == "" {
					s.hub.Cancel(msg.QueryID, protocol.DriverProviderKey(msg.ProviderKey))
					ch <- execOutcome{err: &protocol.PolyDBError{
						Code:    protocol.ErrDriverNotAvailable,
						Message: "provider callback handle unavailable",
					}}
					return
				}
			}
			// emission：投递成功即已推过桥接边界，不可逆（§6.1）。投递经端点
			// 抽象，传输由端点自己决定（会话端点 / 未来的进程端点）。
			if !ep.Emit(&protocol.ServerMessage{
				Type:    protocol.ServerMsgProviderExec,
				QueryID: msg.QueryID,
				SQL:     msg.SQL,
				Params:  msg.Params,
				Handle:  handle,
			}) {
				s.hub.Cancel(msg.QueryID, protocol.DriverProviderKey(msg.ProviderKey))
				ch <- execOutcome{err: &protocol.PolyDBError{
					Code:    protocol.ErrDriverNotAvailable,
					Message: "provider endpoint unreachable: " + msg.ProviderKey,
				}}
				return
			}
			out := <-wait
			var err error
			if out.err != nil {
				err = out.err
			}
			ch <- execOutcome{res: out.result, err: err}
			return
		}
		res, err := s.app.Execute(ctx, connID, msg.SQL, msg.Params...)
		ch <- execOutcome{res, err}
	}()

	// §16.9：provider 路径独有的第三个 select arm。provider_exec 是 emission，
	// 远端可能永不回 provider_result；没有它，cancel/shutdown 后 runQuery 永久
	// 阻塞 → queryWG.Wait() 挂死 → Release 不执行 → reliance 泄漏。仅 provider
	// 路径启用（nil 通道永不触发），内置路径逐字不变——旧路径靠
	// s.app.Execute 响应 ctx 取消从 ch 返回，语义由 cancelled 判定继承。
	cancelDoneCh := (<-chan struct{})(nil)
	if msg.ProviderKey != "" {
		cancelDoneCh = ctx.Done()
	}
	select {
	case result := <-ch:
		// 先取取消判定再注销：注销会 close 取消通道，watcher 随之触发
		// cancelFn，晚一步读 ctx.Err() 会把正常完成误判成已取消。
		cancelled := ctx.Err() != nil
		// §16.15：正常完成只关 done（不触发取消）——若用 retireQuery（关
		// cancel），watcher 会在此后触发 cancelFn，污染 ctx，发射前的二次
		// 判定就会把正常完成误判成已取消（偶发吞掉结果）。
		s.finishQuery(msg.QueryID)
		rows := 0
		if result.res != nil {
			rows = len(result.res.Rows)
		}
		recordQuery(started, result.err, cancelled, rows, stmtKeyword(msg.SQL))
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
		recordQuery(started, nil, false, rows, stmtKeyword(msg.SQL))
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryResult,
			QueryID: msg.QueryID,
			Result:  applyMaxRows(result.res, rowLimitFrom(msg.MaxRows)),
		})
	case <-cancelDoneCh:
		// §16.9.2 step 5 + §16.9.3：cancel/shutdown 的 withhold + compensation。
		// 先清 pending（迟到的 provider_result 被扣住），再向 provider 发
		// best-effort provider_cancel；已发射的帧不撤回（PD-11 边界）。
		s.hub.Cancel(msg.QueryID, protocol.DriverProviderKey(msg.ProviderKey))
		// cancel 通道已由触发源关闭（handleCancel/cancelAll/timeout），此处
		// 只清理表项——二次 close 会 panic（§16.15）。
		s.finishQuery(msg.QueryID)
		recordQuery(started, nil, true, 0, stmtKeyword(msg.SQL))
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryCancelled,
			QueryID: msg.QueryID,
		})
	case <-timeoutCh:
		// 到点先回 TIMEOUT，被放弃的执行在后台自然结束（best-effort）。
		// S-01：超时是"用户视角的取消"。provider 路径同样要 withhold 迟到的
		// provider_result 并发 compensation。
		if msg.ProviderKey != "" {
			s.hub.Cancel(msg.QueryID, protocol.DriverProviderKey(msg.ProviderKey))
		}
		s.retireQuery(msg.QueryID)
		recordQuery(started, nil, true, 0, stmtKeyword(msg.SQL))
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   &protocol.PolyDBError{Code: protocol.ErrTimeout, Retryable: true, Message: fmt.Sprintf("query timed out: %s", msg.QueryID)},
		})
	}
}

// cancelSignalsFor 返回在途查询的成对信号。查询可能完成于注册与 watcher 读取
// 之间；缺项返回 nil——done/cancel 通道都是 nil 时永不触发，watcher 直接退出
// （快查询不累积 goroutine）。
func (s *wsSession) cancelSignalsFor(id string) (*querySignals, bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	sig, ok := s.inFlight[id]
	if !ok {
		return nil, false
	}
	return sig, true
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
