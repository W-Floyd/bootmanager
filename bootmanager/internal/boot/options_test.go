package boot

import (
	"path/filepath"
	"strings"
	"testing"
)

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
