package slots

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"unicode/utf16"
)

// Disk is a whole disk that may hold a GPT.
type Disk interface {
	io.ReaderAt
	io.WriterAt
	Name() string
	BlockSize() int64
	Blocks() int64
	// Sync gets writes to the device and drops what is cached of it, so
	// the next read is the device's.
	Sync() error
	Close() error
}

var errNoGPT = errors.New("no GPT")

var le = binary.LittleEndian

// table is a disk's GPT, both copies, which must agree: only the
// attributes of existing entries ever change, so each copy keeps its own
// layout and a write replaces the same bytes.
type table struct {
	disk    Disk
	hdr     []byte // the primary header, its HeaderSize bytes
	bhdr    []byte // the backup's
	entries []byte // NumberOfEntries * SizeOfEntry bytes, the same in both
	orig    []byte // entries as read
}

type entry struct {
	name string
	attr uint64
	idx  int
}

func readHeader(d Disk, lba int64) ([]byte, error) {
	bs := d.BlockSize()
	b := make([]byte, bs)
	if _, err := d.ReadAt(b, lba*bs); err != nil {
		return nil, err
	}
	if string(b[:8]) != "EFI PART" {
		return nil, errNoGPT
	}
	size := int64(le.Uint32(b[12:]))
	if size < 92 || size > bs {
		return nil, fmt.Errorf("header size %d", size)
	}
	h := b[:size]
	if headerCRC(h) != le.Uint32(h[16:]) {
		return nil, errors.New("header CRC")
	}
	if int64(le.Uint64(h[24:])) != lba {
		return nil, fmt.Errorf("header at LBA %d says it is at %d", lba, le.Uint64(h[24:]))
	}
	return h, nil
}

func headerCRC(h []byte) uint32 {
	c := bytes.Clone(h)
	le.PutUint32(c[16:], 0)
	return crc32.ChecksumIEEE(c)
}

func entriesSize(h []byte) (n, size int, err error) {
	n, size = int(le.Uint32(h[80:])), int(le.Uint32(h[84:]))
	if size < 128 || size%8 != 0 || n <= 0 || n*size > 1<<20 {
		return 0, 0, fmt.Errorf("%d entries of %d bytes", n, size)
	}
	return n, size, nil
}

func readEntries(d Disk, h []byte) ([]byte, error) {
	n, size, err := entriesSize(h)
	if err != nil {
		return nil, err
	}
	b := make([]byte, n*size)
	if _, err := d.ReadAt(b, int64(le.Uint64(h[72:]))*d.BlockSize()); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(b) != le.Uint32(h[88:]) {
		return nil, errors.New("entries CRC")
	}
	return b, nil
}

// readTable reads and checks both copies of d's GPT. errNoGPT: d has none.
func readTable(d Disk) (*table, error) {
	h, err := readHeader(d, 1)
	if err != nil {
		if errors.Is(err, errNoGPT) {
			return nil, err
		}
		return nil, fmt.Errorf("%s: primary GPT: %w", d.Name(), err)
	}
	e, err := readEntries(d, h)
	if err != nil {
		return nil, fmt.Errorf("%s: primary GPT: %w", d.Name(), err)
	}
	bak := int64(le.Uint64(h[32:]))
	if bak <= 1 || bak >= d.Blocks() {
		return nil, fmt.Errorf("%s: backup GPT at LBA %d of %d", d.Name(), bak, d.Blocks())
	}
	bh, err := readHeader(d, bak)
	if err != nil {
		return nil, fmt.Errorf("%s: backup GPT: %w", d.Name(), err)
	}
	be, err := readEntries(d, bh)
	if err != nil {
		return nil, fmt.Errorf("%s: backup GPT: %w", d.Name(), err)
	}
	if !bytes.Equal(e, be) || !bytes.Equal(h[80:88], bh[80:88]) {
		return nil, fmt.Errorf("%s: the backup GPT's entries differ from the primary's", d.Name())
	}
	return &table{disk: d, hdr: h, bhdr: bh, entries: e, orig: bytes.Clone(e)}, nil
}

func (t *table) entrySize() int { return int(le.Uint32(t.hdr[84:])) }

func (t *table) list() []entry {
	var out []entry
	size := t.entrySize()
	for i := 0; i+size <= len(t.entries); i += size {
		e := t.entries[i : i+size]
		if bytes.Equal(e[:16], make([]byte, 16)) {
			continue // unused
		}
		u := make([]uint16, 36)
		for j := range u {
			u[j] = le.Uint16(e[56+2*j:])
		}
		if k := slices.Index(u, 0); k >= 0 {
			u = u[:k]
		}
		out = append(out, entry{name: string(utf16.Decode(u)), attr: le.Uint64(e[48:]), idx: i / size})
	}
	return out
}

func (t *table) setAttr(idx int, attr uint64) {
	le.PutUint64(t.entries[idx*t.entrySize()+48:], attr)
}

func (t *table) changed() bool { return !bytes.Equal(t.entries, t.orig) }

// write puts the changed entries in both copies: the backup, then the
// primary, each checked on the device before the next is written, so one
// valid copy is always there.
func (t *table) write() error {
	crc := crc32.ChecksumIEEE(t.entries)
	for _, c := range []struct {
		name string
		h    []byte
	}{{"backup", t.bhdr}, {"primary", t.hdr}} {
		copyName, h := c.name, c.h
		le.PutUint32(h[88:], crc)
		le.PutUint32(h[16:], headerCRC(h))
		bs := t.disk.BlockSize()
		if _, err := t.disk.WriteAt(t.entries, int64(le.Uint64(h[72:]))*bs); err != nil {
			return fmt.Errorf("%s: %s GPT: %w", t.disk.Name(), copyName, err)
		}
		if _, err := t.disk.WriteAt(h, int64(le.Uint64(h[24:]))*bs); err != nil {
			return fmt.Errorf("%s: %s GPT: %w", t.disk.Name(), copyName, err)
		}
		if err := t.disk.Sync(); err != nil {
			return fmt.Errorf("%s: %s GPT: %w", t.disk.Name(), copyName, err)
		}
		got, err := readHeader(t.disk, int64(le.Uint64(h[24:])))
		if err == nil {
			var e []byte
			if e, err = readEntries(t.disk, got); err == nil && !bytes.Equal(e, t.entries) {
				err = errors.New("entries read back differ")
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %s GPT, read back: %w", t.disk.Name(), copyName, err)
		}
	}
	t.orig = bytes.Clone(t.entries)
	return nil
}
