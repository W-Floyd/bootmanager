package fastboot

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linux/fs.h; x/sys/unix has neither.
const (
	blkRRPart  = 0x125f
	blkZeroOut = 0x127f
)

// Partitions lists the block devices, from sysfs: every disk and
// partition but RAM, loop and zram devices.
func Partitions() ([]Partition, error) {
	ents, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil, err
	}
	var out []Partition
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "zram") {
			continue
		}
		dir, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", name))
		if err != nil {
			continue
		}
		sectors, _ := strconv.ParseInt(readSys(dir, "size"), 10, 64)
		if sectors == 0 {
			continue
		}
		p := Partition{Dev: name, Size: sectors * 512, Label: ueventValue(dir, "PARTNAME")}
		disk := dir
		if _, err := os.Stat(filepath.Join(dir, "partition")); err != nil {
			p.Disk = true
		} else {
			disk = filepath.Dir(dir)
		}
		p.SD = readSys(disk, "device/type") == "SD"
		out = append(out, p)
	}
	return out, nil
}

func readSys(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(b))
}

func ueventValue(dir, key string) string {
	f, err := os.Open(filepath.Join(dir, "uevent"))
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

// Open opens p's device for writing, exclusively: the kernel refuses it
// while p, or for a disk any of its partitions, is mounted.
func Open(p Partition) (Target, error) {
	f, err := os.OpenFile("/dev/"+p.Dev, os.O_WRONLY|unix.O_EXCL, 0)
	if errors.Is(err, unix.EBUSY) {
		return nil, fmt.Errorf("%s is in use (mounted?)", p.Dev)
	}
	if err != nil {
		return nil, err
	}
	return &blockDev{File: f, disk: p.Disk}, nil
}

type blockDev struct {
	*os.File
	disk bool
}

func (b *blockDev) ZeroRange(off, n int64) error {
	r := [2]uint64{uint64(off), uint64(n)}
	return ioctl(b.Fd(), blkZeroOut, uintptr(unsafe.Pointer(&r)))
}

// Close flushes the device and, for a disk, has the kernel read its
// partition table again: a disk image brings its own.
func (b *blockDev) Close() error {
	err := b.Sync()
	if b.disk && err == nil {
		if err = ioctl(b.Fd(), blkRRPart, 0); err != nil {
			err = fmt.Errorf("%s: rereading the partition table: %w", b.Name(), err)
		}
	}
	if cerr := b.File.Close(); err == nil {
		err = cerr
	}
	return err
}

func ioctl(fd uintptr, req, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}
