package main

import (
	"cmp"
	"flag"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"bootmanager.local/bootmanager/internal/boot"
	"bootmanager.local/bootmanager/internal/fastboot"
)

var fastbootGadget = flag.String("fastboot-gadget", "", "configfs USB gadget the Fastboot service adds its function to; empty: the one bound to a UDC")

// maxDownload: a download is held in RAM; the host sends a larger image
// as sparse pieces of at most this.
const maxDownload = 256 << 20

// rescanDelay: the host flashes a large image as several pieces, each its
// own flash; the boot entries are found again once they stop coming.
const rescanDelay = 2 * time.Second

func (a *app) startFastboot() (func(), error) {
	// Switching it on means staying in the stage.
	a.boot.CancelBy("switching fastboot on")
	var mu sync.Mutex
	var rescan *time.Timer
	flash := newFlashScreen(a.rpc.Broadcast)
	s := &fastboot.Server{
		MaxDownload: maxDownload,
		Vars:        map[string]string{"product": model()},
		Partitions:  fastboot.Partitions,
		Open:        fastboot.Open,
		Slots:       a.slots,
		BeforeWrite: func(fastboot.Partition) error {
			mu.Lock()
			if rescan != nil {
				rescan.Stop()
			}
			mu.Unlock()
			a.disks.UnmountAll()
			return a.boot.Release()
		},
		AfterWrite: func(fastboot.Partition) {
			mu.Lock()
			defer mu.Unlock()
			if rescan != nil {
				rescan.Stop()
			}
			rescan = time.AfterFunc(rescanDelay, a.boot.Rescan)
		},
		Busy:     func(text string) { a.rpc.Broadcast("busy", map[string]string{"text": text}) },
		Progress: flash.Progress,
		Reboot: func(mode string) {
			if _, err := a.power(struct{ Action, Mode string }{"reboot", mode}); err != nil {
				log.Printf("fastboot reboot: %v", err)
				a.rpc.Broadcast("error", map[string]string{"message": err.Error()})
			}
		},
		Continue: func() {
			if err := a.boot.Start(0); err != nil {
				log.Printf("fastboot continue: %v", err)
				a.rpc.Broadcast("error", map[string]string{"message": err.Error()})
			}
		},
	}
	bc := boot.Bootconfig(*blDir)
	if slot, ok := strings.CutPrefix(bc["androidboot.slot_suffix"], "_"); ok {
		s.Vars["current-slot"] = slot
		s.Vars["slot-count"] = "2"
	}
	if serial := cmp.Or(bc["androidboot.serialno"], dtString("serial-number")); serial != "" {
		s.Vars["serialno"] = serial
	}
	stop, err := fastboot.Start(*fastbootGadget, s)
	if err != nil {
		return nil, err
	}
	return func() {
		// Switching it off stops a flash in progress, now: the emergency
		// stop of the flash screen is this, a service set to off.
		busy := flash.Active()
		s.Abort()
		stop()
		flash.Reset()
		if busy {
			a.rpc.Broadcast("error", map[string]string{"message": "Fastboot stopped during a flash: the partition may be half written"})
		}
	}, nil
}

// dtString is a string property of the device tree's root, "" without it.
func dtString(name string) string {
	b, _ := os.ReadFile("/proc/device-tree/" + name)
	return strings.TrimRight(string(b), "\x00\n")
}
