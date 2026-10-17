package main

import (
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"

	"bootmanager.local/bootmanager/internal/rpc"
)

var rekexecCmd = flag.String("rekexec", "", "a command (program and arguments, space separated) that loads another kernel from outside the stage and jumps to it, so a new build can be tried without going back through the vendor system; it only returns on failure, printing why. The UI offers 'Re-kexec' when this is set")

// registerRekexec adds stage.rekexec. Success never returns: the stage is
// replaced, so the caller sees the connection drop.
func (a *app) registerRekexec() {
	rpc.Handle(a.rpc, "stage.rekexec", func(none) (any, error) {
		argv := strings.Fields(*rekexecCmd)
		if len(argv) == 0 {
			return nil, errors.New("this stage cannot re-kexec")
		}
		a.boot.CancelBy("re-kexec")
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if err == nil && msg == "" {
			return nil, nil
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("re-kexec: %s", msg)
	})
}
