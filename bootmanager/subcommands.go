package main

import (
	"io"
	"path/filepath"

	msmfw "github.com/W-Floyd/go-msm-firmware-loader"
	qbootctl "github.com/W-Floyd/go-qbootctl"
)

// subcommands are the multi-call programs: `bootmanager NAME [args]`, or
// the binary run under the name NAME (a symlink), whose args are all of its
// own. They take their own flags, so main handles them before flag.Parse.
var subcommands = map[string]func(args []string, stdout, stderr io.Writer) int{
	"msm-firmware-loader": msmfw.Main,
	"qbootctl":            qbootctl.Main,
}

// subcommand runs the subcommand argv names, if any: ok is false when argv
// is the Boot Manager's own command line.
func subcommand(argv []string, stdout, stderr io.Writer) (code int, ok bool) {
	if f, found := subcommands[filepath.Base(argv[0])]; found {
		return f(argv[1:], stdout, stderr), true
	}
	if len(argv) > 1 {
		if f, found := subcommands[argv[1]]; found {
			return f(argv[2:], stdout, stderr), true
		}
	}
	return 0, false
}
