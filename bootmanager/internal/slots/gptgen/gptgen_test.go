package gptgen

import (
	"bytes"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func checkHeader(t *testing.T, im Image, h []byte, lba, alt int64) []byte {
	t.Helper()
	if string(h[:8]) != "EFI PART" {
		t.Fatal("no signature")
	}
	c := bytes.Clone(h[:92])
	le.PutUint32(c[16:], 0)
	if crc32.ChecksumIEEE(c) != le.Uint32(h[16:]) {
		t.Error("header CRC")
	}
	if int64(le.Uint64(h[24:])) != lba || int64(le.Uint64(h[32:])) != alt {
		t.Errorf("header at %d, alternate %d", le.Uint64(h[24:]), le.Uint64(h[32:]))
	}
	if le.Uint32(h[80:]) != 128 || le.Uint32(h[84:]) != 128 {
		t.Errorf("%d entries of %d bytes", le.Uint32(h[80:]), le.Uint32(h[84:]))
	}
	return h
}

func TestImages(t *testing.T) {
	for _, bs := range []int64{512, 4096} {
		ims, err := FogonaImages(bs)
		if err != nil {
			t.Fatal(err)
		}
		if len(ims) != 6 {
			t.Fatalf("%d disks", len(ims))
		}
		for _, im := range ims {
			if int64(len(im.Head))%bs != 0 || int64(len(im.Tail))%bs != 0 {
				t.Errorf("%s: not whole blocks", im.Name)
			}
			if im.Head[510] != 0x55 || im.Head[511] != 0xaa || im.Head[446+4] != 0xee {
				t.Errorf("%s: protective MBR", im.Name)
			}
			ph := checkHeader(t, im, im.Head[bs:bs+92], 1, im.Blocks-1)
			bh := checkHeader(t, im, im.Tail[int64(len(im.Tail))-bs:][:92], im.Blocks-1, 1)
			if le.Uint64(ph[72:]) != 2 || int64(le.Uint64(bh[72:])) != im.Blocks-1-int64(len(im.Tail))/bs+1 {
				t.Errorf("%s: entries at %d and %d", im.Name, le.Uint64(ph[72:]), le.Uint64(bh[72:]))
			}
			pe := im.Head[2*bs : 2*bs+128*128]
			be := im.Tail[:128*128]
			if !bytes.Equal(pe, be) || crc32.ChecksumIEEE(pe) != le.Uint32(ph[88:]) || le.Uint32(ph[88:]) != le.Uint32(bh[88:]) {
				t.Errorf("%s: entries or their CRC differ", im.Name)
			}
		}
		// Deterministic.
		again, _ := FogonaImages(bs)
		if !bytes.Equal(ims[2].Head, again[2].Head) || !bytes.Equal(ims[2].Tail, again[2].Tail) {
			t.Error("not deterministic")
		}
	}
}

func TestWriteFile(t *testing.T) {
	im, err := Disk{Name: "x", BlockSize: 4096, Blocks: 1 << 13, Parts: []Part{{"boot_a", 1}}}.Build()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x.img")
	if err := im.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() != im.Blocks*4096 {
		t.Fatalf("%v %v", st, err)
	}
	b, _ := os.ReadFile(p)
	if !bytes.HasPrefix(b, im.Head) || !bytes.HasSuffix(b, im.Tail) {
		t.Error("file differs from the image")
	}
}

func TestBuildErrors(t *testing.T) {
	d := Disk{Name: "x", BlockSize: 4096, Blocks: 1 << 16, Parts: []Part{{"a", 0}}}
	if _, err := d.Build(); err != nil {
		t.Fatal(err)
	}
	d.BlockSize = 1000
	if _, err := d.Build(); err == nil {
		t.Error("block size 1000")
	}
	d.BlockSize = 4096
	d.Parts = []Part{{string(make([]rune, 37)), 0}}
	d.Parts[0].Name = "0123456789012345678901234567890123456"
	if _, err := d.Build(); err == nil {
		t.Error("37-unit name")
	}
}
