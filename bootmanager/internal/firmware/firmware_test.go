package firmware

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSlotSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"console=tty0 androidboot.slot_suffix=_a panic=5": "_a",
		"androidboot.slot_suffix=_b":                      "_b",
		"console=tty0":                                    "",
		"":                                                "",
	} {
		if got := SlotSuffix(in); got != want {
			t.Errorf("SlotSuffix(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slotNames("vendor", "_a"); !slices.Equal(got, []string{"vendor_a", "vendor"}) {
		t.Errorf("slotNames = %v", got)
	}
	if got := slotNames("vendor", ""); !slices.Equal(got, []string{"vendor"}) {
		t.Errorf("slotNames without a slot = %v", got)
	}
}

func TestManifestValidate(t *testing.T) {
	ok := File{Name: "touch", Dest: "/lib/firmware/x.bin", Sources: []Source{{Super: "vendor", Path: "firmware/x.bin"}}}
	with := func(f func(*File)) Manifest {
		c := ok
		c.Sources = slices.Clone(ok.Sources)
		f(&c)
		return Manifest{Files: []File{c}}
	}
	if err := with(func(*File) {}).validate(); err != nil {
		t.Fatalf("a good manifest: %v", err)
	}
	for name, m := range map[string]Manifest{
		"no name":       with(func(f *File) { f.Name = "" }),
		"relative dest": with(func(f *File) { f.Dest = "lib/x.bin" }),
		"unclean dest":  with(func(f *File) { f.Dest = "/lib/../x.bin" }),
		"no sources":    with(func(f *File) { f.Sources = nil }),
		"both kinds":    with(func(f *File) { f.Sources[0].Partition = "modem" }),
		"neither kind":  with(func(f *File) { f.Sources[0].Super = "" }),
		"absolute path": with(func(f *File) { f.Sources[0].Path = "/firmware/x.bin" }),
		"path with ..":  with(func(f *File) { f.Sources[0].Path = "../x.bin" }),
		"no path":       with(func(f *File) { f.Sources[0].Path = "" }),
		"half a rebind": with(func(f *File) { f.Rebind = &Rebind{Bus: "spi"} }),
	} {
		if err := m.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// sysBlock makes a fake /sys/class/block with these partitions: name -> devname.
func sysBlock(t *testing.T, parts map[string]string) Env {
	t.Helper()
	root := t.TempDir()
	for name, dev := range parts {
		d := filepath.Join(root, "sys", dev)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		u := fmt.Sprintf("MAJOR=259\nMINOR=1\nDEVNAME=%s\nPARTNAME=%s\n", dev, name)
		if err := os.WriteFile(filepath.Join(d, "uevent"), []byte(u), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Env{SysBlock: filepath.Join(root, "sys"), SysBus: filepath.Join(root, "bus"), Dev: "/dev", Log: t.Logf}
}

func TestFindPartition(t *testing.T) {
	env := sysBlock(t, map[string]string{"super": "sde25", "modem_a": "sdd13", "persist": "sde2"})
	for name, want := range map[string]string{"super": "/dev/sde25", "modem_a": "/dev/sdd13"} {
		if got, err := findPartition(env, name); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
	if got, err := findPartition(env, "modem"); err == nil {
		t.Errorf("a name that is only a prefix matched: %q", got)
	}
}

func TestInstallLeavesWhatIsThere(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lib", "firmware", "x.bin")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("embedded"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No sources could be read here; that must not matter for a file in place.
	m := Manifest{Files: []File{{Name: "x", Dest: dest, Sources: []Source{{Super: "vendor", Path: "x"}}}}}
	var log []string
	env := sysBlock(t, nil)
	env.Log = func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }
	if err := Install(m, env); err != nil {
		t.Errorf("a file already in place: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "embedded" {
		t.Errorf("a file in place was replaced: %q", b)
	}
	if len(log) != 1 || !strings.Contains(log[0], "already at") {
		t.Errorf("log = %v", log)
	}
}

func TestInstallReportsWhatIsMissing(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "x.bin")
	env := sysBlock(t, nil) // no super, no modem
	m := Manifest{Files: []File{
		{Name: "a", Dest: dest, Sources: []Source{{Super: "vendor", Path: "x"}, {Partition: "modem", Path: "x"}}},
	}}
	err := Install(m, env)
	if err == nil || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
	if _, serr := os.Stat(dest); serr == nil {
		t.Error("a file was written from nothing")
	}
}

func TestRebind(t *testing.T) {
	env := sysBlock(t, nil)
	dev := filepath.Join(env.SysBus, "spi", "devices", "spi0.0")
	drv := filepath.Join(env.SysBus, "spi", "drivers", "chipone_icnl9916_spi")
	other := filepath.Join(env.SysBus, "spi", "devices", "spi1.0")
	for _, d := range []string{dev, drv, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// sysfs has bind and unbind already; so must the fake, or the write makes
	// them write-only and the test cannot read what was written.
	for _, f := range []string{"bind", "unbind"} {
		os.WriteFile(filepath.Join(drv, f), nil, 0o644)
	}
	os.WriteFile(filepath.Join(dev, "modalias"), []byte("spi:icnl9916\n"), 0o644)
	os.WriteFile(filepath.Join(other, "modalias"), []byte("spi:spidev\n"), 0o644)
	if err := rebind(Rebind{Bus: "spi", Driver: "chipone_icnl9916_spi", Modalias: "icnl9916"}, env); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(drv, "bind")); err != nil || string(b) != "spi0.0" {
		t.Errorf("bind = %q, %v; want spi0.0 (and not the other device)", b, err)
	}
	if err := rebind(Rebind{Bus: "nope", Driver: "x", Modalias: "y"}, env); err == nil {
		t.Error("a bus that is not there rebound without an error")
	}
}
