package main

import (
	"fmt"
	"io"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/reprobe"
)

// reprobeMain is `bootmanager reprobe [--dry-run]`: ask the kernel to probe
// every device that has no driver, once firmware has appeared for a driver
// that gave up at probe time. It logs each device and a summary to stderr
// and exits 0, 2 for a bad argument, 1 if /sys/bus cannot be read.
func reprobeMain(args []string, stdout, stderr io.Writer) int {
	dry := false
	for _, a := range args {
		if a != "--dry-run" && a != "-dry-run" {
			fmt.Fprintf(stderr, "usage: bootmanager reprobe [--dry-run]\n")
			return 2
		}
		dry = true
	}
	s, err := reprobe.Run("/sys", dry, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "reprobe:", err)
		return 1
	}
	fmt.Fprintln(stderr, s)
	return 0
}
