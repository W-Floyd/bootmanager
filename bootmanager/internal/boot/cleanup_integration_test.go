package boot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
)

// loopsInUse are the loop devices bound to a file.
func loopsInUse() []string {
	var out []string
	files, _ := filepath.Glob("/sys/block/loop*/loop/backing_file")
	for _, f := range files {
		if b, _ := os.ReadFile(f); len(strings.TrimSpace(string(b))) > 0 {
			out = append(out, strings.Split(f, "/")[3])
		}
	}
	slices.Sort(out)
	return out
}

func ourMounts() []string {
	b, _ := os.ReadFile("/proc/mounts")
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "/bman-") {
			out = append(out, l)
		}
	}
	return out
}

func sortedLabels(imgs []boot.OSImage) []string {
	var out []string
	for _, i := range imgs {
		out = append(out, i.Label())
	}
	slices.Sort(out)
	return out
}

// A scan's loop devices and mounts are gone after Unmount, and a second scan
// (the user's Rescan, after a Release) finds the same entries and leaves
// nothing behind either.
func TestScanCleanupReal(t *testing.T) {
	media := os.Getenv("INTEGRATION_MEDIA")
	if media == "" {
		t.Skip("INTEGRATION_MEDIA not set (test/run.sh)")
	}
	removableDev = func(string) bool { return true }
	mp := &mount.MountPoint{Path: media, Device: "/dev/sdz1", FSType: "vfat"}
	before := loopsInUse()

	var first []string
	for cycle := 1; cycle <= 2; cycle++ {
		m := New(Config{LiveImages: true})
		imgs := m.liveEntries([]*mount.MountPoint{mp}, func(*boot.LinuxImage) {})
		if len(imgs) == 0 {
			t.Fatalf("cycle %d: no entries", cycle)
		}
		if during := loopsInUse(); len(during) <= len(before) {
			t.Errorf("cycle %d: %d loop devices in use while the entries are held, %d before", cycle, len(during), len(before))
		}
		if len(ourMounts()) == 0 {
			t.Errorf("cycle %d: no mounts while the entries are held", cycle)
		}
		if first == nil {
			first = sortedLabels(imgs)
		} else if got := sortedLabels(imgs); !slices.Equal(got, first) {
			t.Errorf("cycle %d: entries %v, first cycle's %v", cycle, got, first)
		}

		m.Unmount()
		// A lazy unmount ends when the last user lets go; the loop device
		// follows (autoclear). On a busy machine (the packages' tests run at
		// once) that takes seconds; a leak never ends, so the wait is long and
		// ends as soon as they are gone.
		var after, left []string
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if after, left = loopsInUse(), ourMounts(); slices.Equal(after, before) && len(left) == 0 {
				break
			}
		}
		if !slices.Equal(after, before) {
			t.Errorf("cycle %d: loop devices in use after Unmount: %v, before %v", cycle, after, before)
		}
		if len(left) > 0 {
			t.Errorf("cycle %d: mounts left: %v", cycle, left)
		}
	}
}
