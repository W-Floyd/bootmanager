// Package firmware gets the files a device's stage needs but cannot carry (a
// vendor's blobs) from where the device already keeps them: its own stock
// partitions. A device describes what it needs in a manifest; nothing here
// knows what a file is for.
package firmware

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/u-root/u-root/pkg/mount"
	"golang.org/x/sys/unix"
)

// Manifest lists the files a device needs.
type Manifest struct {
	Files []File `json:"files"`
}

// File is one needed file: where it goes in the stage, and the places it can
// be found, in order. A file already there (embedded in the stage, or put
// by an earlier step) is left alone.
type File struct {
	// Name is for the log.
	Name string `json:"name"`
	// Dest is the absolute path in the stage.
	Dest    string   `json:"dest"`
	Sources []Source `json:"sources"`
	// Rebind, if set, re-probes a device once the file is in place: a driver
	// asked for the file when it probed, before there was one, and gave up.
	Rebind *Rebind `json:"rebind,omitempty"`
}

// Source is one place a file can be: a file system on a partition. Exactly one
// of Super and Partition is set.
type Source struct {
	// Super names a logical partition of the dynamic-partition super
	// partition (Android's "vendor", say).
	Super string `json:"super,omitempty"`
	// Partition names a partition by its GPT name.
	Partition string `json:"partition,omitempty"`
	// Path is the file within the partition's file system.
	Path string `json:"path"`
	// FSType, if the file system cannot be told from its superblock.
	FSType string `json:"fstype,omitempty"`
}

// Rebind finds the devices on Bus whose modalias contains Modalias and binds
// them to Driver again.
type Rebind struct {
	Bus      string `json:"bus"`
	Driver   string `json:"driver"`
	Modalias string `json:"modalias"`
}

// LoadManifest reads and checks a manifest.
func LoadManifest(file string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(file)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", file, err)
	}
	if err := m.validate(); err != nil {
		return m, fmt.Errorf("%s: %w", file, err)
	}
	return m, nil
}

func (m Manifest) validate() error {
	for i, f := range m.Files {
		where := fmt.Sprintf("file %d (%q)", i, f.Name)
		if f.Name == "" {
			return fmt.Errorf("file %d: no name", i)
		}
		if !path.IsAbs(f.Dest) || path.Clean(f.Dest) != f.Dest {
			return fmt.Errorf("%s: dest %q is not a clean absolute path", where, f.Dest)
		}
		if len(f.Sources) == 0 {
			return fmt.Errorf("%s: no sources", where)
		}
		for j, s := range f.Sources {
			if (s.Super == "") == (s.Partition == "") {
				return fmt.Errorf("%s: source %d needs exactly one of super and partition", where, j)
			}
			if s.Path == "" || path.IsAbs(s.Path) || strings.Contains(s.Path, "..") {
				return fmt.Errorf("%s: source %d: path %q must be relative, without ..", where, j, s.Path)
			}
		}
		if r := f.Rebind; r != nil && (r.Bus == "" || r.Driver == "" || r.Modalias == "") {
			return fmt.Errorf("%s: rebind needs bus, driver and modalias", where)
		}
	}
	return nil
}

// Env is what the device looks like to this: paths, so a test can stand in.
type Env struct {
	// SlotSuffix is the A/B slot's, "_a": a partition is looked for by its
	// name with it first, then without.
	SlotSuffix string
	SysBlock   string // /sys/class/block
	SysBus     string // /sys/bus
	Dev        string // /dev
	Log        func(format string, args ...any)
}

// DefaultEnv is the running system's.
func DefaultEnv() Env {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	return Env{
		SlotSuffix: SlotSuffix(string(cmdline)),
		SysBlock:   "/sys/class/block",
		SysBus:     "/sys/bus",
		Dev:        "/dev",
		Log:        func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	}
}

// SlotSuffix is androidboot.slot_suffix from a kernel command line.
func SlotSuffix(cmdline string) string {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, "androidboot.slot_suffix="); ok {
			return v
		}
	}
	return ""
}

// slotNames are the names to look a partition up by.
func slotNames(name, suffix string) []string {
	if suffix == "" {
		return []string{name}
	}
	return []string{name + suffix, name}
}

// Install puts every file of m in place. It goes on past a file it cannot get
// and returns an error for those.
func Install(m Manifest, env Env) error {
	var errs []error
	for _, f := range m.Files {
		if err := installFile(f, env); err != nil {
			env.Log("firmware: %s: %v", f.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", f.Name, err))
		}
	}
	return errors.Join(errs...)
}

func installFile(f File, env Env) error {
	if fi, err := os.Stat(f.Dest); err == nil && fi.Size() > 0 {
		env.Log("firmware: %s: already at %s", f.Name, f.Dest)
		return nil
	}
	var tried []string
	for _, s := range f.Sources {
		data, from, err := readSource(s, env)
		if err != nil {
			tried = append(tried, err.Error())
			continue
		}
		if err := writeAtomic(f.Dest, data); err != nil {
			return err
		}
		env.Log("firmware: %s: %d bytes to %s, from %s", f.Name, len(data), f.Dest, from)
		if f.Rebind != nil {
			if err := rebind(*f.Rebind, env); err != nil {
				env.Log("firmware: %s: rebind: %v", f.Name, err)
			}
		}
		return nil
	}
	return errors.New("not found: " + strings.Join(tried, "; "))
}

func writeAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// readSource reads the file from one source, and says where it came from.
func readSource(s Source, env Env) (data []byte, from string, err error) {
	if s.Super != "" {
		superDev, err := findPartition(env, "super")
		if err != nil {
			return nil, "", err
		}
		var last error
		for _, name := range slotNames(s.Super, env.SlotSuffix) {
			data, err := readFromSuper(superDev, name, s, env)
			if err == nil {
				return data, fmt.Sprintf("%s:%s %s", path.Base(superDev), name, s.Path), nil
			}
			last = err
		}
		return nil, "", fmt.Errorf("super %s: %w", s.Super, last)
	}
	var last error
	for _, name := range slotNames(s.Partition, env.SlotSuffix) {
		dev, err := findPartition(env, name)
		if err != nil {
			last = err
			continue
		}
		data, err := readFromDevice(dev, s.FSType, s.Path)
		if err == nil {
			return data, fmt.Sprintf("%s %s", name, s.Path), nil
		}
		last = fmt.Errorf("partition %s: %w", name, err)
	}
	return nil, "", last
}

// findPartition is the device node of the partition with this GPT name: its
// PARTNAME in sysfs, which is there without udev.
func findPartition(env Env, name string) (string, error) {
	ents, err := os.ReadDir(env.SysBlock)
	if err != nil {
		return "", err
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(env.SysBlock, e.Name(), "uevent"))
		if err != nil {
			continue
		}
		var devname string
		match := false
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "DEVNAME="); ok {
				devname = v
			}
			if v, ok := strings.CutPrefix(line, "PARTNAME="); ok && v == name {
				match = true
			}
		}
		if match && devname != "" {
			return filepath.Join(env.Dev, devname), nil
		}
	}
	return "", fmt.Errorf("no partition named %q", name)
}

// readFromSuper reads a file from a logical partition of super that is one
// extent, through a loop device over just that extent.
func readFromSuper(superDev, name string, s Source, env Env) ([]byte, error) {
	f, err := os.Open(superDev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ex, err := PartitionExtents(f, name)
	if err != nil {
		return nil, err
	}
	if len(ex) != 1 {
		return nil, fmt.Errorf("%s is in %d pieces; only a single extent can be mounted here", name, len(ex))
	}
	loopDev, detach, err := attachLoop(env, superDev, ex[0].Start*sectorSize, ex[0].Sectors*sectorSize)
	if err != nil {
		return nil, err
	}
	defer detach()
	return readFromDevice(loopDev, s.FSType, s.Path)
}

// readFromDevice mounts dev read-only, reads one file, and unmounts.
func readFromDevice(dev, fstype, file string) ([]byte, error) {
	if fstype == "" {
		var err error
		if fstype, err = detectFS(dev); err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp("", "firmware-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(dir)
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_NOATIME)
	if err := unix.Mount(dev, dir, fstype, flags, readOnlyData(fstype)); err != nil {
		return nil, fmt.Errorf("mount %s as %s: %w", dev, fstype, err)
	}
	defer unix.Unmount(dir, 0)
	return os.ReadFile(filepath.Join(dir, file))
}

// readOnlyData keeps a read-only mount from replaying a journal, which is a
// write.
func readOnlyData(fstype string) string {
	switch fstype {
	case "ext2", "ext3", "ext4":
		return "noload"
	case "f2fs", "xfs":
		return "norecovery"
	}
	return ""
}

const erofsMagic = 0xE0F5E1E2

// detectFS tells the file system on dev from its superblock. u-root's
// detection does not know erofs, which Android's partitions are.
func detectFS(dev string) (string, error) {
	f, err := os.Open(dev)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var sb [4]byte
	if _, err := f.ReadAt(sb[:], 1024); err == nil && binary.LittleEndian.Uint32(sb[:]) == erofsMagic {
		return "erofs", nil
	}
	fs, _, err := mount.FSFromBlock(dev)
	return fs, err
}

// attachLoop makes a read-only loop device over [off, off+size) of backing.
func attachLoop(env Env, backing string, off, size uint64) (dev string, detach func(), err error) {
	ctl, err := os.OpenFile(filepath.Join(env.Dev, "loop-control"), os.O_RDWR, 0)
	if err != nil {
		return "", nil, fmt.Errorf("loop-control: %w", err)
	}
	defer ctl.Close()
	nr, err := unix.IoctlRetInt(int(ctl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", nil, fmt.Errorf("no free loop device: %w", err)
	}
	dev = filepath.Join(env.Dev, fmt.Sprintf("loop%d", nr))
	// devtmpfs makes the node a moment after the device exists.
	var lf *os.File
	for i := 0; i < 40; i++ {
		if lf, err = os.OpenFile(dev, os.O_RDWR, 0); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return "", nil, err
	}
	bf, err := os.Open(backing)
	if err != nil {
		lf.Close()
		return "", nil, err
	}
	defer bf.Close()
	cfg := unix.LoopConfig{Fd: uint32(bf.Fd()), Info: unix.LoopInfo64{
		Offset: off, Sizelimit: size,
		Flags: unix.LO_FLAGS_READ_ONLY | unix.LO_FLAGS_AUTOCLEAR,
	}}
	if err := unix.IoctlLoopConfigure(int(lf.Fd()), &cfg); err != nil {
		lf.Close()
		return "", nil, fmt.Errorf("configure %s over %s: %w", dev, backing, err)
	}
	// AUTOCLEAR frees the device when it is last closed, which includes the
	// mount: closing ours after the unmount is what lets it go.
	return dev, func() { lf.Close() }, nil
}

// rebind binds the matching devices to their driver afresh.
func rebind(r Rebind, env Env) error {
	devs := filepath.Join(env.SysBus, r.Bus, "devices")
	drv := filepath.Join(env.SysBus, r.Bus, "drivers", r.Driver)
	ents, err := os.ReadDir(devs)
	if err != nil {
		return err
	}
	for _, e := range ents {
		alias, err := os.ReadFile(filepath.Join(devs, e.Name(), "modalias"))
		if err != nil || !strings.Contains(string(alias), r.Modalias) {
			continue
		}
		if _, err := os.Stat(filepath.Join(drv, e.Name())); err == nil {
			os.WriteFile(filepath.Join(drv, "unbind"), []byte(e.Name()), 0o200)
		}
		if err := os.WriteFile(filepath.Join(drv, "bind"), []byte(e.Name()), 0o200); err != nil {
			return fmt.Errorf("bind %s: %w", e.Name(), err)
		}
		env.Log("firmware: rebound %s", e.Name())
	}
	return nil
}
