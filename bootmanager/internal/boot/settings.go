package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
	"golang.org/x/sys/unix"
)

// Settings are what the user chose of the entries, by key (keysOf): their
// order, and the one to autoboot; and whether to autoboot at all and for
// how long to count down. The last two are unset until chosen, so the
// flags' defaults apply (-autoboot).
type Settings struct {
	Order   []string `json:"order,omitempty"`
	Default string   `json:"default,omitempty"`
	// Autoboot: nil until chosen.
	Autoboot *bool `json:"autoboot,omitempty"`
	// Countdown is the autoboot's length in seconds, 0 until chosen.
	Countdown int `json:"countdown,omitempty"`
}

func (s Settings) clone() Settings {
	s.Order = slices.Clone(s.Order)
	if s.Autoboot != nil {
		on := *s.Autoboot
		s.Autoboot = &on
	}
	return s
}

// settingsFile is where Settings are kept: a file (PATH), or a file on a
// block device's file system (DEV:PATH, DEV a kernel name, PATH from the
// file system's root). The device is mounted only to read or write it.
//
// A third form, @FILE, names a file that holds one of the first two. A stage
// that does not know where its boot partition is when it starts the Boot
// Manager writes it once it has found it (FindBootFiles); until the file
// exists the location is unknown, which is an error, not "no settings", so
// the load is tried again. An empty file means there is nowhere to keep them.
type settingsFile struct{ dev, path, ref string }

// resolved is the location an @FILE names, or f itself.
func (f settingsFile) resolved() (settingsFile, error) {
	if f.ref == "" {
		return f, nil
	}
	b, err := os.ReadFile(f.ref)
	if err != nil {
		return settingsFile{}, fmt.Errorf("settings location: %w", err)
	}
	return parseSettingsFile(strings.TrimSpace(string(b))), nil
}

// persists is whether a change can be kept: there is a location, and it is
// known.
func (f settingsFile) persists() bool {
	r, err := f.resolved()
	return err == nil && r.path != ""
}

func parseSettingsFile(s string) settingsFile {
	if ref, ok := strings.CutPrefix(s, "@"); ok {
		return settingsFile{ref: ref}
	}
	if dev, path, ok := strings.Cut(s, ":"); ok && !strings.Contains(dev, "/") {
		return settingsFile{dev: dev, path: strings.TrimPrefix(path, "/")}
	}
	return settingsFile{path: s}
}

// keysOf names each image by its label, which a reflashed card keeps,
// numbering repeats in scan order: "postmarketOS", "postmarketOS (2)".
func keysOf(imgs []boot.OSImage) []string {
	seen := map[string]int{}
	var keys []string
	for _, img := range imgs {
		l := img.Label()
		seen[l]++
		if n := seen[l]; n > 1 {
			l = fmt.Sprintf("%s (%d)", l, n)
		}
		keys = append(keys, l)
	}
	return keys
}

// arrange orders imgs as s says, those it does not know after the rest in
// scan order, and finds the default: s's, else the entry the scan found
// first, wherever it now is. A default is an entry, never a place.
func arrange(imgs []boot.OSImage, s Settings) (out []boot.OSImage, keys []string, def int, explicit bool) {
	keys = keysOf(imgs)
	idx := make([]int, len(imgs))
	for i := range idx {
		idx[i] = i
	}
	rank := func(i int) int {
		if r := slices.Index(s.Order, keys[i]); r >= 0 {
			return r
		}
		return len(s.Order)
	}
	slices.SortStableFunc(idx, func(a, b int) int { return rank(a) - rank(b) })
	var sorted []string
	for _, i := range idx {
		out = append(out, imgs[i])
		sorted = append(sorted, keys[i])
	}
	if d := slices.Index(sorted, s.Default); s.Default != "" && d >= 0 {
		return out, sorted, d, true
	}
	return out, sorted, max(0, slices.Index(idx, 0)), false
}

func (f settingsFile) load() (Settings, error) {
	var s Settings
	f, err := f.resolved()
	if err != nil {
		return s, err
	}
	if f.path == "" {
		return s, nil
	}
	var b []byte
	err = f.withRoot(false, func(root string) error {
		var err error
		b, err = os.ReadFile(filepath.Join(root, f.path))
		return err
	})
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}

func (f settingsFile) save(s Settings) error {
	f, err := f.resolved()
	if err != nil {
		return err
	}
	if f.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return err
	}
	return f.withRoot(true, func(root string) error { return writeFileSync(filepath.Join(root, f.path), append(b, '\n')) })
}

// writeFileSync replaces p with b, whole or not at all: a new file,
// synced, renamed over it, and the directory synced.
func writeFileSync(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(p)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// withRoot runs fn on the file system settings live on: "/" for a plain
// path, else dev mounted at a directory of its own. To write, dev's other
// mounts (the boot scan's, read-only) go first, as ext4 will not have the
// file system read-only and read-write at once, and come back as they
// were after.
func (f settingsFile) withRoot(write bool, fn func(root string) error) error {
	if f.dev == "" {
		return fn("/")
	}
	src := "/dev/" + f.dev
	fstype, _, err := mount.FSFromBlock(src)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	flags := uintptr(unix.MS_NOATIME | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	data := ""
	if write {
		held, err := mountsOf(src)
		if err != nil {
			return err
		}
		for i, m := range held {
			if err := unix.Unmount(m.dir, 0); err != nil {
				remount(src, held[:i])
				return fmt.Errorf("unmount %s from %s: %w", src, m.dir, err)
			}
		}
		defer remount(src, held)
	} else {
		flags |= unix.MS_RDONLY
		data = readOnlyData(fstype)
	}
	dir, err := os.MkdirTemp("", "bootmanager-settings-")
	if err != nil {
		return err
	}
	defer os.Remove(dir)
	if err := unix.Mount(src, dir, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s: %w", src, err)
	}
	err = fn(dir)
	if uerr := unix.Unmount(dir, 0); err == nil && uerr != nil {
		err = fmt.Errorf("unmount %s: %w", src, uerr)
	}
	return err
}

// readOnlyData keeps a read-only mount from replaying a journal, which is
// a write (as storage's browsing mounts do).
func readOnlyData(fstype string) string {
	switch fstype {
	case "ext2", "ext3", "ext4":
		return "noload"
	case "f2fs", "xfs":
		return "norecovery"
	}
	return ""
}

type mountEntry struct{ dir, fstype, opts string }

// mountsOf is where src is mounted, from /proc/self/mounts.
func mountsOf(src string) ([]mountEntry, error) {
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	var out []mountEntry
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[0] == src {
			out = append(out, mountEntry{unescape(f[1]), f[2], f[3]})
		}
	}
	return out, nil
}

// unescape undoes /proc/self/mounts' octal escapes (\040 for a space).
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// remount mounts src again where it was, with the options it had.
func remount(src string, ms []mountEntry) {
	flagOf := map[string]uintptr{
		"ro": unix.MS_RDONLY, "nosuid": unix.MS_NOSUID, "nodev": unix.MS_NODEV, "noexec": unix.MS_NOEXEC,
		"noatime": unix.MS_NOATIME, "nodiratime": unix.MS_NODIRATIME, "relatime": unix.MS_RELATIME,
		"sync": unix.MS_SYNCHRONOUS, "dirsync": unix.MS_DIRSYNC,
	}
	for _, m := range ms {
		var flags uintptr
		var data []string
		for _, o := range strings.Split(m.opts, ",") {
			switch v, ok := flagOf[o]; {
			case ok:
				flags |= v
			case o != "rw" && o != "seclabel":
				data = append(data, o)
			}
		}
		if err := unix.Mount(src, m.dir, m.fstype, flags, strings.Join(data, ",")); err != nil {
			log.Printf("remount %s at %s: %v", src, m.dir, err)
		}
	}
}
