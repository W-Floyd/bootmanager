package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"bootmanager.local/bootmanager/internal/distro"
	"bootmanager.local/bootmanager/internal/initrd"
)

var (
	initrdFirmware    = flag.String("initrd-firmware", "", "firmware files, by name, to append to every Linux entry's initramfs under lib/firmware/: what the booted system needs before its own userspace can fetch it (the touch controller's, for a passphrase screen you can tap)")
	initrdFirmwareDir = flag.String("initrd-firmware-dir", "/run/msm-firmware-loader/target", "where -initrd-firmware finds those files: the tree of firmware the stage's own loader linked from the device's partitions")
)

// extraInitrd is boot.Config.InitrdExtra: what is appended to a Linux entry's
// initramfs, as one archive (the kernel unpacks archive after archive), and
// the words to add to its command line for it:
//
//   - the passphrase of its encrypted root, when this stage unlocked that
//     volume. How the system takes it is the distribution's (internal/distro:
//     a file and a command-line word its initramfs looks for); its own
//     unlocker asks for the passphrase if the key is not there or does not
//     open the root;
//   - the firmware files of -initrd-firmware (lib/firmware/NAME): the stage
//     has read the device's partitions, which the booted system can only do
//     once its root is open, and its unlocker has a touch screen to read the
//     passphrase from only if the controller has its firmware by then.
//
// nil when there is nothing to add.
func (a *app) extraInitrd(cmdline string) ([]byte, []string) {
	var files []initrd.File
	var words []string
	if d := distro.For(cmdline); d != nil {
		if pass := a.keys.Take(d.EncryptedRoot(cmdline)); pass != nil {
			defer clear(pass)
			f, w := d.UnlockKey(pass)
			files, words = append(files, f...), append(words, w...)
			log.Printf("unlock key for the %s system on %s", d.Name(), strings.TrimSpace(d.EncryptedRoot(cmdline)))
		}
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
		return nil, nil
	}
	b, err := initrd.Build(files)
	if err != nil {
		log.Printf("initramfs extras: %v (the boot goes without)", err)
		return nil, nil
	}
	return b, words
}
