package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/u-root/u-root/pkg/mount"
)

// Live restarts, for working on the recovery without a reflash.
//
// The backend restarts by exec'ing itself (or an uploaded build) in place:
// the PID stays, so sshd and the UI stay its children and keep running --
// an SSH session survives -- and a state file hands the new image what it
// cannot rediscover: those PIDs, which services were on, and whether the
// autoboot was already stopped. The sockets close on exec; the UI's
// transport reconnects by itself.
//
// Uploads, with the Dev uploads service on (it rides on Remote UI's HTTP
// port, so both must be on), from the container:
//
//	curl -T recovery-ui      http://172.16.42.1:8080/dev/recovery-ui       # restarts the UI
//	curl -T recovery-backend http://172.16.42.1:8080/dev/recovery-backend  # re-execs the backend
//	curl -T index.html       http://172.16.42.1:8080/dev/web/index.html    # served next reload
//
// tools/linuxboot/recovery/dev.sh builds and sends them. Uploads live in
// /tmp/dev, in RAM, and win over the built-in copies until the next boot.

const (
	devDir    = "/tmp/dev"
	statePath = "/tmp/recovery-state.json"
)

type handoff struct {
	Cancelled bool           `json:"cancelled"`
	UIPid     int            `json:"ui_pid"`
	Services  map[string]int `json:"services"` // name -> pid (0: in-process)
}

// loadHandoff returns the state an exec left, once.
func loadHandoff() *handoff {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	os.Remove(statePath)
	var h handoff
	if err := json.Unmarshal(b, &h); err != nil {
		log.Printf("handoff: %v", err)
		return nil
	}
	return &h
}

// reexec replaces this process with the uploaded backend, or with itself.
func reexec() error {
	h := handoff{UIPid: uiPid(), Services: map[string]int{}}
	cdMu.Lock()
	h.Cancelled = cancelled || remaining < 0
	cdMu.Unlock()
	svcMu.Lock()
	for _, s := range services {
		if !s.Running {
			continue
		}
		h.Services[s.Name] = s.pid
		if s.pid == 0 {
			// In-process: stopped here, started again by the new image.
			s.stop()
		}
	}
	svcMu.Unlock()
	b, _ := json.Marshal(h)
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		return err
	}

	unmountBrowsed()
	mountPool.UnmountAll(mount.MNT_DETACH)

	bin := filepath.Join(devDir, "recovery-backend")
	argv := os.Args
	if !isFile(bin) {
		// /proc/self/exe is u-root's bb for a built-in backend, which picks
		// the command from argv[0]; os.Args keeps that.
		bin = "/proc/self/exe"
	}
	log.Printf("re-exec %s (ui pid %d, services %v)", bin, h.UIPid, h.Services)
	return syscall.Exec(bin, argv, os.Environ())
}

// devHandler takes the uploads.
func devHandler(w http.ResponseWriter, r *http.Request) {
	if !serviceRunning("dev") {
		http.Error(w, "Dev uploads is off (Services on the phone)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "PUT a file", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/dev/")
	switch {
	case name == "recovery-ui", name == "recovery-backend":
	case strings.HasPrefix(name, "web/") && !strings.Contains(name[4:], "/"):
	default:
		http.Error(w, "upload recovery-ui, recovery-backend or web/<file>", http.StatusBadRequest)
		return
	}
	dst := filepath.Join(devDir, name)
	if err := saveUpload(dst, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("dev: received %s", dst)

	switch name {
	case "recovery-ui":
		restartUI()
		fmt.Fprintln(w, "ui restarted")
	case "recovery-backend":
		fmt.Fprintln(w, "backend re-exec")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := reexec(); err != nil {
				log.Printf("re-exec: %v", err)
			}
		}()
	default:
		fmt.Fprintln(w, "saved; reload the page")
	}
}

func saveUpload(dst string, body io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("empty upload")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// A rename, so a running copy of the old file is never half-overwritten.
	return os.Rename(tmp, dst)
}

// devRestart is the socket command: {"what": "ui"|"backend"}, for restarting
// without an upload (after one sent another way, or just to reset state).
func devRestart(args []byte) (any, error) {
	a, err := decode[struct{ What string }](args)
	if err != nil {
		return nil, err
	}
	switch a.What {
	case "ui":
		restartUI()
	case "backend":
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := reexec(); err != nil {
				log.Printf("re-exec: %v", err)
			}
		}()
	default:
		return nil, fmt.Errorf("restart ui or backend, not %q", a.What)
	}
	return nil, nil
}
