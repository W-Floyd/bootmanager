package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// A synthetic super laid out the way fogona's is: three partitions of one
// linear extent each, the first at sector 2048, vendor_a last. The sizes and
// starts are made up (they imitate, not copy, a real phone's); nothing here
// is read from a device.
func TestPartitionExtentsFogonaShape(t *testing.T) {
	lin := func(sectors, start uint64) fakeExtent { return fakeExtent{sectors, start, targetLinear, 0} }
	b := fakeSuper(t, [][]fakePart{{
		{"product_a", []fakeExtent{lin(7494216, 2048)}},
		{"system_a", []fakeExtent{lin(1277088, 7497728)}},
		{"vendor_a", []fakeExtent{lin(1094152, 10194944)}},
	}})
	r := bytes.NewReader(b)
	for name, want := range map[string]Extent{
		"vendor_a":  {10194944, 1094152},
		"product_a": {2048, 7494216},
		"system_a":  {7497728, 1277088},
	} {
		got, err := PartitionExtents(r, name)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("%s: %v, %v; want [%v]", name, got, err, want)
		}
	}
	if _, err := PartitionExtents(r, "no_such_partition"); err == nil {
		t.Error("a partition that is not there was found")
	}
}

// fakePart and fakeExtent build a super image to test what the real one does
// not have: several extents, other slots, other block devices, damage.
type fakePart struct {
	name    string
	extents []fakeExtent
}
type fakeExtent struct {
	sectors, start uint64
	typ, source    uint32
}

func le32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func le64(b []byte, v uint64) { binary.LittleEndian.PutUint64(b, v) }

// fakeSuper lays out geometry and slots as liblp does. slots[i] are the
// partitions of metadata slot i.
func fakeSuper(t *testing.T, slots [][]fakePart) []byte {
	t.Helper()
	const maxSize = 4096
	img := make([]byte, geometryOffset+2*geometrySize+len(slots)*maxSize)

	g := img[geometryOffset : geometryOffset+geometrySize]
	le32(g[0:], geometryMagic)
	le32(g[4:], 52)
	le32(g[40:], maxSize)
	le32(g[44:], uint32(len(slots)))
	le32(g[48:], 4096)
	sum := sha256.Sum256(g[:52])
	copy(g[8:], sum[:])

	for n, parts := range slots {
		var ptab, etab []byte
		for _, p := range parts {
			e := make([]byte, 52)
			copy(e, p.name)
			le32(e[40:], uint32(len(etab)/24))
			le32(e[44:], uint32(len(p.extents)))
			ptab = append(ptab, e...)
			for _, x := range p.extents {
				xe := make([]byte, 24)
				le64(xe[0:], x.sectors)
				le32(xe[8:], x.typ)
				le64(xe[12:], x.start)
				le32(xe[20:], x.source)
				etab = append(etab, xe...)
			}
		}
		tables := append(append([]byte(nil), ptab...), etab...)
		h := make([]byte, 256)
		le32(h[0:], headerMagic)
		binary.LittleEndian.PutUint16(h[4:], 10)
		binary.LittleEndian.PutUint16(h[6:], 2)
		le32(h[8:], 256)
		le32(h[44:], uint32(len(tables)))
		tsum := sha256.Sum256(tables)
		copy(h[48:], tsum[:])
		// partitions at 0, extents after them, in the tables.
		le32(h[80:], 0)
		le32(h[84:], uint32(len(parts)))
		le32(h[88:], 52)
		le32(h[92:], uint32(len(ptab)))
		le32(h[96:], uint32(len(etab)/24))
		le32(h[100:], 24)
		hsum := sha256.Sum256(h)
		copy(h[12:], hsum[:])
		off := geometryOffset + 2*geometrySize + n*maxSize
		copy(img[off:], h)
		copy(img[off+256:], tables)
	}
	return img
}

func TestPartitionExtentsSynthetic(t *testing.T) {
	lin := func(sectors, start uint64) fakeExtent { return fakeExtent{sectors, start, targetLinear, 0} }
	img := fakeSuper(t, [][]fakePart{
		{
			{"one_a", []fakeExtent{lin(100, 2048)}},
			{"joined_a", []fakeExtent{lin(100, 4096), lin(50, 4196)}},
			{"split_a", []fakeExtent{lin(100, 8192), lin(50, 9000)}},
			{"zero_a", []fakeExtent{{100, 0, 1, 0}}},
			{"elsewhere_a", []fakeExtent{{100, 0, targetLinear, 1}}},
		},
		{
			{"only_in_slot_one_b", []fakeExtent{lin(10, 100)}},
		},
	})
	r := bytes.NewReader(img)
	for _, c := range []struct {
		name string
		want []Extent
	}{
		{"one_a", []Extent{{2048, 100}}},
		{"joined_a", []Extent{{4096, 150}}},
		{"split_a", []Extent{{8192, 100}, {9000, 50}}},
		{"only_in_slot_one_b", []Extent{{100, 10}}},
	} {
		got, err := PartitionExtents(r, c.name)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	for _, name := range []string{"zero_a", "elsewhere_a", "missing"} {
		if got, err := PartitionExtents(r, name); err == nil {
			t.Errorf("%s: no error, got %v", name, got)
		}
	}
}

func TestPartitionExtentsDamaged(t *testing.T) {
	good := fakeSuper(t, [][]fakePart{{{"one_a", []fakeExtent{{100, 2048, targetLinear, 0}}}}})
	if _, err := PartitionExtents(bytes.NewReader(good), "one_a"); err != nil {
		t.Fatalf("the undamaged image: %v", err)
	}
	mut := func(at int) []byte {
		b := slices.Clone(good)
		b[at] ^= 0xff
		return b
	}
	slot0 := geometryOffset + 2*geometrySize
	for name, img := range map[string][]byte{
		"geometry magic":    mut(geometryOffset),
		"geometry checksum": mut(geometryOffset + 44),
		"header magic":      mut(slot0),
		"header checksum":   mut(slot0 + 50),
		"tables":            mut(slot0 + 256 + 3),
		"empty":             nil,
	} {
		if got, err := PartitionExtents(bytes.NewReader(img), "one_a"); err == nil {
			t.Errorf("%s: accepted, got %v", name, got)
		}
	}
	if err := checkSum(make([]byte, 40), 8); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a zero struct passed its checksum: %v", err)
	}
}
