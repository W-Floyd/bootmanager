package distro

import (
	"strings"

	"bootmanager.local/bootmanager/internal/initrd"
)

// keyFile is where the pmOS initramfs finds the passphrase.
const keyFile = "bman-unlock.key"

// PostmarketOS: postmarketOS names its root by pmos_root_uuid= (the LUKS
// UUID, for an encrypted one), and its initramfs opens the root with the key
// file a cryptkey= names before it asks anyone
// (postmarketos-initramfs, unlock_root_partition).
type PostmarketOS struct{}

func (PostmarketOS) Name() string { return "postmarketOS" }

func (PostmarketOS) EncryptedRoot(cmdline string) string {
	return strings.ToLower(Value(cmdline, "pmos_root_uuid"))
}

// isPostmarketOS: a command line that names a pmOS root.
func isPostmarketOS(cmdline string) bool {
	return HasWord(cmdline, "pmos_root_uuid") || HasWord(cmdline, "pmos_boot_uuid")
}

// Options: the splash. postmarketOS shows plymouth's, when its initramfs and
// its systemd units are not told otherwise by pmos.nosplash and
// plymouth.enable=0.
func (PostmarketOS) Options() []Option {
	return []Option{{
		ID:      "splash",
		Title:   "Boot splash",
		Detail:  "On shows the splash screen; off shows the console",
		Applies: isPostmarketOS,
		On:      Edit{Remove: []string{"pmos.nosplash", "plymouth.enable"}},
		Off:     Edit{Remove: []string{"pmos.nosplash", "plymouth.enable"}, Add: []string{"pmos.nosplash", "plymouth.enable=0"}},
	}}
}

func (PostmarketOS) UnlockKey(pass []byte) ([]initrd.File, []string) {
	return []initrd.File{{Path: keyFile, Mode: 0o400, Data: pass}}, []string{"cryptkey=/" + keyFile}
}
