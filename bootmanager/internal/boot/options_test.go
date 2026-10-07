package boot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
)

// The editor starts from the entry's command line with the options applied,
// and refuses an entry with none to edit.
func TestCmdlineForTheEditor(t *testing.T) {
	m := New(Config{Settings: filepath.Join(t.TempDir(), "s.json"), Emit: func(string, any) {}})
	m.setImages([]boot.OSImage{
		&boot.LinuxImage{Name: "pmOS", Cmdline: "pmos.nosplash pmos_root_uuid=aaa loglevel=7"},
		&boot.LinuxImage{Name: "other"},
	})
	if err := m.SetOption("splash", "on"); err != nil {
		t.Fatal(err)
	}
	got, err := m.Cmdline(0)
	if err != nil || strings.Contains(got, "pmos.nosplash") || !strings.Contains(got, "loglevel=7") {
		t.Fatalf("Cmdline(0) = %q, %v", got, err)
	}
	if err := m.StartWith(0, "   "); err == nil {
		t.Error("an empty command line was started")
	}
	if _, err := m.Cmdline(5); err == nil {
		t.Error("a missing entry has a command line")
	}
}

func TestOptionsAreKeptAndApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linuxboot", "bootmanager.json")
	m := New(Config{Settings: path, Emit: func(string, any) {}})

	for _, o := range m.Options() {
		if o.State != "" {
			t.Fatalf("%s starts as %q, want unset", o.ID, o.State)
		}
	}
	if err := m.SetOption("splash", "on"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetOption("quiet", "on"); err != nil {
		t.Fatal(err)
	}
	cmdline := "pmos.nosplash plymouth.enable=0 loglevel=7 ignore_loglevel pmos_root_uuid=aaa"
	got := m.optionsCmdline(cmdline)
	for _, w := range []string{"pmos.nosplash", "plymouth.enable=0", "ignore_loglevel", "loglevel=7"} {
		if strings.Contains(" "+got+" ", " "+w+" ") {
			t.Errorf("%q still on the command line: %q", w, got)
		}
	}
	if !strings.Contains(got, "quiet") || !strings.Contains(got, "pmos_root_uuid=aaa") {
		t.Errorf("command line: %q", got)
	}

	// Kept: a manager started again reads them back.
	m2 := New(Config{Settings: path, Emit: func(string, any) {}})
	states := map[string]string{}
	for _, o := range m2.Options() {
		states[o.ID] = o.State
	}
	if states["splash"] != "on" || states["quiet"] != "on" {
		t.Errorf("after a restart: %v", states)
	}

	// Back to the entry's own words.
	if err := m2.SetOption("splash", ""); err != nil {
		t.Fatal(err)
	}
	if got := m2.optionsCmdline("pmos.nosplash pmos_root_uuid=aaa"); !strings.Contains(got, "pmos.nosplash") {
		t.Errorf("an unset splash still edited it: %q", got)
	}

	if err := m2.SetOption("nope", "on"); err == nil {
		t.Error("an unknown option was accepted")
	}
	if err := m2.SetOption("quiet", "maybe"); err == nil {
		t.Error("a state that is not on or off was accepted")
	}
}
