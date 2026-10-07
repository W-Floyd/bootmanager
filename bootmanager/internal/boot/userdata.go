package boot

import (
	"log"
	"os"

	"github.com/u-root/u-root/pkg/mount/block"

	"bootmanager.local/bootmanager/internal/storage"
)

// userdataReplaced says whether the partition named userdata no longer holds
// Android's: stock Android cannot boot without its /data, and an install of
// something else (pmOS on userdata, the point of a UFS install) leaves it
// with a signature Android's never has.
func userdataReplaced(devs block.BlockDevices) bool {
	for _, d := range devs.FilterPartLabel("userdata") {
		kind := d.FSType
		if f, err := os.Open(d.DevicePath()); err == nil {
			if t := storage.SuperblockType(f); t != "" {
				kind = t
			}
			f.Close()
		}
		if foreignUserdata(kind) {
			log.Printf("android: userdata is %s, not Android's: no Android entry", kind)
			return true
		}
	}
	return false
}

// foreignUserdata: a file system type (or "crypto_LUKS") that Android's
// userdata is not. Android's is encrypted from its first byte (metadata
// encryption), so it reads as random bytes, with no signature at all; f2fs is
// what it is where that is off. A LUKS header, or any other file system, was
// put there by something else.
func foreignUserdata(kind string) bool { return kind != "" && kind != "f2fs" }
