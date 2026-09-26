package storage

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const maxPreview = 256 << 10

type Text struct {
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary,omitempty"`
	Text      string `json:"text,omitempty"`
}

// Read returns the start of a file as text, or says it is binary.
func (b *Browser) Read(dev, p string) (Text, error) {
	f, err := b.Open(dev, p)
	if err != nil {
		return Text{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Text{}, err
	}
	if st.IsDir() {
		return Text{}, os.ErrInvalid
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxPreview))
	if err != nil {
		return Text{}, err
	}
	t := Text{Size: st.Size(), Truncated: st.Size() > int64(len(buf))}
	if isBinary(buf) {
		t.Binary = true
	} else {
		t.Text = strings.ToValidUTF8(string(buf), "�")
	}
	return t, nil
}

// isBinary: a NUL in the first 8 KiB, or more than a tenth invalid UTF-8.
func isBinary(b []byte) bool {
	head := b[:min(len(b), 8192)]
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	n, bad := len(head), 0
	for len(head) > 0 {
		r, size := utf8.DecodeRune(head)
		if r == utf8.RuneError && size == 1 {
			bad++
		}
		head = head[size:]
	}
	return bad > n/10
}

// ServeHTTP serves GET /files/<dev>/<path>: inline for the browser to
// render, or as an attachment with ?download=1.
//
// It is served on its own port, a different origin from the page that
// drives the phone, so nothing from the card can act as that page. As a
// second line, every response but a PDF's is sandboxed by CSP (browsers
// refuse to show a sandboxed PDF, and their viewers run isolated anyway),
// sniffing is off, and markup a browser would render live goes out as text.
func (b *Browser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	dev, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/files/"), "/")
	f, err := b.Open(dev, p)
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

	name := path.Base(p)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	disposition, t := "inline", inlineType(f, name)
	if r.URL.Query().Has("download") {
		disposition, t = "attachment", "application/octet-stream"
	}
	if t != "application/pdf" {
		h.Set("Content-Security-Policy", "sandbox")
	}
	h.Set("Content-Type", t)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// inlineType is what the browser should render the file as: its
// extension's type when that is safe to render, text for anything that
// reads as text (configs, logs, extlinux.conf), else binary.
func inlineType(f io.ReadSeeker, name string) string {
	t := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
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
