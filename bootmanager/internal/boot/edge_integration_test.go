package boot

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
	"golang.org/x/sys/unix"
)

func edgeDir(t *testing.T) string {
	t.Helper()
	d := os.Getenv("INTEGRATION_EDGE")
	if d == "" {
		t.Skip("INTEGRATION_EDGE not set (test/run.sh)")
	}
	removableDev = func(dev string) bool { return strings.HasPrefix(dev, "sdz") }
	return d
}

func entryNames(imgs []boot.OSImage) []string {
	var out []string
	for _, i := range imgs {
		out = append(out, i.Label())
	}
	slices.Sort(out)
	return out
}

func readAll(t *testing.T, r io.ReaderAt) []byte {
	t.Helper()
	b, err := io.ReadAll(io.NewSectionReader(r, 0, 1<<24))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each case's directory is a drive of its own: what the scan finds is exactly
// the entries listed, no more (a duplicate shows) and no fewer.
func TestEdgeCasesReal(t *testing.T) {
	edge := edgeDir(t)
	v := make([]string, 16)
	for i := range v {
		v[i] = fmt.Sprintf("v%02d", i+1)
	}
	for _, c := range []struct {
		name  string
		want  []string
		check func(t *testing.T, imgs []boot.OSImage)
	}{
		{name: "gpt-badcrc"},
		{name: "truncated", want: []string{"t-ext4", "t-vfat"}},
		{name: "crypt-swap", want: []string{"c-ext4"}},
		{name: "gpt-4kn", want: []string{"k-ext4", "k-vfat"}},
		{name: "sq-nokernel"},
		{name: "sq-noinitrd"},
		{name: "spaces"},
		{name: "depth", want: []string{"Live ok3"}},
		{name: "sq-beside", want: []string{"Live sq"}, check: func(t *testing.T, imgs []boot.OSImage) {
			li, _ := asLinux(imgs[0])
			if k := readAll(t, li.Kernel); !bytes.HasPrefix(k, []byte("beside-kernel")) {
				t.Errorf("kernel %q, want the one beside the image", k)
			}
			if r := readAll(t, li.Initrd); !bytes.HasPrefix(r, []byte("beside-initrd")) {
				t.Errorf("initramfs starts %q, want the one beside the image", r[:min(len(r), 20)])
			}
		}},
		{name: "persist-pairing", want: []string{"Live alpha (persistent)", "Live beta (persistent)"}, check: func(t *testing.T, imgs []boot.OSImage) {
			got := map[string]string{}
			for _, i := range imgs {
				li, _ := asLinux(i)
				got[i.Label()] = li.Cmdline
			}
			// By name where there is one, else the first persistence image.
			for l, want := range map[string]string{
				"Live alpha (persistent)": "bman.live=/alpha.img bman.persist=/alpha-persistence.img",
				"Live beta (persistent)":  "bman.live=/beta.img bman.persist=/alpha-persistence.img",
			} {
				if got[l] != want {
					t.Errorf("%s: %q, want %q", l, got[l], want)
				}
			}
		}},
		{name: "btrfs-default", want: []string{"e-other", "e-root"}, check: func(t *testing.T, imgs []boot.OSImage) {
			src := map[string]string{}
			for _, i := range imgs {
				src[i.Label()] = sourceOf(i)
			}
			if src["e-root"] != "USB drive os.img" || src["e-other"] != "USB drive os.img (other)" {
				t.Errorf("sources %v", src)
			}
		}},
		{name: "btrfs-many", want: v},
		{name: "btrfs-bls", want: []string{"Fedora Test 6.1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New(Config{LiveImages: true})
			defer m.Unmount()
			mp := &mount.MountPoint{Path: filepath.Join(edge, c.name), Device: "/dev/sdz1", FSType: "vfat"}
			imgs := m.liveEntries([]*mount.MountPoint{mp}, func(*boot.LinuxImage) {})
			if got := entryNames(imgs); !slices.Equal(got, c.want) && (len(got) > 0 || len(c.want) > 0) {
				t.Fatalf("entries %v, want %v", got, c.want)
			}
			if c.check != nil && len(imgs) > 0 {
				c.check(t, imgs)
			}
		})
	}
}

// attachDisk binds the disk image to a loop device with its partitions, and
// makes their device nodes (a container has no udev to).
func attachDisk(t *testing.T, img string) string {
	t.Helper()
	out, err := exec.Command("losetup", "-P", "-f", "--show", img).Output()
	if err != nil {
		t.Fatalf("losetup: %v", err)
	}
	dev := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("losetup", "-d", dev).Run() })
	name := filepath.Base(dev)
	parts, _ := filepath.Glob("/sys/class/block/" + name + "p*")
	if len(parts) == 0 {
		t.Fatalf("%s has no partitions", dev)
	}
	for _, p := range parts {
		b, err := os.ReadFile(filepath.Join(p, "dev"))
		if err != nil {
			t.Fatal(err)
		}
		var major, minor uint32
		if _, err := fmt.Sscanf(string(b), "%d:%d", &major, &minor); err != nil {
			t.Fatal(err)
		}
		node := "/dev/" + filepath.Base(p)
		os.Remove(node)
		if err := unix.Mknod(node, unix.S_IFBLK|0o600, int(unix.Mkdev(major, minor))); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(node) })
	}
	return name
}

// The whole scan, from block devices: a stick (a loop device with a GPT) whose
// FAT partition has an extlinux entry and whose ext4 partition has live
// images. The console edit applies to what u-root found; the live entry comes
// from the image; Release and Rescan find the same again.
func TestScanReal(t *testing.T) {
	edge := edgeDir(t)
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	name := attachDisk(t, filepath.Join(edge, "scan-disk.img"))
	removableDev = func(dev string) bool { return strings.HasPrefix(dev, name) }

	m := New(Config{
		LiveImages: true,
		Emit:       func(string, any) {},
		Teardown:   func() {},
	})
	defer m.Unmount()
	scan := func(what string) {
		t.Helper()
		imgs, err := m.scan()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		m.setImages(imgs)
		var stick, live bool
		for _, e := range m.List().Entries {
			switch e.Label {
			case "stick-boot":
				stick = true
				if strings.Contains(e.Detail, "ttyS9") || !strings.Contains(e.Detail, "root=/dev/x") {
					t.Errorf("%s: stick-boot command line %q: the entry's console= goes, the rest stays", what, e.Detail)
				}
				if e.Source != name+"p1" {
					t.Errorf("%s: stick-boot found on %q, want %sp1", what, e.Source, name)
				}
			case "Live debian (persistent)":
				live = true
				if e.Source != "USB drive" {
					t.Errorf("%s: live source %q", what, e.Source)
				}
			}
		}
		if !stick || !live {
			t.Errorf("%s: stick-boot %v, live image %v; entries %v", what, stick, live, entryNames(m.images))
		}
	}
	scan("first scan")
	if err := m.Release(); err != nil {
		t.Fatal(err)
	}
	m.Rescan()
	scan("after Release and Rescan")
}
