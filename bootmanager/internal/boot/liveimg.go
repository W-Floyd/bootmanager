package boot

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/loop"
	"golang.org/x/sys/unix"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/disk"
	"github.com/W-Floyd/bootmanager/bootmanager/internal/initrd"
	"github.com/W-Floyd/bootmanager/bootmanager/internal/storage"
)

// A btrfs .img (zstd compressed or not) is instead searched like a partition
// (subvol.go): its default subvolume and the others are mounted, and the
// entries in them are its own, with their own initramfs to find the image
// again.
//
// Live entries: a squashfs root file system in a .img file on removable media
// (a USB drive), booted with its own kernel and initramfs, the root an
// overlay of the squashfs over an ext4 persistence .img beside it, or over
// RAM when there is none.
//
// The kernel and initramfs are the squashfs's (/boot) or sit beside it. The
// initramfs is the one the system ships, with its /init replaced by liveInit
// (liveinit.go): finding the media and assembling the overlay are the same
// for every distribution, which an initramfs's own live hooks are not.

const (
	liveMaxDepth = 3     // directories below a file system's root searched for images
	liveMaxFiles = 20000 // files looked at per file system
)

// liveImage is an entry found in an image; dev is the media's kernel name.
type liveImage struct {
	*boot.LinuxImage
	dev string
}

type imageKind int

const (
	otherImage imageKind = iota
	squashImage
	persistImage
	btrfsImage // a whole btrfs file system, which carries its own entries
	diskImage  // a whole disk: a partition table and partitions
)

// classifyImage says what the file at r, called name, of size bytes, is: a
// squashfs, a btrfs, a disk with a partition table, an ext
// file system that is a persistence image (labelled or named for it), or
// neither.
func classifyImage(r io.ReaderAt, name string, size int64) imageKind {
	var b [1024 + 136]byte
	if n, _ := r.ReadAt(b[:], 0); n < len(b) {
		return otherImage
	}
	if string(b[:4]) == "hsqs" {
		return squashImage
	}
	var bm [8]byte
	if n, _ := r.ReadAt(bm[:], 0x10040); n == len(bm) && string(bm[:]) == "_BHRfS_M" {
		return btrfsImage
	}
	// ext2/3/4: magic 0xEF53 at 56 in the superblock, the label at 120.
	if b[1024+56] != 0x53 || b[1024+57] != 0xef {
		if ps, err := disk.ParseTable(r, size); err == nil && len(ps) > 0 {
			return diskImage
		}
		return otherImage
	}
	label := strings.ToLower(strings.TrimRight(string(b[1024+120:1024+136]), "\x00"))
	for _, w := range []string{"persist", "casper-rw", "writable"} {
		if strings.HasPrefix(label, w) {
			return persistImage
		}
	}
	if strings.Contains(strings.ToLower(name), "persist") {
		return persistImage
	}
	return otherImage
}

// findImages lists the .img files under root, as paths relative to it, in
// name order. Hidden directories and a FAT volume's housekeeping are skipped.
func findImages(root string) []string {
	var out []string
	seen := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "lost+found" || name == "System Volume Information" || strings.HasPrefix(name, "$")) {
				return filepath.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) >= liveMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > liveMaxFiles {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(rel), ".img") {
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// persistenceFor picks the persistence image for the squashfs sq among
// candidates (relative paths): one in its directory whose name starts with
// the squashfs's (debian.img, debian-persistence.img), else the first in its
// directory. "" for none.
func persistenceFor(sq string, candidates []string) string {
	dir := filepath.Dir(sq)
	stem := strings.TrimSuffix(filepath.Base(sq), filepath.Ext(sq))
	var first string
	for _, c := range candidates {
		if filepath.Dir(c) != dir {
			continue
		}
		if first == "" {
			first = c
		}
		if strings.HasPrefix(filepath.Base(c), stem) {
			return c
		}
	}
	return first
}

// naturalCmp orders names with their numbers by value: vmlinuz-6.9 before
// vmlinuz-6.10.
func naturalCmp(a, b string) int {
	for a != "" && b != "" {
		if unicode.IsDigit(rune(a[0])) && unicode.IsDigit(rune(b[0])) {
			i, j := 0, 0
			for i < len(a) && unicode.IsDigit(rune(a[i])) {
				i++
			}
			for j < len(b) && unicode.IsDigit(rune(b[j])) {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if c := cmp.Or(cmp.Compare(len(x), len(y)), strings.Compare(x, y)); c != 0 {
				return c
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

// kernelIn finds the newest kernel in dir and the initramfs made for it.
func kernelIn(dir string) (kernel, initramfs string, ok bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	var kernels, initrds []string
	for _, e := range ents {
		n := e.Name()
		if !e.Type().IsRegular() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		switch ext := filepath.Ext(n); {
		case ext == ".old" || ext == ".sig":
		case strings.HasPrefix(n, "vmlinuz"):
			kernels = append(kernels, n)
		case strings.HasPrefix(n, "initrd") || strings.HasPrefix(n, "initramfs"):
			initrds = append(initrds, n)
		}
	}
	if len(kernels) == 0 || len(initrds) == 0 {
		return "", "", false
	}
	slices.SortFunc(kernels, naturalCmp)
	slices.SortFunc(initrds, naturalCmp)
	k := kernels[len(kernels)-1]
	// vmlinuz-6.1.0-amd64 goes with initrd.img-6.1.0-amd64 (Debian),
	// initramfs-6.1.0-amd64.img (Fedora), initramfs-linux.img (Arch).
	if v, has := strings.CutPrefix(k, "vmlinuz-"); has {
		for _, c := range []string{"initrd.img-" + v, "initramfs-" + v + ".img", "initramfs-" + v, "initrd-" + v, "initrd.img-" + v + ".gz"} {
			if slices.Contains(initrds, c) {
				return filepath.Join(dir, k), filepath.Join(dir, c), true
			}
		}
	}
	return filepath.Join(dir, k), filepath.Join(dir, initrds[len(initrds)-1]), true
}

// liveCmdline is the command line that has liveInit find the squashfs and
// persistence image at these paths from the media's root.
func liveCmdline(sq, persist string) string {
	c := "bman.live=/" + filepath.ToSlash(sq)
	if persist != "" {
		c += " bman.persist=/" + filepath.ToSlash(persist)
	}
	return c
}

// liveEntries returns an entry per squashfs image on the removable media
// among mounts, which are the scan's file system mounts. The squashfs and
// loop device of each entry stay mounted, in the pool and m.loops, until
// Unmount; edit is the stage's command-line edit.
func (m *Manager) liveEntries(mounts []*mount.MountPoint, edit func(*boot.LinuxImage)) []boot.OSImage {
	var out []boot.OSImage
	for _, mp := range mounts {
		dev := filepath.Base(mp.Device)
		switch mp.FSType {
		case "squashfs", "erofs", "iso9660":
			continue
		}
		if !storage.Removable(dev) {
			continue
		}
		var squash, persist, btrfsImgs, diskImgs []string
		for _, rel := range findImages(mp.Path) {
			if strings.ContainsAny(rel, " \t\n") {
				log.Printf("live: %s on %s: a space in the path, skipped", rel, dev)
				continue
			}
			f, err := os.Open(filepath.Join(mp.Path, rel))
			if err != nil {
				continue
			}
			var size int64
			if st, err := f.Stat(); err == nil {
				size = st.Size()
			}
			kind := classifyImage(f, rel, size)
			f.Close()
			switch kind {
			case squashImage:
				squash = append(squash, rel)
			case persistImage:
				persist = append(persist, rel)
			case btrfsImage:
				btrfsImgs = append(btrfsImgs, rel)
			case diskImage:
				diskImgs = append(diskImgs, rel)
			}
		}
		for _, rel := range diskImgs {
			out = append(out, m.diskImageEntries(mp, dev, rel, edit)...)
		}
		for _, rel := range btrfsImgs {
			out = append(out, m.btrfsImageEntries(mp, dev, rel, edit)...)
		}
		for _, sq := range squash {
			p := persistenceFor(sq, persist)
			img, err := m.liveImage(mp, dev, sq, p)
			if err != nil {
				log.Printf("live: %s on %s: %v", sq, dev, err)
				continue
			}
			edit(img.LinuxImage)
			log.Printf("scan: live image %s on %s (persistence %q)", sq, dev, p)
			out = append(out, img)
		}
	}
	return out
}

// attachLoop binds a loop device to size bytes of the file src from offset
// (size 0: to its end).
func attachLoop(src string, offset, size uint64) (*loop.Loop, error) {
	dev, err := loop.FindDevice()
	if err != nil {
		return nil, fmt.Errorf("loop: %w", err)
	}
	if err := loop.SetFile(dev, src); err != nil {
		return nil, fmt.Errorf("loop: %w", err)
	}
	l := &loop.Loop{Dev: dev, Source: src}
	if offset != 0 || size != 0 {
		d, err := os.Open(dev)
		if err == nil {
			err = unix.IoctlLoopSetStatus64(int(d.Fd()), &unix.LoopInfo64{Offset: offset, Sizelimit: size})
			d.Close()
		}
		if err != nil {
			l.Free()
			return nil, fmt.Errorf("loop range: %w", err)
		}
	}
	return l, nil
}

// mountLoop mounts the file system in the file src read-only, through a loop
// device; drop undoes it, and until then the caller owns both.
func mountLoop(src, fstype string) (mp *mount.MountPoint, l *loop.Loop, drop func(), err error) {
	l, err = attachLoop(src, 0, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	l.FSType = fstype
	dir, err := os.MkdirTemp("", "bman-live-")
	if err != nil {
		l.Free()
		return nil, nil, nil, err
	}
	mp, err = l.Mount(dir, mount.ReadOnly)
	if err != nil {
		os.Remove(dir)
		l.Free()
		return nil, nil, nil, fmt.Errorf("mount: %w", err)
	}
	return mp, l, func() {
		mp.Unmount(mount.MNT_DETACH)
		os.Remove(dir)
		l.Free()
	}, nil
}

var errNoLiveKernel = errors.New("no kernel and initramfs in it or beside it")

// liveImage mounts the squashfs sq of mp and makes its entry.
func (m *Manager) liveImage(mp *mount.MountPoint, dev, sq, persist string) (*liveImage, error) {
	sqm, l, drop, err := mountLoop(filepath.Join(mp.Path, sq), "squashfs")
	if err != nil {
		return nil, err
	}
	dir := sqm.Path
	var kernel, ramfs string
	for _, d := range []string{filepath.Join(dir, "boot"), dir, filepath.Join(mp.Path, filepath.Dir(sq))} {
		var ok bool
		if kernel, ramfs, ok = kernelIn(d); ok {
			break
		}
	}
	if kernel == "" {
		drop()
		return nil, errNoLiveKernel
	}
	k, err := os.Open(kernel)
	if err != nil {
		drop()
		return nil, err
	}
	r, err := os.Open(ramfs)
	if err != nil {
		k.Close()
		drop()
		return nil, err
	}
	shim, err := initrd.Build([]initrd.File{{Path: "init", Mode: 0o755, Data: []byte(liveInit)}})
	if err != nil {
		k.Close()
		r.Close()
		drop()
		return nil, err
	}
	m.pool.Add(sqm)
	m.loops = append(m.loops, l)
	name := "Live " + strings.TrimSuffix(filepath.Base(sq), filepath.Ext(sq))
	if persist != "" {
		name += " (persistent)"
	}
	return &liveImage{
		LinuxImage: &boot.LinuxImage{
			Name:    name,
			Kernel:  k,
			Initrd:  boot.CatInitrds(r, bytes.NewReader(shim)),
			Cmdline: liveCmdline(sq, persist),
		},
		dev: dev,
	}, nil
}

func (l *liveImage) source() string {
	if strings.HasPrefix(l.dev, "mmcblk") {
		return "microSD card"
	}
	return "USB drive"
}
