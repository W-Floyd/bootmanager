package storage

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
)

// LUKS is the type of an encrypted container, the name blkid gives it.
// There is no file system to browse until it is unlocked.
const LUKS = "crypto_LUKS"

// SuperblockType names the file systems u-root's magic table lacks (f2fs,
// exFAT) and LUKS containers from the first 2 KiB of a device or image,
// "" for any other.
func SuperblockType(r io.ReaderAt) string {
	b := make([]byte, 2048)
	if n, _ := r.ReadAt(b, 0); n < len(b) {
		return ""
	}
	// A LUKS header (v1 or v2) opens the device: "LUKS", 0xBA 0xBE.
	if bytes.Equal(b[:6], []byte("LUKS\xba\xbe")) {
		return LUKS
	}
	// F2FS_SUPER_MAGIC 0xF2F52010, little-endian, at the second 1 KiB.
	if binary.LittleEndian.Uint32(b[1024:]) == 0xF2F52010 {
		return "f2fs"
	}
	// exFAT's boot sector: the name after the jump instruction.
	if bytes.Equal(b[3:11], []byte("EXFAT   ")) {
		return "exfat"
	}
	return ""
}

func superblockType(dev string) string {
	f, err := os.Open("/dev/" + dev)
	if err != nil {
		return ""
	}
	defer f.Close()
	return SuperblockType(f)
}
