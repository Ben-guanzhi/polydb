package server

import (
	"bytes"
	"context"
	"net/http"
	"sync"

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
	conn     *websocket.Conn
	app      *appcore.AppCore
	token    string
	connID   string
	authed   bool
	inFlight map[string]chan struct{}
	writeCh  chan []byte
	shutdown chan struct{}
}

func (s *wsSession) runOutbound() {
	for {
		select {
		case data := <-s.writeCh:
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

func (s *wsSession) send(msg *protocol.ServerMessage) {
	data, err := msgpack.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case s.writeCh <- data:
	case <-s.shutdown:
	}
}

func (s *wsSession) runInbound() {
	for {
		_, data, err := s.conn.ReadMessage()
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

	cancel := make(chan struct{})
	s.inFlight[msg.QueryID] = cancel

	s.send(&protocol.ServerMessage{
		Type:    protocol.ServerMsgQueryStarted,
		QueryID: msg.QueryID,
	})

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	go func() {
		select {
		case <-cancel:
			cancelFn()
		case <-s.shutdown:
		}
	}()

	out, err := s.app.Execute(ctx, connID, msg.SQL, msg.Params...)
	delete(s.inFlight, msg.QueryID)
	if ctx.Err() != nil {
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryCancelled,
			QueryID: msg.QueryID,
		})
		return
	}
	if err != nil {
		var pe *protocol.PolyDBError
		if e, ok := err.(*protocol.PolyDBError); ok {
			pe = e
		} else {
			pe = &protocol.PolyDBError{Code: protocol.ErrUnknown, Message: err.Error()}
		}
		s.send(&protocol.ServerMessage{
			Type:    protocol.ServerMsgQueryError,
			QueryID: msg.QueryID,
			Error:   pe,
		})
		return
	}
	s.send(&protocol.ServerMessage{
		Type:    protocol.ServerMsgQueryResult,
		QueryID: msg.QueryID,
		Result:  out,
	})
}

func (s *wsSession) handleCancel(msg protocol.ClientMessage) {
	if cancel, ok := s.inFlight[msg.QueryID]; ok {
		close(cancel)
		return
	}
	s.send(&protocol.ServerMessage{
		Type:    protocol.ServerMsgQueryError,
		QueryID: msg.QueryID,
		Error:   &protocol.PolyDBError{Code: protocol.ErrInvalidParam, Message: "query not found: " + msg.QueryID},
	})
}

func (s *wsSession) cancelAll() {
	for _, cancel := range s.inFlight {
		close(cancel)
	}
	s.stop()
}
