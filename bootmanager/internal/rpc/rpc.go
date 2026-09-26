// Package rpc serves the backend protocol: one JSON object per message.
//
// A client sends {"id":N,"cmd":"...","args":{...}} and gets back
// {"id":N,"data":...} or {"id":N,"error":"..."}. The server also pushes
// {"event":"...","data":...} to every client unasked. Over a stream
// (Unix socket, TCP) a message is a line; over a WebSocket, a text frame.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// Handler answers one command. args is the request's raw "args", possibly
// empty.
type Handler func(args json.RawMessage) (any, error)

// Handle registers f for cmd, decoding its arguments into A.
func Handle[A any](s *Server, cmd string, f func(A) (any, error)) {
	s.handlers[cmd] = func(raw json.RawMessage) (any, error) {
		var a A
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("bad args: %w", err)
			}
		}
		return f(a)
	}
}

// Server dispatches requests to handlers and fans events out to clients.
type Server struct {
	handlers map[string]Handler
	// Greeting returns the events a new client is sent first: it missed
	// every one so far.
	Greeting func() []Event

	mu      sync.Mutex
	clients map[*client]struct{}
}

func NewServer() *Server {
	return &Server{handlers: map[string]Handler{}, clients: map[*client]struct{}{}}
}

type Event struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

type request struct {
	ID   int64           `json:"id"`
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args"`
}

type reply struct {
	ID    int64  `json:"id"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// Broadcast sends an event to every client. It never blocks: a client too
// slow to keep up is dropped, so no peer can hold up the caller (the
// autoboot countdown among them).
func (s *Server) Broadcast(name string, data any) {
	b, err := json.Marshal(Event{name, data})
	if err != nil {
		log.Printf("rpc: marshal %s: %v", name, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.send(b)
	}
}

// client is one connection. Messages queue in out; one goroutine writes
// them, so a stalled peer blocks only itself.
type client struct {
	out  chan []byte
	once sync.Once
	done chan struct{}
}

func (c *client) send(b []byte) {
	select {
	case c.out <- b:
	case <-c.done:
	default:
		log.Printf("rpc: client not reading, dropping it")
		c.close()
	}
}

func (c *client) close() { c.once.Do(func() { close(c.done) }) }

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("rpc: marshal %T: %v", v, err)
		return
	}
	c.send(b)
}

// transport moves whole messages.
type transport interface {
	read() ([]byte, error)
	write([]byte) error
	close() error
}

func (s *Server) serve(t transport) {
	c := &client{out: make(chan []byte, 64), done: make(chan struct{})}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		c.close()
		t.close()
	}()

	go func() {
		defer t.close() // unblocks read when the peer stops reading
		for {
			select {
			case b := <-c.out:
				if t.write(b) != nil {
					c.close()
					return
				}
			case <-c.done:
				return
			}
		}
	}()

	if s.Greeting != nil {
		for _, e := range s.Greeting() {
			c.sendJSON(e)
		}
	}
	for {
		msg, err := t.read()
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			c.sendJSON(reply{Error: "bad request: " + err.Error()})
			continue
		}
		h, ok := s.handlers[req.Cmd]
		if !ok {
			c.sendJSON(reply{ID: req.ID, Error: "unknown command " + req.Cmd})
			continue
		}
		// Each request on its own goroutine: a mount or a boot load takes
		// seconds and must not hold up autoboot.cancel behind it.
		go func() {
			data, err := h(req.Args)
			if err != nil {
				c.sendJSON(reply{ID: req.ID, Error: err.Error()})
				return
			}
			c.sendJSON(reply{ID: req.ID, Data: data})
		}()
	}
}

// ServeListener serves line-delimited connections from ln until it closes.
func (s *Server) ServeListener(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.serve(newLineConn(conn))
	}
}

type lineConn struct {
	conn net.Conn
	sc   *bufio.Scanner
}

func newLineConn(conn net.Conn) *lineConn {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	return &lineConn{conn, sc}
}

func (l *lineConn) read() ([]byte, error) {
	if !l.sc.Scan() {
		if err := l.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return l.sc.Bytes(), nil
}

func (l *lineConn) write(b []byte) error {
	_, err := l.conn.Write(append(b, '\n'))
	return err
}

func (l *lineConn) close() error { return l.conn.Close() }

type wsConn struct{ ws *websocket.Conn }

func (w wsConn) read() ([]byte, error) {
	_, b, err := w.ws.Read(context.Background())
	return b, err
}

func (w wsConn) write(b []byte) error {
	return w.ws.Write(context.Background(), websocket.MessageText, b)
}

func (w wsConn) close() error { return w.ws.CloseNow() }

// ServeWebSocket serves one WebSocket. websocket.Accept refuses a
// cross-origin handshake: any page the host's browser has open could
// otherwise drive the phone as root.
func (s *Server) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	s.serve(wsConn{ws})
}
