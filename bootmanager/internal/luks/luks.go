// Package luks unlocks and locks LUKS containers with cryptsetup, which a
// stage packs as a separate binary. Unlocking is for rescue: the volume
// shows up as a dm-N device the Boot Manager can browse and check like any
// partition. Booting an encrypted postmarketOS needs none of it, as the
// pmOS initramfs asks for the passphrase itself.
package luks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"bootmanager.local/bootmanager/internal/storage"
)

// ErrWrongPassphrase: cryptsetup's exit code 2, "no key available".
var ErrWrongPassphrase = errors.New("wrong passphrase")

// Available reports whether the cryptsetup binary is there. Unlocking is an
// optional feature, and the UI leaves it out without it.
func Available(bin string) bool {
	if bin == "" {
		return false
	}
	_, err := os.Stat(bin)
	return err == nil
}

type Runner struct {
	Bin string
}

// MapperName is what dev's volume is called: luks-<dev>.
func MapperName(dev string) string { return "luks-" + dev }

func (r Runner) run(stdin []byte, args ...string) ([]byte, int, error) {
	cmd := exec.Command(r.Bin, args...)
	// No udev in the stage: without this libdevmapper waits for a udev
	// cookie nobody completes, and makes the node itself.
	cmd.Env = append(os.Environ(), "DM_DISABLE_UDEV=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), 0, err
}

// Unlock opens /dev/<dev> with the passphrase and returns the dm-N device
// that appears. The passphrase goes over stdin, never a command line or a
// log; it is exactly the bytes given, as pmOS's own unlock does.
func (r Runner) Unlock(dev string, passphrase []byte) (string, error) {
	if dev == "" || strings.ContainsAny(dev, "/.") || strings.HasPrefix(dev, "dm-") {
		return "", fmt.Errorf("bad device %q", dev)
	}
	if len(passphrase) == 0 {
		return "", errors.New("empty passphrase")
	}
	name := MapperName(dev)
	if d := storage.DMDevice(name); d != "" {
		return "", fmt.Errorf("%s is already unlocked as %s", dev, d)
	}
	// What pmOS's initramfs passes: the workqueue bypasses are a large win
	// on a phone's CPU. --disable-locks: no /run/cryptsetup in the stage.
	out, code, err := r.run(passphrase, "open", "--disable-locks",
		"--perf-no_read_workqueue", "--perf-no_write_workqueue", "/dev/"+dev, name, "-")
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
	case 2:
		return "", ErrWrongPassphrase
	default:
		return "", fmt.Errorf("cryptsetup: %s", lastLine(out))
	}
	d := storage.DMDevice(name)
	if d == "" {
		return "", fmt.Errorf("unlocked, but no device named %s appeared", name)
	}
	return d, nil
}

// UUID is the LUKS UUID of /dev/<dev>: what a pmOS command line names its
// root by (pmos_root_uuid=). Lower case, as the initramfs compares it.
func (r Runner) UUID(dev string) (string, error) {
	if dev == "" || strings.ContainsAny(dev, "/.") {
		return "", fmt.Errorf("bad device %q", dev)
	}
	out, code, err := r.run(nil, "luksUUID", "/dev/"+dev)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("cryptsetup: %s", lastLine(out))
	}
	return strings.ToLower(strings.TrimSpace(string(out))), nil
}

// Lock closes the volume behind dm-N.
func (r Runner) Lock(dmdev string) error {
	name := storage.DMName(dmdev)
	if name == "" {
		return fmt.Errorf("%s is not a mapped device", dmdev)
	}
	out, code, err := r.run(nil, "close", "--disable-locks", name)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("cryptsetup: %s", lastLine(out))
	}
	return nil
}

func lastLine(b []byte) string {
	ls := strings.Split(strings.TrimSpace(string(b)), "\n")
	return ls[len(ls)-1]
}
