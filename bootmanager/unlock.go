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
// volumeUUID is a LUKS partition's UUID by kernel name ("sde27"), "" for
// anything else or when this stage cannot read it.
func volumeUUID(dev string) string {
	if !luks.Available(*cryptsetupBin) {
		return ""
	}
	uuid, err := luks.Runner{Bin: *cryptsetupBin}.UUID(dev)
	if err != nil {
		return ""
	}
	return uuid
}

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
		// initramfs: keep it for that boot (unlockKeyFor), in memory only.
		runner := luks.Runner{Bin: *cryptsetupBin}
		uuid, err := runner.UUID(x.Dev)
		if err == nil {
			a.keys.Put(x.Dev, uuid, []byte(x.Passphrase))
		} else {
			log.Printf("unlock %s: no LUKS UUID (%v): its boot will ask for the passphrase again", x.Dev, err)
		}
		// An encrypted install keeps its kernel and boot entries inside
		// the volume (a single-partition image has nowhere else to put
		// them): look for them now it is open. No countdown starts on
		// the entries that turn up, as one does on a boot, unless this
		// unlock is the one the autoboot asked for: then its default
		// entry is booted once they are in.
		bootAfter := a.boot.TakeUnlockBoot(uuid)
		a.boot.CancelBy("unlocking an encrypted partition")
		go func() {
			a.boot.Rescan()
			if bootAfter {
				a.boot.BootDefault()
			}
		}()
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
