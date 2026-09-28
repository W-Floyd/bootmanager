// Package boot finds boot entries, runs the autoboot countdown and hands
// over to the chosen kernel.
//
// Entries are found as u-root's boot command finds them (localboot over
// every non-empty block device, read-only mounts kept in a pool until the
// handover), with the same command-line edit: drop the entry's console=
// and reuse the stage's.
package boot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
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
	// Settings is where the entries' order and default are kept
	// (settingsFile): PATH or DEV:PATH. Empty: for this run only.
	Settings string
}

type Manager struct {
	cfg    Config
	scanMu sync.Mutex // held over a scan and over the pool's mounts' release
	pool   mount.Pool

	mu       sync.Mutex
	images   []boot.OSImage // in the user's order
	keys     []string       // images' keys (keysOf)
	settings Settings
	def      int  // the default entry
	explicit bool // chosen, not the one the scan found first
	first    int  // with no default chosen, the scan's first entry: its place now
	scanned  bool
	booting  bool
	left     int // seconds of countdown left; -1 when none is running

	stop     chan struct{}
	stopOnce sync.Once
}

func New(cfg Config) *Manager {
	m := &Manager{cfg: cfg, left: -1, stop: make(chan struct{})}
	var err error
	if m.settings, err = parseSettingsFile(cfg.Settings).load(); err != nil {
		log.Printf("settings %s: %v", cfg.Settings, err)
	}
	return m
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
}

type Entries struct {
	Scanning bool    `json:"scanning"`
	Entries  []Entry `json:"entries"`
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
	imgs, err := m.scan()
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
	if len(imgs) == 0 || m.cfg.Autoboot <= 0 {
		return
	}
	if m.held() {
		m.Cancel()
		return
	}

	t := time.NewTicker(time.Second)
	defer t.Stop()
	for left := m.cfg.Autoboot; left > 0; left-- {
		if !m.setLeft(left) {
			return
		}
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
	}
	m.Cancel()
	if m.held() {
		return
	}
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
func (m *Manager) Cancel() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	was := m.left
	m.left = -1
	m.mu.Unlock()
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
	e := Entries{Scanning: !m.scanned, Entries: []Entry{}, Default: m.def, Explicit: m.explicit}
	for _, img := range m.images {
		detail := ""
		if li, ok := img.(*boot.LinuxImage); ok {
			detail = li.Cmdline
		}
		e.Entries = append(e.Entries, Entry{Label: img.Label(), Detail: detail})
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
	m.Cancel()
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

	log.Printf("booting %s", img)
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
			m.settings.Default, m.def, m.explicit = "", m.first, false
			return nil
		}
		m.settings.Default, m.def, m.explicit = m.keys[i], i, true
		return nil
	})
}

// change applies f to the entries and settings and saves them, undoing
// all of it if the save fails.
func (m *Manager) change(f func() error) error {
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
	}{slices.Clone(m.images), slices.Clone(m.keys), Settings{slices.Clone(m.settings.Order), m.settings.Default}, m.def, m.explicit, m.first}
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
