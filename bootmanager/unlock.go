package main

import (
	"errors"
	"flag"
	"log"
	"strings"

	"bootmanager.local/bootmanager/internal/luks"
	"bootmanager.local/bootmanager/internal/rpc"
	"bootmanager.local/bootmanager/internal/storage"
)

var cryptsetupBin = flag.String("cryptsetup", "/usr/libexec/cryptsetup/cryptsetup", "cryptsetup, for unlocking LUKS partitions to browse and check them; unlocking is left out of the UI when it is missing (or this is empty)")

// registerUnlock adds disk.unlock and disk.lock: LUKS volumes opened for
// rescue (browse, Check, Repair), as dm-N devices. Not needed to boot an
// encrypted pmOS: its initramfs asks for the passphrase itself.
func (a *app) registerUnlock() {
	rpc.Handle(a.rpc, "disk.unlock", afterReady(a, func(x struct {
		Dev        string
		Passphrase string
	}) (any, error) {
		if !luks.Available(*cryptsetupBin) {
			return nil, errors.New("this stage cannot unlock encrypted partitions")
		}
		if storage.FSType(x.Dev) != storage.LUKS {
			return nil, errors.New(x.Dev + " is not a LUKS partition")
		}
		dm, err := luks.Runner{Bin: *cryptsetupBin}.Unlock(x.Dev, []byte(x.Passphrase))
		if err != nil {
			return nil, err
		}
		// The system on this volume asks for the passphrase again in its
		// initramfs: keep it for that boot (keyForCmdline), in memory only.
		runner := luks.Runner{Bin: *cryptsetupBin}
		if uuid, err := runner.UUID(x.Dev); err == nil {
			a.keys.Put(x.Dev, uuid, []byte(x.Passphrase))
		} else {
			log.Printf("unlock %s: no LUKS UUID (%v): its boot will ask for the passphrase again", x.Dev, err)
		}
		// An encrypted install keeps its kernel and boot entries inside
		// the volume (a single-partition image has nowhere else to put
		// them): look for them now it is open. No countdown starts on
		// the entries that turn up, as one does on a boot.
		a.boot.CancelBy("unlocking an encrypted partition")
		go a.boot.Rescan()
		return map[string]string{"dev": dm}, nil
	}))
	rpc.Handle(a.rpc, "disk.lock", afterReady(a, func(x struct{ Dev string }) (any, error) {
		if !luks.Available(*cryptsetupBin) {
			return nil, errors.New("this stage cannot unlock encrypted partitions")
		}
		if !strings.HasPrefix(x.Dev, "dm-") {
			return nil, errors.New(x.Dev + " is not an unlocked volume")
		}
		a.disks.Unmount(x.Dev)
		a.keys.Forget(strings.TrimPrefix(storage.DMName(x.Dev), luks.MapperName("")))
		// Its entries go with it: a boot scan holds its own mount of the
		// volume, which has to be let go before the device can close.
		if err := a.boot.Release(); err != nil {
			return nil, err
		}
		defer a.boot.Rescan()
		return nil, luks.Runner{Bin: *cryptsetupBin}.Lock(x.Dev)
	}))
}

// keyForCmdline is boot.Config.InitrdExtra: the archive that carries the
// passphrase of the encrypted root a command line names (pmos_root_uuid=),
// when this stage unlocked that volume; nil otherwise. The key is used once:
// the booted system's initramfs tries it, deletes it, and asks for the
// passphrase itself if it does not open the root.
func (a *app) keyForCmdline(cmdline string) []byte {
	uuid := cmdlineValue(cmdline, "pmos_root_uuid")
	if uuid == "" {
		return nil
	}
	pass := a.keys.Take(strings.ToLower(uuid))
	if pass == nil {
		return nil
	}
	defer clear(pass)
	b, err := luks.KeyInitrd(pass)
	if err != nil {
		log.Printf("unlock key for %s: %v (the boot will ask for it)", uuid, err)
		return nil
	}
	return b
}

// cmdlineValue is the value of key=value on a kernel command line, "" if
// there is none.
func cmdlineValue(cmdline, key string) string {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}
