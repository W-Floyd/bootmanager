// Package boot finds boot entries, runs the autoboot countdown and hands
// over to the chosen kernel.
//
// Entries are found as u-root's boot command finds them (localboot over
// every non-empty block device, read-only mounts kept in a pool until the
// handover), with the same command-line edit: drop the entry's console=
// and reuse the stage's.
package boot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/boot/localboot"
	"github.com/u-root/u-root/pkg/boot/syslinux"
	"github.com/u-root/u-root/pkg/cmdline"
	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
	"github.com/u-root/u-root/pkg/ulog"
	"golang.org/x/sys/unix"

	"bootmanager.local/bootmanager/internal/distro"
)

type Config struct {
	// ScanDir parses this directory's boot config instead of probing block
	// devices, and never hands over: for running anywhere, for UI work.
	ScanDir string
	// Autoboot is the countdown in seconds; 0 disables it.
	Autoboot int
	// HoldFile, when it exists as the countdown starts or ends, stops the
	// autoboot.
	HoldFile string
	// BootloaderDir holds the device tree and initramfs the bootloader
	// handed the stage (fdt, initrd), when the stage keeps them: with it,
	// an Android boot image on a partition is an entry (android.go).
	BootloaderDir string
	// AndroidPartitions are partitions, by GPT label, that may hold an
	// Android boot image, read raw (android.go). Nil: the boot partition
	// of the A/B slot the bootloader did not boot.
	AndroidPartitions []string
	// Emit sends an event to every client.
	Emit func(name string, data any)
	// Teardown runs just before a handover, and before power actions:
	// whatever must not outlive the stage (the UI, services, mounts).
	Teardown func()
	// InitrdExtra, when set, is asked at a handover for what to append to a
	// Linux entry's initramfs, given its command line: nil for nothing. The
	// kernel unpacks an initramfs archive after archive, so this is how a
	// secret (an unlocked volume's passphrase) reaches the booted system
	// without a command line or a file on disk.
	//
	// It returns the archive, and words to add to the entry's command line
	// for it (none when the archive needs none).
	InitrdExtra func(cmdline string) (archive []byte, cmdlineWords []string)
	// CmdlineExtra, when set, is asked at a handover for words to add to a
	// Linux entry's command line, for a try that should not be written into
	// its boot config (a dev build's: the pmOS debug shell).
	CmdlineExtra func() string
	// Settings is where the entries' order and default are kept
	// (settingsFile): PATH or DEV:PATH. Empty: for this run only.
	Settings string
	// VolumeUUID is the LUKS UUID of a partition (kernel name), "" when it
	// is not known: how a default inside an encrypted volume is remembered
	// and then found again while the volume is locked.
	VolumeUUID func(dev string) string
}

type Manager struct {
	cfg    Config
	scanMu sync.Mutex // held over a scan and over the pool's mounts' release
	pool   mount.Pool

	mu       sync.Mutex
	images   []boot.OSImage // in the user's order
	keys     []string       // images' keys (keysOf)
	settings Settings
	// settingsLoaded: the settings file has been read (or there is none).
	settingsLoaded bool
	def            int  // the default entry
	explicit       bool // chosen, not the one the scan found first
	first          int  // with no default chosen, the scan's first entry: its place now
	scanned        bool
	locked         []Locked // encrypted volumes found and not open
	// pendingUnlock is the locked volume the default entry is inside: the
	// autoboot waits for it to be unlocked, then boots the entry.
	pendingUnlock *Locked
	// oneBoot is a command line edited for the boot being started (StartWith).
	oneBoot    *string
	booting    bool
	left       int       // seconds of countdown left; -1 when none is running
	countStart time.Time // when the countdown began; zero before

	stop     chan struct{}
	stopOnce sync.Once
}

func New(cfg Config) *Manager {
	m := &Manager{cfg: cfg, left: -1, stop: make(chan struct{})}
	if err := m.loadSettings(); err != nil {
		log.Printf("settings %s: %v (tried again at the scan)", cfg.Settings, err)
	}
	return m
}

// loadSettings reads the settings, once. Until it has worked they are the
// defaults, and a change is refused rather than saved over what could not
// be read: the card may not have probed yet, or the stage may still hold it
// mounted (the boot scan, or the uinit's own writes).
func (m *Manager) loadSettings() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settingsLoaded {
		return nil
	}
	s, err := parseSettingsFile(m.cfg.Settings).load()
	if err != nil {
		return err
	}
	m.settings, m.settingsLoaded = s, true
	return nil
}

// setImages takes a scan's images, arranged as the settings say.
func (m *Manager) setImages(imgs []boot.OSImage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.images, m.keys, m.def, m.explicit = arrange(imgs, m.settings)
	_, _, m.first, _ = arrange(imgs, Settings{Order: m.settings.Order})
}

type Entry struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	// Source is where the entry was found ("microSD card", "UFS userdata
	// (encrypted)"), to tell apart entries of the same name; "" if unknown.
	Source string `json:"source"`
}

type Entries struct {
	Scanning bool    `json:"scanning"`
	Entries  []Entry `json:"entries"`
	// Locked are encrypted volumes not open: entries may be inside, and
	// unlocking one (disk.unlock) finds them.
	Locked []Locked `json:"locked"`
	// Unlock is the locked volume to unlock for the autoboot, when the
	// default entry is inside one: the UI asks for its passphrase.
	Unlock *Locked `json:"unlock,omitempty"`
	// Default is the entry the autoboot boots; Explicit, that the user
	// chose it, rather than it being the one the scan found first.
	Default  int  `json:"default"`
	Explicit bool `json:"explicit"`
}

type Autoboot struct {
	Remaining int    `json:"remaining"`
	Label     string `json:"label"`
}

func (m *Manager) scan() ([]boot.OSImage, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if m.cfg.ScanDir != "" {
		return syslinux.ParseLocalConfig(context.Background(), m.cfg.ScanDir)
	}
	devs, err := block.GetBlockDevices()
	if err != nil {
		return nil, err
	}
	imgs, err := localboot.Localboot(ulog.Null, devs.FilterZeroSize(), &m.pool)
	if err != nil {
		return nil, err
	}
	locked := findLocked(devs.FilterZeroSize())
	for i, l := range locked {
		if m.cfg.VolumeUUID != nil {
			locked[i].UUID = m.cfg.VolumeUUID(l.Dev)
		}
		log.Printf("scan: %s (%s) is encrypted and locked", l.Dev, l.Source)
	}
	m.mu.Lock()
	m.locked = locked
	m.mu.Unlock()
	imgs = append(imgs, bootDirEntries(m.pool.MountPoints)...)
	f := cmdline.NewUpdateFilter("", []string{"console"}, []string{"console"})
	boot.ApplyLinuxModifiers(imgs, func(li *boot.LinuxImage) {
		li.Cmdline = f.Update(cmdline.NewCmdLine(), li.Cmdline)
	})
	// After the console edit: Android's command line is the bootloader's,
	// console= included.
	imgs = append(imgs, androidEntries(m.pool.MountPoints, devs, m.cfg.AndroidPartitions, m.cfg.BootloaderDir)...)
	return imgs, nil
}

// Run scans for entries, then counts down and boots the first one, unless
// the countdown is cancelled first. It returns once either happens.
func (m *Manager) Run() {
	// The stage is ready: the card is up and its own writes are done. A
	// few tries, as the card can be slow to appear.
	for i := 0; i < 10; i++ {
		err := m.loadSettings()
		if err == nil {
			break
		}
		if i == 9 {
			log.Printf("settings %s: %v (changes are refused until it reads)", m.cfg.Settings, err)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	// The card can be up and mounted by the uinit yet not scannable for a
	// moment: a scan that finds nothing then never counts down. Try again,
	// still "scanning" for the UI, before settling on no entries.
	imgs, err := m.scan()
	// Not when an encrypted volume is there to unlock: that is why there is
	// nothing, and waiting would only keep the splash up for ten seconds.
	for i := 0; i < 10 && len(imgs) == 0 && !m.hasLocked(); i++ {
		log.Printf("scan found 0 entries (%v), trying again", err)
		select {
		case <-m.stop:
			return
		case <-time.After(time.Second):
		}
		imgs, err = m.scan()
	}
	m.setImages(imgs)
	m.mu.Lock()
	m.scanned, imgs = true, m.images
	def := m.def
	m.mu.Unlock()
	if err != nil {
		log.Printf("scan: %v", err)
		m.cfg.Emit("error", map[string]string{"message": "boot scan: " + err.Error()})
	}
	log.Printf("found %d boot entries", len(imgs))
	for i, img := range imgs {
		mark := ""
		if i == def {
			mark = " (default)"
		}
		log.Printf("  %d: %s%s", i, img.Label(), mark)
	}
	m.cfg.Emit("entries", m.List())
	m.mu.Lock()
	enabled, seconds := m.autobootLocked()
	// A default inside a locked volume is not among the entries, so the
	// countdown below would boot some other (or none): ask for the
	// passphrase, and boot the default once it is in.
	m.pendingUnlock = m.unlockToBootLocked(enabled)
	want := m.pendingUnlock
	m.mu.Unlock()
	if want != nil && !m.held() {
		log.Printf("autoboot: %s is on %s, locked: asking for its passphrase", m.settings.Default, want.Source)
		m.cfg.Emit("entries", m.List())
		return
	}
	if len(imgs) == 0 || !enabled {
		why := "no boot entries"
		if len(imgs) > 0 {
			why = "autoboot is off"
		}
		log.Printf("autoboot: not started (%s)", why)
		return
	}
	if m.held() {
		log.Printf("autoboot: not started (hold file %s exists)", m.cfg.HoldFile)
		m.cancel("hold file", false)
		return
	}

	t := time.NewTicker(time.Second)
	defer t.Stop()
	label := imgs[min(max(def, 0), len(imgs)-1)].Label()
	for left := seconds; left > 0; left-- {
		if !m.setLeft(left) {
			return
		}
		if left == seconds {
			m.mu.Lock()
			m.countStart = time.Now()
			m.mu.Unlock()
			log.Printf("autoboot: countdown started (%d s, entry %s)", seconds, label)
		}
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
	}
	m.cancel("countdown over", false)
	if m.held() {
		log.Printf("autoboot: not booting (hold file %s exists at the end of the countdown)", m.cfg.HoldFile)
		return
	}
	log.Printf("autoboot: booting %s", label)
	if err := m.Start(def); err != nil {
		log.Printf("autoboot: %v", err)
		m.cfg.Emit("error", map[string]string{"message": err.Error()})
	}
}

// held: the hold file exists, so stay in the stage.
func (m *Manager) held() bool {
	if m.cfg.HoldFile == "" {
		return false
	}
	if _, err := os.Stat(m.cfg.HoldFile); err != nil {
		return false
	}
	log.Printf("%s exists, not booting", m.cfg.HoldFile)
	return true
}

// setLeft publishes the countdown; false once it has been cancelled.
func (m *Manager) setLeft(n int) bool {
	select {
	case <-m.stop:
		return false
	default:
	}
	m.mu.Lock()
	m.left = n
	m.mu.Unlock()
	m.cfg.Emit("autoboot", m.Autoboot())
	return true
}

// Cancel stops the countdown for good, running or not yet started.
func (m *Manager) Cancel() { m.CancelBy("internal call") }

// CancelBy is Cancel, saying in the log what asked for it. Only the call
// that stops the countdown is logged, as
//
//	autoboot: cancelled by REASON (3.4 s into the countdown)
//
// or "(countdown not started)" when it had not begun.
func (m *Manager) CancelBy(reason string) {
	m.mu.Lock()
	m.pendingUnlock = nil
	m.mu.Unlock()
	m.cancel(reason, true)
	m.cfg.Emit("entries", m.List())
}

func (m *Manager) cancel(reason string, report bool) {
	first := false
	m.stopOnce.Do(func() { close(m.stop); first = true })
	m.mu.Lock()
	was := m.left
	m.left = -1
	start := m.countStart
	m.mu.Unlock()
	if first && report {
		if start.IsZero() {
			log.Printf("autoboot: cancelled by %s (countdown not started)", reason)
		} else {
			log.Printf("autoboot: cancelled by %s (%.1f s into the countdown)", reason, time.Since(start).Seconds())
		}
	}
	if was > 0 {
		log.Printf("autoboot stopped with %d s left", was)
	}
	m.cfg.Emit("autoboot", m.Autoboot())
}

// Cancelled reports whether the countdown can no longer boot anything.
func (m *Manager) Cancelled() bool {
	select {
	case <-m.stop:
		return true
	default:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.scanned && m.left < 0
	}
}

// defaultCountdown is the length when autoboot was off by flag and is
// switched on by the user, who chose no length.
const defaultCountdown = 10

// MaxCountdown is the longest countdown that can be chosen, in seconds.
const MaxCountdown = 300

// AutobootSettings is whether the stage autoboots at start and for how long
// it counts down: the user's choice, else the -autoboot flag's. They apply
// from the next start; a countdown already over is not run again.
type AutobootSettings struct {
	Enabled bool `json:"enabled"`
	Seconds int  `json:"seconds"`
	// Saved: there is a settings location, so a change outlives this run.
	Saved bool `json:"saved"`
}

// autobootLocked is the effective setting. m.mu is held.
func (m *Manager) autobootLocked() (enabled bool, seconds int) {
	enabled = m.cfg.Autoboot > 0
	if m.settings.Autoboot != nil {
		enabled = *m.settings.Autoboot
	}
	seconds = m.cfg.Autoboot
	if m.settings.Countdown > 0 {
		seconds = m.settings.Countdown
	}
	if seconds <= 0 {
		seconds = defaultCountdown
	}
	return enabled, seconds
}

func (m *Manager) AutobootSettings() AutobootSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	on, s := m.autobootLocked()
	return AutobootSettings{Enabled: on, Seconds: s, Saved: parseSettingsFile(m.cfg.Settings).persists()}
}

// SetAutoboot chooses whether to autoboot and the countdown's length in
// seconds (0: leave it), and saves it.
func (m *Manager) SetAutoboot(enabled bool, seconds int) error {
	if seconds < 0 || seconds > MaxCountdown {
		return fmt.Errorf("countdown must be 1 to %d seconds", MaxCountdown)
	}
	return m.change(func() error {
		m.settings.Autoboot = &enabled
		if seconds > 0 {
			m.settings.Countdown = seconds
		}
		return nil
	})
}

func (m *Manager) Autoboot() Autoboot {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := Autoboot{Remaining: m.left}
	if m.def < len(m.images) {
		a.Label = m.images[m.def].Label()
	}
	return a
}

func (m *Manager) List() Entries {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := Entries{Scanning: !m.scanned, Entries: []Entry{}, Locked: append([]Locked{}, m.locked...), Unlock: m.pendingUnlock, Default: m.def, Explicit: m.explicit}
	for _, img := range m.images {
		detail := ""
		if li, ok := img.(*boot.LinuxImage); ok {
			detail = li.Cmdline
		}
		e.Entries = append(e.Entries, Entry{Label: img.Label(), Detail: detail, Source: sourceOf(img)})
	}
	return e
}

// Image returns entry i.
func (m *Manager) Image(i int) (boot.OSImage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.images) {
		return nil, fmt.Errorf("no boot entry %d", i)
	}
	return m.images[i], nil
}

// Start boots entry i; it returns only on failure.
func (m *Manager) Start(i int) error {
	m.CancelBy("boot start")
	img, err := m.Image(i)
	if err != nil {
		return err
	}
	return m.Handover(img)
}

// Handover loads img and kexecs it, the one path every boot takes; it
// returns only on failure.
func (m *Manager) Handover(img boot.OSImage) error {
	m.mu.Lock()
	if m.booting {
		m.mu.Unlock()
		return errors.New("a boot is already in progress")
	}
	m.booting = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.booting = false
		m.mu.Unlock()
		m.cfg.Emit("busy", map[string]string{"text": ""})
	}()

	// The options the user set, on the entry's own command line first: what
	// a dev build then adds for one try goes after, and wins.
	if li, ok := img.(*boot.LinuxImage); ok {
		m.mu.Lock()
		one := m.oneBoot
		m.mu.Unlock()
		if one != nil {
			// An edit for this boot: the whole line, the options not applied
			// over it.
			log.Printf("booting %s: command line edited for this boot: %s", img.Label(), *one)
			li.Cmdline = *one
		} else if edited := m.optionsCmdline(li.Cmdline); edited != li.Cmdline {
			log.Printf("booting %s: command line by options: %s", img.Label(), edited)
			li.Cmdline = edited
		}
	}
	if li, ok := img.(*boot.LinuxImage); ok && m.cfg.CmdlineExtra != nil {
		if extra := strings.TrimSpace(m.cfg.CmdlineExtra()); extra != "" {
			li.Cmdline += " " + extra
			log.Printf("booting %s: command line words added: %s", img.Label(), extra)
		}
	}
	log.Printf("booting %s", img)
	if li, ok := img.(*boot.LinuxImage); ok && m.cfg.InitrdExtra != nil {
		if extra, words := m.cfg.InitrdExtra(li.Cmdline); len(extra) > 0 {
			// What the extra archive needs the system to be told (where its
			// unlock key is, for a distribution whose initramfs wants that on
			// the command line); a word already there is the user's.
			for _, w := range words {
				key, _, _ := strings.Cut(w, "=")
				if !distro.HasWord(li.Cmdline, key) {
					li.Cmdline += " " + w
				}
			}
			if li.Initrd != nil {
				li.Initrd = boot.CatInitrds(li.Initrd, bytes.NewReader(extra))
			} else {
				li.Initrd = bytes.NewReader(extra)
			}
			log.Printf("booting %s: the unlock key goes with its initramfs", img.Label())
		}
	}
	m.cfg.Emit("busy", map[string]string{"text": "Loading " + img.Label() + "…"})
	if err := img.Load(boot.WithLogger(ulog.Log)); err != nil {
		return fmt.Errorf("load %s: %w", img.Label(), err)
	}
	if m.cfg.ScanDir != "" {
		return errors.New("loaded; not handing over with -scan")
	}
	m.cfg.Emit("busy", map[string]string{"text": "Starting " + img.Label() + "…"})
	m.cfg.Teardown()
	return fmt.Errorf("kexec returned: %w", boot.Execute())
}

// Release drops the entries and the scan's mounts, before a partition
// they are on is rewritten; Rescan finds them again. The UI keeps showing
// the old entries until then.
func (m *Manager) Release() error {
	m.mu.Lock()
	if m.booting {
		m.mu.Unlock()
		return errors.New("a boot is in progress")
	}
	m.images = nil
	m.mu.Unlock()
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	m.Unmount()
	m.pool = mount.Pool{}
	return nil
}

// Rescan finds the entries again, after Release.
func (m *Manager) Rescan() {
	imgs, err := m.scan()
	if err != nil {
		log.Printf("rescan: %v", err)
	}
	m.setImages(imgs)
	log.Printf("rescan: %d boot entries", len(imgs))
	m.cfg.Emit("entries", m.List())
}

// Move moves entry i by places (-1 left, 1 right) and keeps the order:
// the whole of it, so entries on a card that is out stay where they were.
func (m *Manager) Move(i, by int) error {
	return m.change(func() error {
		j := i + by
		if i < 0 || j < 0 || i >= len(m.images) || j >= len(m.images) {
			return fmt.Errorf("cannot move entry %d by %d", i, by)
		}
		m.images[i], m.images[j] = m.images[j], m.images[i]
		m.keys[i], m.keys[j] = m.keys[j], m.keys[i]
		// The default, chosen or not, is an entry: it goes with it.
		for _, d := range []*int{&m.def, &m.first} {
			switch *d {
			case i:
				*d = j
			case j:
				*d = i
			}
		}
		// Those not found this time stay in the order, after these.
		order := slices.Clone(m.keys)
		for _, k := range m.settings.Order {
			if !slices.Contains(order, k) {
				order = append(order, k)
			}
		}
		m.settings.Order = order
		return nil
	})
}

// SetDefault makes entry i the one to autoboot, or, with i < 0, goes back
// to the first.
func (m *Manager) SetDefault(i int) error {
	return m.change(func() error {
		if i >= len(m.images) {
			return fmt.Errorf("no boot entry %d", i)
		}
		if i < 0 {
			m.settings.Default, m.settings.DefaultVolume, m.def, m.explicit = "", "", m.first, false
			return nil
		}
		m.settings.Default, m.def, m.explicit = m.keys[i], i, true
		m.settings.DefaultVolume = m.volumeOf(m.images[i])
		return nil
	})
}

// volumeOf is the LUKS UUID of the encrypted volume img was found in, "" if
// it is not in one (or the UUID is not known).
func (m *Manager) volumeOf(img boot.OSImage) string {
	if m.cfg.VolumeUUID == nil {
		return ""
	}
	dev := entryDev(img)
	if !strings.HasPrefix(dev, "dm-") {
		return ""
	}
	slaves, _ := os.ReadDir(filepath.Join(sysBlock, dev, "slaves"))
	if len(slaves) == 0 {
		return ""
	}
	return m.cfg.VolumeUUID(slaves[0].Name())
}

func (m *Manager) hasLocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.locked) > 0
}

// unlockToBootLocked is the locked volume the chosen default lives in, when
// autoboot is on and the default is not among the entries. m.mu is held.
func (m *Manager) unlockToBootLocked(enabled bool) *Locked {
	if !enabled || !m.settingsLoaded || m.settings.Default == "" || m.settings.DefaultVolume == "" || m.explicit {
		return nil
	}
	for _, l := range m.locked {
		if l.UUID == m.settings.DefaultVolume {
			return &l
		}
	}
	return nil
}

// TakeUnlockBoot reports whether the volume with this LUKS UUID is the one
// the autoboot is waiting on, and stops waiting: the caller unlocks it and
// then calls BootDefault.
func (m *Manager) TakeUnlockBoot(uuid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ok := uuid != "" && m.pendingUnlock != nil && m.pendingUnlock.UUID == uuid
	m.pendingUnlock = nil
	return ok
}

// BootDefault boots the default entry, once an unlock has brought it into
// the entries: the end of an autoboot that began with a passphrase.
func (m *Manager) BootDefault() {
	m.mu.Lock()
	def, found, key := m.def, m.explicit, m.settings.Default
	m.mu.Unlock()
	if !found {
		log.Printf("autoboot: %s not found after the unlock", key)
		m.cfg.Emit("error", map[string]string{"message": "the default entry " + key + " is not on this volume any more"})
		return
	}
	log.Printf("autoboot: unlocked, booting %s", key)
	if err := m.Start(def); err != nil {
		log.Printf("autoboot: %v", err)
		m.cfg.Emit("error", map[string]string{"message": err.Error()})
	}
}

// change applies f to the entries and settings and saves them, undoing
// all of it if the save fails.
func (m *Manager) change(f func() error) error {
	if err := m.loadSettings(); err != nil {
		return fmt.Errorf("settings not readable, so not changed: %w", err)
	}
	m.mu.Lock()
	if m.booting {
		m.mu.Unlock()
		return errors.New("a boot is in progress")
	}
	saved := struct {
		images   []boot.OSImage
		keys     []string
		settings Settings
		def      int
		explicit bool
		first    int
	}{slices.Clone(m.images), slices.Clone(m.keys), m.settings.clone(), m.def, m.explicit, m.first}
	err := f()
	if err == nil {
		m.scanMu.Lock()
		err = parseSettingsFile(m.cfg.Settings).save(m.settings)
		m.scanMu.Unlock()
		if err != nil {
			err = fmt.Errorf("saving settings: %w", err)
		}
	}
	if err != nil {
		m.images, m.keys, m.settings, m.def, m.explicit, m.first = saved.images, saved.keys, saved.settings, saved.def, saved.explicit, saved.first
	}
	m.mu.Unlock()
	m.cfg.Emit("entries", m.List())
	m.cfg.Emit("autoboot", m.Autoboot())
	return err
}

// Unmount releases the scan's mounts and syncs, for a handover or a
// power action.
func (m *Manager) Unmount() {
	if err := m.pool.UnmountAll(mount.MNT_DETACH); err != nil {
		log.Printf("unmount: %v", err)
	}
	unix.Sync()
}
