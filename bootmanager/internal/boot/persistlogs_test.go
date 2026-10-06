package boot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A plain settings path: the copies land beside it, with a header each, and
// nothing else is written.
func TestPersistLogs(t *testing.T) {
	src, boot := t.TempDir(), t.TempDir()
	settings := filepath.Join(boot, "bootmanager.json")
	writeTemp(t, boot, "bootmanager.json", "{}")
	m := &Manager{cfg: Config{Settings: settings}}
	copies := []LogCopy{
		{writeTemp(t, src, "uinit.log", "uinit line\n"), "stage-uinit.log"},
		{filepath.Join(src, "missing.log"), "stage-record_boot.log"},
		{writeTemp(t, src, "bm.log", "bm line\n"), "stage-bootmanager.log"},
	}
	if err := m.PersistLogs(copies, 12345*time.Millisecond, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(boot)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != "bootmanager.json stage-bootmanager.log stage-record_boot.log stage-uinit.log" {
		t.Errorf("files: %s", got)
	}
	b, _ := os.ReadFile(filepath.Join(boot, "stage-uinit.log"))
	want := "# bootmanager: copy of " + copies[0].Src + ", taken at Boot Manager uptime 12.3 s\nuinit line\n"
	if string(b) != want {
		t.Errorf("stage-uinit.log %q, want %q", b, want)
	}
	b, _ = os.ReadFile(filepath.Join(boot, "stage-record_boot.log"))
	if !strings.Contains(string(b), "# not copied: ") || !strings.HasPrefix(string(b), "# bootmanager: copy of ") {
		t.Errorf("a missing source: %q", b)
	}
	// Overwritten, not appended.
	if err := m.PersistLogs(copies[:1], time.Second, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(boot, "stage-uinit.log"))
	if strings.Count(string(b), "uinit line") != 1 || !strings.Contains(string(b), "uptime 1.0 s") {
		t.Errorf("second run: %q", b)
	}
}

func TestPersistLogsSkipsWithoutALocation(t *testing.T) {
	src := t.TempDir()
	c := []LogCopy{{writeTemp(t, src, "a.log", "x"), "stage-a.log"}}
	ref := filepath.Join(t.TempDir(), "location")
	for name, settings := range map[string]string{
		"none":                  "",
		"@FILE not written yet": "@" + ref,
		"@FILE that is empty":   "@" + writeTemp(t, filepath.Dir(ref), "location", ""),
		"a device that is gone": "nosuchdev0:linuxboot/bootmanager.json",
	} {
		m := &Manager{cfg: Config{Settings: settings}}
		if err := m.PersistLogs(c, 0, time.Second); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "stage-a.log")); err == nil {
		t.Error("a copy written without a location")
	}
}

func TestPersistLogsErrorIsReturnedNotFatal(t *testing.T) {
	src := t.TempDir()
	c := []LogCopy{{writeTemp(t, src, "a.log", "x"), "stage-a.log"}}
	// The directory beside the settings does not exist: not made, an error.
	m := &Manager{cfg: Config{Settings: filepath.Join(t.TempDir(), "gone", "bootmanager.json")}}
	err := m.PersistLogs(c, 0, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "no directory") {
		t.Errorf("err %v", err)
	}
	// And the next call is not stuck behind it.
	if err := m.PersistLogs(c, 0, 5*time.Second); err == nil || strings.Contains(err.Error(), "already running") {
		t.Errorf("second err %v", err)
	}
}

func TestPersistLogsRefusesAConcurrentRun(t *testing.T) {
	boot := t.TempDir()
	writeTemp(t, boot, "bootmanager.json", "{}")
	m := &Manager{cfg: Config{Settings: filepath.Join(boot, "bootmanager.json")}}
	persistBusy.Lock()
	defer persistBusy.Unlock()
	if err := m.PersistLogs(nil, 0, time.Second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("err %v", err)
	}
}

func TestLogCopyKeepsTheEnd(t *testing.T) {
	p := writeTemp(t, t.TempDir(), "big.log", strings.Repeat("a", maxLogCopy)+"TAIL")
	b := string(logCopy(p, 0))
	if !strings.HasSuffix(b, "aaaTAIL") || !strings.Contains(b, "# only the last ") || len(b) > maxLogCopy+300 {
		t.Errorf("%d bytes, head %q", len(b), b[:120])
	}
}
