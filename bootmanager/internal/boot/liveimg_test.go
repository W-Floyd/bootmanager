package boot

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func extHeader(label string) []byte {
	b := make([]byte, 2048)
	b[1024+56], b[1024+57] = 0x53, 0xef
	copy(b[1024+120:], label)
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
		{"short.img", []byte("hsqs"), otherImage},
		{"zero.img", make([]byte, 2048), otherImage},
	} {
		if got := classifyImage(bytes.NewReader(c.data), c.name); got != c.want {
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
	for _, w := range []string{"bman.live=", "bman.persist=", "switch_root /newroot"} {
		if !strings.Contains(liveInit, w) {
			t.Errorf("liveInit lacks %q", w)
		}
	}
	if !strings.HasPrefix(liveInit, "#!/bin/sh\n") {
		t.Error("liveInit has no interpreter line")
	}
}

func TestLiveSource(t *testing.T) {
	if got := liveSource(&liveImage{dev: "sda1"}); got != "USB drive" {
		t.Error(got)
	}
	if got := liveSource(&liveImage{dev: "mmcblk0p1"}); got != "microSD card" {
		t.Error(got)
	}
}
