// Package storage lists partitions and browses them read-only.
//
// Every path is opened through an os.Root at the partition's mount point,
// so neither ".." nor a symlink on the card reaches outside it: an
// absolute symlink would otherwise resolve against the stage's own root,
// where /etc/ssh holds the host key. The cost: an absolute symlink inside
// the partition is refused rather than followed.
package storage

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
	"golang.org/x/sys/unix"
)

// Browsing mounts are read-only and ask for the journal to be left alone
// (ext4 noload, f2fs/xfs norecovery): a plain ro mount still replays a
// dirty journal, which is a write. That only holds for a partition nothing
// has mounted yet. A second mount shares the first one's superblock and
// options, and the boot scan has already mounted every partition it
// could, plain ro -- as u-root's boot always has, and as it must for the
// scan to read a current extlinux.conf.
const browseRoot = "/mnt/browse"

type Browser struct {
	mu     sync.Mutex
	mounts map[string]*os.Root // dev -> its mount point
}

func NewBrowser() *Browser { return &Browser{mounts: map[string]*os.Root{}} }

type Disk struct {
	Dev    string `json:"dev"`
	Name   string `json:"name"`
	FSType string `json:"fstype"`
	Size   uint64 `json:"size"`
}

// Disks returns the partitions that carry a file system, removable media
// (SD cards, USB) first: that is where an OS on a phone usually lives, and
// internal storage can have dozens of partitions.
func (b *Browser) Disks() ([]Disk, error) {
	devs, err := block.GetBlockDevices()
	if err != nil {
		return nil, err
	}
	out := []Disk{}
	ext := map[string]bool{}
	for _, d := range devs.FilterZeroSize() {
		fstype := fsType(d.Name)
		if fstype == "" {
			continue
		}
		size, _ := d.Size()
		out = append(out, Disk{Dev: d.Name, Name: ueventValue(d.Name, "PARTNAME"), FSType: fstype, Size: size})
		ext[d.Name] = removable(d.Name)
	}
	slices.SortStableFunc(out, func(x, y Disk) int {
		if ext[x.Dev] != ext[y.Dev] {
			if ext[x.Dev] {
				return -1
			}
			return 1
		}
		return cmp.Compare(x.Dev, y.Dev)
	})
	return out, nil
}

// removable: the partition's disk is an SD card or marked removable (USB).
func removable(dev string) bool {
	p, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", dev))
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(p, "partition")); err == nil {
		p = filepath.Dir(p)
	}
	read := func(f string) string {
		b, _ := os.ReadFile(filepath.Join(p, f))
		return strings.TrimSpace(string(b))
	}
	return read("removable") == "1" || read("device/type") == "SD"
}

// ueventValue reads one KEY=value from a block device's uevent.
func ueventValue(dev, key string) string {
	f, err := os.Open(filepath.Join("/sys/class/block", dev, "uevent"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+"="); ok {
			return v
		}
	}
	return ""
}

// fsType names the file system on /dev/<dev> from its superblock, "" when
// the kernel has none it recognises. block.GetBlockDevices leaves FSType
// empty.
func fsType(dev string) string {
	fs, _, err := mount.FSFromBlock("/dev/" + dev)
	if err != nil {
		return ""
	}
	return fs
}

// root mounts dev for browsing, once, and returns its os.Root.
func (b *Browser) root(dev string) (*os.Root, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.mounts[dev]; ok {
		return r, nil
	}
	if dev == "" || strings.ContainsAny(dev, "/.") {
		return nil, fmt.Errorf("bad device %q", dev)
	}
	if _, err := block.Device(dev); err != nil {
		return nil, err
	}
	fstype := fsType(dev)
	if fstype == "" {
		return nil, fmt.Errorf("%s: no file system recognised", dev)
	}
	data := ""
	switch fstype {
	case "ext2", "ext3", "ext4":
		data = "noload"
	case "f2fs", "xfs":
		data = "norecovery"
	}
	mp := filepath.Join(browseRoot, dev)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return nil, err
	}
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("/dev/"+dev, mp, fstype, flags, data); err != nil {
		return nil, fmt.Errorf("mount %s (%s, ro): %w", dev, fstype, err)
	}
	r, err := os.OpenRoot(mp)
	if err != nil {
		unix.Unmount(mp, unix.MNT_DETACH)
		return nil, err
	}
	b.mounts[dev] = r
	return r, nil
}

// UnmountAll releases every browsing mount.
func (b *Browser) UnmountAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for dev, r := range b.mounts {
		mp := r.Name()
		r.Close()
		unix.Unmount(mp, unix.MNT_DETACH)
		delete(b.mounts, dev)
	}
}

// clean turns a path inside a partition into os.Root's form ("." for the
// top) and the form shown to the user ("/a/b").
func clean(p string) (rel, shown string) {
	shown = path.Clean("/" + p)
	rel = strings.TrimPrefix(shown, "/")
	if rel == "" {
		rel = "."
	}
	return rel, shown
}

// Open opens dev's file at p, a path inside the partition.
func (b *Browser) Open(dev, p string) (*os.File, error) {
	r, err := b.root(dev)
	if err != nil {
		return nil, err
	}
	rel, _ := clean(p)
	return r.Open(rel)
}

type File struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Link string `json:"link,omitempty"`
	Size int64  `json:"size"`
}

type Listing struct {
	Path      string `json:"path"`
	Entries   []File `json:"entries"`
	Truncated bool   `json:"truncated"`
}

const maxListing = 1000

func (b *Browser) List(dev, p string) (Listing, error) {
	r, err := b.root(dev)
	if err != nil {
		return Listing{}, err
	}
	rel, shown := clean(p)
	d, err := r.Open(rel)
	if err != nil {
		return Listing{}, err
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return Listing{}, err
	}
	files := make([]File, 0, len(ents))
	for _, e := range ents {
		f := File{Name: e.Name(), Dir: e.IsDir()}
		// Through the root: DirEntry.Info would stat relative to the cwd.
		p := path.Join(rel, e.Name())
		if info, err := r.Lstat(p); err == nil {
			f.Size = info.Size()
			if info.Mode()&os.ModeSymlink != 0 {
				f.Link, _ = r.Readlink(p)
			}
		}
		files = append(files, f)
	}
	slices.SortStableFunc(files, func(x, y File) int {
		if x.Dir != y.Dir {
			if x.Dir {
				return -1
			}
			return 1
		}
		return cmp.Compare(x.Name, y.Name)
	})
	l := Listing{Path: shown, Entries: files}
	if len(files) > maxListing {
		l.Entries, l.Truncated = files[:maxListing], true
	}
	return l, nil
}
