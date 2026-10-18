package btrfs

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func item(obj, off uint64, typ uint32, data []byte) []byte {
	h := make([]byte, 32, 32+len(data))
	le := binary.LittleEndian
	le.PutUint64(h[8:], obj)
	le.PutUint64(h[16:], off)
	le.PutUint32(h[24:], typ)
	le.PutUint32(h[28:], uint32(len(data)))
	return append(h, data...)
}

func rootRef(dirid uint64, name string) []byte {
	b := make([]byte, 18, 18+len(name))
	binary.LittleEndian.PutUint64(b, dirid)
	binary.LittleEndian.PutUint16(b[16:], uint16(len(name)))
	return append(b, name...)
}

func TestStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(searchArgs{}); s != 4096 {
		t.Errorf("searchArgs %d bytes, the ioctl's is 4096", s)
	}
	if s := unsafe.Sizeof(inoLookupArgs{}); s != 4096 {
		t.Errorf("inoLookupArgs %d bytes, want 4096", s)
	}
	if ioctlNum(17) != 0xd0009411 || ioctlNum(18) != 0xd0009412 {
		t.Errorf("ioctl numbers %#x %#x", ioctlNum(17), ioctlNum(18))
	}
}

func TestParseResults(t *testing.T) {
	buf := append(item(256, 5, rootBackref, rootRef(256, "root")), item(257, 256, 1, []byte("x"))...)
	buf = append(buf, item(258, 256, rootBackref, rootRef(300, "home"))...)
	refs, obj, off, err := parseResults(buf, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != (backref{256, 5, 256, "root"}) || refs[1] != (backref{258, 256, 300, "home"}) {
		t.Errorf("refs %+v", refs)
	}
	if obj != 258 || off != 256 {
		t.Errorf("last %d %d", obj, off)
	}
	if _, _, _, err := parseResults(buf[:40], 3); err == nil {
		t.Error("a short result parsed")
	}
}

// Paths are the parent's, the directory the subvolume is in, its name.
func TestResolve(t *testing.T) {
	refs := []backref{
		{256, 5, 256, "@"},
		{257, 256, 77, "home"},
		{258, 5, 256, "snap"},
		{259, 999, 256, "orphan"},
	}
	dirs := map[[2]uint64]string{{256, 77}: "var/", {5, 256}: "", {999, 256}: ""}
	got, err := resolve(refs, func(tree, dirid uint64) (string, error) { return dirs[[2]uint64{tree, dirid}], nil })
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range got {
		paths = append(paths, s.Path)
	}
	want := []string{"@", "@/var/home", "snap"}
	if len(paths) != len(want) {
		t.Fatalf("%v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("%v, want %v", paths, want)
		}
	}
}
