// Package disk reads the partition table (GPT or MBR) of a whole-disk image
// file, to find where its partitions are in it.
package disk

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/u-root/u-root/pkg/mount/gpt"
)

// ErrNoTable is a file that does not start with a partition table.
var ErrNoTable = errors.New("no partition table")

// Part is a partition: its number in the table (logical MBR partitions from
// 5), where it starts and its size, in bytes, and its type as the table has
// it (a GPT type GUID, lower case, or an MBR type as "0x83"). Name is the
// GPT name, "" in MBR.
type Part struct {
	Index       int
	Start, Size uint64
	Type, Name  string
}

const maxParts = gpt.MaxNPart

// ParseTable reads the table of the disk image r of size bytes: GPT when
// there is a valid one, else MBR. A FAT, NTFS or exFAT boot sector, which
// ends in the same 0x55AA, is not a table.
func ParseTable(r io.ReaderAt, size int64) ([]Part, error) {
	if ps, ok := parseGPT(r, size); ok {
		return ps, nil
	}
	return parseMBR(r, size)
}

// parseGPT reads the primary GPT with u-root's reader, which checks the
// header and entry CRCs; 512-byte sectors only, as images are.
func parseGPT(r io.ReaderAt, size int64) ([]Part, bool) {
	g, err := gpt.Table(r, gpt.HeaderOff)
	if err != nil {
		return nil, false
	}
	var out []Part
	for i, e := range g.Parts {
		if e.PartGUID == (gpt.GUID{}) || e.LastLBA < e.FirstLBA || (e.LastLBA+1)*gpt.BlockSize > uint64(size) {
			continue
		}
		var name []uint16
		for j := 0; j+1 < len(e.Name); j += 2 {
			c := binary.LittleEndian.Uint16(e.Name[j:])
			if c == 0 {
				break
			}
			name = append(name, c)
		}
		out = append(out, Part{
			Index: i + 1, Start: e.FirstLBA * gpt.BlockSize, Size: (e.LastLBA - e.FirstLBA + 1) * gpt.BlockSize,
			Type: e.PartGUID.String(), Name: string(utf16.Decode(name)),
		})
	}
	return out, true
}

func isExtended(t byte) bool { return t == 0x05 || t == 0x0f || t == 0x85 }

func parseMBR(r io.ReaderAt, size int64) ([]Part, error) {
	le := binary.LittleEndian
	var s [512]byte
	if n, _ := r.ReadAt(s[:], 0); n < len(s) || s[510] != 0x55 || s[511] != 0xaa {
		return nil, ErrNoTable
	}
	// A file system's boot sector ends the same way.
	if string(s[3:11]) == "NTFS    " || string(s[3:11]) == "EXFAT   " ||
		strings.HasPrefix(string(s[54:59]), "FAT") || strings.HasPrefix(string(s[82:87]), "FAT") {
		return nil, ErrNoTable
	}
	type ent struct {
		typ         byte
		start, size uint64
	}
	entries := func(b []byte) (out []ent, ok bool) {
		for i := range 4 {
			e := b[446+16*i:]
			if e[0] != 0 && e[0] != 0x80 {
				return nil, false
			}
			out = append(out, ent{e[4], uint64(le.Uint32(e[8:])) * 512, uint64(le.Uint32(e[12:])) * 512})
		}
		return out, true
	}
	prim, ok := entries(s[:])
	if !ok {
		return nil, ErrNoTable
	}
	var out []Part
	var ext *ent
	for i, e := range prim {
		if e.typ == 0 || e.size == 0 || e.typ == 0xee {
			continue
		}
		if e.start == 0 || e.start+e.size > uint64(size) {
			return nil, ErrNoTable
		}
		if isExtended(e.typ) {
			ext = &prim[i]
			continue
		}
		out = append(out, Part{Index: i + 1, Start: e.start, Size: e.size, Type: hex(e.typ)})
	}
	if len(out) == 0 && ext == nil {
		return nil, ErrNoTable
	}
	// Logical partitions: a chain of boot records, each with a partition
	// (relative to itself) and a link to the next (relative to the
	// extended partition).
	if ext != nil {
		next := uint64(0)
		for n := 5; n < 5+maxParts; n++ {
			var b [512]byte
			at := ext.start + next
			if rd, _ := r.ReadAt(b[:], int64(at)); rd < len(b) || b[510] != 0x55 || b[511] != 0xaa {
				break
			}
			es, ok := entries(b[:])
			if !ok {
				break
			}
			if e := es[0]; e.typ != 0 && e.size > 0 && !isExtended(e.typ) && at+e.start+e.size <= uint64(size) {
				out = append(out, Part{Index: n, Start: at + e.start, Size: e.size, Type: hex(e.typ)})
			}
			if !isExtended(es[1].typ) || es[1].start == 0 {
				break
			}
			next = es[1].start
		}
	}
	return out, nil
}

func hex(b byte) string {
	const d = "0123456789abcdef"
	return "0x" + string([]byte{d[b>>4], d[b&15]})
}
