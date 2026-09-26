// Command recovery-backend is the fogona LinuxBoot stage's recovery service:
// it owns every privileged action (boot, services, mounts, power) and serves
// them over a Unix socket to recovery-ui, the Slint front end, which it
// starts and restarts.
//
//	recovery-backend [-socket /tmp/recovery.sock] [-ui /bin/recovery-ui]
//	                 [-autoboot 10] [-scan DIR]
//
// The protocol is one JSON object per line. The UI sends
// {"id":N,"cmd":"...","args":{...}} and gets back {"id":N,"data":...} or
// {"id":N,"error":"..."}; the backend also pushes {"event":"...","data":...}
// to every client unasked (autoboot countdown, busy state, errors). Any
// client works: `nc -U /tmp/recovery.sock` over SSH drives it by hand.
//
// Autoboot lives here, not in the UI, so the stage still boots its default
// entry if the UI never draws. /tmp/hold stops it, as with fogona-boot.sh.
//
// -scan DIR parses DIR's extlinux.conf instead of probing block devices, and
// lets the backend run anywhere for UI work; nothing it does then needs root
// except the actions that obviously do.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
)

var (
	socketPath = flag.String("socket", "/tmp/recovery.sock", "IPC socket")
	uiPath     = flag.String("ui", "/bin/recovery-ui", "UI binary to run and restart; empty for none")
	autoboot   = flag.Int("autoboot", 10, "seconds before booting the default entry; 0 disables")
	scanDir    = flag.String("scan", "", "parse this directory's boot config instead of probing block devices")
	logPath    = flag.String("log", "/tmp/recovery.log", "log file, also served by log.tail")
)

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

type event struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

// client is one connection; writes are serialised because replies and
// broadcast events come from different goroutines.
type client struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (c *client) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal %T: %v", v, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w.Write(b)
	c.w.WriteByte('\n')
	c.w.Flush()
}

var (
	clientsMu sync.Mutex
	clients   = map[*client]bool{}
)

func broadcast(name string, data any) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for c := range clients {
		c.send(event{Event: name, Data: data})
	}
}

type handler func(args []byte) (any, error)

// Filled in init: a literal would be an initialisation cycle, since the
// Remote UI service's handlers serve connections that look up handlers.
var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"hello": func([]byte) (any, error) {
			return map[string]any{"version": 1}, nil
		},
		"boot.list":       bootList,
		"boot.start":      bootStart,
		"autoboot.cancel": autobootCancel,
		"service.list":    serviceList,
		"service.set":     serviceSet,
		"disk.list":       diskList,
		"fs.list":         fsList,
		"log.tail":        logTail,
		"power":           power,
		"dev.restart":     devRestart,
		"fs.read":         fsRead,
		"fs.thumb":        fsThumb,
		"display.info":    displayInfo,
	}
}

func serve(conn net.Conn) { serveStream(conn, conn) }

// serveStream reads requests from r, one per line, and replies on conn.
func serveStream(r io.Reader, conn net.Conn) {
	c := &client{w: bufio.NewWriter(conn)}
	clientsMu.Lock()
	clients[c] = true
	clientsMu.Unlock()
	defer func() {
		clientsMu.Lock()
		delete(clients, c)
		clientsMu.Unlock()
		conn.Close()
	}()
	// A new client missed every event so far; give it the current state.
	c.send(event{Event: "autoboot", Data: autobootState()})

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			c.send(reply{Error: "bad request: " + err.Error()})
			continue
		}
		h, ok := handlers[req.Cmd]
		if !ok {
			c.send(reply{ID: req.ID, Error: "unknown command " + req.Cmd})
			continue
		}
		// Each request runs on its own goroutine: a mount or a boot load
		// takes seconds and must not hold up autoboot.cancel behind it.
		go func() {
			data, err := h(req.Args)
			if err != nil {
				c.send(reply{ID: req.ID, Error: err.Error()})
				return
			}
			c.send(reply{ID: req.ID, Data: data})
		}()
	}
}

func main() {
	flag.Parse()
	if f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("recovery-backend starting")

	os.Remove(*socketPath)
	ln, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatalf("listen %s: %v", *socketPath, err)
	}

	// After a live restart (dev.go) the processes are already running and
	// the user has already stopped the autoboot; take them over instead.
	adoptUI := 0
	if h := loadHandoff(); h != nil {
		log.Printf("restarted in place: ui pid %d, services %v", h.UIPid, h.Services)
		cdMu.Lock()
		cancelled = h.Cancelled
		cdMu.Unlock()
		adoptServices(h.Services)
		adoptUI = h.UIPid
	} else {
		startDefaultServices()
	}
	go scanAndCountdown()
	switch {
	case *uiPath == "":
		log.Printf("-ui empty, running headless")
	case adoptUI > 0 || hasUI():
		go superviseUI(adoptUI)
	default:
		log.Printf("no UI embedded or at %s, running headless", *uiPath)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		go serve(conn)
	}
}

func decode[T any](args []byte) (T, error) {
	var v T
	if len(args) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("bad args: %w", err)
	}
	return v, nil
}
