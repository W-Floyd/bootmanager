package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	// -h of qbootctl prints its usage to stderr and exits 0, touching no disk.
	for _, argv := range [][]string{
		{"bootmanager", "qbootctl", "-h"},
		{"/usr/bin/qbootctl", "-h"},
		{"qbootctl", "-h"},
	} {
		errOut.Reset()
		code, ok := subcommand(argv, &out, &errOut)
		if !ok || code != 0 || !strings.Contains(errOut.String(), "qbootctl [-c|-m|-s|-u|-b|-n|-x]") {
			t.Errorf("%v: ok %v code %d err %q", argv, ok, code, errOut.String())
		}
	}
	// An unknown flag is msm-firmware-loader's usage error (2); nothing is mounted.
	errOut.Reset()
	if code, ok := subcommand([]string{"bootmanager", "msm-firmware-loader", "--no-such-flag"}, &out, &errOut); !ok || code != 2 {
		t.Errorf("msm-firmware-loader: ok %v code %d", ok, code)
	}
	for _, argv := range [][]string{{"bootmanager"}, {"bootmanager", "-send", "x"}, {"bootmanager", "other"}} {
		if _, ok := subcommand(argv, &out, &errOut); ok {
			t.Errorf("%v taken as a subcommand", argv)
		}
	}
}
