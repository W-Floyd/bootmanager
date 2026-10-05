package reprobe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSys makes /sys/bus/<bus>/devices/<dev>[/driver], and an empty
// drivers_probe unless the bus is listed in noProbe.
func fakeSys(t *testing.T, buses map[string][]string, bound map[string]bool, noProbe ...string) string {
	t.Helper()
	root := t.TempDir()
	for bus, devs := range buses {
		if err := os.MkdirAll(filepath.Join(root, "bus", bus, "devices"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !slicesContains(noProbe, bus) {
			if err := os.WriteFile(filepath.Join(root, "bus", bus, "drivers_probe"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range devs {
			dir := filepath.Join(root, "bus", bus, "devices", d)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if bound[bus+"/"+d] {
				if err := os.Symlink("../../../../bus/"+bus+"/drivers/x", filepath.Join(dir, "driver")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return root
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRun(t *testing.T) {
	root := fakeSys(t, map[string][]string{
		"i2c":      {"0-0038", "0-0050"},
		"platform": {"a.panel", "b.bound"},
		"spmi":     {"spmi-0"},
	}, map[string]bool{"platform/b.bound": true, "i2c/0-0050": true}, "spmi")
	var log bytes.Buffer
	s, err := Run(root, false, &log)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Summary{Buses: 3, Unbound: 3, Probed: 2, NoProbe: 1}); s != want {
		t.Errorf("summary %+v, want %+v\n%s", s, want, log.String())
	}
	if got := read(t, filepath.Join(root, "bus/i2c/drivers_probe")); got != "0-0038" {
		t.Errorf("i2c drivers_probe %q: only the device without a driver, once", got)
	}
	if got := read(t, filepath.Join(root, "bus/platform/drivers_probe")); got != "a.panel" {
		t.Errorf("platform drivers_probe %q: the bound device must be skipped", got)
	}
	if _, err := os.Stat(filepath.Join(root, "bus/spmi/drivers_probe")); err == nil {
		t.Error("a missing drivers_probe was created")
	}
	for _, w := range []string{"i2c/0-0038: probed", "spmi/spmi-0: no driver, and the bus has no drivers_probe"} {
		if !strings.Contains(log.String(), w) {
			t.Errorf("log lacks %q:\n%s", w, log.String())
		}
	}
	if strings.Contains(log.String(), "0-0050") || strings.Contains(log.String(), "b.bound") {
		t.Errorf("a bound device was logged:\n%s", log.String())
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	root := fakeSys(t, map[string][]string{"platform": {"a.panel"}}, nil)
	var log bytes.Buffer
	s, err := Run(root, true, &log)
	if err != nil || s.Unbound != 1 || s.Probed != 0 {
		t.Fatalf("%+v %v", s, err)
	}
	if got := read(t, filepath.Join(root, "bus/platform/drivers_probe")); got != "" {
		t.Errorf("--dry-run wrote %q", got)
	}
	if !strings.Contains(log.String(), "platform/a.panel: no driver, would probe") {
		t.Errorf("log: %s", log.String())
	}
}

func TestWriteError(t *testing.T) {
	root := fakeSys(t, map[string][]string{"platform": {"a"}}, nil)
	p := filepath.Join(root, "bus/platform/drivers_probe")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil { // exists, but cannot be written
		t.Fatal(err)
	}
	s, err := Run(root, false, &bytes.Buffer{})
	if err != nil || s.WriteErrs != 1 || s.Probed != 0 {
		t.Errorf("%+v %v", s, err)
	}
}

func TestNoSysBus(t *testing.T) {
	if _, err := Run(t.TempDir(), false, &bytes.Buffer{}); err == nil {
		t.Error("no /sys/bus: no error")
	}
}
