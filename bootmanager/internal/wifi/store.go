package wifi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoreName is the file, on the USB stick, that keeps the networks joined.
// RAM does not outlast a boot and the stock eMMC is never written, so the
// stick the stage came from is the one place settings can live. The keys are
// in it as typed.
const StoreName = "stage-wifi.json"

// Saved is a network joined before.
type Saved struct {
	SSID string `json:"ssid"`
	PSK  string `json:"psk"`
}

// stickDir is where the stage's USB stick is mounted (the one holding
// NEMESIS.sh), "" if it is not.
func stickDir() string {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.HasPrefix(f[0], "/dev/sd") {
			continue
		}
		if _, err := os.Stat(filepath.Join(f[1], "NEMESIS.sh")); err == nil {
			return f[1]
		}
	}
	return ""
}

// Load reads the saved networks; none is not an error.
func (r Runner) Load() ([]Saved, error) {
	dir := stickDir()
	if dir == "" {
		return nil, errors.New("no USB stick mounted")
	}
	b, err := os.ReadFile(filepath.Join(dir, StoreName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Saved
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", StoreName, err)
	}
	return list, nil
}

// Save writes the list. The stick is mounted read-only (as the scan wants
// it), so it is made writable for the write and put back.
func (r Runner) Save(list []Saved) error {
	dir := stickDir()
	if dir == "" {
		return errors.New("no USB stick mounted")
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if out, err := r.busybox("mount", "-o", "remount,rw", dir); err != nil {
		return fmt.Errorf("remount %s read-write: %s", dir, out)
	}
	defer func() {
		r.busybox("sync")
		r.busybox("mount", "-o", "remount,ro", dir)
	}()
	tmp := filepath.Join(dir, StoreName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, StoreName))
}

// Upsert returns list with s in it, replacing the same SSID.
func Upsert(list []Saved, s Saved) []Saved {
	for i := range list {
		if list[i].SSID == s.SSID {
			list[i] = s
			return list
		}
	}
	return append(list, s)
}

// Forget returns list without ssid ("" empties it).
func Forget(list []Saved, ssid string) []Saved {
	if ssid == "" {
		return nil
	}
	var out []Saved
	for _, s := range list {
		if s.SSID != ssid {
			out = append(out, s)
		}
	}
	return out
}

// Find is the saved entry for ssid.
func Find(list []Saved, ssid string) (Saved, bool) {
	for _, s := range list {
		if s.SSID == ssid {
			return s, true
		}
	}
	return Saved{}, false
}
