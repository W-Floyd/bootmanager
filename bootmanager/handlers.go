package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"bootmanager.local/bootmanager/internal/fsck"
	"bootmanager.local/bootmanager/internal/luks"
	"bootmanager.local/bootmanager/internal/panel"
	"bootmanager.local/bootmanager/internal/power"
	"bootmanager.local/bootmanager/internal/rpc"
)

// protocolVersion changes whenever a command or event changes shape; the
// UI refuses a backend of another version.
const protocolVersion = 7

type none struct{}

type fileArgs struct {
	Dev  string
	Path string
	// fs.thumb's box: Size square, or Width x Height.
	Size, Width, Height int
}

func (a *app) register() {
	s := a.rpc
	rpc.Handle(s, "hello", func(none) (any, error) {
		return map[string]any{"version": protocolVersion, "files_port": filesPort, "model": model(), "fsck": fsck.Available(*fsckDir), "luks": luks.Available(*cryptsetupBin)}, nil
	})
	rpc.Handle(s, "stage.ready", func(none) (any, error) {
		if !a.isReady() {
			log.Printf("stage.ready")
		}
		a.markReady()
		return nil, nil
	})
	rpc.Handle(s, "boot.list", func(none) (any, error) { return a.boot.List(), nil })
	// Cmdline, when set, is the whole command line for this boot only (the
	// editor's, like GRUB's e): nothing is saved.
	rpc.Handle(s, "boot.start", func(x struct {
		Index   int
		Cmdline string
	}) (any, error) {
		if x.Cmdline != "" {
			return nil, a.boot.StartWith(x.Index, x.Cmdline)
		}
		return nil, a.boot.Start(x.Index)
	})
	// An entry's command line as it would boot: for the editor to start from.
	rpc.Handle(s, "boot.cmdline", func(x struct{ Index int }) (any, error) { return a.boot.Cmdline(x.Index) })
	// Saving may remount the partition the settings are on: the browsing
	// mounts go first, as they hold it open.
	rpc.Handle(s, "boot.move", func(x struct{ Index, By int }) (any, error) {
		a.disks.UnmountAll()
		err := a.boot.Move(x.Index, x.By)
		return a.boot.List(), err
	})
	rpc.Handle(s, "boot.default", func(x struct{ Index int }) (any, error) {
		a.disks.UnmountAll()
		err := a.boot.SetDefault(x.Index)
		return a.boot.List(), err
	})
	// A UI says what it saw last: the input event that made it ask, and
	// how long before. That goes in the log, as the cause of the cancel.
	rpc.Handle(s, "autoboot.cancel", func(x struct {
		Reason string `json:"reason"`
		AgeMs  int64  `json:"age_ms"`
	}) (any, error) {
		a.boot.CancelBy(cancelReason(x.Reason, x.AgeMs))
		return nil, nil
	})
	// Whether to autoboot and for how long, from the next start. Saving
	// may remount the partition the settings are on, as boot.move does.
	rpc.Handle(s, "autoboot.settings", func(none) (any, error) { return a.boot.AutobootSettings(), nil })
	rpc.Handle(s, "autoboot.set", func(x struct {
		Enabled bool
		Seconds int
	}) (any, error) {
		a.disks.UnmountAll()
		err := a.boot.SetAutoboot(x.Enabled, x.Seconds)
		return a.boot.AutobootSettings(), err
	})
	// The command-line options kept for every boot (a splash, quiet): their
	// states, and one changed. Saving remounts like autoboot.set.
	rpc.Handle(s, "boot.options", func(none) (any, error) { return a.boot.Options(), nil })
	rpc.Handle(s, "boot.option.set", func(x struct {
		ID    string
		State string
	}) (any, error) {
		a.disks.UnmountAll()
		err := a.boot.SetOption(x.ID, x.State)
		return a.boot.Options(), err
	})
	rpc.Handle(s, "service.list", func(none) (any, error) { return a.svcs.List(), nil })
	rpc.Handle(s, "service.set", afterReady(a, func(x struct {
		Name    string
		Running bool
	}) (any, error) {
		err := a.svcs.Set(x.Name, x.Running)
		list := a.svcs.List()
		s.Broadcast("services", list)
		return list, err
	}))
	rpc.Handle(s, "disk.list", afterReady(a, func(none) (any, error) { return a.disks.Disks() }))
	rpc.Handle(s, "fs.list", afterReady(a, func(x fileArgs) (any, error) { return a.disks.List(x.Dev, x.Path) }))
	rpc.Handle(s, "fs.read", afterReady(a, func(x fileArgs) (any, error) { return a.disks.Read(x.Dev, x.Path) }))
	rpc.Handle(s, "fs.thumb", afterReady(a, func(x fileArgs) (any, error) {
		if x.Width == 0 && x.Height == 0 {
			x.Width, x.Height = x.Size, x.Size
		}
		return a.disks.Thumb(x.Dev, x.Path, x.Width, x.Height)
	}))
	a.registerFsck()
	a.registerUnlock()
	rpc.Handle(s, "display.info", func(none) (any, error) { return displayInfo() })
	rpc.Handle(s, "log.tail", func(x struct{ Lines int }) (any, error) { return a.logTail(x.Lines), nil })
	rpc.Handle(s, "power.modes", func(none) (any, error) { return power.Modes(allowedModes()), nil })
	rpc.Handle(s, "power", a.power)
}

// afterReady refuses f until stage.ready: the disks are the card the stage
// still has mounted read-write, and the services need its keys and usb0.
func afterReady[A any](a *app, f func(A) (any, error)) func(A) (any, error) {
	return func(x A) (any, error) {
		if !a.isReady() {
			return nil, errNotReady
		}
		return f(x)
	}
}

func allowedModes() []string { return strings.Split(*rebootModes, ",") }

// model is the device tree's name for the device, "" without one.
func model() string {
	b, _ := os.ReadFile("/proc/device-tree/model")
	return strings.TrimRight(string(b), "\x00\n")
}

func displayInfo() (any, error) {
	compat := strings.Fields(*compatible)
	if len(compat) == 0 {
		compat = panel.Compatibles()
	}
	p, c, err := panel.Lookup(compat, *panelFile)
	return map[string]any{"compatible": c, "panel": p}, err
}

func (a *app) logTail(n int) []string {
	if n <= 0 {
		n = 200
	}
	var lines []string
	for _, p := range a.logFiles() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines = append(lines, "== "+p)
		lines = append(lines, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	}
	return lines[max(0, len(lines)-n):]
}

// power reboots, into a device-tree reboot mode when one is given, or
// powers off.
func (a *app) power(x struct{ Action, Mode string }) (any, error) {
	var busy string
	switch x.Action {
	case "reboot":
		if err := power.Check(x.Mode, allowedModes()); err != nil {
			return nil, err
		}
		busy = "Rebooting…"
		if x.Mode != "" {
			busy = "Rebooting to " + x.Mode + "…"
		}
	case "poweroff":
		busy = "Powering off…"
	default:
		return nil, fmt.Errorf("unknown power action %q", x.Action)
	}
	if *scanDir != "" {
		return nil, errors.New("not with -scan")
	}
	a.rpc.Broadcast("busy", map[string]string{"text": busy})
	a.teardown()
	if x.Action == "poweroff" {
		return nil, power.PowerOff()
	}
	return nil, power.Reboot(x.Mode)
}

// cancelReason is the cause of an autoboot.cancel for the log: what the UI
// sent, or that it sent nothing (an older UI, or a script).
func cancelReason(reason string, ageMs int64) string {
	if reason == "" {
		return "rpc autoboot.cancel (no reason given)"
	}
	reason = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, reason)
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return fmt.Sprintf("ui: %s, input %d ms earlier", reason, ageMs)
}
