package boot

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/dt"
	"github.com/u-root/u-root/pkg/mount"
)

// bootImage is a v4 boot image with kernel and the header's own cmdline.
func bootImage(kernel []byte, osVersion uint32, cmdline string) []byte {
	b := make([]byte, androidPageSize+len(kernel))
	copy(b, "ANDROID!")
	le := binary.LittleEndian
	le.PutUint32(b[8:], uint32(len(kernel)))
	le.PutUint32(b[16:], osVersion)
	le.PutUint32(b[40:], 4)
	copy(b[44:], cmdline)
	copy(b[androidPageSize:], kernel)
	return b
}

func TestAndroidEntries(t *testing.T) {
	part, bl := t.TempDir(), t.TempDir()
	// Android 14, security patch 2025-02.
	osVersion := uint32(14<<14)<<11 | (25<<4 | 2)
	kernel := []byte("an arm64 Image")
	if err := os.MkdirAll(filepath.Join(part, "android"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(part, AndroidImage), bootImage(kernel, osVersion, "extra=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fdt bytes.Buffer
	tree := &dt.FDT{RootNode: dt.NewNode("/", dt.WithChildren(
		dt.NewNode("chosen", dt.WithProperty(dt.PropertyString("bootargs", "console=null bootconfig"))),
	))}
	tree.Header.Magic = dt.Magic
	tree.Header.Version = 17
	if _, err := tree.Write(&fdt); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bl, "fdt"), fdt.Bytes(), 0o644)
	os.WriteFile(filepath.Join(bl, "initrd"), []byte("ramdisks#BOOTCONFIG\n"), 0o644)

	mounts := []*mount.MountPoint{{Path: t.TempDir()}, {Path: part}}
	imgs := androidEntries(mounts, nil, nil, bl)
	if len(imgs) != 1 {
		t.Fatalf("androidEntries = %d entries, want 1", len(imgs))
	}
	li := imgs[0].(*boot.LinuxImage)
	if li.Name != "Android 14 (2025-02)" {
		t.Errorf("Name = %q", li.Name)
	}
	if li.Cmdline != "console=null bootconfig extra=1" {
		t.Errorf("Cmdline = %q", li.Cmdline)
	}
	got, _ := io.ReadAll(io.NewSectionReader(li.Kernel, 0, 1<<20))
	if !bytes.Equal(got, kernel) {
		t.Errorf("Kernel = %q, want %q", got, kernel)
	}

	// No kept bootloader tree: no Android entries.
	if imgs := androidEntries(mounts, nil, nil, t.TempDir()); len(imgs) != 0 {
		t.Errorf("without a bootloader tree: %d entries, want 0", len(imgs))
	}
}

func TestAndroidHeaderRejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"not android": make([]byte, 8192),
		"v2":          func() []byte { b := bootImage([]byte("k"), 0, ""); b[40] = 2; return b }(),
		"no kernel":   bootImage(nil, 0, ""),
	} {
		if _, err := readAndroidHeader(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: readAndroidHeader succeeded", name)
		}
	}
}

// withBootconfig is ramdisk followed by a bootconfig trailer holding text.
func withBootconfig(ramdisk []byte, text string) []byte {
	data := []byte(text)
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	var sum uint32
	for _, c := range data {
		sum += uint32(c)
	}
	out := append(append([]byte{}, ramdisk...), data...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	out = binary.LittleEndian.AppendUint32(out, sum)
	return append(out, "#BOOTCONFIG\n"...)
}

func TestOtherSlotBoot(t *testing.T) {
	dir := t.TempDir()
	for text, want := range map[string]string{
		"androidboot.hardware=qcom\nandroidboot.slot_suffix=_a\n": "boot_b",
		"androidboot.slot_suffix = \"_b\"\n":                      "boot_a",
		"androidboot.hardware=qcom\n":                             "",
	} {
		p := filepath.Join(dir, "initrd")
		os.WriteFile(p, withBootconfig([]byte("ramdisk"), text), 0o644)
		if got := otherSlotBoot(p); got != want {
			t.Errorf("otherSlotBoot(%q) = %q, want %q", text, got, want)
		}
	}
	p := filepath.Join(dir, "plain")
	os.WriteFile(p, []byte("a ramdisk with no bootconfig"), 0o644)
	if got := otherSlotBoot(p); got != "" {
		t.Errorf("otherSlotBoot(no trailer) = %q, want \"\"", got)
	}
}

// ABL_INITRD: an initramfs a bootloader handed a stage, to read its slot.
func TestOtherSlotBootABL(t *testing.T) {
	p := os.Getenv("ABL_INITRD")
	if p == "" {
		t.Skip("ABL_INITRD not set")
	}
	t.Logf("%s: bootconfig has %d keys, other slot's boot %q", p, len(bootconfig(p)), otherSlotBoot(p))
}
