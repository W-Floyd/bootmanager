// Package initrd builds the extra initramfs archive the stage appends to a
// Linux entry's own: the kernel unpacks an initramfs archive after archive,
// so a few files (a secret, a firmware blob) reach the booted system
// without a command line or a file on disk.
package initrd

import (
	"bytes"
	"strings"

	"github.com/u-root/u-root/pkg/cpio"
)

// File is one file of the archive: Path from the root without a leading
// slash ("lib/firmware/x.bin"), Mode its permission bits.
type File struct {
	Path string
	Mode uint64
	Data []byte
}

// Build is a newc cpio archive of the files, and nothing else: no record for
// the directories above them. The directories must be in the initramfs
// already, and an archive that names one is worse than one that does not: the
// kernel, unpacking a record over a path that exists with another type,
// removes what is there first. pmOS's initramfs has /lib as a symlink, so a
// "lib" directory record swapped it for an empty directory, took the dynamic
// loader with it, and the kernel found no working init.
func Build(files []File) ([]byte, error) {
	var buf bytes.Buffer
	w := cpio.Newc.Writer(&buf)
	for _, f := range files {
		rec := cpio.StaticRecord(f.Data, cpio.Info{Name: strings.TrimPrefix(f.Path, "/"), Mode: cpio.S_IFREG | f.Mode})
		if err := w.WriteRecord(rec); err != nil {
			return nil, err
		}
	}
	if err := cpio.WriteTrailer(w); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
