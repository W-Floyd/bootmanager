package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
)

func images(labels ...string) []boot.OSImage {
	var out []boot.OSImage
	for _, l := range labels {
		out = append(out, &boot.LinuxImage{Name: l})
	}
	return out
}

func labels(imgs []boot.OSImage) []string {
	var out []string
	for _, img := range imgs {
		out = append(out, img.Label())
	}
	return out
}

func TestArrange(t *testing.T) {
	imgs := images("postmarketOS", "Rescue", "postmarketOS", "Android")
	out, keys, def, explicit := arrange(imgs, Settings{Order: []string{"Android", "gone", "postmarketOS (2)"}, Default: "Rescue"})
	if want := []string{"Android", "postmarketOS (2)", "postmarketOS", "Rescue"}; !slices.Equal(keys, want) {
		t.Errorf("keys %q, want %q", keys, want)
	}
	if labels(out)[3] != "Rescue" || def != 3 || !explicit {
		t.Errorf("default %d (%v) of %q", def, explicit, labels(out))
	}
	// None there: the scan's first, postmarketOS, now third.
	if _, keys, def, explicit := arrange(imgs, Settings{Order: []string{"Android", "postmarketOS (2)"}, Default: "gone"}); keys[def] != "postmarketOS" || explicit {
		t.Errorf("a default that is not there: %d (%q), %v", def, keys, explicit)
	}
}

func TestMoveAndDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linuxboot", "bootmanager.json")
	m := New(Config{Settings: path, Emit: func(string, any) {}})
	m.setImages(images("A", "B", "C"))

	// Not chosen: the scan's first, A, wherever it is moved.
	if err := m.Move(0, 1); err != nil {
		t.Fatal(err)
	}
	if e := m.List(); e.Entries[0].Label != "B" || e.Default != 1 || e.Explicit {
		t.Errorf("after moving A right: %+v", e)
	}
	// Chosen: it goes with its entry.
	if err := m.SetDefault(1); err != nil {
		t.Fatal(err)
	}
	if err := m.Move(1, 1); err != nil {
		t.Fatal(err)
	}
	if e := m.List(); e.Entries[2].Label != "A" || e.Default != 2 || !e.Explicit {
		t.Errorf("after choosing A and moving it right: %+v", e)
	}
	if err := m.Move(2, 1); err == nil {
		t.Error("moved the last entry right")
	}

	var s Settings
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	if err != nil || !slices.Equal(s.Order, []string{"B", "C", "A"}) || s.Default != "A" {
		t.Fatalf("saved %s (%v)", b, err)
	}

	// A new scan, C gone (its card out) and D new: the order holds, C
	// keeps its place for when it is back.
	m2 := New(Config{Settings: path, Emit: func(string, any) {}})
	m2.setImages(images("D", "A", "B"))
	if e := m2.List(); e.Entries[0].Label != "B" || e.Entries[1].Label != "A" || e.Entries[2].Label != "D" || e.Default != 1 {
		t.Errorf("reloaded: %+v", e)
	}
	if err := m2.Move(2, -1); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	json.Unmarshal(b, &s)
	if !slices.Equal(s.Order, []string{"B", "D", "A", "C"}) {
		t.Errorf("order with C away: %q", s.Order)
	}

	// Cleared: back to the scan's first, D, where its card is (third).
	if err := m2.SetDefault(-1); err != nil {
		t.Fatal(err)
	}
	if e := m2.List(); e.Entries[e.Default].Label != "D" || e.Explicit {
		t.Errorf("default cleared: %+v", e)
	}
}

func TestSettingsFile(t *testing.T) {
	for in, want := range map[string]settingsFile{
		"mmcblk0p1:linuxboot/bootmanager.json": {"mmcblk0p1", "linuxboot/bootmanager.json"},
		"/tmp/bm.json":                         {"", "/tmp/bm.json"},
		"":                                     {},
	} {
		if got := parseSettingsFile(in); got != want {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
	if got := unescape(`/mnt/a\040b`); got != "/mnt/a b" {
		t.Errorf("unescape: %q", got)
	}
}

// On a device, as root, with BM_SETTINGS_DEV naming a partition the boot
// scan has mounted read-only: saving must mount it read-write past that
// mount and leave it as it was.
func TestSettingsOnDevice(t *testing.T) {
	dev := os.Getenv("BM_SETTINGS_DEV")
	if dev == "" {
		t.Skip("BM_SETTINGS_DEV not set")
	}
	f := settingsFile{dev, "bootmanager-test.json"}
	before, err := mountsOf("/dev/" + dev)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{Order: []string{"b", "a"}, Default: "a"}
	if err := f.save(want); err != nil {
		t.Fatal(err)
	}
	got, err := f.load()
	if err != nil || !slices.Equal(got.Order, want.Order) || got.Default != want.Default {
		t.Errorf("loaded %+v, %v", got, err)
	}
	if err := f.withRoot(true, func(root string) error { return os.Remove(filepath.Join(root, f.path)) }); err != nil {
		t.Error(err)
	}
	after, _ := mountsOf("/dev/" + dev)
	if !slices.Equal(before, after) {
		t.Errorf("mounts before %v, after %v", before, after)
	}
	t.Logf("mounts kept: %v", after)
}

func TestAutobootSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bm.json")
	newManager := func(flag int) *Manager {
		return New(Config{Autoboot: flag, Settings: file, Emit: func(string, any) {}})
	}
	// Unset: the flag decides. Off by flag, a user's "on" counts down for the default.
	if s := newManager(10).AutobootSettings(); !s.Enabled || s.Seconds != 10 || !s.Saved {
		t.Errorf("flag 10: %+v", s)
	}
	m := newManager(0)
	if s := m.AutobootSettings(); s.Enabled {
		t.Errorf("flag 0: %+v", s)
	}
	if err := m.SetAutoboot(true, 0); err != nil {
		t.Fatal(err)
	}
	if s := m.AutobootSettings(); !s.Enabled || s.Seconds != defaultCountdown {
		t.Errorf("on, no length: %+v", s)
	}
	// Kept, and read back by the next start.
	if err := m.SetAutoboot(false, 25); err != nil {
		t.Fatal(err)
	}
	if s := newManager(10).AutobootSettings(); s.Enabled || s.Seconds != 25 {
		t.Errorf("after restart: %+v", s)
	}
	// A length alone leaves the entries' settings alone, and a bad one is refused.
	for _, bad := range []int{-1, MaxCountdown + 1} {
		if err := m.SetAutoboot(true, bad); err == nil {
			t.Errorf("%d accepted", bad)
		}
	}
	if s := m.AutobootSettings(); s.Enabled || s.Seconds != 25 {
		t.Errorf("a refused change changed it: %+v", s)
	}
	// With no settings location it is for this run, and says so.
	if s := New(Config{Autoboot: 10, Emit: func(string, any) {}}).AutobootSettings(); s.Saved {
		t.Errorf("no file but saved: %+v", s)
	}
}

// A settings file that cannot be read (its card is not up yet) is not
// written over with defaults: a change is refused until it reads.
func TestChangeRefusedWhileSettingsUnreadable(t *testing.T) {
	m := New(Config{Autoboot: 10, Settings: "nonexistent0:linuxboot/bootmanager.json", Emit: func(string, any) {}})
	if err := m.SetAutoboot(false, 0); err == nil {
		t.Fatal("changed settings that could not be read")
	}
	if s := m.AutobootSettings(); !s.Enabled {
		t.Errorf("a refused change took effect: %+v", s)
	}
}
