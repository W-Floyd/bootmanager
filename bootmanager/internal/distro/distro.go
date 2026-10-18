// Package distro is what the stage knows about the system it boots, kept in
// one place so the rest of it need not: how a system's kernel command line
// names its encrypted root, and how a passphrase the stage was given for it
// reaches that system's initramfs (each distribution's initramfs has its own
// hook for a key). postmarketOS is the first; another distribution is one
// more type in All, with its tests.
package distro

import (
	"strings"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/initrd"
)

// Distro is a family of systems the stage can boot.
type Distro interface {
	// Name is for logs: "postmarketOS".
	Name() string
	// EncryptedRoot is the LUKS UUID, lower case, of the encrypted root
	// a kernel command line names, "" when it names none (or is not this
	// distribution's).
	EncryptedRoot(cmdline string) string
	// UnlockKey says how to hand the system the passphrase of that root:
	// files for its initramfs, and words to add to its command line. A
	// system's own unlocker asks for the passphrase when the key does not
	// open the root, so a wrong key costs only the prompt.
	UnlockKey(pass []byte) (files []initrd.File, cmdline []string)
	// Options are the command-line switches particular to this
	// distribution's systems (a splash screen, say), each with an Applies
	// that tells its systems from others.
	Options() []Option
}

// All are the distributions known, tried in order.
var All = []Distro{PostmarketOS{}}

// For finds the distribution whose encrypted root a command line names, nil
// for none.
func For(cmdline string) Distro {
	for _, d := range All {
		if d.EncryptedRoot(cmdline) != "" {
			return d
		}
	}
	return nil
}

// Value is the value of key=value on a kernel command line, "" if there is
// none.
func Value(cmdline, key string) string {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

// HasWord reports whether a command line has the word (key, or key=value)
// whose key is key.
func HasWord(cmdline, key string) bool {
	for _, f := range strings.Fields(cmdline) {
		if f == key || strings.HasPrefix(f, key+"=") {
			return true
		}
	}
	return false
}
