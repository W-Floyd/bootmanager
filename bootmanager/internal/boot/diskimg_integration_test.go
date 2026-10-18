package boot

import (
	"os"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
)

// DISK_TEST_MEDIA: a directory with gpt.img and mbr.img, whole-disk images
// whose partitions each hold an extlinux entry named after them (set up as
// root by the script that runs this).
func TestDiskImageEntriesReal(t *testing.T) {
	media := os.Getenv("DISK_TEST_MEDIA")
	if media == "" {
		t.Skip("DISK_TEST_MEDIA not set")
	}
	for img, want := range map[string]map[string]string{
		"gpt.img": {
			"e-vfat":  "USB drive gpt.img bootfat",
			"e-ext4":  "USB drive gpt.img rootext",
			"e-btrfs": "USB drive gpt.img data",
			"e-sub":   "USB drive gpt.img data (sub)",
		},
		"mbr.img": {
			"e-vfat":  "USB drive mbr.img p1",
			"e-ext4":  "USB drive mbr.img p2",
			"e-btrfs": "USB drive mbr.img p5",
			"e-sub":   "USB drive mbr.img p5 (sub)",
		},
	} {
		m := New(Config{})
		imgs := m.diskImageEntries(&mount.MountPoint{Path: media, Device: "/dev/sdz1", FSType: "vfat"}, "sdz1", img, func(*boot.LinuxImage) {})
		got := map[string]string{}
		for _, i := range imgs {
			got[i.Label()] = sourceOf(i)
		}
		t.Logf("%s: %v", img, got)
		if len(got) != len(want) {
			t.Errorf("%s: %v, want %v", img, got, want)
		}
		for l, src := range want {
			if got[l] != src {
				t.Errorf("%s: %s from %q, want %q", img, l, got[l], src)
			}
		}
		m.Unmount()
	}
}
