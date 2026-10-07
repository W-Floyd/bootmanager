package boot

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/boot/syslinux"
	"github.com/u-root/u-root/pkg/mount"
)

// bootDirEntries finds the entries of installs whose /boot is a directory of
// the root file system, not a partition of its own: a single-partition image
// (the only kind that fits one partition, as an encrypted install on userdata
// must) keeps its extlinux.conf in /boot/extlinux/, and its `kernel /vmlinuz`
// is relative to /boot. u-root's search of a file system looks under its root
// (extlinux/, boot/syslinux/, ...), not boot/extlinux/, and would resolve
// /vmlinuz against the root; so each mounted file system is searched again
// with its boot/ as the root. A file system with no boot/, or an empty one (a
// pmOS root whose /boot is a partition, mounted elsewhere), has none.
func bootDirEntries(mounts []*mount.MountPoint) []boot.OSImage {
	var out []boot.OSImage
	for _, mp := range mounts {
		dir := filepath.Join(mp.Path, "boot")
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		imgs, err := syslinux.ParseLocalConfig(context.Background(), dir)
		if err != nil {
			continue
		}
		log.Printf("scan: %d entries in %s's /boot", len(imgs), mp.Device)
		out = append(out, imgs...)
	}
	return out
}
