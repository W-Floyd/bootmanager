package boot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogCopy is one log to keep: Src is read, and its copy written as Name
// next to the settings file.
type LogCopy struct{ Src, Name string }

// maxLogCopy is how much of the end of a log is kept.
const maxLogCopy = 1 << 20

var persistBusy sync.Mutex

// PersistLogs writes copies of logs beside the settings file (the same
// directory on the same device), so they outlive the stage: a stage's /tmp
// is gone once the next system starts. Each copy is replaced whole and
// starts with a line saying where it came from and the Boot Manager's
// uptime. Nothing else is written.
//
// It is for the moment before a handover and must never hold one up: it
// returns after timeout whatever it is doing, a failure is only returned
// (the caller logs it), no known location, or a device that is not there,
// is not a failure, and a second call while one runs does nothing.
func (m *Manager) PersistLogs(copies []LogCopy, uptime, timeout time.Duration) error {
	loc, err := parseSettingsFile(m.cfg.Settings).resolved()
	if err != nil || loc.path == "" {
		return nil // nowhere known to keep them
	}
	if loc.dev != "" {
		if _, err := os.Stat("/dev/" + loc.dev); err != nil {
			return nil // the device is gone
		}
	}
	if !persistBusy.TryLock() {
		return errors.New("already running")
	}
	done := make(chan error, 1)
	go func() {
		defer persistBusy.Unlock()
		done <- writeLogCopies(loc, copies, uptime)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("not finished after %v, going on without it", timeout)
	}
}

// writeLogCopies puts copies in the directory loc's file is in, which must
// exist: it is not made.
func writeLogCopies(loc settingsFile, copies []LogCopy, uptime time.Duration) error {
	dir := filepath.Dir(loc.path)
	return loc.withRoot(true, func(root string) error {
		d := filepath.Join(root, dir)
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			return fmt.Errorf("no directory %s beside the settings", dir)
		}
		var errs []error
		for _, c := range copies {
			if err := writeFileSync(filepath.Join(d, c.Name), logCopy(c.Src, uptime)); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
			}
		}
		return errors.Join(errs...)
	})
}

// logCopy is the bytes of a copy: the header line, then the end of src (or
// what went wrong reading it).
func logCopy(src string, uptime time.Duration) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# bootmanager: copy of %s, taken at Boot Manager uptime %.1f s\n", src, uptime.Seconds())
	body, truncated, err := tailOf(src, maxLogCopy)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "# not copied: %v\n", err)
	case truncated:
		fmt.Fprintf(&b, "# only the last %d bytes\n", maxLogCopy)
	}
	b.Write(body)
	return []byte(b.String())
}

// tailOf is the last n bytes of the file at p, or all of it if smaller.
func tailOf(p string, n int64) (b []byte, truncated bool, err error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > n {
		if _, err := f.Seek(-n, io.SeekEnd); err != nil {
			return nil, false, err
		}
		truncated = true
	}
	b, err = io.ReadAll(io.LimitReader(f, n))
	return b, truncated, err
}
