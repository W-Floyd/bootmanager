package initrd

import (
	"bytes"
	"io"
	"testing"

	"github.com/u-root/u-root/pkg/cpio"
)

func TestBuild(t *testing.T) {
	fw := bytes.Repeat([]byte{0xde, 0xad}, 100)
	b, err := Build([]File{
		{Path: "lib/firmware/chipone_firmware.bin", Mode: 0o644, Data: fw},
		{Path: "bman-unlock.key", Mode: 0o400, Data: []byte("fogona")},
		{Path: "lib/firmware/other.bin", Mode: 0o644, Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
		dir  bool
		mode uint64
		data []byte
	}
	got := map[string]rec{}
	var order []string
	r := cpio.Newc.Reader(bytes.NewReader(b))
	for {
		x, err := r.ReadRecord()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(io.NewSectionReader(x.ReaderAt, 0, int64(x.Info.FileSize)))
		got[x.Info.Name] = rec{x.Info.Mode&cpio.S_IFMT == cpio.S_IFDIR, x.Info.Mode & 0o777, data}
		order = append(order, x.Info.Name)
	}
	want := []string{"lib", "lib/firmware", "lib/firmware/chipone_firmware.bin", "bman-unlock.key", "lib/firmware/other.bin"}
	if len(order) != len(want) {
		t.Fatalf("records %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("record %d is %q, want %q", i, order[i], want[i])
		}
	}
	if !got["lib"].dir || !got["lib/firmware"].dir || got["lib/firmware"].mode != 0o755 {
		t.Errorf("parents: %+v %+v", got["lib"], got["lib/firmware"])
	}
	if f := got["lib/firmware/chipone_firmware.bin"]; f.dir || f.mode != 0o644 || !bytes.Equal(f.data, fw) {
		t.Errorf("firmware record wrong: dir %v mode %o, %d bytes", f.dir, f.mode, len(f.data))
	}
	if f := got["bman-unlock.key"]; f.mode != 0o400 || string(f.data) != "fogona" {
		t.Errorf("key record: mode %o data %q", f.mode, f.data)
	}
}
