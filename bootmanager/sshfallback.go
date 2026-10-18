package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/boot"
)

var sshFallback = flag.String("ssh-fallback", "", "give sshd what the boot partition did not: the authorized keys kept in the tail of this GPT partition (boot_a), and a host key made now if there is none, then exit; a client, not the backend")

// sshFallbackCmd is run by the stage's uinit after it has looked for the
// boot partition's linuxboot/ directory. With no keys from there (an
// encrypted root is closed when the stage starts) it takes them from the
// partition's tail, where the package's install script keeps a copy, and
// with no host key it makes one for this boot.
func sshFallbackCmd(label string) int {
	code := 0
	if st, err := os.Stat(*sshKeys); err != nil || st.Size() == 0 {
		keys, err := boot.RawKeys(label)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "ssh-fallback: keys from %s: %v\n", label, err)
			code = 1
		case len(keys) == 0:
			fmt.Fprintf(os.Stderr, "ssh-fallback: no keys in %s either, so nobody can log in\n", label)
		default:
			if err := writeKeys(*sshKeys, keys); err != nil {
				fmt.Fprintf(os.Stderr, "ssh-fallback: %v\n", err)
				code = 1
			} else {
				fmt.Fprintf(os.Stderr, "ssh-fallback: keys from %s\n", label)
			}
		}
	}
	if _, err := os.Stat(*sshHostKey); err != nil {
		if err := os.MkdirAll(filepath.Dir(*sshHostKey), 0o755); err == nil {
			err = boot.WriteHostKey(*sshHostKey)
			if err == nil {
				fmt.Fprintf(os.Stderr, "ssh-fallback: made a host key for this boot\n")
			}
		}
		if _, err := os.Stat(*sshHostKey); err != nil {
			fmt.Fprintf(os.Stderr, "ssh-fallback: no host key: %v\n", err)
			code = 1
		}
	}
	return code
}

func writeKeys(path string, keys []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(keys, '\n'), 0o600)
}
