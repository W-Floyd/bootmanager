package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
	"golang.org/x/sys/unix"
)

// Browsing mounts are MS_RDONLY and ask for the journal to be left alone
// (ext4 noload, f2fs norecovery): a plain ro mount still replays a dirty
// journal, which is a write. That only holds for a partition nothing has
// mounted yet. A second mount of a device shares the first one's
// superblock and options, and the boot scan has already mounted every
// partition it could, plain ro, so on those any replay happened there --
// as it always has in u-root's boot, and as it must for the scan to read
// a current extlinux.conf.
const browseRoot = "/mnt/browse"

type diskJSON struct {
	Dev    string `json:"dev"`
	Name   string `json:"name"`
	FSType string `json:"fstype"`
	Size   uint64 `json:"size"`
}

// ueventValue reads one KEY=value from a block device's uevent.
func ueventValue(dev, key string) string {
	f, err := os.Open(filepath.Join("/sys/class/block", dev, "uevent"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+"="); ok {
			return v
		}
	}
	return ""
}

// diskList returns the partitions that carry a file system, the microSD
// card first: it is where pmOS lives, and UFS has sixty-odd partitions.
func diskList([]byte) (any, error) {
	devs, err := block.GetBlockDevices()
	if err != nil {
		return nil, err
	}
	out := []diskJSON{}
	for _, d := range devs.FilterZeroSize() {
		fstype := fsType(d.Name)
		if fstype == "" {
			continue
		}
		size, _ := d.Size()
		out = append(out, diskJSON{
			Dev:    d.Name,
			Name:   ueventValue(d.Name, "PARTNAME"),
			FSType: fstype,
			Size:   size,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := strings.HasPrefix(out[i].Dev, "mmcblk"), strings.HasPrefix(out[j].Dev, "mmcblk")
		if mi != mj {
			return mi
		}
		return out[i].Dev < out[j].Dev
	})
	return out, nil
}

var (
	browseMu sync.Mutex
	browsed  = map[string]string{} // dev -> mount point
)

func mountForBrowse(dev string) (string, error) {
	browseMu.Lock()
	defer browseMu.Unlock()
	if mp, ok := browsed[dev]; ok {
		return mp, nil
	}
	if strings.ContainsAny(dev, "/.") {
		return "", fmt.Errorf("bad device %q", dev)
	}
	if _, err := block.Device(dev); err != nil {
		return "", err
	}
	fstype := fsType(dev)
	if fstype == "" {
		return "", fmt.Errorf("%s: no file system recognised", dev)
	}
	data := ""
	switch fstype {
	case "ext2", "ext3", "ext4":
		data = "noload"
	case "f2fs", "xfs":
		data = "norecovery"
	}
	mp := filepath.Join(browseRoot, dev)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("/dev/"+dev, mp, fstype, flags, data); err != nil {
		return "", fmt.Errorf("mount %s (%s, ro): %w", dev, fstype, err)
	}
	browsed[dev] = mp
	return mp, nil
}

func unmountBrowsed() {
	browseMu.Lock()
	defer browseMu.Unlock()
	for dev, mp := range browsed {
		unix.Unmount(mp, unix.MNT_DETACH)
		delete(browsed, dev)
	}
}

type fileJSON struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Link string `json:"link,omitempty"`
	Size int64  `json:"size"`
}

const maxListing = 1000

func fsList(args []byte) (any, error) {
	a, err := decode[struct{ Dev, Path string }](args)
	if err != nil {
		return nil, err
	}
	mp, err := mountForBrowse(a.Dev)
	if err != nil {
		return nil, err
	}
	// Clean against "/" so no path climbs out of the mount.
	rel := path.Clean("/" + a.Path)
	ents, err := os.ReadDir(filepath.Join(mp, rel))
	if err != nil {
		return nil, err
	}
	files := []fileJSON{}
	for _, e := range ents {
		f := fileJSON{Name: e.Name(), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			f.Size = info.Size()
			if info.Mode()&os.ModeSymlink != 0 {
				f.Link, _ = os.Readlink(filepath.Join(mp, rel, e.Name()))
			}
		}
		files = append(files, f)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Dir != files[j].Dir {
			return files[i].Dir
		}
		return files[i].Name < files[j].Name
	})
	truncated := len(files) > maxListing
	if truncated {
		files = files[:maxListing]
	}
	return map[string]any{"path": rel, "entries": files, "truncated": truncated}, nil
}

func logTail(args []byte) (any, error) {
	a, err := decode[struct{ Lines int }](args)
	if err != nil {
		return nil, err
	}
	if a.Lines <= 0 {
		a.Lines = 200
	}
	var lines []string
	for _, p := range []string{"/tmp/uinit.log", *logPath} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines = append(lines, "== "+p)
		lines = append(lines, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	}
	if len(lines) > a.Lines {
		lines = lines[len(lines)-a.Lines:]
	}
	return lines, nil
}

// power reboots, reboots into ABL's fastboot, or powers off. "bootloader"
// is the reboot-mode argument the PON's reboot-mode node maps for ABL.
func power(args []byte) (any, error) {
	a, err := decode[struct{ Action string }](args)
	if err != nil {
		return nil, err
	}
	cmd, arg := 0, ""
	switch a.Action {
	case "reboot":
		cmd = unix.LINUX_REBOOT_CMD_RESTART
	case "bootloader":
		cmd, arg = unix.LINUX_REBOOT_CMD_RESTART2, "bootloader"
	case "poweroff":
		cmd = unix.LINUX_REBOOT_CMD_POWER_OFF
	default:
		return nil, fmt.Errorf("unknown power action %q", a.Action)
	}
	if *scanDir != "" {
		return nil, errors.New("not with -scan")
	}
	broadcast("busy", map[string]string{"text": strings.ToUpper(a.Action[:1]) + a.Action[1:] + "…"})
	stopCountdown()
	stopUI()
	stopServices()
	unmountBrowsed()
	mountPool.UnmountAll(unix.MNT_DETACH)
	unix.Sync()
	if arg != "" {
		p, _ := unix.BytePtrFromString(arg)
		_, _, errno := unix.Syscall6(unix.SYS_REBOOT, unix.LINUX_REBOOT_MAGIC1,
			unix.LINUX_REBOOT_MAGIC2, uintptr(cmd), uintptr(unsafe.Pointer(p)), 0, 0)
		return nil, errno
	}
	return nil, unix.Reboot(cmd)
}

// fsType names the file system on /dev/<dev> from its superblock magic,
// when the kernel supports it; "" otherwise. block.GetBlockDevices leaves
// BlockDev.FSType empty -- u-root's own mounting tries every type instead.
func fsType(dev string) string {
	fs, _, err := mount.FSFromBlock("/dev/" + dev)
	if err != nil {
		return ""
	}
	return fs
}
