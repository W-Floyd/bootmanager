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
	imgs := androidEntries(mounts, bl)
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
	if imgs := androidEntries(mounts, t.TempDir()); len(imgs) != 0 {
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
