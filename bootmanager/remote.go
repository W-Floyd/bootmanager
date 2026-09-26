package main

import (
	"flag"
	"io"
	"net/http"
	"path"
	"strings"

	"golang.org/x/net/websocket"
)

var webDir = flag.String("web", "/usr/share/bootmanager-web", "web build of bootmanager-ui on disk, for a backend built without it embedded")

// webHandler serves the web build of bootmanager-ui and, at /ws, the backend
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
	mux.Handle("/", pageFallback(webFS()))
	return mux
}

// pageFallback serves the web build's files, and the page itself for any
// other path: the UI keeps its screen in the URL (/partitions/mmcblk0p2/usr,
// /file/..., /boot), so a reload or a pasted link has to load the page,
// which then opens that screen. /ws, /dev/ and /files/ have their own
// routes and never get here.
func pageFallback(fsys http.FileSystem) http.Handler {
	files := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, err := fsys.Open(path.Clean(r.URL.Path)); err == nil {
			st, err := f.Stat()
			f.Close()
			if err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
