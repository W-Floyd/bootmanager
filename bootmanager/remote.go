package main

import (
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

// Remote UI's ports, both on -listen. The files port is a separate origin
// from the page's, so nothing served off a card can act as the page.
const (
	webPort   = "8080" // the page, and the protocol over a WebSocket at /ws
	filesPort = "8081" // /files/<dev>/<path>
)

type webServer struct {
	mux *http.ServeMux
	// static is the web build of bootmanager-ui (-web); the dev build
	// layers uploads over it.
	static http.FileSystem
}

func newWebServer(a *app) *webServer {
	w := &webServer{mux: http.NewServeMux(), static: http.Dir(*webDir)}
	w.mux.HandleFunc("/ws", a.rpc.ServeWebSocket)
	w.mux.HandleFunc("/", w.page)
	return w
}

// page serves the web build's files, and the page itself for any other
// path: the UI keeps its screen in the URL (/partitions/mmcblk0p2/usr, /boot),
// so a reload or a pasted link has to load it.
func (w *webServer) page(rw http.ResponseWriter, r *http.Request) {
	if f, err := w.static.Open(path.Clean(r.URL.Path)); err == nil {
		st, err := f.Stat()
		f.Close()
		if err == nil && !st.IsDir() {
			http.FileServer(w.static).ServeHTTP(rw, r)
			return
		}
	}
	f, err := w.static.Open("/index.html")
	if err != nil {
		http.Error(rw, "no web build of the UI (-web)", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(rw, r, "index.html", st.ModTime(), f)
}

// ipHostOnly refuses a request whose Host is a name rather than an
// address: a DNS-rebinding page, whose name has been pointed at the
// device, would otherwise count as this server's own origin.
func ipHostOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			http.Error(w, "use the device's address, not a name", http.StatusMisdirectedRequest)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// startRemote serves the UI to the host over the USB network. Nothing
// here asks for a password, so it listens on -listen only.
func (a *app) startRemote() (func(), error) {
	var closers []func() error
	stop := func() {
		for _, c := range closers {
			c()
		}
	}
	listen := func(port string) (net.Listener, error) {
		ln, err := net.Listen("tcp", net.JoinHostPort(*listen, port))
		if err == nil {
			closers = append(closers, ln.Close)
		}
		return ln, err
	}

	for port, h := range map[string]http.Handler{webPort: a.web.mux, filesPort: a.disks} {
		ln, err := listen(port)
		if err != nil {
			stop()
			return nil, err
		}
		srv := &http.Server{Handler: ipHostOnly(h), ReadHeaderTimeout: 10 * time.Second}
		closers = append(closers, srv.Close)
		go srv.Serve(ln)
	}
	return stop, nil
}
