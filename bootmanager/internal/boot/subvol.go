package boot

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/boot/bls"
	"github.com/u-root/u-root/pkg/boot/syslinux"
	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/ulog"

	"bootmanager.local/bootmanager/internal/btrfs"
)

// Subvolume entries: the scan mounts a btrfs file system's default
// subvolume only, and a distribution installed onto btrfs keeps its root and
// /boot in another (@, root). Each of the others is mounted by id, read-only,
// and searched as a file system of its own; one with no entries is unmounted
// again.

// maxSubvols bounds the subvolumes mounted per file system: a snapshotting
// system (openSUSE's snapper) has hundreds.
const maxSubvols = 16

// subvolImage is an entry found in a btrfs file system; where says which.
type subvolImage struct {
	*boot.LinuxImage
	where string
}

func (s *subvolImage) source() string { return s.where }

// snapshotPath: sv is under a snapshot directory, which duplicates the
// subvolume it was taken of.
func snapshotPath(p string) bool {
	return slices.ContainsFunc(strings.Split(p, "/"), func(c string) bool { return c == ".snapshots" || c == "snapshots" })
}

// scanSubvol finds the entries of a subvolume mounted at dir, its /boot
// included.
func scanSubvol(dir string) []boot.OSImage {
	var out []boot.OSImage
	for _, root := range []string{dir, filepath.Join(dir, "boot")} {
		if imgs, err := bls.ScanBLSEntries(ulog.Null, root, nil, ""); err == nil {
			out = append(out, imgs...)
		}
	}
	if imgs, err := syslinux.ParseLocalConfig(context.Background(), dir); err == nil {
		out = append(out, imgs...)
	}
	return append(out, bootDirEntries([]*mount.MountPoint{{Path: dir, Device: dir}})...)
}

// subvolEntries returns the entries in the subvolumes of the btrfs file
// systems among mounts.
func (m *Manager) subvolEntries(mounts []*mount.MountPoint, edit func(*boot.LinuxImage)) []boot.OSImage {
	var out []boot.OSImage
	for _, mp := range mounts {
		if mp.FSType == "btrfs" {
			dev := filepath.Base(mp.Device)
			out = append(out, m.otherSubvols(mp, func(sv string) string { return describeDev(dev, 0) + " (" + sv + ")" }, edit)...)
		}
	}
	return out
}

// tag marks the entries found in a btrfs file system, for their card.
func tag(imgs []boot.OSImage, where string, edit func(*boot.LinuxImage)) []boot.OSImage {
	for i, img := range imgs {
		if li, ok := img.(*boot.LinuxImage); ok {
			edit(li)
			imgs[i] = &subvolImage{LinuxImage: li, where: where}
		}
	}
	return imgs
}

// btrfsImageEntries is the entries in the btrfs image rel of mp (media dev):
// its default subvolume's, then the other subvolumes'. The mounts stay, in
// the pool and m.loops, while there are entries.
func (m *Manager) btrfsImageEntries(mp *mount.MountPoint, dev, rel string, edit func(*boot.LinuxImage)) []boot.OSImage {
	media := (&liveImage{dev: dev}).source() + " " + rel
	bm, l, drop, err := mountLoop(filepath.Join(mp.Path, rel), "btrfs")
	if err != nil {
		log.Printf("scan: %s on %s: %v", rel, dev, err)
		return nil
	}
	out := tag(scanSubvol(bm.Path), media, edit)
	// The other subvolumes mount the loop device by id; they and this
	// mount live and die together.
	out = append(out, m.otherSubvols(bm, func(sv string) string { return media + " (" + sv + ")" }, edit)...)
	if len(out) == 0 {
		drop()
		return nil
	}
	log.Printf("scan: %d entries in btrfs image %s on %s", len(out), rel, dev)
	m.pool.Add(bm)
	m.loops = append(m.loops, l)
	return out
}

// otherSubvols returns the entries in the subvolumes of the btrfs mounted
// as mp other than the one mounted, their mounts kept in the pool while they
// have any; where names a subvolume's origin for its card.
func (m *Manager) otherSubvols(mp *mount.MountPoint, where func(subvol string) string, edit func(*boot.LinuxImage)) []boot.OSImage {
	var out []boot.OSImage
	name := filepath.Base(mp.Device)
	subs, err := btrfs.Subvolumes(mp.Path)
	if err != nil {
		log.Printf("scan: subvolumes of %s: %v", name, err)
		return nil
	}
	cur, _ := btrfs.MountedID(mp.Path)
	tried := 0
	for _, sv := range subs {
		if sv.ID == cur || snapshotPath(sv.Path) {
			continue
		}
		if tried++; tried > maxSubvols {
			log.Printf("scan: %s has more than %d subvolumes, the rest not searched", name, maxSubvols)
			break
		}
		dir, err := os.MkdirTemp("", "bman-subvol-")
		if err != nil {
			return out
		}
		sm, err := mount.Mount(mp.Device, dir, "btrfs", "subvolid="+strconv.FormatUint(sv.ID, 10), mount.ReadOnly)
		if err != nil {
			log.Printf("scan: %s subvolume %s: %v", name, sv.Path, err)
			os.Remove(dir)
			continue
		}
		imgs := scanSubvol(dir)
		if len(imgs) == 0 {
			sm.Unmount(mount.MNT_DETACH)
			os.Remove(dir)
			continue
		}
		m.pool.Add(sm)
		log.Printf("scan: %d entries in %s subvolume %s", len(imgs), name, sv.Path)
		out = append(out, tag(imgs, where(sv.Path), edit)...)
	}
	return out
}
