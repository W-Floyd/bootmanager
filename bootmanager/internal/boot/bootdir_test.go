package boot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/u-root/u-root/pkg/mount"
)

const singlePartitionConf = `timeout 1
default postmarketOS
label postmarketOS
	kernel /vmlinuz
	initrd /initramfs
	append root=/dev/x pmos_root_uuid=abc
`

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A single-partition install: boot/extlinux/extlinux.conf under the root.
func TestBootDirEntries(t *testing.T) {
	root, bare, empty := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "boot/extlinux/extlinux.conf"), singlePartitionConf)
	write(t, filepath.Join(root, "boot/vmlinuz"), "kernel")
	write(t, filepath.Join(root, "boot/initramfs"), "initramfs")
	write(t, filepath.Join(bare, "etc/os-release"), "no boot dir")
	if err := os.MkdirAll(filepath.Join(empty, "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	imgs := bootDirEntries([]*mount.MountPoint{
		{Path: bare, Device: "/dev/bare"},
		{Path: empty, Device: "/dev/empty"},
		{Path: root, Device: "/dev/dm-0"},
	})
	if len(imgs) != 1 {
		t.Fatalf("%d entries, want 1 (only the install with a boot/extlinux)", len(imgs))
	}
	if got := imgs[0].Label(); got != "postmarketOS" {
		t.Errorf("label %q", got)
	}
}
