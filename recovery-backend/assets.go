package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// The UI travels inside the backend: recovery/build.sh copies the phone
// build of recovery-ui and its web build into assets/ before the backend is
// compiled, so one binary is the whole recovery and the two cannot drift
// apart. assets/ holds only a placeholder in git; a backend built without
// the UI falls back to -ui and -web on disk.
//
// A file uploaded to /tmp/dev (dev.go) wins over both.
//
//go:embed all:assets
var assets embed.FS

var (
	memfdMu   sync.Mutex
	memfdPath string
)

// uiExecutable is the UI binary to start: an upload, else the embedded
// copy through a memfd (nothing is written to disk), else -ui.
func uiExecutable() string {
	if p := filepath.Join(devDir, "recovery-ui"); isFile(p) {
		return p
	}
	memfdMu.Lock()
	defer memfdMu.Unlock()
	if memfdPath != "" {
		return memfdPath
	}
	b, err := assets.ReadFile("assets/recovery-ui")
	if err != nil || len(b) == 0 {
		return *uiPath
	}
	fd, err := unix.MemfdCreate("recovery-ui", 0)
	if err != nil {
		log.Printf("memfd: %v", err)
		return *uiPath
	}
	f := os.NewFile(uintptr(fd), "recovery-ui")
	if _, err := f.Write(b); err != nil {
		log.Printf("memfd: %v", err)
		f.Close()
		return *uiPath
	}
	// Kept open for the life of the backend; the path names this process's
	// fd, so it means the same in the child that execs it.
	memfdPath = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd)
	return memfdPath
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// webFS is the web build: files uploaded to /tmp/dev/web first, then the
// embedded ones, then -web.
func webFS() http.FileSystem {
	var layers []http.FileSystem
	layers = append(layers, http.Dir(filepath.Join(devDir, "web")))
	if sub, err := fs.Sub(assets, "assets/web"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			layers = append(layers, http.FS(sub))
		}
	}
	layers = append(layers, http.Dir(*webDir))
	return overlay(layers)
}

type overlay []http.FileSystem

func (o overlay) Open(name string) (http.File, error) {
	var first error
	for _, l := range o {
		f, err := l.Open(name)
		if err == nil {
			return f, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}

// hasUI reports whether any UI can be started.
func hasUI() bool {
	p := uiExecutable()
	return p != "" && isFile(p)
}
