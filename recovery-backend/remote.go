package main

import (
	"flag"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/websocket"
)

var webDir = flag.String("web", "/usr/share/recovery-web", "web build of recovery-ui on disk, for a backend built without it embedded")

// webHandler serves the web build of recovery-ui and, at /ws, the backend
// protocol over a WebSocket: each text frame carries one JSON message either
// way. Incoming frames are re-joined with a newline after each, because a
// browser sends a frame without one and the line reader would otherwise
// wait for the end of a line that never comes.
func webHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Server{
		// Any origin: the page is ours, served from this same port, and the
		// link is USB only. The default check would refuse other tools.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.TextFrame
			pr, pw := io.Pipe()
			go func() {
				for {
					var msg string
					if err := websocket.Message.Receive(ws, &msg); err != nil {
						pw.CloseWithError(err)
						return
					}
					io.WriteString(pw, strings.TrimRight(msg, "\n")+"\n")
				}
			}()
			serveStream(pr, ws)
		},
	})
	mux.HandleFunc("/dev/", devHandler)
	mux.HandleFunc("/files/", filesHandler)
	mux.Handle("/", http.FileServer(webFS()))
	return mux
}
