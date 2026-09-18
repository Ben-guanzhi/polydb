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

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
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

	sess := &wsSession{
		conn:     conn,
		app:      s.app,
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
	connID   string
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
	s.send(&protocol.ServerMessage{
		Type:          protocol.ServerMsgHelloAck,
		ServerVersion: wsServerVersion,
	})
}

func (s *wsSession) handleQuery(msg protocol.ClientMessage) {
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
