package main

import (
	"flag"
	"net/http"

	"golang.org/x/net/websocket"
)

var webDir = flag.String("web", "/usr/share/recovery-web", "web build of recovery-ui, served by the Remote UI service")

// webHandler serves the web build of recovery-ui and, at /ws, the backend
// protocol over a WebSocket: each text frame carries one JSON line either
// way, so serve() handles it exactly like a socket.
func webHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Server{
		// Any origin: the page is ours, served from this same port, and the
		// link is USB only. The default check would refuse other tools.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.TextFrame
			serve(ws)
		},
	})
	mux.Handle("/", http.FileServer(http.Dir(*webDir)))
	return mux
}
