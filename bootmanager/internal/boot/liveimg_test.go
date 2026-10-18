package boot

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/u-root/u-root/pkg/mount"
)

func extHeader(label string) []byte {
	b := make([]byte, 2048)
	b[1024+56], b[1024+57] = 0x53, 0xef
	copy(b[1024+120:], label)
	return b
}

func mbrDisk() []byte {
	b := make([]byte, 1<<20)
	b[510], b[511] = 0x55, 0xaa
	b[446+4] = 0x83
	b[446+8] = 8   // start sector
	b[446+12] = 16 // sectors
	return b
}

func btrfsHeader() []byte {
	b := make([]byte, 0x10048)
	copy(b[0x10040:], "_BHRfS_M")
	return b
}

func TestClassifyImage(t *testing.T) {
	squash := append([]byte("hsqs"), make([]byte, 2044)...)
	for _, c := range []struct {
		name string
		data []byte
		want imageKind
	}{
		{"live.img", squash, squashImage},
		{"p.img", extHeader("persistence"), persistImage},
		{"casper-rw", extHeader("casper-rw"), persistImage},
		{"my-persist.img", extHeader(""), persistImage},
		{"data.img", extHeader("data"), otherImage},
		{"os.img", btrfsHeader(), btrfsImage},
		{"disk.img", mbrDisk(), diskImage},
		{"short.img", []byte("hsqs"), otherImage},
		{"zero.img", make([]byte, 2048), otherImage},
	} {
		// The file system is u-root's, which needs the host kernel to have it.
		if fs := map[imageKind]string{squashImage: "squashfs", btrfsImage: "btrfs", persistImage: "ext4"}[c.want]; fs != "" && mount.FindFileSystem(fs) != nil {
			t.Logf("%s: kernel has no %s, skipped", c.name, fs)
			continue
		}
		path := filepath.Join(t.TempDir(), c.name)
		// u-root's detection reads 128 KiB.
		data := append(slices.Clone(c.data), make([]byte, 1<<17)...)
		if c.name == "short.img" {
			data = c.data
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := classifyImage(path, c.name); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFindImages(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.img", "B.IMG", "live/c.img", "a/b/c/d/deep.img", ".hidden/x.img", "System Volume Information/y.img", "notes.txt", "live/x.iso"} {
		write(t, filepath.Join(root, f), "x")
	}
	got := findImages(root)
	want := []string{"B.IMG", "a.img", "live/c.img"}
	if !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestPersistenceFor(t *testing.T) {
	cands := []string{"live/other-persistence.img", "live/debian-persistence.img", "x/p.img"}
	for _, c := range []struct{ sq, want string }{
		{"live/debian.img", "live/debian-persistence.img"},
		{"live/mint.img", "live/other-persistence.img"},
		{"mint.img", ""},
		{"x/q.img", "x/p.img"},
	} {
		if got := persistenceFor(c.sq, cands); got != c.want {
			t.Errorf("%s: %q, want %q", c.sq, got, c.want)
		}
	}
}

func TestKernelIn(t *testing.T) {
	for _, c := range []struct {
		name         string
		files        []string
		kernel, init string
	}{
		{"debian, newest by version", []string{"vmlinuz-6.9.1", "vmlinuz-6.10.2", "vmlinuz-6.10.2.old", "initrd.img-6.9.1", "initrd.img-6.10.2"}, "vmlinuz-6.10.2", "initrd.img-6.10.2"},
		{"arch", []string{"vmlinuz-linux", "initramfs-linux.img", "initramfs-linux-fallback.img"}, "vmlinuz-linux", "initramfs-linux.img"},
		{"unversioned", []string{"vmlinuz", "initrd.img"}, "vmlinuz", "initrd.img"},
		{"no initramfs", []string{"vmlinuz"}, "", ""},
		{"no kernel", []string{"initrd.img"}, "", ""},
	} {
		dir := t.TempDir()
		for _, f := range c.files {
			write(t, filepath.Join(dir, f), "x")
		}
		k, i, ok := kernelIn(dir)
		if ok != (c.kernel != "") || filepath.Base(k) != c.kernel && ok || filepath.Base(i) != c.init && ok {
			t.Errorf("%s: %q %q %v, want %q %q", c.name, k, i, ok, c.kernel, c.init)
		}
	}
	if _, _, ok := kernelIn(filepath.Join(t.TempDir(), "none")); ok {
		t.Error("a missing directory has a kernel")
	}
}

func TestLiveCmdline(t *testing.T) {
	if got, want := liveCmdline("live/a.img", "live/p.img"), "bman.live=/live/a.img bman.persist=/live/p.img"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if got := liveCmdline("a.img", ""); strings.Contains(got, "persist") {
		t.Errorf("%q has persistence", got)
	}
}

// The shim must read what liveCmdline writes.
func TestLiveInitReadsCmdline(t *testing.T) {
	for _, w := range []string{"bman.live=", "bman.persist=", "bman.dev=", "switch_root} /newroot"} {
		if !strings.Contains(liveInit, w) {
			t.Errorf("liveInit lacks %q", w)
		}
	}
	if !strings.HasPrefix(liveInit, "#!/bin/sh\n") {
		t.Error("liveInit has no interpreter line")
	}
}

func TestLiveSource(t *testing.T) {
	if got := (&liveImage{dev: "sda1"}).source(); got != "USB drive" {
		t.Error(got)
	}
	if got := (&liveImage{dev: "mmcblk0p1"}).source(); got != "microSD card" {
		t.Error(got)
	}
}

func TestSnapshotPath(t *testing.T) {
	for p, want := range map[string]bool{"@": false, "@/.snapshots/1/snapshot": true, "root": false, "a/snapshots": true, "snapshotter": false} {
		if got := snapshotPath(p); got != want {
			t.Errorf("%s: %v", p, got)
		}
	}
}

// The shim is shell: a syntax error would only show on a booting device.
func TestLiveInitSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	f := filepath.Join(t.TempDir(), "init")
	if err := os.WriteFile(f, []byte(liveInit), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v\n%s", err, out)
	}
}
