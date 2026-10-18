package boot

import (
	"os"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
)

// BTRFS_TEST_MEDIA: a directory with nura/os.img, a zstd btrfs whose default
// subvolume @pristine and subvolume other each have boot/extlinux (set up by
// the script that runs this, as root).
func TestBtrfsImageEntriesReal(t *testing.T) {
	media := os.Getenv("BTRFS_TEST_MEDIA")
	if media == "" {
		t.Skip("BTRFS_TEST_MEDIA not set")
	}
	m := New(Config{})
	imgs := m.btrfsImageEntries(&mount.MountPoint{Path: media, Device: "/dev/sdz1", FSType: "vfat"}, "sdz1", "nura/os.img", func(*boot.LinuxImage) {})
	defer m.Unmount()
	got := map[string]string{}
	for _, i := range imgs {
		got[i.Label()] = sourceOf(i)
		t.Logf("%s: %s", i.Label(), sourceOf(i))
	}
	for label, src := range map[string]string{
		"live-pristine": "USB drive nura/os.img",
		"live-other":    "USB drive nura/os.img (other)",
	} {
		if got[label] != src {
			t.Errorf("%s: source %q, want %q (all: %v)", label, got[label], src, got)
		}
	}
}
