package storage

import (
	"bytes"
	"testing"
)

func TestSuperblockType(t *testing.T) {
	f2fs := make([]byte, 4096)
	copy(f2fs[1024:], []byte{0x10, 0x20, 0xF5, 0xF2})
	exfat := make([]byte, 4096)
	copy(exfat[3:], "EXFAT   ")
	luks := make([]byte, 4096)
	copy(luks, "LUKS\xba\xbe\x00\x02")
	for name, c := range map[string]struct {
		img  []byte
		want string
	}{
		"f2fs":  {f2fs, "f2fs"},
		"exfat": {exfat, "exfat"},
		"luks":  {luks, LUKS},
		"zeros": {make([]byte, 4096), ""},
		"short": {make([]byte, 100), ""},
	} {
		if got := SuperblockType(bytes.NewReader(c.img)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
