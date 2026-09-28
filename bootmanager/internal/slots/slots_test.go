package slots

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// memDisk holds a disk's first and last sectors, as dumped from a
// Moto G Play (2024)'s UFS: the GPTs and nothing between.
type memDisk struct {
	name       string
	bs, blocks int64
	head, tail []byte
	failWrites bool
}

func (d *memDisk) region(off int64, n int) ([]byte, error) {
	tailOff := d.blocks*d.bs - int64(len(d.tail))
	switch {
	case off >= 0 && off+int64(n) <= int64(len(d.head)):
		return d.head[off : off+int64(n)], nil
	case off >= tailOff && off+int64(n) <= d.blocks*d.bs:
		return d.tail[off-tailOff : off-tailOff+int64(n)], nil
	}
	return nil, fmt.Errorf("%s: %d bytes at %d: not in the dump", d.name, n, off)
}

func (d *memDisk) ReadAt(p []byte, off int64) (int, error) {
	r, err := d.region(off, len(p))
	if err != nil {
		return 0, err
	}
	return copy(p, r), nil
}

func (d *memDisk) WriteAt(p []byte, off int64) (int, error) {
	if d.failWrites {
		return 0, errors.New("write failed")
	}
	r, err := d.region(off, len(p))
	if err != nil {
		return 0, err
	}
	return copy(r, p), nil
}

func (d *memDisk) Name() string     { return d.name }
func (d *memDisk) BlockSize() int64 { return 4096 }
func (d *memDisk) Blocks() int64    { return d.blocks }
func (d *memDisk) Sync() error      { return nil }
func (d *memDisk) Close() error     { return nil }

func fixture(t *testing.T, name string) *memDisk {
	t.Helper()
	head, err := os.ReadFile("testdata/" + name + "-head.bin")
	if err != nil {
		t.Fatal(err)
	}
	tail, err := os.ReadFile("testdata/" + name + "-tail.bin")
	if err != nil {
		t.Fatal(err)
	}
	return &memDisk{name: name, bs: 4096, blocks: 241664, head: head, tail: tail}
}

func testStore(t *testing.T) (*Store, *memDisk, *memDisk) {
	sdd, sdf := fixture(t, "sdd"), fixture(t, "sdf")
	return &Store{Booted: "a", Open: func(bool) ([]Disk, error) { return []Disk{sdd, sdf}, nil }}, sdd, sdf
}

// attrs reads a disk's partitions' attributes, both GPT copies checked.
func attrs(t *testing.T, d Disk) map[string]uint64 {
	t.Helper()
	tb, err := readTable(d)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]uint64{}
	for _, e := range tb.list() {
		m[e.name] = e.attr
	}
	return m
}

func TestList(t *testing.T) {
	s, _, _ := testStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Slot{
		{Name: "a", Booted: true, Active: true, Priority: 3, Retries: 3, Successful: true},
		{Name: "b"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("List:\n%+v\nwant\n%+v", got, want)
	}
}

func TestSetActive(t *testing.T) {
	s, sdd, sdf := testStore(t)
	if err := s.SetActive("b"); err != nil {
		t.Fatal(err)
	}
	a, b := attrs(t, sdd), attrs(t, sdf)
	// As ABL's set_active leaves them: b's boot at 0x3f, a's a priority
	// down and not active; bits 0-47 and 56-63 untouched.
	if b["boot_b"] != 0x103f000000000048 || a["boot_a"] != 0x105a000000000048 {
		t.Errorf("boot_a %016x, boot_b %016x", a["boot_a"], b["boot_b"])
	}
	for n, v := range a {
		if strings.HasSuffix(n, "_a") && v>>bitActive&1 != 0 {
			t.Errorf("%s still active: %016x", n, v)
		}
	}
	for n, v := range b {
		if strings.HasSuffix(n, "_b") && v>>bitActive&1 != 1 {
			t.Errorf("%s not active: %016x", n, v)
		}
	}
	if v := a["vendor_boot_a"]; v != 0x0040000000000048 {
		t.Errorf("vendor_boot_a %016x: more than the active bit changed", v)
	}

	if err := s.SetActive("a"); err != nil {
		t.Fatal(err)
	}
	a, b = attrs(t, sdd), attrs(t, sdf)
	if a["boot_a"] != 0x103f000000000048 || b["boot_b"] != 0x103a000000000048 {
		t.Errorf("back to a: boot_a %016x, boot_b %016x", a["boot_a"], b["boot_b"])
	}
}

func TestMark(t *testing.T) {
	s, sdd, _ := testStore(t)
	if err := s.MarkUnbootable("a"); err != nil {
		t.Fatal(err)
	}
	if v := attrs(t, sdd)["boot_a"]; v != 0x1098000000000048 {
		t.Errorf("unbootable: boot_a %016x", v)
	}
	if err := s.MarkSuccessful("a"); err != nil {
		t.Fatal(err)
	}
	if v := attrs(t, sdd)["boot_a"]; v != 0x1058000000000048 {
		t.Errorf("successful: boot_a %016x", v)
	}
	if err := s.MarkSuccessful("c"); err == nil {
		t.Error("slot c marked")
	}
}

func TestRefusesBadGPT(t *testing.T) {
	s, sdd, sdf := testStore(t)
	sdf.tail[len(sdf.tail)-4096+40] ^= 1 // the backup header's first usable LBA
	before := string(sdd.head) + string(sdd.tail)
	if err := s.SetActive("b"); !errors.Is(err, ErrNoSlots) || !strings.Contains(err.Error(), "sdf: backup GPT: header CRC") {
		t.Errorf("SetActive with a bad backup GPT: %v", err)
	}
	if string(sdd.head)+string(sdd.tail) != before {
		t.Error("sdd written although sdf's GPT is bad")
	}
}

func TestWriteFailureKeepsBootable(t *testing.T) {
	s, sdd, sdf := testStore(t)
	sdd.failWrites = true
	if err := s.SetActive("b"); err == nil {
		t.Fatal("SetActive succeeded with sdd unwritable")
	}
	// sdf, boot_b's, went first: b is bootable, and a still is.
	a, b := attrs(t, sdd), attrs(t, sdf)
	if b["boot_b"] != 0x103f000000000048 || a["boot_a"] != 0x105f000000000048 {
		t.Errorf("boot_a %016x, boot_b %016x", a["boot_a"], b["boot_b"])
	}
}
