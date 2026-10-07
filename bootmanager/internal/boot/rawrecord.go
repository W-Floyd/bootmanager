package boot

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Settings with no file system to keep them on. A boot image's partition
// (boot_a, where the stage lives) cannot be formatted, as ABL wants the image
// at its start, but what lies after the image is free: boot-deploy writes the
// image's own length and no more, and AVB is off here. The record is kept
// there in the way U-Boot keeps its environment on a raw partition: at a
// fixed offset from the end, in two copies, each with a sequence number and a
// CRC, the newest valid one winning, so a write cut short leaves the other
// whole.
//
// Android's misc partition and U-Boot's raw environment are the precedents.

const (
	// rawBlock is the unit written: a block device wants whole sectors.
	rawBlock = 4096
	// rawSlotSize is room for one copy: the header and the JSON.
	rawSlotSize = 64 << 10
	// rawMinSize is the smallest partition used: below it the copies would
	// reach into the image.
	rawMinSize = 16 << 20
	// The copies sit this far from the partition's end, clear of the AVB
	// footer in its last 4 KiB. A offset-from-the-end layout keeps its place
	// whatever the size.
	rawOffA = 2 << 20
	rawOffB = 3 << 20

	rawHeaderLen = 8 + 8 + 4 + 4
)

var rawMagic = [8]byte{'B', 'M', 'S', 'E', 'T', '0', '0', '1'}

// rawStore is a file or block device kept to the record's layout.
type rawStore struct{ path string }

type rawSlot struct {
	seq  uint64
	data []byte
	ok   bool
}

func (r rawStore) offsets(size int64) (a, b int64, err error) {
	if size < rawMinSize {
		return 0, 0, fmt.Errorf("%s: %d bytes is too small to keep settings in", r.path, size)
	}
	return size - rawOffA, size - rawOffB, nil
}

func sizeOf(f *os.File) (int64, error) {
	return f.Seek(0, io.SeekEnd)
}

// readSlot decodes the copy at off, or a slot that is not ok when there is
// none (never written, torn, or someone else's data).
func readSlot(f *os.File, off int64) rawSlot {
	buf := make([]byte, rawSlotSize)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return rawSlot{}
	}
	if [8]byte(buf[:8]) != rawMagic {
		return rawSlot{}
	}
	seq := binary.LittleEndian.Uint64(buf[8:])
	n := binary.LittleEndian.Uint32(buf[16:])
	sum := binary.LittleEndian.Uint32(buf[20:])
	if n > rawSlotSize-rawHeaderLen {
		return rawSlot{}
	}
	data := buf[rawHeaderLen : rawHeaderLen+n]
	if crc32.ChecksumIEEE(data) != sum {
		return rawSlot{}
	}
	return rawSlot{seq: seq, data: append([]byte(nil), data...), ok: true}
}

// load is the newest valid copy's data; none, with no error, when neither
// copy is valid (nothing saved yet).
func (r rawStore) load() ([]byte, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := sizeOf(f)
	if err != nil {
		return nil, err
	}
	a, b, err := r.offsets(size)
	if err != nil {
		return nil, err
	}
	sa, sb := readSlot(f, a), readSlot(f, b)
	switch {
	case sa.ok && (!sb.ok || sa.seq >= sb.seq):
		return sa.data, nil
	case sb.ok:
		return sb.data, nil
	}
	return nil, nil
}

// save writes data over the older copy, newer than the other by one, and
// syncs it. The other copy is untouched, so it stands if this is cut short.
func (r rawStore) save(data []byte) error {
	if len(data) > rawSlotSize-rawHeaderLen {
		return fmt.Errorf("settings are %d bytes, more than the %d a copy holds", len(data), rawSlotSize-rawHeaderLen)
	}
	f, err := os.OpenFile(r.path, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := sizeOf(f)
	if err != nil {
		return err
	}
	a, b, err := r.offsets(size)
	if err != nil {
		return err
	}
	sa, sb := readSlot(f, a), readSlot(f, b)
	// The older (or invalid) copy is the one to write.
	off, seq := a, uint64(1)
	switch {
	case sa.ok && sb.ok:
		seq = max(sa.seq, sb.seq) + 1
		if sa.seq > sb.seq {
			off = b
		}
	case sa.ok:
		seq, off = sa.seq+1, b
	case sb.ok:
		seq, off = sb.seq+1, a
	}
	buf := make([]byte, (rawHeaderLen+len(data)+rawBlock-1)/rawBlock*rawBlock)
	copy(buf, rawMagic[:])
	binary.LittleEndian.PutUint64(buf[8:], seq)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(data)))
	binary.LittleEndian.PutUint32(buf[20:], crc32.ChecksumIEEE(data))
	copy(buf[rawHeaderLen:], data)
	if _, err := f.WriteAt(buf, off); err != nil {
		return err
	}
	return f.Sync()
}

// partitionByName is the block device of the GPT partition with this name
// (boot_a), found from the kernel's PARTNAME of each block device: the stage
// has no udev to make /dev/disk/by-partlabel.
func partitionByName(sys, dev, name string) (string, error) {
	ents, err := filepath.Glob(filepath.Join(sys, "*", "uevent"))
	if err != nil {
		return "", err
	}
	for _, p := range ents {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PARTNAME="); ok && v == name {
				return filepath.Join(dev, filepath.Base(filepath.Dir(p))), nil
			}
		}
	}
	return "", fmt.Errorf("no partition named %s", name)
}
