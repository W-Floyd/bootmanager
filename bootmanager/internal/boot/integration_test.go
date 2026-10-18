package boot

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
)

// INTEGRATION_MEDIA: a directory of image files made by test/fixtures.sh. Run
// with test/run.sh, which makes it in a container.
func TestMediaScanReal(t *testing.T) {
	media := os.Getenv("INTEGRATION_MEDIA")
	if media == "" {
		t.Skip("INTEGRATION_MEDIA not set (test/run.sh)")
	}
	removableDev = func(string) bool { return true }
	m := New(Config{LiveImages: true})
	defer m.Unmount()
	imgs := m.liveEntries([]*mount.MountPoint{{Path: media, Device: "/dev/sdz1", FSType: "vfat"}}, func(*boot.LinuxImage) {})

	const usb = "USB drive "
	want := map[string]string{
		"Live debian (persistent)": "USB drive",
		"live-pristine":            usb + "nura/os.img",
		"live-other":               usb + "nura/os.img (other)",
		"g-vfat":                   usb + "gpt.img bootfat",
		"g-ext4":                   usb + "gpt.img rootext",
		"g-btrfs":                  usb + "gpt.img data",
		"g-sub":                    usb + "gpt.img data (sub)",
		"m-vfat":                   usb + "mbr.img p1",
		"m-ext4":                   usb + "mbr.img p2",
		"m-btrfs":                  usb + "mbr.img p5",
		"m-sub":                    usb + "mbr.img p5 (sub)",
	}
	got := map[string]string{}
	for _, i := range imgs {
		got[i.Label()] = sourceOf(i)
	}
	t.Logf("found: %v", got)
	for l, src := range want {
		if g, ok := got[l]; !ok || g != src {
			t.Errorf("%s: source %q (found %v), want %q", l, g, ok, src)
		}
	}
	for l := range got {
		if _, ok := want[l]; !ok {
			t.Errorf("unexpected entry %q (junk.img and data.img must give none)", l)
		}
	}

	// The live entry boots the newest kernel with the shim as its /init.
	for _, i := range imgs {
		li, ok := asLinux(i)
		if !ok || i.Label() != "Live debian (persistent)" {
			continue
		}
		if want := "bman.live=/live/debian.img bman.persist=/live/debian-persistence.img"; li.Cmdline != want {
			t.Errorf("cmdline %q, want %q", li.Cmdline, want)
		}
		k, _ := io.ReadAll(io.NewSectionReader(li.Kernel, 0, 1<<20))
		if !bytes.HasPrefix(k, []byte("kernel")) {
			t.Errorf("kernel %q", k)
		}
		r, _ := io.ReadAll(io.NewSectionReader(li.Initrd, 0, 1<<20))
		if !bytes.HasPrefix(r, []byte("initrd")) || !bytes.Contains(r, []byte("bman live:")) {
			t.Errorf("initramfs is not the system's with the shim after it (%d bytes)", len(r))
		}
	}
}
