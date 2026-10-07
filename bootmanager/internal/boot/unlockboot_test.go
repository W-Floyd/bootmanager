package boot

import "testing"

// Settings kept in a raw partition's tail, through the same load and save as
// a file's.
func TestSettingsInARawPartition(t *testing.T) {
	r := rawFile(t, 96<<20)
	old := rawDevice
	rawDevice = func(name string) (string, error) { return r.path, nil }
	defer func() { rawDevice = old }()

	f := parseSettingsFile("raw:boot_a")
	if f.raw != "boot_a" || !f.persists() {
		t.Fatalf("parsed %+v", f)
	}
	if s, err := f.load(); err != nil || s.Default != "" {
		t.Fatalf("empty partition: %+v, %v", s, err)
	}
	want := Settings{Order: []string{"B", "A"}, Default: "A", DefaultVolume: "uuid-1", Countdown: 4}
	if err := f.save(want); err != nil {
		t.Fatal(err)
	}
	got, err := f.load()
	if err != nil || got.Default != "A" || got.DefaultVolume != "uuid-1" || got.Countdown != 4 || len(got.Order) != 2 {
		t.Fatalf("loaded %+v, %v", got, err)
	}
}

func TestUnlockToBoot(t *testing.T) {
	locked := []Locked{{Dev: "sde27", Name: "userdata", UUID: "uuid-1"}, {Dev: "sdf1", UUID: "uuid-2"}}
	m := &Manager{settingsLoaded: true, locked: locked, settings: Settings{Default: "postmarketOS", DefaultVolume: "uuid-1"}}

	if l := m.unlockToBootLocked(true); l == nil || l.Dev != "sde27" {
		t.Fatalf("the default's volume is locked, wanted sde27, got %+v", l)
	}
	if m.unlockToBootLocked(false) != nil {
		t.Error("asked to unlock with autoboot off")
	}
	m.explicit = true // the default is among the entries now
	if m.unlockToBootLocked(true) != nil {
		t.Error("asked to unlock though the default is already there")
	}
	m.explicit = false
	m.settings.DefaultVolume = "gone"
	if m.unlockToBootLocked(true) != nil {
		t.Error("asked to unlock a volume that is not there")
	}
	m.settings.DefaultVolume = ""
	if m.unlockToBootLocked(true) != nil {
		t.Error("a default outside any volume needs no unlock")
	}
}

func TestTakeUnlockBoot(t *testing.T) {
	m := &Manager{pendingUnlock: &Locked{UUID: "uuid-1"}}
	if m.TakeUnlockBoot("other") {
		t.Error("another volume's unlock took the autoboot")
	}
	m.pendingUnlock = &Locked{UUID: "uuid-1"}
	if !m.TakeUnlockBoot("uuid-1") || m.pendingUnlock != nil {
		t.Error("the awaited unlock did not take it")
	}
	if m.TakeUnlockBoot("uuid-1") {
		t.Error("taken twice")
	}
}
