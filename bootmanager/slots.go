package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	qbootctl "github.com/W-Floyd/go-qbootctl"

	"bootmanager.local/bootmanager/internal/boot"
	"bootmanager.local/bootmanager/internal/rpc"
)

var markSuccessful = flag.Bool("mark-successful", false, "mark the booted A/B slot successful once the stage is ready, as Android does once it is up, so the bootloader stops counting its tries down")

// bootloaderInfo is what the Diagnostics screen shows of the bootloader's
// bootconfig.
var bootloaderInfo = []struct{ key, title string }{
	{"androidboot.bootloader", "Version"},
	{"androidboot.slot_suffix", "Booted slot"},
	{"androidboot.bootreason", "Boot reason"},
	{"androidboot.verifiedbootstate", "Verified boot state"},
	{"androidboot.serialno", "Serial number"},
}

type infoRow struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type slotsInfo struct {
	Slots      []qbootctl.Slot `json:"slots"`
	SlotsError string          `json:"slots_error"`
	Bootloader []infoRow       `json:"bootloader"`
}

func newSlots() *qbootctl.Store {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	slot := bootedSlot(boot.Bootconfig(*blDir), string(cmdline))
	return &qbootctl.Store{Open: qbootctl.OpenDisks, Booted: slot}
}

// bootedSlot is the slot the bootloader booted ("a"): from the bootconfig it
// handed the stage if that has one, else from the kernel command line, where
// ABL also puts androidboot.slot_suffix. Without any slot, nothing is marked.
func bootedSlot(bootconfig map[string]string, cmdline string) string {
	if slot, _ := strings.CutPrefix(bootconfig["androidboot.slot_suffix"], "_"); slot != "" {
		return slot
	}
	return qbootctl.BootedSlot(cmdline)
}

func (a *app) slotsInfo() slotsInfo {
	info := slotsInfo{Slots: []qbootctl.Slot{}, Bootloader: []infoRow{}}
	var err error
	if info.Slots, err = a.slots.List(); err != nil {
		info.Slots, info.SlotsError = []qbootctl.Slot{}, err.Error()
	}
	bc := boot.Bootconfig(*blDir)
	for _, k := range bootloaderInfo {
		if v := bc[k.key]; v != "" {
			info.Bootloader = append(info.Bootloader, infoRow{k.title, v})
		}
	}
	return info
}

func (a *app) registerSlots() {
	rpc.Handle(a.rpc, "slots.info", func(none) (any, error) { return a.slotsInfo(), nil })
	rpc.Handle(a.rpc, "slots.set", func(x struct{ Slot, Action string }) (any, error) {
		var err error
		switch x.Action {
		case "active":
			err = a.slots.SetActive(x.Slot)
		case "successful":
			err = a.slots.MarkSuccessful(x.Slot)
		case "unbootable":
			err = a.slots.MarkUnbootable(x.Slot)
		default:
			err = fmt.Errorf("unknown slot action %q", x.Action)
		}
		if err != nil {
			return nil, err
		}
		log.Printf("slot %s: %s", x.Slot, x.Action)
		return a.slotsInfo(), nil
	})
}

// markBootedSuccessful is -mark-successful.
func (a *app) markBootedSuccessful() {
	list, err := a.slots.List()
	if err != nil {
		log.Printf("mark-successful: %v", err)
		return
	}
	for _, sl := range list {
		if sl.Booted && !sl.Successful {
			if err := a.slots.MarkSuccessful(sl.Name); err != nil {
				log.Printf("mark-successful: %v", err)
				return
			}
			log.Printf("slot %s marked successful", sl.Name)
		}
	}
}
