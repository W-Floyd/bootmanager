// Package btrfs lists a btrfs file system's subvolumes. A distribution that
// installs onto btrfs keeps its root, and its /boot, in a subvolume (@,
// root), and the kernel mounts only the default one: the others are found by
// searching the root tree and mounted by id (subvolid=).
package btrfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	rootTreeID    = 1
	topLevelID    = 5
	firstFreeID   = 256
	lastFreeID    = ^uint64(0) - 255
	rootBackref   = 144 // BTRFS_ROOT_BACKREF_KEY
	maxDepth      = 32
	searchBufSize = 4096 - 104
)

// Subvol is a subvolume: its id, its parent's, and its path from the top
// level subvolume (id 5), whichever is mounted.
type Subvol struct {
	ID     uint64
	Parent uint64
	Path   string
}

// ioctl numbers are _IOWR(0x94, nr, 4096-byte struct).
func ioctlNum(nr uintptr) uintptr { return 3<<30 | 4096<<16 | 0x94<<8 | nr }

type searchKey struct {
	TreeID                              uint64
	MinObjectID, MaxObjectID            uint64
	MinOffset, MaxOffset                uint64
	MinTransID, MaxTransID              uint64
	MinType, MaxType, NrItems, unused32 uint32
	unused64                            [4]uint64
}

type searchArgs struct {
	Key searchKey
	Buf [searchBufSize]byte
}

type inoLookupArgs struct {
	TreeID, ObjectID uint64
	Name             [4080]byte
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// backref is a ROOT_BACKREF item: subvolume id is named name in directory
// dirid of its parent.
type backref struct {
	id, parent, dirid uint64
	name              string
}

// parseResults reads the n items a tree search returned (a 32-byte header
// each: transid, objectid, offset, type, length; then the item), the
// backrefs among them and the last item's objectid and offset.
func parseResults(buf []byte, n int) (refs []backref, lastObj, lastOff uint64, err error) {
	le := binary.LittleEndian
	for range n {
		if len(buf) < 32 {
			return nil, 0, 0, errors.New("short search result")
		}
		obj, off, typ, size := le.Uint64(buf[8:]), le.Uint64(buf[16:]), le.Uint32(buf[24:]), int(le.Uint32(buf[28:]))
		if len(buf) < 32+size {
			return nil, 0, 0, errors.New("short search item")
		}
		item := buf[32 : 32+size]
		buf = buf[32+size:]
		lastObj, lastOff = obj, off
		if typ != rootBackref || len(item) < 18 {
			continue
		}
		// btrfs_root_ref: dirid, sequence, name_len, name.
		nl := int(le.Uint16(item[16:]))
		if len(item) < 18+nl {
			continue
		}
		refs = append(refs, backref{id: obj, parent: off, dirid: le.Uint64(item), name: string(item[18 : 18+nl])})
	}
	return refs, lastObj, lastOff, nil
}

// Subvolumes lists the subvolumes of the file system mounted at dir, by path.
// The top level subvolume is not among them.
func Subvolumes(dir string) ([]Subvol, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []backref
	var args searchArgs
	args.Key = searchKey{
		TreeID: rootTreeID, MinObjectID: firstFreeID, MaxObjectID: lastFreeID,
		MaxOffset: ^uint64(0), MaxTransID: ^uint64(0),
		MinType: rootBackref, MaxType: rootBackref,
	}
	for {
		args.Key.NrItems = 4096
		if err := ioctl(f, ioctlNum(17), unsafe.Pointer(&args)); err != nil {
			return nil, fmt.Errorf("btrfs tree search: %w", err)
		}
		n := int(args.Key.NrItems)
		if n == 0 {
			break
		}
		refs, obj, off, err := parseResults(args.Buf[:], n)
		if err != nil {
			return nil, err
		}
		all = append(all, refs...)
		args.Key.MinObjectID, args.Key.MinOffset = obj, off+1
		if off+1 == 0 {
			args.Key.MinObjectID++
		}
		if args.Key.MinObjectID > lastFreeID {
			break
		}
	}
	return resolve(all, func(tree, dirid uint64) (string, error) { return inoLookup(f, tree, dirid) })
}

// resolve gives each backref its path from the top level: its parent's, the
// directory it is in, its name. lookup is the path of a directory inode
// within a subvolume, with a trailing slash ("" for its root).
func resolve(refs []backref, lookup func(tree, dirid uint64) (string, error)) ([]Subvol, error) {
	byID := map[uint64]backref{}
	for _, r := range refs {
		if _, dup := byID[r.id]; !dup {
			byID[r.id] = r
		}
	}
	var pathOf func(id uint64, depth int) (string, error)
	pathOf = func(id uint64, depth int) (string, error) {
		if id == topLevelID {
			return "", nil
		}
		r, ok := byID[id]
		if !ok || depth > maxDepth {
			return "", fmt.Errorf("subvolume %d: no path", id)
		}
		up, err := pathOf(r.parent, depth+1)
		if err != nil {
			return "", err
		}
		in, err := lookup(r.parent, r.dirid)
		if err != nil {
			return "", err
		}
		return up + in + r.name + "/", nil
	}
	var out []Subvol
	for _, r := range byID {
		p, err := pathOf(r.id, 0)
		if err != nil {
			continue
		}
		out = append(out, Subvol{ID: r.id, Parent: r.parent, Path: strings.TrimSuffix(p, "/")})
	}
	slices.SortFunc(out, func(a, b Subvol) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func inoLookup(f *os.File, tree, dirid uint64) (string, error) {
	a := inoLookupArgs{TreeID: tree, ObjectID: dirid}
	if err := ioctl(f, ioctlNum(18), unsafe.Pointer(&a)); err != nil {
		return "", fmt.Errorf("btrfs ino lookup: %w", err)
	}
	n := slices.Index(a.Name[:], 0)
	if n < 0 {
		n = len(a.Name)
	}
	return string(a.Name[:n]), nil
}

// MountedID is the id of the subvolume mounted at dir.
func MountedID(dir string) (uint64, error) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	a := inoLookupArgs{ObjectID: firstFreeID}
	if err := ioctl(f, ioctlNum(18), unsafe.Pointer(&a)); err != nil {
		return 0, err
	}
	return a.TreeID, nil
}
