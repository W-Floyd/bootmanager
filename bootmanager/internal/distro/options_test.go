package distro

import (
	"slices"
	"strings"
	"testing"
)

const fogonaCmdline = "plymouth.ignore-serial-consoles pmos.nosplash loglevel=7 ignore_loglevel plymouth.enable=0 console=tty0 pmos_root_uuid=aaa"

func words(s string) []string { return strings.Fields(s) }

func TestEditApply(t *testing.T) {
	e := Edit{Remove: []string{"loglevel", "quiet"}, Add: []string{"quiet", "loglevel=3"}}
	got := words(e.Apply("a loglevel=7 b quiet c"))
	want := []string{"a", "b", "c", "quiet", "loglevel=3"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// A key takes off the bare word and every value of it, not a longer name.
	if got := e.Apply("loglevelx=1 loglevel"); got != "loglevelx=1 quiet loglevel=3" {
		t.Errorf("got %q", got)
	}
}

func TestSplashOptionOnAndOff(t *testing.T) {
	on := words(ApplyOptions(fogonaCmdline, map[string]string{"splash": "on"}))
	for _, w := range []string{"pmos.nosplash", "plymouth.enable=0"} {
		if slices.Contains(on, w) {
			t.Errorf("splash on left %q: %q", w, on)
		}
	}
	if !slices.Contains(on, "splash") {
		t.Errorf("splash on did not add the word the initramfs reads: %q", on)
	}
	if !slices.Contains(on, "pmos_root_uuid=aaa") || !slices.Contains(on, "plymouth.ignore-serial-consoles") {
		t.Errorf("splash on took too much: %q", on)
	}
	off := words(ApplyOptions("console=tty0 splash pmos_root_uuid=aaa", map[string]string{"splash": "off"}))
	if slices.Contains(off, "splash") || !slices.Contains(off, "plymouth.enable=0") {
		t.Errorf("splash off: %q", off)
	}
}

func TestQuietOption(t *testing.T) {
	on := words(ApplyOptions(fogonaCmdline, map[string]string{"quiet": "on"}))
	if !slices.Contains(on, "quiet") || !slices.Contains(on, "loglevel=3") || slices.Contains(on, "ignore_loglevel") || slices.Contains(on, "loglevel=7") {
		t.Errorf("quiet on: %q", on)
	}
	off := words(ApplyOptions("quiet loglevel=3 x", map[string]string{"quiet": "off"}))
	if slices.Contains(off, "quiet") || !slices.Contains(off, "loglevel=7") || !slices.Contains(off, "ignore_loglevel") {
		t.Errorf("quiet off: %q", off)
	}
}

// An option not chosen leaves the command line exactly as it was, and one
// that names another distribution's words stays off a system that is not its.
func TestOptionsLeaveWhatIsNotAsked(t *testing.T) {
	if got := ApplyOptions(fogonaCmdline, nil); got != fogonaCmdline {
		t.Errorf("no choices changed it: %q", got)
	}
	other := "root=/dev/sda2 quiet"
	if got := ApplyOptions(other, map[string]string{"splash": "off"}); got != other {
		t.Errorf("a pmOS option reached another system: %q", got)
	}
}
