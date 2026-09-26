// Package power reboots and powers off, including into the modes the
// device tree maps for its reboot-mode driver (bootloader, recovery, …).
package power

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Modes lists the reboot modes the running device tree maps: the "mode-*"
// properties of reboot-mode nodes (syscon-reboot-mode, qcom's PON, nvmem
// reboot-mode, …), without the prefix. A device without any cannot be
// asked to reboot into its bootloader this way.
func Modes() []string {
	modes := []string{}
	filepath.WalkDir("/proc/device-tree", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if m, ok := strings.CutPrefix(d.Name(), "mode-"); ok && m != "normal" && !slices.Contains(modes, m) {
			modes = append(modes, m)
		}
		return nil
	})
	slices.Sort(modes)
	return modes
}

// Check reports whether Reboot can be asked for mode.
func Check(mode string) error {
	if mode != "" && !slices.Contains(Modes(), mode) {
		return fmt.Errorf("no reboot mode %q in the device tree", mode)
	}
	return nil
}

// Reboot restarts, into mode when it is not empty. It returns only on
// failure.
func Reboot(mode string) error {
	if mode == "" {
		return unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
	}
	// x/sys/unix has no wrapper for RESTART2, which takes the mode string.
	p, err := unix.BytePtrFromString(mode)
	if err != nil {
		return err
	}
	_, _, errno := unix.Syscall6(unix.SYS_REBOOT, unix.LINUX_REBOOT_MAGIC1, unix.LINUX_REBOOT_MAGIC2,
		unix.LINUX_REBOOT_CMD_RESTART2, uintptr(unsafe.Pointer(p)), 0, 0)
	return errno
}

func PowerOff() error { return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF) }
