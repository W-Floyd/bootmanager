package boot

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// AppSpec is a tile that runs a program on the stage (a game, a tool) in
// place of booting a kernel. It follows the boot entries, is never the
// default, and cannot be moved or edited. The UI is down while it runs and
// comes back when it exits.
type AppSpec struct {
	Label  string
	Detail string
	Argv   []string
}

// appAt is the app at entry index i, which counts the boot entries first.
func (m *Manager) appAt(i int) (AppSpec, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := i - len(m.images)
	if j < 0 || j >= len(m.cfg.Apps) {
		return AppSpec{}, false
	}
	return m.cfg.Apps[j], true
}

// IsApp reports whether entry i is an app.
func (m *Manager) IsApp(i int) bool {
	_, ok := m.appAt(i)
	return ok
}

// runApp runs app to its end. A failure to start, or a non-zero exit, is
// the error, with what the program printed.
func (m *Manager) runApp(app AppSpec) error {
	if len(app.Argv) == 0 {
		return errors.New(app.Label + " has no command")
	}
	m.mu.Lock()
	if m.booting {
		m.mu.Unlock()
		return errors.New("a boot is already in progress")
	}
	m.booting = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.booting = false
		m.mu.Unlock()
		m.cfg.Emit("busy", map[string]string{"text": ""})
	}()

	m.cfg.Emit("busy", map[string]string{"text": "Starting " + app.Label + "…"})
	if m.cfg.Suspend != nil {
		m.cfg.Suspend()
	}
	if m.cfg.Resume != nil {
		defer m.cfg.Resume()
	}
	log.Printf("app %s: %s", app.Label, strings.Join(app.Argv, " "))
	out, err := exec.Command(app.Argv[0], app.Argv[1:]...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		log.Printf("app %s: %v: %s", app.Label, err, strings.TrimSpace(string(out)))
		text := fmt.Sprintf("%s: %v %s", app.Label, err, msg)
		// The UI that asked is gone (it was taken down for the app); tell
		// the one that replaces it once it is up.
		go func() {
			time.Sleep(4 * time.Second)
			m.cfg.Emit("error", map[string]string{"message": text})
		}()
		return errors.New(text)
	}
	log.Printf("app %s exited", app.Label)
	return nil
}
