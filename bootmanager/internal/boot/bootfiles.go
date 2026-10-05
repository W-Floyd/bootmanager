package boot

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
)

// bootFilesDirs are where a stage keeps its own files (keys, firmware,
// settings, logs) on a file system: linuxboot at its root, when the file
// system is /boot (the microSD card's first partition), or boot/linuxboot,
// when /boot is a directory of the root file system (an install on UFS).
var bootFilesDirs = []string{"linuxboot", "boot/linuxboot"}

// bootFilesFS are the file systems worth looking in: the ones an install
// puts its /boot on. Android's erofs, squashfs and verity images are not.
var bootFilesFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "f2fs": true, "vfat": true, "xfs": true,
}

// bootFilesIn is the first of bootFilesDirs that is a directory under root.
func bootFilesIn(root string) (string, bool) {
	for _, d := range bootFilesDirs {
		if fi, err := os.Stat(filepath.Join(root, d)); err == nil && fi.IsDir() {
			return d, true
		}
	}
	return "", false
}

// searchOrder is the order to look in: a microSD card's partitions (mmcblk)
// first, as a card is put in on purpose, then the rest by name.
func searchOrder(names []string) []string {
	out := slices.Clone(names)
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := strings.HasPrefix(out[i], "mmcblk"), strings.HasPrefix(out[j], "mmcblk")
		if ci != cj {
			return ci
		}
		return out[i] < out[j]
	})
	return out
}

// FindBootFiles finds where the stage keeps its files: the first block
// device whose file system has a linuxboot directory (bootFilesDirs). It
// returns the device (a kernel name), its file system type and the
// directory, from the file system's root. Each candidate is mounted
// read-only only to look.
func FindBootFiles() (dev, fstype, dir string, err error) {
	devs, err := block.GetBlockDevices()
	if err != nil {
		return "", "", "", err
	}
	var names []string
	for _, d := range devs.FilterZeroSize() {
		names = append(names, d.Name)
	}
	for _, name := range searchOrder(names) {
		fs, _, err := mount.FSFromBlock("/dev/" + name)
		if err != nil || !bootFilesFS[fs] {
			continue
		}
		var found string
		err = settingsFile{dev: name}.withRoot(false, func(root string) error {
			found, _ = bootFilesIn(root)
			return nil
		})
		if err == nil && found != "" {
			return name, fs, found, nil
		}
	}
	return "", "", "", errors.New("no file system with a linuxboot directory")
}
