package initrd

import (
	"bytes"
	"io"
	"testing"

	"github.com/u-root/u-root/pkg/cpio"
)

// The archive holds the files and nothing else. A directory record over a
// path the initramfs has as a symlink (pmOS's /lib) makes the kernel remove
// the symlink and make a directory: the loader goes, and the boot panics
// with no working init.
func TestBuild(t *testing.T) {
	fw := bytes.Repeat([]byte{0xde, 0xad}, 100)
	b, err := Build([]File{
		{Path: "lib/firmware/chipone_firmware.bin", Mode: 0o644, Data: fw},
		{Path: "/bman-unlock.key", Mode: 0o400, Data: []byte("fogona")},
	})
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
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
		if x.Info.Mode&cpio.S_IFMT != cpio.S_IFREG {
			t.Errorf("record %q is not a regular file (mode %o): an archive of files only", x.Info.Name, x.Info.Mode)
		}
		data, _ := io.ReadAll(io.NewSectionReader(x.ReaderAt, 0, int64(x.Info.FileSize)))
		got[x.Info.Name] = rec{x.Info.Mode & 0o777, data}
		order = append(order, x.Info.Name)
	}
	want := []string{"lib/firmware/chipone_firmware.bin", "bman-unlock.key"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("records %v, want %v", order, want)
	}
	if f := got["lib/firmware/chipone_firmware.bin"]; f.mode != 0o644 || !bytes.Equal(f.data, fw) {
		t.Errorf("firmware record: mode %o, %d bytes", f.mode, len(f.data))
	}
	if f := got["bman-unlock.key"]; f.mode != 0o400 || string(f.data) != "fogona" {
		t.Errorf("key record: mode %o data %q (a leading slash is dropped)", f.mode, f.data)
	}
}
