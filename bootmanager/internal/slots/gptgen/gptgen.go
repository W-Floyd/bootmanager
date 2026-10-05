// Package gptgen synthesises GPT disk images for tests: a protective MBR,
// the primary header at LBA 1 with its entries at LBA 2, and the backup
// entries and header at the end of the disk, all CRCs correct. Nothing in
// it comes from a real device: the GUIDs are derived from a seed, and the
// layout (Fogona) only imitates the shape of a Moto G Play 2024's UFS LUNs.
//
// An Image holds only the two ends of the disk, as a GPT occupies nothing
// between them; WriteFile writes a (sparse) full-size file for inspection
// with sgdisk, gdisk or hexdump.
//
// This is a copy of internal/gptgen of github.com/W-Floyd/go-qbootctl (same
// author), which the Boot Manager is to import instead of internal/slots; it
// goes away with this package.
package gptgen

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"unicode/utf16"
)

// The A/B attribute bits, as ABL reads them (the same as slots.go's).
const (
	BitPriority   = 48 // 2 bits
	BitActive     = 50
	BitRetries    = 51 // 3 bits
	BitSuccessful = 54
	BitUnbootable = 55
)

// AB is an entry's A/B state as attribute bits 48-55.
type AB struct {
	Priority   int // 0-3
	Active     bool
	Retries    int // 0-7
	Successful bool
	Unbootable bool
}

// Attr is base with the A/B bits (48-55) replaced by ab.
func Attr(base uint64, ab AB) uint64 {
	v := base &^ (0xff << BitPriority)
	v |= uint64(ab.Priority&3) << BitPriority
	v |= uint64(ab.Retries&7) << BitRetries
	for bit, on := range map[int]bool{BitActive: ab.Active, BitSuccessful: ab.Successful, BitUnbootable: ab.Unbootable} {
		if on {
			v |= 1 << bit
		}
	}
	return v
}

// Part is one partition entry.
type Part struct {
	Name string // at most 36 UTF-16 units
	Attr uint64 // all 64 attribute bits
}

// Disk is the description of one disk.
type Disk struct {
	Name      string // "sdd"; also part of the GUID seed
	BlockSize int64  // logical block size: 4096 on UFS, 512 elsewhere
	Blocks    int64
	Entries   int    // entries in the array, 128 when 0
	Seed      string // GUIDs derive from it
	Parts     []Part
}

// Image is a built disk: Head is the start of the disk (the MBR, the
// primary header and entries), Tail its end (the backup entries and
// header), both whole blocks.
type Image struct {
	Name       string
	BlockSize  int64
	Blocks     int64
	Head, Tail []byte
}

const entrySize = 128

var le = binary.LittleEndian

func guid(seed string, parts ...string) [16]byte {
	h := sha256.New()
	h.Write([]byte(seed))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	var g [16]byte
	copy(g[:], h.Sum(nil))
	g[7] = g[7]&0x0f | 0x40 // version 4 layout
	g[8] = g[8]&0x3f | 0x80
	return g
}

// Build lays the disk out and writes both GPT copies.
func (d Disk) Build() (Image, error) {
	n := d.Entries
	if n == 0 {
		n = 128
	}
	bs := d.BlockSize
	if bs < 512 || bs&(bs-1) != 0 {
		return Image{}, fmt.Errorf("gptgen: block size %d", bs)
	}
	if len(d.Parts) > n {
		return Image{}, fmt.Errorf("gptgen: %d partitions in %d entries", len(d.Parts), n)
	}
	entryBlocks := (int64(n)*entrySize + bs - 1) / bs
	last := d.Blocks - 1
	firstUsable, lastUsable := 2+entryBlocks, last-1-entryBlocks

	entries := make([]byte, int64(n)*entrySize)
	const align = 1 << 20 // partitions are 1 MiB, 1 MiB aligned
	start := (firstUsable*bs + align - 1) / align * align / bs
	pblocks := align / bs
	for i, p := range d.Parts {
		u := utf16.Encode([]rune(p.Name))
		if len(u) > 36 {
			return Image{}, fmt.Errorf("gptgen: name %q too long", p.Name)
		}
		if start+pblocks-1 > lastUsable {
			return Image{}, fmt.Errorf("gptgen: %s does not fit", p.Name)
		}
		e := entries[i*entrySize : (i+1)*entrySize]
		t := guid(d.Seed, "type", p.Name)
		u16 := guid(d.Seed, "unique", d.Name, p.Name)
		copy(e[0:], t[:])
		copy(e[16:], u16[:])
		le.PutUint64(e[32:], uint64(start))
		le.PutUint64(e[40:], uint64(start+pblocks-1))
		le.PutUint64(e[48:], p.Attr)
		for j, c := range u {
			le.PutUint16(e[56+2*j:], c)
		}
		start += pblocks
	}
	ecrc := crc32.ChecksumIEEE(entries)
	diskGUID := guid(d.Seed, "disk", d.Name)

	header := func(my, alt, entriesLBA int64) []byte {
		b := make([]byte, bs)
		copy(b, "EFI PART")
		le.PutUint32(b[8:], 0x00010000)
		le.PutUint32(b[12:], 92)
		le.PutUint64(b[24:], uint64(my))
		le.PutUint64(b[32:], uint64(alt))
		le.PutUint64(b[40:], uint64(firstUsable))
		le.PutUint64(b[48:], uint64(lastUsable))
		copy(b[56:], diskGUID[:])
		le.PutUint64(b[72:], uint64(entriesLBA))
		le.PutUint32(b[80:], uint32(n))
		le.PutUint32(b[84:], entrySize)
		le.PutUint32(b[88:], ecrc)
		le.PutUint32(b[16:], crc32.ChecksumIEEE(b[:92]))
		return b
	}

	mbr := make([]byte, bs)
	mbr[446+4] = 0xee // protective MBR: one partition of type EE
	mbr[446+1], mbr[446+2] = 0, 2
	mbr[446+5], mbr[446+6], mbr[446+7] = 0xff, 0xff, 0xff
	le.PutUint32(mbr[446+8:], 1)
	size := uint64(d.Blocks - 1)
	if size > 0xffffffff {
		size = 0xffffffff
	}
	le.PutUint32(mbr[446+12:], uint32(size))
	mbr[510], mbr[511] = 0x55, 0xaa

	pad := func(b []byte) []byte { // to whole blocks
		out := make([]byte, (int64(len(b))+bs-1)/bs*bs)
		copy(out, b)
		return out
	}
	head := append(append(mbr, header(1, last, 2)...), pad(entries)...)
	tail := append(pad(entries), header(last, 1, last-entryBlocks)...)
	return Image{Name: d.Name, BlockSize: bs, Blocks: d.Blocks, Head: head, Tail: tail}, nil
}

// WriteFile writes the disk to path as a full-size sparse file.
func (im Image) WriteFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	size := im.Blocks * im.BlockSize
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteAt(im.Head, 0); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteAt(im.Tail, size-int64(len(im.Tail))); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
