package boot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/dt"
	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
)

// Android entries: stock Android, booted with what its bootloader would
// hand it. A Qualcomm ABL builds, for the Android it would boot, a device
// tree (its vendor_boot DTB with dtbo's overlays, plus its own /memory,
// /reserved-memory and /chosen, command line included) and an initramfs
// (vendor_boot's and init_boot's ramdisks and a bootconfig trailer), and it
// hands both to whatever it boots instead. A stage that keeps them
// (Config.BootloaderDir: fdt and initrd) has all of it but the kernel,
// which comes from an Android boot image (header v3 or v4): a partition of
// its own, read raw -- by default the other A/B slot's boot partition
// (otherSlotBoot), or those Config.AndroidPartitions names -- or a file at
// AndroidImage on a scanned partition.

// AndroidImage is where on a partition an Android boot image is looked for.
const AndroidImage = "android/boot.img"

// androidEntries returns an entry per Android boot image found, or none
// when the stage kept no bootloader tree.
func androidEntries(mounts []*mount.MountPoint, devs block.BlockDevices, partitions []string, bootloaderDir string) []boot.OSImage {
	if bootloaderDir == "" || userdataReplaced(devs) {
		return nil
	}
	fdtBytes, err := os.ReadFile(filepath.Join(bootloaderDir, "fdt"))
	if err != nil {
		return nil
	}
	initrd := filepath.Join(bootloaderDir, "initrd")
	if _, err := os.Stat(initrd); err != nil {
		return nil
	}
	bootargs, err := chosenBootargs(fdtBytes)
	if err != nil {
		return nil
	}
	if partitions == nil {
		if p := otherSlotBoot(initrd); p != "" {
			partitions = []string{p}
		}
	}
	found := func(paths []string) []boot.OSImage {
		var imgs []boot.OSImage
		for _, path := range paths {
			img, err := androidImage(path, bytes.NewReader(fdtBytes), initrd, bootargs)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					log.Printf("%s: %v", path, err)
				}
				continue
			}
			imgs = append(imgs, img)
		}
		return imgs
	}
	var parts []string
	for _, label := range partitions {
		for _, d := range devs.FilterPartLabel(label) {
			parts = append(parts, d.DevicePath())
		}
	}
	// A boot image in a partition wins over a file: it is the same
	// Android, and the one that needs no removable card.
	if imgs := found(parts); len(imgs) > 0 {
		return imgs
	}
	var files []string
	for _, mp := range mounts {
		files = append(files, filepath.Join(mp.Path, AndroidImage))
	}
	return found(files)
}

// otherSlotBoot is the boot partition of the A/B slot the bootloader did
// not boot, from androidboot.slot_suffix in the bootconfig at the end of
// its initramfs, or "" without one. The stage is in the booted slot's
// boot partition, so Android's kernel can only be in the other; the rest
// of that Android (vendor_boot, init_boot and dtbo, which the bootloader
// loaded, and system and vendor, which the bootconfig names) is the booted
// slot's.
func otherSlotBoot(initrd string) string {
	switch bootconfig(initrd)["androidboot.slot_suffix"] {
	case "_a":
		return "boot_b"
	case "_b":
		return "boot_a"
	}
	return ""
}

// Bootconfig is the bootconfig of the initramfs the bootloader handed
// the stage (in bootloaderDir), nil without one: androidboot.slot_suffix,
// androidboot.serialno, ...
func Bootconfig(bootloaderDir string) map[string]string {
	return bootconfig(filepath.Join(bootloaderDir, "initrd"))
}

// bootconfig reads the bootconfig trailer at the end of an initramfs
// (Documentation/admin-guide/bootconfig.rst: the text, its size and
// checksum as little-endian u32s, "#BOOTCONFIG\n") as key-value pairs,
// flat keys only, which is all a bootloader writes.
func bootconfig(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 20 {
		return nil
	}
	tail := make([]byte, 20)
	if _, err := f.ReadAt(tail, st.Size()-20); err != nil || string(tail[8:]) != "#BOOTCONFIG\n" {
		return nil
	}
	size := int64(binary.LittleEndian.Uint32(tail))
	if size > st.Size()-20 || size > 32<<10 {
		return nil
	}
	text := make([]byte, size)
	if _, err := f.ReadAt(text, st.Size()-20-size); err != nil {
		return nil
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(bytes.TrimRight(text, "\x00")), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return kv
}

func chosenBootargs(fdtBytes []byte) (string, error) {
	fdt, err := dt.ReadFDT(bytes.NewReader(fdtBytes))
	if err != nil {
		return "", err
	}
	chosen, ok := fdt.NodeByName("chosen")
	if !ok {
		return "", errors.New("no /chosen")
	}
	p, ok := chosen.LookProperty("bootargs")
	if !ok {
		return "", nil
	}
	return strings.TrimRight(string(p.Value), "\x00"), nil
}

// androidImage is the entry for the boot image at path: its kernel, the
// bootloader's tree and initramfs, and the bootloader's command line with
// the image's own after it (as ABL joins vendor_boot's and boot's).
func androidImage(path string, fdt io.ReaderAt, initrd, bootargs string) (*boot.LinuxImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	h, err := readAndroidHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	rd, err := os.Open(initrd)
	if err != nil {
		f.Close()
		return nil, err
	}
	cmdline := bootargs
	if h.cmdline != "" {
		cmdline = strings.TrimSpace(cmdline + " " + h.cmdline)
	}
	return &boot.LinuxImage{
		Name:    h.label(),
		Kernel:  io.NewSectionReader(f, androidPageSize, int64(h.kernelSize)),
		Initrd:  rd,
		DTB:     fdt,
		Cmdline: cmdline,
	}, nil
}

// The v3 and v4 boot image header (AOSP's bootimg.h). Both have a fixed
// 4096-byte page, and the kernel is the first thing after the header page.
const androidPageSize = 4096

type androidHeader struct {
	kernelSize uint32
	osVersion  uint32
	cmdline    string
}

func readAndroidHeader(r io.ReaderAt) (*androidHeader, error) {
	b := make([]byte, 44+1536)
	if _, err := r.ReadAt(b, 0); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if string(b[:8]) != "ANDROID!" {
		return nil, errors.New("not an Android boot image")
	}
	le := binary.LittleEndian
	h := &androidHeader{kernelSize: le.Uint32(b[8:]), osVersion: le.Uint32(b[16:])}
	if v := le.Uint32(b[40:]); v != 3 && v != 4 {
		return nil, fmt.Errorf("boot image header v%d, want v3 or v4", v)
	}
	if h.kernelSize == 0 {
		return nil, errors.New("no kernel in the boot image")
	}
	h.cmdline = strings.TrimSpace(string(bytes.TrimRight(b[44:], "\x00")))
	return h, nil
}

// label is "Android 14 (2025-02)" from os_version: A.B.C in bits 31-11
// (7 bits each), the security patch level's year-2000 and month below.
func (h *androidHeader) label() string {
	v, p := h.osVersion>>11, h.osVersion&0x7ff
	a, b, c := v>>14&0x7f, v>>7&0x7f, v&0x7f
	if a == 0 {
		return "Android"
	}
	s := fmt.Sprintf("Android %d", a)
	if b != 0 || c != 0 {
		s += fmt.Sprintf(".%d", b)
		if c != 0 {
			s += fmt.Sprintf(".%d", c)
		}
	}
	if p != 0 {
		s += fmt.Sprintf(" (%d-%02d)", 2000+p>>4, p&0xf)
	}
	return s
}
