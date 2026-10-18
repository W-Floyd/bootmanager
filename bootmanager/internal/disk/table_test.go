package disk

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
	"unicode/utf16"
)

func gptImage(ss int, parts [][3]uint64, names []string) []byte {
	le := binary.LittleEndian
	img := make([]byte, 1<<20)
	h := img[ss:]
	copy(h, "EFI PART")
	le.PutUint32(h[12:], 92)
	le.PutUint64(h[72:], 2)
	le.PutUint32(h[80:], 4)
	le.PutUint32(h[8:], 0x10000)
	le.PutUint32(h[84:], 128)
	for i, p := range parts {
		e := img[2*ss+128*i:]
		// type GUID 0FC63DAF-8483-4772-8E79-3D69D8477DE4 (Linux), mixed endian
		copy(e, []byte{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4})
		le.PutUint64(e[32:], p[0])
		le.PutUint64(e[40:], p[1])
		for j, c := range utf16.Encode([]rune(names[i])) {
			le.PutUint16(e[56+2*j:], c)
		}
	}
	le.PutUint32(h[88:], crc32.ChecksumIEEE(img[2*ss:2*ss+4*128]))
	le.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
	return img
}

func TestGPT(t *testing.T) {
	for _, ss := range []int{512} {
		img := gptImage(ss, [][3]uint64{{34, 99, 0}, {100, 199, 0}}, []string{"boot", "root"})
		ps, err := ParseTable(bytes.NewReader(img), int64(len(img)))
		if err != nil || len(ps) != 2 {
			t.Fatalf("ss %d: %v %v", ss, ps, err)
		}
		s := uint64(ss)
		if p := ps[1]; p.Index != 2 || p.Start != 100*s || p.Size != 100*s || p.Name != "root" || p.Type != "0fc63daf-8483-4772-8e79-3d69d8477de4" {
			t.Errorf("ss %d: %+v", ss, p)
		}
	}
	// A bad header CRC is not a table.
	img := gptImage(512, [][3]uint64{{34, 99, 0}}, []string{"x"})
	img[512+20] ^= 1
	if _, err := ParseTable(bytes.NewReader(img), int64(len(img))); err == nil {
		t.Error("corrupt header accepted")
	}
	// A partition past the end of the file is left out.
	img = gptImage(512, [][3]uint64{{34, 99, 0}, {100, 1 << 30, 0}}, []string{"a", "b"})
	if ps, _ := ParseTable(bytes.NewReader(img), int64(len(img))); len(ps) != 1 {
		t.Errorf("%+v", ps)
	}
}

func mbrEntry(b []byte, boot, typ byte, start, n uint32) {
	b[0], b[4] = boot, typ
	binary.LittleEndian.PutUint32(b[8:], start)
	binary.LittleEndian.PutUint32(b[12:], n)
}

func TestMBR(t *testing.T) {
	img := make([]byte, 8<<20)
	img[510], img[511] = 0x55, 0xaa
	mbrEntry(img[446:], 0x80, 0x0c, 2048, 100)
	mbrEntry(img[462:], 0, 0x83, 4096, 200)
	mbrEntry(img[478:], 0, 0x0f, 8192, 400) // extended
	// Two logical partitions: EBRs at 8192 and 8192+500, in sectors.
	ebr := img[8192*512:]
	ebr[510], ebr[511] = 0x55, 0xaa
	mbrEntry(ebr[446:], 0, 0x83, 8, 50)
	mbrEntry(ebr[462:], 0, 0x05, 100, 100)
	ebr2 := img[(8192+100)*512:]
	ebr2[510], ebr2[511] = 0x55, 0xaa
	mbrEntry(ebr2[446:], 0, 0x82, 8, 20)
	ps, err := ParseTable(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{1, 2048 * 512, 100 * 512, "0x0c", ""},
		{2, 4096 * 512, 200 * 512, "0x83", ""},
		{5, (8192 + 8) * 512, 50 * 512, "0x83", ""},
		{6, (8192 + 100 + 8) * 512, 20 * 512, "0x82", ""},
	}
	if len(ps) != len(want) {
		t.Fatalf("%+v", ps)
	}
	for i := range want {
		if ps[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, ps[i], want[i])
		}
	}
}

func TestNotATable(t *testing.T) {
	fat := make([]byte, 1<<20)
	fat[510], fat[511] = 0x55, 0xaa
	copy(fat[54:], "FAT16   ")
	mbrEntry(fat[446:], 0, 0x83, 1, 1) // what a boot sector's code looks like
	for name, b := range map[string][]byte{
		"zeros": make([]byte, 4096),
		"fat":   fat,
		"short": []byte("x"),
	} {
		if _, err := ParseTable(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("%s: a table", name)
		}
	}
	// Garbage in the status byte.
	junk := make([]byte, 1<<20)
	junk[510], junk[511] = 0x55, 0xaa
	junk[446] = 0x41
	if _, err := ParseTable(bytes.NewReader(junk), int64(len(junk))); err == nil {
		t.Error("junk status accepted")
	}
}
