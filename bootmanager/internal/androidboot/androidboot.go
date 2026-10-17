// Package androidboot reads Android boot images, header versions 0 to 4
// (AOSP's bootimg.h), without caring which device or bootloader they are
// for: the kernel, the ramdisk, the device tree when the image carries one,
// and the command line.
//
// Versions 0, 1 and 2 share a header that starts with the kernel's and
// ramdisk's load addresses and the page size, and lay their parts out in
// whole pages after it: kernel, ramdisk, second stage, then (v1) the recovery
// DTBO and (v2) the DTB. Versions 3 and 4 have a fixed 4096-byte page and no
// addresses; the ramdisk there is only the generic part (a bootloader adds
// vendor_boot's), and v4 may end in a signature.
//
// Old Qualcomm bootloaders put the size of a "QCDT" device tree table, which
// they append after the second stage, where later versions have header_version.
// A value above 4 there is read as that size, and the image as version 0.
package androidboot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Magic starts every boot image.
const Magic = "ANDROID!"

const (
	// v0Size is the size of the version 0 header; v1 and v2 extend it.
	v0Size = 1632
	// v3PageSize is the page of version 3 and 4 images.
	v3PageSize = 4096
	// maxPage bounds a header's page size, which is a power of two from 2048.
	maxPage = 1 << 20
)

// Header is an image's header, with where each part is.
type Header struct {
	// Version is 0 to 4.
	Version int
	// PageSize is what the parts are aligned to.
	PageSize uint32

	KernelSize, RamdiskSize, SecondSize uint32
	// QCDTSize is the size of a Qualcomm device tree table after the second
	// stage, in a version 0 image from a bootloader that has one.
	QCDTSize uint32
	// RecoveryDTBOSize is version 1's; DTBSize is version 2's.
	RecoveryDTBOSize, DTBSize uint32
	// SignatureSize is version 4's.
	SignatureSize uint32

	// The addresses the bootloader loads the parts at (versions 0 to 2).
	// A kexec from a running kernel ignores them.
	KernelAddr, RamdiskAddr, SecondAddr, TagsAddr uint32
	DTBAddr                                       uint64

	// OSVersion is "A.B.C" and OSPatch "YYYY-MM", "" when the image has none.
	OSVersion, OSPatch string
	// Name is the product name of versions 0 to 2.
	Name string
	// Cmdline is the whole command line: the header's, and in versions 1
	// and 2 the extra part after it.
	Cmdline string

	// Offsets of the parts from the start of the image; a size of 0 means
	// the part is absent.
	KernelOff, RamdiskOff, SecondOff, QCDTOff, RecoveryDTBOOff, DTBOff int64
}

// Image is a parsed boot image and what to read the parts from.
type Image struct {
	Header
	r io.ReaderAt
}

// Parse reads the header of the boot image in r.
func Parse(r io.ReaderAt) (*Image, error) {
	b := make([]byte, v0Size+16+8+4+4)
	n, err := r.ReadAt(b, 0)
	if n < v0Size && err != nil {
		return nil, fmt.Errorf("androidboot: %w", err)
	}
	b = b[:n]
	if len(b) < v0Size || string(b[:8]) != Magic {
		return nil, errors.New("androidboot: not an Android boot image")
	}
	le := binary.LittleEndian
	ver := le.Uint32(b[40:])
	h := Header{}
	switch {
	case ver == 3 || ver == 4:
		h.Version = int(ver)
		h.PageSize = v3PageSize
		h.KernelSize, h.RamdiskSize = le.Uint32(b[8:]), le.Uint32(b[12:])
		h.setOS(le.Uint32(b[16:]))
		h.Cmdline = cstring(b[44 : 44+1536])
		if ver == 4 {
			if len(b) < 1584 {
				return nil, errors.New("androidboot: truncated version 4 header")
			}
			h.SignatureSize = le.Uint32(b[1580:])
		}
		h.KernelOff = v3PageSize
		h.RamdiskOff = h.KernelOff + align(int64(h.KernelSize), v3PageSize)
	default:
		h.Version = int(ver)
		if ver > 2 { // header_version is really the QCDT size
			h.QCDTSize = ver
			h.Version = 0
		}
		h.KernelSize, h.KernelAddr = le.Uint32(b[8:]), le.Uint32(b[12:])
		h.RamdiskSize, h.RamdiskAddr = le.Uint32(b[16:]), le.Uint32(b[20:])
		h.SecondSize, h.SecondAddr = le.Uint32(b[24:]), le.Uint32(b[28:])
		h.TagsAddr = le.Uint32(b[32:])
		h.PageSize = le.Uint32(b[36:])
		if h.PageSize < 512 || h.PageSize > maxPage || h.PageSize&(h.PageSize-1) != 0 {
			return nil, fmt.Errorf("androidboot: page size %d", h.PageSize)
		}
		h.setOS(le.Uint32(b[44:]))
		h.Name = cstring(b[48:64])
		// extra_cmdline continues the line, with nothing between the two.
		h.Cmdline = strings.TrimSpace(cbytes(b[64:576]) + cbytes(b[608:1632]))
		pg := int64(h.PageSize)
		h.KernelOff = pg
		h.RamdiskOff = h.KernelOff + align(int64(h.KernelSize), pg)
		h.SecondOff = h.RamdiskOff + align(int64(h.RamdiskSize), pg)
		next := h.SecondOff + align(int64(h.SecondSize), pg)
		if h.QCDTSize > 0 {
			h.QCDTOff = next
		}
		if h.Version >= 1 {
			if len(b) < 1648 {
				return nil, errors.New("androidboot: truncated version 1 header")
			}
			h.RecoveryDTBOSize = le.Uint32(b[1632:])
			h.RecoveryDTBOOff = int64(le.Uint64(b[1636:]))
			next = max(next, h.RecoveryDTBOOff+align(int64(h.RecoveryDTBOSize), pg))
			if h.RecoveryDTBOSize == 0 {
				h.RecoveryDTBOOff = 0
			}
		}
		if h.Version >= 2 {
			if len(b) < 1660 {
				return nil, errors.New("androidboot: truncated version 2 header")
			}
			h.DTBSize = le.Uint32(b[1648:])
			h.DTBAddr = le.Uint64(b[1652:])
			h.DTBOff = next
		}
	}
	if h.KernelSize == 0 {
		return nil, errors.New("androidboot: the image has no kernel")
	}
	return &Image{Header: h, r: r}, nil
}

func (h *Header) setOS(v uint32) {
	if v == 0 {
		return
	}
	// The version is 7 bits each of A, B and C; the patch level 7 bits of
	// year since 2000 and 4 of month.
	ver, patch := v>>11, v&0x7ff
	h.OSVersion = fmt.Sprintf("%d.%d.%d", ver>>14&0x7f, ver>>7&0x7f, ver&0x7f)
	h.OSPatch = fmt.Sprintf("%d-%02d", 2000+patch>>4, patch&0xf)
}

func align(n, pg int64) int64 { return (n + pg - 1) / pg * pg }

// cbytes is the text before the first NUL.
func cbytes(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func cstring(b []byte) string { return strings.TrimSpace(cbytes(b)) }

func (m *Image) part(off int64, size uint32) *io.SectionReader {
	if size == 0 || off == 0 {
		return nil
	}
	return io.NewSectionReader(m.r, off, int64(size))
}

// Kernel is the kernel, the zImage or Image the bootloader would jump to.
func (m *Image) Kernel() *io.SectionReader { return m.part(m.KernelOff, m.KernelSize) }

// Ramdisk is the ramdisk, nil when there is none. In versions 3 and 4 it is
// the generic ramdisk only.
func (m *Image) Ramdisk() *io.SectionReader { return m.part(m.RamdiskOff, m.RamdiskSize) }

// DTB is the device tree of a version 2 image, nil in the others (the
// bootloader or a separate partition has it there).
func (m *Image) DTB() *io.SectionReader { return m.part(m.DTBOff, m.DTBSize) }

// QCDT is the device tree table of an old Qualcomm image, nil without one.
func (m *Image) QCDT() *io.SectionReader { return m.part(m.QCDTOff, m.QCDTSize) }

// Label names the image for a menu: its product name and Android version
// when it has them.
func (m *Image) Label() string {
	var parts []string
	if m.Name != "" {
		parts = append(parts, m.Name)
	}
	if m.OSVersion != "" {
		parts = append(parts, "Android "+m.OSVersion)
	}
	return strings.Join(parts, " ")
}

// Check reads the parts' ends to see the image is not truncated: a partition
// shorter than its header says, say.
func (m *Image) Check() error {
	for _, p := range []struct {
		name string
		off  int64
		size uint32
	}{
		{"kernel", m.KernelOff, m.KernelSize},
		{"ramdisk", m.RamdiskOff, m.RamdiskSize},
		{"dtb", m.DTBOff, m.DTBSize},
	} {
		if p.size == 0 {
			continue
		}
		var one [1]byte
		if _, err := m.r.ReadAt(one[:], p.off+int64(p.size)-1); err != nil {
			return fmt.Errorf("androidboot: %s runs past the end of the image: %w", p.name, err)
		}
	}
	return nil
}
