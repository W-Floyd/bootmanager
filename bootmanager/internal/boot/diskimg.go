package boot

import (
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/disk"
)

// Whole-disk images: a .img with a partition table (GPT or MBR) is searched
// partition by partition, each mounted read-only through a loop device
// bound to its byte range of the file, as the partitions of a disk are. The
// entries found are the image's own: their command lines name the image's
// root as a disk would, which only an initramfs that looks inside the image
// (as the live entries' does) can find again.

// skipPart: partitions that hold no file system to search.
var skipPart = map[string]bool{
	"0x82": true, "0x05": true, "0x0f": true, "0x85": true, "0xee": true,
	"0657fd6d-a4ab-43c4-84e5-0933c84b4f4f": true, // Linux swap
}

// diskImageEntries is the entries in the partitions of the disk image rel
// of mp (media dev). Mounts and loop devices of partitions with entries stay,
// in the pool and m.loops.
func (m *Manager) diskImageEntries(mp *mount.MountPoint, dev, rel string, edit func(*boot.LinuxImage)) []boot.OSImage {
	src := filepath.Join(mp.Path, rel)
	f, err := os.Open(src)
	if err != nil {
		return nil
	}
	st, err := f.Stat()
	var parts []disk.Part
	if err == nil {
		parts, err = disk.ParseTable(f, st.Size())
	}
	f.Close()
	if err != nil {
		log.Printf("scan: %s on %s: %v", rel, dev, err)
		return nil
	}
	media := (&liveImage{dev: dev}).source() + " " + rel
	var out []boot.OSImage
	for _, p := range parts {
		if skipPart[p.Type] {
			continue
		}
		l, err := attachLoop(src, p.Start, p.Size)
		if err != nil {
			log.Printf("scan: %s partition %d: %v", rel, p.Index, err)
			continue
		}
		dir, err := os.MkdirTemp("", "bman-part-")
		if err != nil {
			l.Free()
			return out
		}
		pm, err := mount.TryMount(l.Dev, dir, "", mount.ReadOnly)
		if err != nil {
			// A swap area, an encrypted volume, a raw partition.
			os.Remove(dir)
			l.Free()
			continue
		}
		where := media + " p" + strconv.Itoa(p.Index)
		if p.Name != "" {
			where = media + " " + p.Name
		}
		imgs := tag(scanSubvol(pm.Path), where, edit)
		if pm.FSType == "btrfs" {
			imgs = append(imgs, m.otherSubvols(pm, func(sv string) string { return where + " (" + sv + ")" }, edit)...)
		}
		if len(imgs) == 0 {
			pm.Unmount(mount.MNT_DETACH)
			os.Remove(dir)
			l.Free()
			continue
		}
		log.Printf("scan: %d entries in %s partition %d (%s)", len(imgs), rel, p.Index, pm.FSType)
		m.pool.Add(pm)
		m.loops = append(m.loops, l)
		out = append(out, imgs...)
	}
	return out
}
