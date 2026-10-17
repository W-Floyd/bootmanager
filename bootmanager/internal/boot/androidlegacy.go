package boot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/dt"
	"github.com/u-root/u-root/pkg/mount/block"
	"github.com/u-root/uio/uio"

	"bootmanager.local/bootmanager/internal/androidboot"
)

// Legacy Android entries: the boot image of header version 0, 1 or 2, which
// carries its own ramdisk (and, in version 2, its device tree) and is booted
// by a bootloader that jumps to the kernel with them. Read raw from a
// partition named in Config.AndroidBoot, it is an entry with that kernel and
// ramdisk, the device tree from the image, a partition (Config.AndroidDTB)
// or the one the bootloader handed the stage, and the command line of the
// image with what the bootloader would have added after it
// (Config.AndroidCmdline). Versions 3 and 4 are androidEntries', which needs
// the bootloader's tree.

// KexecTools is how to run the kexec-tools program, for a platform whose
// kernel u-root cannot kexec itself (32-bit ARM): its argv, and optionally
// a command to run once the kernel is loaded and before the jump, to quiesce
// what could write to memory (a Wi-Fi chip's DMA, say).
type KexecTools struct {
	Argv []string
	Prep []string
}

// kexecToolsImage is a LinuxImage that is loaded by kexec-tools instead of
// u-root, as Load is all that differs: the options, edits and the command
// line see the LinuxImage inside it (asLinux).
type kexecToolsImage struct {
	*boot.LinuxImage
	kx KexecTools
}

// Load writes the kernel, initramfs and tree to RAM and has kexec-tools
// load them. The files stay until the jump, which wipes memory anyway.
func (k *kexecToolsImage) Load(...boot.LoadOption) error {
	if len(k.kx.Argv) == 0 {
		return errors.New("no kexec-tools to load with")
	}
	dir, err := os.MkdirTemp("", "kexec-image-")
	if err != nil {
		return err
	}
	write := func(name string, r io.ReaderAt) (string, error) {
		if r == nil {
			return "", nil
		}
		b, err := uio.ReadAll(r)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, b, 0o600)
	}
	kernel, err := write("kernel", k.Kernel)
	if err != nil {
		return err
	}
	initrd, err := write("initrd", k.Initrd)
	if err != nil {
		return err
	}
	dtb, err := write("dtb", k.DTB)
	if err != nil {
		return err
	}
	args := append(append([]string{}, k.kx.Argv[1:]...), "-l", kernel, "--append="+k.Cmdline)
	if initrd != "" {
		args = append(args, "--initrd="+initrd)
	}
	if dtb != "" {
		args = append(args, "--dtb="+dtb)
	}
	log.Printf("kexec-tools: %s %s", k.kx.Argv[0], strings.Join(args, " "))
	if out, err := exec.Command(k.kx.Argv[0], args...).CombinedOutput(); err != nil {
		return fmt.Errorf("kexec -l: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if len(k.kx.Prep) > 0 {
		if out, err := exec.Command(k.kx.Prep[0], k.kx.Prep[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("before the jump: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// asLinux is the LinuxImage of an entry that has one: the entry itself, or
// the one a kexec-tools entry wraps.
func asLinux(img boot.OSImage) (*boot.LinuxImage, bool) {
	switch v := img.(type) {
	case *boot.LinuxImage:
		return v, true
	case *kexecToolsImage:
		return v.LinuxImage, true
	}
	return nil, false
}

// fdtSection is the device tree at the start of r, cut at its own size,
// nil when r does not start with one. A partition holds more than the tree.
func fdtSection(r io.ReaderAt) io.ReaderAt {
	var h [8]byte
	if _, err := r.ReadAt(h[:], 0); err != nil || binary.BigEndian.Uint32(h[:]) != 0xd00dfeed {
		return nil
	}
	size := int64(binary.BigEndian.Uint32(h[4:]))
	if size < 40 || size > 8<<20 {
		return nil
	}
	tree := io.NewSectionReader(r, 0, size)
	if _, err := dt.ReadFDT(tree); err != nil {
		return nil
	}
	return tree
}

// legacyAndroidEntries returns an entry per partition of labels that holds
// a version 0, 1 or 2 Android boot image.
func legacyAndroidEntries(devs block.BlockDevices, labels []string, dtbLabel, cmdlineFile, bootloaderDir string, kx KexecTools) []boot.OSImage {
	if len(labels) == 0 {
		return nil
	}
	var dtb io.ReaderAt
	if dtbLabel != "" {
		for _, d := range devs.FilterPartLabel(dtbLabel) {
			if f, err := os.Open(d.DevicePath()); err == nil {
				if dtb = fdtSection(f); dtb != nil {
					break
				}
				f.Close()
			}
		}
		if dtb == nil {
			log.Printf("android: no device tree in partition %q", dtbLabel)
		}
	}
	if dtb == nil && bootloaderDir != "" {
		if b, err := os.ReadFile(filepath.Join(bootloaderDir, "fdt")); err == nil {
			dtb = bytes.NewReader(b)
		}
	}
	var extra string
	if cmdlineFile != "" {
		if b, err := os.ReadFile(cmdlineFile); err != nil {
			log.Printf("android: %v", err)
		} else {
			extra = strings.Join(strings.Fields(string(b)), " ")
		}
	}
	var imgs []boot.OSImage
	for _, label := range labels {
		for _, d := range devs.FilterPartLabel(label) {
			img, err := legacyAndroidImage(d.DevicePath(), label, dtb, extra, kx)
			if err != nil {
				if !errors.Is(err, errNotLegacy) {
					log.Printf("android: %s: %v", d.DevicePath(), err)
				}
				continue
			}
			imgs = append(imgs, img)
		}
	}
	return imgs
}

// errNotLegacy is a partition that does not hold a version 0 to 2 image.
var errNotLegacy = errors.New("not a legacy Android boot image")

// legacyAndroidImage is the entry for the boot image on dev. The tree the
// image carries (version 2) wins over dtb.
func legacyAndroidImage(dev, label string, dtb io.ReaderAt, extraCmdline string, kx KexecTools) (boot.OSImage, error) {
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	var magic [8]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != androidboot.Magic {
		f.Close()
		return nil, errNotLegacy
	}
	m, err := androidboot.Parse(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if m.Version > 2 {
		f.Close()
		return nil, errNotLegacy
	}
	if err := m.Check(); err != nil {
		f.Close()
		return nil, err
	}
	li := &boot.LinuxImage{
		Name:    fmt.Sprintf("Android (%s)", label),
		Kernel:  m.Kernel(),
		Cmdline: strings.TrimSpace(m.Cmdline + " " + extraCmdline),
	}
	if v := m.OSVersion; v != "" {
		li.Name = fmt.Sprintf("Android %s (%s)", v, label)
	}
	if r := m.Ramdisk(); r != nil {
		li.Initrd = r
	}
	if t := m.DTB(); t != nil {
		li.DTB = t
	} else if dtb != nil {
		li.DTB = dtb
	}
	if len(kx.Argv) > 0 {
		return &kexecToolsImage{LinuxImage: li, kx: kx}, nil
	}
	return li, nil
}
