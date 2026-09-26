package main

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Files from the browsed partitions, for viewing and downloading.
//
// Every path is opened through an os.Root at the partition's mount point,
// so neither ".." nor a symlink on the card can reach outside it -- an
// absolute symlink would otherwise resolve against the stage's own root,
// where /etc/ssh holds the host key.

// openBrowsed opens dev's file at p (a path inside the partition).
func openBrowsed(dev, p string) (*os.File, string, error) {
	mp, err := mountForBrowse(dev)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(mp)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	rel := strings.TrimPrefix(path.Clean("/"+p), "/")
	if rel == "" {
		rel = "."
	}
	f, err := root.Open(rel)
	return f, "/" + strings.TrimPrefix(rel, "."), err
}

const maxPreview = 256 << 10

// fsRead returns the start of a file as text for the UI's viewer, or says
// it is binary.
func fsRead(args []byte) (any, error) {
	a, err := decode[struct{ Dev, Path string }](args)
	if err != nil {
		return nil, err
	}
	f, _, err := openBrowsed(a.Dev, a.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, os.ErrInvalid
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPreview))
	if err != nil {
		return nil, err
	}
	res := map[string]any{"size": st.Size(), "truncated": st.Size() > int64(len(b))}
	if isBinary(b) {
		res["binary"] = true
		return res, nil
	}
	res["text"] = strings.ToValidUTF8(string(b), "�")
	return res, nil
}

// isBinary: a NUL in the first 8 KiB, or mostly invalid UTF-8.
func isBinary(b []byte) bool {
	head := b[:min(len(b), 8192)]
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	bad := 0
	for len(head) > 0 {
		r, n := utf8.DecodeRune(head)
		if r == utf8.RuneError && n == 1 {
			bad++
		}
		head = head[n:]
	}
	return bad > len(b[:min(len(b), 8192)])/10
}

// filesHandler serves GET /files/<dev>/<path>: inline for the browser to
// render, or as an attachment with ?download=1.
//
// The page on this origin drives the phone as root (/ws, /dev/), so a file
// from the card must never run as it: every response but a PDF's (see
// below) is sandboxed by CSP
// (an opaque origin, no scripts), sniffing is off, and markup that a
// browser would render live (HTML, SVG, XML) goes out as plain text.
func filesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/files/")
	dev, p, _ := strings.Cut(rest, "/")
	if dev == "" || strings.ContainsAny(dev, "/.") {
		http.Error(w, "GET /files/<dev>/<path>", http.StatusBadRequest)
		return
	}
	f, clean, err := openBrowsed(dev, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}

	name := filepath.Base(clean)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") != "" {
		h.Set("Content-Security-Policy", "sandbox")
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	} else {
		t := inlineType(f, name)
		// Browsers refuse to show a PDF in a sandboxed document, and do not
		// need it sandboxed: their PDF viewers run in their own isolated
		// context, never as this origin's page.
		if t != "application/pdf" {
			h.Set("Content-Security-Policy", "sandbox")
		}
		h.Set("Content-Type", t)
		h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// inlineType picks what the browser should render the file as: its
// extension's type when that is safe to render, text for anything that
// reads as text (configs, logs, extlinux.conf, System.map), else binary.
func inlineType(f *os.File, name string) string {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	base, _, _ := mime.ParseMediaType(t)
	switch {
	case strings.HasPrefix(base, "image/") && base != "image/svg+xml",
		strings.HasPrefix(base, "video/"), strings.HasPrefix(base, "audio/"),
		base == "application/pdf":
		return t
	}
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	f.Seek(0, io.SeekStart)
	if !isBinary(head[:n]) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
