package boot

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// switchRootStub stands in for switch_root: it says whether the new root
// already had what an earlier run wrote (persistence), writes it, and shows
// the squashfs's file and its own arguments.
const switchRootStub = `#!/bin/sh
[ -e /newroot/etc/bman-test ] && echo "STATE seen" || echo "STATE new"
touch /newroot/etc/bman-test
cat /newroot/etc/os-release
echo "ARGS $*"
`

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// The shim, run as an initramfs's /init would be (a chroot of busybox, in a
// mount namespace of its own) against a FAT stick on a loop device: it finds
// the stick, assembles the overlay, writes land in the persistence image and
// are there on the next run, and without one the root is RAM.
func TestInitShimReal(t *testing.T) {
	media := os.Getenv("INTEGRATION_MEDIA")
	if media == "" {
		t.Skip("INTEGRATION_MEDIA not set (test/run.sh)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	for _, tool := range []string{"losetup", "unshare", "chroot", "busybox"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	if fs, _ := os.ReadFile("/proc/filesystems"); !strings.Contains(string(fs), "overlay") {
		t.Skip("kernel has no overlayfs")
	}

	dir := t.TempDir()
	stick := filepath.Join(dir, "stick.img")
	copyFile(t, filepath.Join(media, "stick.img"), stick)
	out, err := exec.Command("losetup", "-f", "--show", stick).Output()
	if err != nil {
		t.Fatalf("losetup: %v", err)
	}
	loopDev := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("losetup", "-d", loopDev).Run() })

	root := filepath.Join(dir, "root")
	for _, d := range []string{"bin", "proc", "sys", "dev"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bb, _ := exec.LookPath("busybox")
	copyFile(t, bb, filepath.Join(root, "bin/busybox"))
	for _, a := range []string{"sh", "mount", "umount", "mkdir", "modprobe", "sleep", "cat", "touch", "echo", "["} {
		if err := os.Symlink("busybox", filepath.Join(root, "bin", a)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin/switch_root_stub"), []byte(switchRootStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "init"), []byte(liveInit), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(cmdline string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "cmdline"), []byte(cmdline+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "unshare", "-m", "chroot", root, "/init")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BMAN_CMDLINE=/cmdline", "BMAN_WAIT=2", "BMAN_SWITCH_ROOT=/bin/switch_root_stub"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("init: %v", err)
		}
		return string(out)
	}
	pin := "bman.dev=" + filepath.Base(loopDev)
	live := " bman.live=/live/debian.img "
	persist := "bman.persist=/live/debian-persistence.img "

	// A fresh persistence image: the squashfs shows through, nothing seen.
	got := run(pin + live + persist)
	for _, w := range []string{"STATE new", "ID=test", "ARGS /newroot /sbin/init"} {
		if !strings.Contains(got, w) {
			t.Errorf("first run lacks %q:\n%s", w, got)
		}
	}
	// What it wrote is on the persistence image the next time, and init=
	// is passed on.
	got = run(pin + live + persist + "init=/sbin/custom")
	for _, w := range []string{"STATE seen", "ARGS /newroot /sbin/custom"} {
		if !strings.Contains(got, w) {
			t.Errorf("second run lacks %q:\n%s", w, got)
		}
	}
	// Without a persistence image the root is RAM: nothing is kept.
	for range 2 {
		if got = run(pin + live); !strings.Contains(got, "STATE new") {
			t.Errorf("a run without persistence saw an earlier write:\n%s", got)
		}
	}
	// A path that is not on the media ends at the shell, saying why.
	if got = run(pin + " bman.live=/live/none.img"); !strings.Contains(got, "bman live: no media with /live/none.img") {
		t.Errorf("a missing image:\n%s", got)
	}
	if got = run(pin); !strings.Contains(got, "bman live: no bman.live=") {
		t.Errorf("no bman.live=:\n%s", got)
	}
}
