package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"bootmanager.local/bootmanager/internal/initrd"
	"bootmanager.local/bootmanager/internal/luks"
)

var (
	initrdFirmware    = flag.String("initrd-firmware", "", "firmware files, by name, to append to every Linux entry's initramfs under lib/firmware/: what the booted system needs before its own userspace can fetch it (the touch controller's, for a passphrase screen you can tap)")
	initrdFirmwareDir = flag.String("initrd-firmware-dir", "/run/msm-firmware-loader/target", "where -initrd-firmware finds those files: the tree of firmware the stage's own loader linked from the device's partitions")
)

// extraInitrd is boot.Config.InitrdExtra: what is appended to a Linux entry's
// initramfs, as one archive (the kernel unpacks archive after archive):
//
//   - the passphrase of its encrypted root, when this stage unlocked that
//     volume (bman-unlock.key): the initramfs's unlocker tries it once and
//     deletes it, and asks for the passphrase itself if it is not there or
//     does not open the root;
//   - the firmware files of -initrd-firmware (lib/firmware/NAME): the stage
//     has read the device's partitions, which the booted system can only do
//     once its root is open, and its unlocker has a touch screen to read the
//     passphrase from only if the controller has its firmware by then.
//
// nil when there is nothing to add.
func (a *app) extraInitrd(cmdline string) []byte {
	var files []initrd.File
	if pass := a.unlockKeyFor(cmdline); pass != nil {
		defer clear(pass)
		files = append(files, initrd.File{Path: luks.KeyFile, Mode: 0o400, Data: pass})
	}
	for _, name := range splitList(*initrdFirmware) {
		data, err := os.ReadFile(filepath.Join(*initrdFirmwareDir, name))
		if err != nil {
			log.Printf("initramfs firmware %s: %v", name, err)
			continue
		}
		files = append(files, initrd.File{Path: "lib/firmware/" + name, Mode: 0o644, Data: data})
	}
	if len(files) == 0 {
		return nil
	}
	b, err := initrd.Build(files)
	if err != nil {
		log.Printf("initramfs extras: %v (the boot goes without)", err)
		return nil
	}
	return b
}
