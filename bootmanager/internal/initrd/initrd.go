// Package initrd builds the extra initramfs archive the stage appends to a
// Linux entry's own: the kernel unpacks an initramfs archive after archive,
// so a few files (a secret, a firmware blob) reach the booted system
// without a command line or a file on disk.
package initrd

import (
	"bytes"
	"path"
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

// Build is a newc cpio archive of the files, with a record for each
// directory above them (a file's parents must exist when it is unpacked; one
// that already does, even as a link to another, is left as it is).
func Build(files []File) ([]byte, error) {
	var buf bytes.Buffer
	w := cpio.Newc.Writer(&buf)
	made := map[string]bool{}
	for _, f := range files {
		var dirs []string
		for d := path.Dir(f.Path); d != "." && d != "/" && !made[d]; d = path.Dir(d) {
			dirs = append(dirs, d)
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			made[dirs[i]] = true
			if err := w.WriteRecord(cpio.Directory(dirs[i], 0o755)); err != nil {
				return nil, err
			}
		}
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
