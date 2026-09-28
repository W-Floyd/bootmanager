package slots

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// blkFlsBuf drops a block device's cached pages (linux/fs.h BLKFLSBUF).
const blkFlsBuf = 0x1261

// OpenDisks opens every whole disk in /sys/block but RAM, loop, zram,
// device-mapper, and eMMC boot and RPMB areas.
func OpenDisks(write bool) ([]Disk, error) {
	ents, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	flags := os.O_RDONLY
	if write {
		flags = os.O_RDWR
	}
	var out []Disk
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "zram") ||
			strings.HasPrefix(n, "dm-") || strings.Contains(n, "boot") || strings.Contains(n, "rpmb") {
			continue
		}
		bs := readInt(filepath.Join("/sys/block", n, "queue/logical_block_size"))
		sectors := readInt(filepath.Join("/sys/block", n, "size")) // 512-byte units, whatever the block size
		if bs <= 0 || sectors <= 0 {
			continue
		}
		f, err := os.OpenFile("/dev/"+n, flags, 0)
		if err != nil {
			continue
		}
		out = append(out, &blockDisk{File: f, name: n, bs: bs, blocks: sectors * 512 / bs})
	}
	return out, nil
}

func readInt(p string) int64 {
	b, _ := os.ReadFile(p)
	v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v
}

type blockDisk struct {
	*os.File
	name       string
	bs, blocks int64
}

func (d *blockDisk) Name() string     { return d.name }
func (d *blockDisk) BlockSize() int64 { return d.bs }
func (d *blockDisk) Blocks() int64    { return d.blocks }

func (d *blockDisk) Sync() error {
	if err := d.File.Sync(); err != nil {
		return err
	}
	return unix.IoctlSetInt(int(d.Fd()), blkFlsBuf, 0)
}
