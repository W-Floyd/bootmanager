package main

import (
	"errors"
	"flag"
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
		return nil, luks.Runner{Bin: *cryptsetupBin}.Lock(x.Dev)
	}))
}
