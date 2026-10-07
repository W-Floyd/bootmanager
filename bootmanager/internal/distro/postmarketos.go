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

func (PostmarketOS) UnlockKey(pass []byte) ([]initrd.File, []string) {
	return []initrd.File{{Path: keyFile, Mode: 0o400, Data: pass}}, []string{"cryptkey=/" + keyFile}
}
