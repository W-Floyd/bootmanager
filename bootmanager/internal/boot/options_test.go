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
	got := m.optionsCmdline(nil, cmdline)
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
	if got := m2.optionsCmdline(nil, "pmos.nosplash pmos_root_uuid=aaa"); !strings.Contains(got, "pmos.nosplash") {
		t.Errorf("an unset splash still edited it: %q", got)
	}

	if err := m2.SetOption("nope", "on"); err == nil {
		t.Error("an unknown option was accepted")
	}
	if err := m2.SetOption("quiet", "maybe"); err == nil {
		t.Error("a state that is not on or off was accepted")
	}
}

// A choice made for one entry wins over the global one, for that entry only,
// and the chips (EntryOptions) show what it will boot with.
func TestOptionsPerEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linuxboot", "bootmanager.json")
	m := New(Config{Settings: path, Emit: func(string, any) {}})
	m.setImages([]boot.OSImage{
		&boot.LinuxImage{Name: "pmOS", Cmdline: "pmos.nosplash plymouth.enable=0 loglevel=7 pmos_root_uuid=aaa"},
		&boot.LinuxImage{Name: "pmOS", Cmdline: "pmos.nosplash pmos_root_uuid=bbb"},
		&boot.LinuxImage{Name: "Debian", Cmdline: "root=/dev/sda2"},
	})
	state := func(i int, id string) (on, own bool) {
		t.Helper()
		opts, err := m.EntryOptions(i)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range opts {
			if o.ID == id {
				return o.On, o.Override
			}
		}
		t.Fatalf("entry %d has no option %s: %v", i, id, opts)
		return
	}
	if on, own := state(0, "splash"); on || own {
		t.Errorf("as it stands, entry 0 splash on=%v own=%v", on, own)
	}
	// Only entry 0.
	if err := m.SetEntryOption(0, "splash", "on"); err != nil {
		t.Fatal(err)
	}
	if on, own := state(0, "splash"); !on || !own {
		t.Errorf("after setting entry 0: on=%v own=%v", on, own)
	}
	if on, own := state(1, "splash"); on || own {
		t.Errorf("entry 1 changed with entry 0: on=%v own=%v", on, own)
	}
	// It wins over a global choice that says otherwise, for this entry.
	if err := m.SetOption("splash", "off"); err != nil {
		t.Fatal(err)
	}
	if on, _ := state(0, "splash"); !on {
		t.Error("the global choice beat the entry's own")
	}
	if on, _ := state(1, "splash"); on {
		t.Error("the global off did not reach entry 1")
	}
	// A pmOS option is not offered on another system's entry.
	opts, _ := m.EntryOptions(2)
	for _, o := range opts {
		if o.ID == "splash" {
			t.Error("the pmOS splash was offered on a Debian entry")
		}
	}
	// Kept, and clearable.
	m2 := New(Config{Settings: path, Emit: func(string, any) {}})
	m2.setImages([]boot.OSImage{&boot.LinuxImage{Name: "pmOS", Cmdline: "pmos.nosplash pmos_root_uuid=aaa"}})
	if on, own := func() (bool, bool) {
		o, _ := m2.EntryOptions(0)
		for _, x := range o {
			if x.ID == "splash" {
				return x.On, x.Override
			}
		}
		return false, false
	}(); !on || !own {
		t.Errorf("after a restart: on=%v own=%v", on, own)
	}
	if err := m2.SetEntryOption(0, "splash", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := m2.Cmdline(0); !strings.Contains(got, "pmos.nosplash") && strings.Contains(got, "plymouth.enable") {
		t.Errorf("after clearing: %q", got)
	}
}
