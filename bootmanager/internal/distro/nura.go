package distro

import (
	"strings"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/initrd"
)

// keyFile is where the pmOS initramfs finds the passphrase.
const keyFile = "bman-unlock.key"

// Nura (formerly postmarketOS) names its root by pmos_root_uuid= (the LUKS
// UUID, for an encrypted one), and its initramfs opens the root with the key
// file a cryptkey= names before it asks anyone
// (postmarketos-initramfs, unlock_root_partition). The words keep the old
// name, as does the upstream code this follows.
type Nura struct{}

func (Nura) Name() string { return "Nura" }

func (Nura) EncryptedRoot(cmdline string) string {
	return strings.ToLower(Value(cmdline, "pmos_root_uuid"))
}

// isNura: a command line that names a pmOS root.
func isNura(cmdline string) bool {
	return HasWord(cmdline, "pmos_root_uuid") || HasWord(cmdline, "pmos_boot_uuid")
}

// Options: the splash. postmarketOS's initramfs shows plymouth's only when
// the word `splash` is on the command line (init_functions.sh: nosplash
// defaults to y, `splash` sets it n), and plymouth in the booted system also
// falls back to a text view without it. plymouth.enable=0 keeps plymouth off
// altogether, which is also what skips its start-up in the system. (A
// `pmos.nosplash` word, which older command lines carry, is not read by
// anything: it is taken off either way.)
func (Nura) Options() []Option {
	return []Option{{
		ID:      "splash",
		Title:   "Boot splash",
		Detail:  "On shows the splash screen; off shows the console",
		Applies: isNura,
		On:      Edit{Remove: []string{"pmos.nosplash", "plymouth.enable", "splash"}, Add: []string{"splash"}},
		Off:     Edit{Remove: []string{"pmos.nosplash", "plymouth.enable", "splash"}, Add: []string{"plymouth.enable=0"}},
	}}
}

func (Nura) UnlockKey(pass []byte) ([]initrd.File, []string) {
	return []initrd.File{{Path: keyFile, Mode: 0o400, Data: pass}}, []string{"cryptkey=/" + keyFile}
}
