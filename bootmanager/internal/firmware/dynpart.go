package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Android's dynamic partitions (liblp): a super partition holds a table of
// logical partitions, each a list of extents of the super partition. A
// partition of one extent can be mounted through a loop device with an
// offset, with no device-mapper. The layout, from the start of super:
//
//	4096 bytes reserved
//	primary geometry (4096), backup geometry (4096)
//	metadata slots, each metadata_max_size long, primary copy first
const (
	geometryOffset = 4096
	geometrySize   = 4096
	geometryMagic  = 0x616c4467 // "gDla"
	headerMagic    = 0x414c5030 // "0PLA"
	sectorSize     = 512

	targetLinear = 0
)

// Extent is a run of sectors on the super device.
type Extent struct{ Start, Sectors uint64 }

func (e Extent) End() uint64 { return e.Start + e.Sectors }

// geometry is LpMetadataGeometry, the part this needs.
type geometry struct {
	maxSize, slots uint32
}

func readGeometry(r io.ReaderAt) (geometry, error) {
	b := make([]byte, geometrySize)
	if _, err := r.ReadAt(b, geometryOffset); err != nil {
		return geometry{}, fmt.Errorf("geometry: %w", err)
	}
	if binary.LittleEndian.Uint32(b[0:]) != geometryMagic {
		return geometry{}, errors.New("not a dynamic-partition super (no geometry magic)")
	}
	size := binary.LittleEndian.Uint32(b[4:])
	if size < 52 || size > geometrySize {
		return geometry{}, fmt.Errorf("geometry: bad struct size %d", size)
	}
	if err := checkSum(b[:size], 8); err != nil {
		return geometry{}, fmt.Errorf("geometry: %w", err)
	}
	return geometry{
		maxSize: binary.LittleEndian.Uint32(b[40:]),
		slots:   binary.LittleEndian.Uint32(b[44:]),
	}, nil
}

// checkSum verifies a struct's SHA-256, kept in the 32 bytes at sumAt, over
// the struct with those bytes zeroed.
func checkSum(b []byte, sumAt int) error {
	want := append([]byte(nil), b[sumAt:sumAt+32]...)
	c := append([]byte(nil), b...)
	clear(c[sumAt : sumAt+32])
	got := sha256.Sum256(c)
	if !bytes.Equal(got[:], want) {
		return errors.New("checksum mismatch")
	}
	return nil
}

// table is one of the header's table descriptors.
type table struct{ offset, count, entrySize uint32 }

func readTable(h []byte, at int) table {
	return table{
		binary.LittleEndian.Uint32(h[at:]),
		binary.LittleEndian.Uint32(h[at+4:]),
		binary.LittleEndian.Uint32(h[at+8:]),
	}
}

// slot is one metadata copy, parsed.
type slot struct {
	tables             []byte
	parts, extents, bd table
}

func readSlot(r io.ReaderAt, g geometry, n uint32) (*slot, error) {
	buf := make([]byte, g.maxSize)
	off := int64(geometryOffset + 2*geometrySize + uint64(n)*uint64(g.maxSize))
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("metadata slot %d: %w", n, err)
	}
	if binary.LittleEndian.Uint32(buf[0:]) != headerMagic {
		return nil, fmt.Errorf("metadata slot %d: no header magic", n)
	}
	hsize := binary.LittleEndian.Uint32(buf[8:])
	if hsize < 128 || hsize > g.maxSize {
		return nil, fmt.Errorf("metadata slot %d: bad header size %d", n, hsize)
	}
	if err := checkSum(buf[:hsize], 12); err != nil {
		return nil, fmt.Errorf("metadata slot %d header: %w", n, err)
	}
	tsize := binary.LittleEndian.Uint32(buf[44:])
	if uint64(hsize)+uint64(tsize) > uint64(g.maxSize) {
		return nil, fmt.Errorf("metadata slot %d: tables overrun the slot", n)
	}
	tables := buf[hsize : hsize+tsize]
	sum := sha256.Sum256(tables)
	if !bytes.Equal(sum[:], buf[48:80]) {
		return nil, fmt.Errorf("metadata slot %d tables: checksum mismatch", n)
	}
	return &slot{
		tables:  tables,
		parts:   readTable(buf, 80),
		extents: readTable(buf, 92),
		bd:      readTable(buf, 116),
	}, nil
}

// entry is the i'th entry of t, or nil if it is not all within the tables.
func (s *slot) entry(t table, i uint32) []byte {
	if i >= t.count {
		return nil
	}
	start := uint64(t.offset) + uint64(i)*uint64(t.entrySize)
	end := start + uint64(t.entrySize)
	if end > uint64(len(s.tables)) {
		return nil
	}
	return s.tables[start:end]
}

// extentsOf is the named partition's extents in this slot, found or not.
func (s *slot) extentsOf(name string) ([]Extent, bool, error) {
	for i := uint32(0); i < s.parts.count; i++ {
		p := s.entry(s.parts, i)
		if p == nil || len(p) < 48 {
			return nil, false, errors.New("partition table overruns the metadata")
		}
		if cstr(p[:36]) != name {
			continue
		}
		first := binary.LittleEndian.Uint32(p[40:])
		num := binary.LittleEndian.Uint32(p[44:])
		var out []Extent
		for j := uint32(0); j < num; j++ {
			e := s.entry(s.extents, first+j)
			if e == nil || len(e) < 24 {
				return nil, true, errors.New("extent table overruns the metadata")
			}
			if t := binary.LittleEndian.Uint32(e[8:]); t != targetLinear {
				return nil, true, fmt.Errorf("%s: extent %d is not linear (type %d)", name, j, t)
			}
			// Only the super device itself (block device 0) is supported.
			if src := binary.LittleEndian.Uint32(e[20:]); src != 0 {
				return nil, true, fmt.Errorf("%s: extent %d is on block device %d, not super", name, j, src)
			}
			out = append(out, Extent{
				Start:   binary.LittleEndian.Uint64(e[12:]),
				Sectors: binary.LittleEndian.Uint64(e[0:]),
			})
		}
		return out, true, nil
	}
	return nil, false, nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// PartitionExtents is where the named logical partition is on the super
// device r reads, from the first metadata slot that lists it, adjacent
// extents merged. A partition in several pieces comes back as several
// Extents.
func PartitionExtents(r io.ReaderAt, name string) ([]Extent, error) {
	g, err := readGeometry(r)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for n := uint32(0); n < g.slots; n++ {
		s, err := readSlot(r, g, n)
		if err != nil {
			lastErr = err
			continue
		}
		ex, found, err := s.extentsOf(name)
		if err != nil {
			return nil, err
		}
		if found {
			return mergeExtents(ex), nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no logical partition %q", name)
}

// mergeExtents joins extents that follow one another on the device.
func mergeExtents(in []Extent) []Extent {
	var out []Extent
	for _, e := range in {
		if e.Sectors == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End() == e.Start {
			out[n-1].Sectors += e.Sectors
			continue
		}
		out = append(out, e)
	}
	return out
}
