package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"bootmanager.local/bootmanager/internal/panel"
	"bootmanager.local/bootmanager/internal/power"
	"bootmanager.local/bootmanager/internal/rpc"
)

// protocolVersion changes whenever a command or event changes shape; the
// UI refuses a backend of another version.
const protocolVersion = 2

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
		return map[string]any{"version": protocolVersion, "files_port": filesPort, "model": model()}, nil
	})
	rpc.Handle(s, "boot.list", func(none) (any, error) { return a.boot.List(), nil })
	rpc.Handle(s, "boot.start", func(x struct{ Index int }) (any, error) { return nil, a.boot.Start(x.Index) })
	rpc.Handle(s, "autoboot.cancel", func(none) (any, error) { a.boot.Cancel(); return nil, nil })
	rpc.Handle(s, "service.list", func(none) (any, error) { return a.svcs.List(), nil })
	rpc.Handle(s, "service.set", func(x struct {
		Name    string
		Running bool
	}) (any, error) {
		err := a.svcs.Set(x.Name, x.Running)
		list := a.svcs.List()
		s.Broadcast("services", list)
		return list, err
	})
	rpc.Handle(s, "disk.list", func(none) (any, error) { return a.disks.Disks() })
	rpc.Handle(s, "fs.list", func(x fileArgs) (any, error) { return a.disks.List(x.Dev, x.Path) })
	rpc.Handle(s, "fs.read", func(x fileArgs) (any, error) { return a.disks.Read(x.Dev, x.Path) })
	rpc.Handle(s, "fs.thumb", func(x fileArgs) (any, error) {
		if x.Width == 0 && x.Height == 0 {
			x.Width, x.Height = x.Size, x.Size
		}
		return a.disks.Thumb(x.Dev, x.Path, x.Width, x.Height)
	})
	rpc.Handle(s, "display.info", func(none) (any, error) { return displayInfo() })
	rpc.Handle(s, "log.tail", func(x struct{ Lines int }) (any, error) { return a.logTail(x.Lines), nil })
	rpc.Handle(s, "power.modes", func(none) (any, error) { return power.Modes(allowedModes()), nil })
	rpc.Handle(s, "power", a.power)
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
