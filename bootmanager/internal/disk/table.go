// Package disk reads the partition table (GPT or MBR) of a whole-disk image
// file, to find where its partitions are in it.
package disk

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"github.com/rekby/gpt"
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

const maxParts = 128

// ParseTable reads the table of the disk image r of size bytes: GPT when
// there is a valid one, else MBR. A FAT, NTFS or exFAT boot sector, which
// ends in the same 0x55AA, is not a table.
func ParseTable(r io.ReaderAt, size int64) ([]Part, error) {
	if ps, ok := parseGPT(r, size); ok {
		return ps, nil
	}
	return parseMBR(r, size)
}

// parseGPT reads the primary GPT, with 512-byte sectors or 4096 (4Kn), using
// rekby/gpt, which checks the header and entry CRCs (u-root's reader is for
// 512 only). That library trusts the header's counts and sizes, so they are
// bounded here first: an image is a file from someone's drive.
func parseGPT(r io.ReaderAt, size int64) ([]Part, bool) {
	le := binary.LittleEndian
	for _, ss := range []uint64{512, 4096} {
		h := make([]byte, 92)
		if n, _ := r.ReadAt(h, int64(ss)); n < len(h) || string(h[:8]) != "EFI PART" {
			continue
		}
		start, count, esize := le.Uint64(h[72:]), le.Uint32(h[80:]), le.Uint32(h[84:])
		if count == 0 || count > maxParts || esize < 128 || esize > 1024 || start > uint64(size)/ss ||
			start*ss+uint64(count)*uint64(esize) > uint64(size) {
			continue
		}
		sr := io.NewSectionReader(r, 0, size)
		if _, err := sr.Seek(int64(ss), io.SeekStart); err != nil {
			continue
		}
		t, err := gpt.ReadTable(sr, ss)
		if err != nil {
			continue
		}
		var out []Part
		for i, p := range t.Partitions {
			if p.IsEmpty() || p.LastLBA < p.FirstLBA || p.LastLBA >= uint64(size)/ss {
				continue
			}
			out = append(out, Part{
				Index: i + 1, Start: p.FirstLBA * ss, Size: (p.LastLBA - p.FirstLBA + 1) * ss,
				Type: strings.ToLower(p.Type.String()), Name: p.Name(),
			})
		}
		return out, true
	}
	return nil, false
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
