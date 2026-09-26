package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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

// The entries are found the way u-root's boot command finds them
// (localboot over every non-empty block device, read-only mounts kept in
// mountPool until the handover), with the same command-line edit: drop the
// entry's console= and reuse the stage's.
var (
	bootMu    sync.Mutex
	scanned   bool
	images    []boot.OSImage
	mountPool = &mount.Pool{}
	booting   bool

	cdMu      sync.Mutex
	remaining = -1 // seconds left, -1 when no countdown is running
	cancelled bool
)

type entryJSON struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

func scan() ([]boot.OSImage, error) {
	if *scanDir != "" {
		return syslinux.ParseLocalConfig(context.Background(), *scanDir)
	}
	devs, err := block.GetBlockDevices()
	if err != nil {
		return nil, err
	}
	imgs, err := localboot.Localboot(ulog.Null, devs.FilterZeroSize(), mountPool)
	if err != nil {
		return nil, err
	}
	f := cmdline.NewUpdateFilter("", []string{"console"}, []string{"console"})
	boot.ApplyLinuxModifiers(imgs, func(li *boot.LinuxImage) {
		li.Cmdline = f.Update(cmdline.NewCmdLine(), li.Cmdline)
	})
	return imgs, nil
}

func scanAndCountdown() {
	imgs, err := scan()
	bootMu.Lock()
	images, scanned = imgs, true
	bootMu.Unlock()
	if err != nil {
		log.Printf("scan: %v", err)
		broadcast("error", map[string]string{"message": "boot scan: " + err.Error()})
	}
	log.Printf("found %d boot entries", len(imgs))
	for i, img := range imgs {
		log.Printf("  %d: %s", i, img.Label())
	}
	broadcast("entries", bootListData())
	if len(imgs) == 0 || *autoboot <= 0 {
		return
	}

	cdMu.Lock()
	if cancelled {
		cdMu.Unlock()
		return
	}
	remaining = *autoboot
	cdMu.Unlock()
	for {
		broadcast("autoboot", autobootState())
		time.Sleep(time.Second)
		cdMu.Lock()
		if cancelled {
			cdMu.Unlock()
			return
		}
		remaining--
		left := remaining
		cdMu.Unlock()
		if left > 0 {
			continue
		}
		if _, err := os.Stat("/tmp/hold"); err == nil {
			log.Printf("/tmp/hold exists, not booting")
			stopCountdown()
			return
		}
		stopCountdown()
		if err := startEntry(0); err != nil {
			log.Printf("autoboot: %v", err)
			broadcast("error", map[string]string{"message": err.Error()})
		}
		return
	}
}

func stopCountdown() {
	cdMu.Lock()
	cancelled, remaining = true, -1
	cdMu.Unlock()
	broadcast("autoboot", autobootState())
}

func autobootState() map[string]any {
	cdMu.Lock()
	left := remaining
	cdMu.Unlock()
	label := ""
	bootMu.Lock()
	if len(images) > 0 {
		label = images[0].Label()
	}
	bootMu.Unlock()
	return map[string]any{"remaining": left, "label": label}
}

func autobootCancel([]byte) (any, error) {
	cdMu.Lock()
	was := remaining
	cdMu.Unlock()
	if was >= 0 {
		log.Printf("autoboot cancelled with %d s left", was)
	}
	stopCountdown()
	return nil, nil
}

func bootListData() map[string]any {
	bootMu.Lock()
	defer bootMu.Unlock()
	entries := []entryJSON{}
	for _, img := range images {
		detail := ""
		if li, ok := img.(*boot.LinuxImage); ok {
			detail = li.Cmdline
		}
		entries = append(entries, entryJSON{Label: img.Label(), Detail: detail})
	}
	return map[string]any{"scanning": !scanned, "entries": entries}
}

func bootList([]byte) (any, error) { return bootListData(), nil }

func bootStart(args []byte) (any, error) {
	a, err := decode[struct{ Index int }](args)
	if err != nil {
		return nil, err
	}
	stopCountdown()
	return nil, startEntry(a.Index)
}

// startEntry loads an entry and kexecs it; it returns only on failure.
func startEntry(i int) error {
	bootMu.Lock()
	if booting {
		bootMu.Unlock()
		return errors.New("a boot is already in progress")
	}
	if i < 0 || i >= len(images) {
		bootMu.Unlock()
		return fmt.Errorf("no boot entry %d", i)
	}
	img := images[i]
	booting = true
	bootMu.Unlock()
	defer func() {
		bootMu.Lock()
		booting = false
		bootMu.Unlock()
		broadcast("busy", map[string]string{"text": ""})
	}()

	log.Printf("booting %s", img)
	broadcast("busy", map[string]string{"text": "Loading " + img.Label() + "…"})
	if err := img.Load(boot.WithLogger(ulog.Log)); err != nil {
		return fmt.Errorf("load %s: %w", img.Label(), err)
	}
	if *scanDir != "" {
		return errors.New("loaded; not handing over with -scan")
	}
	broadcast("busy", map[string]string{"text": "Starting " + img.Label() + "…"})
	stopUI()
	stopServices()
	unmountBrowsed()
	if err := mountPool.UnmountAll(mount.MNT_DETACH); err != nil {
		log.Printf("unmount: %v", err)
	}
	unix.Sync()
	err := boot.Execute()
	return fmt.Errorf("kexec returned: %w", err)
}
