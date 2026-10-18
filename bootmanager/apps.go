package main

import (
	"errors"
	"flag"
	"strings"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/boot"
)

// appFlags is -app, once per tile.
type appFlags []boot.AppSpec

func (f *appFlags) String() string { return "" }

// Set takes LABEL=PROGRAM ARGS: the program and its arguments split on
// spaces, as in -rekexec.
func (f *appFlags) Set(v string) error {
	label, cmd, ok := strings.Cut(v, "=")
	argv := strings.Fields(cmd)
	if !ok || label == "" || len(argv) == 0 {
		return errors.New("want LABEL=PROGRAM ARGS")
	}
	*f = append(*f, boot.AppSpec{Label: label, Detail: "App", Argv: argv})
	return nil
}

var apps appFlags

func init() {
	flag.Var(&apps, "app", "a tile that runs a program instead of booting a kernel, as LABEL=PROGRAM ARGS; may be given more than once. The UI is down while it runs")
}
