package main

import (
	"flag"
	"fmt"
	"os"

	"bootmanager.local/bootmanager/internal/firmware"
)

var firmwareManifest = flag.String("firmware", "", "a client: put the files this device's manifest lists in place, from its stock partitions (a logical partition of super, or a partition by its GPT name), then exit; non-zero if any is missing. A file already in place is left alone. See internal/firmware for the format")

func firmwareCmd() int {
	m, err := firmware.LoadManifest(*firmwareManifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "firmware:", err)
		return 2
	}
	if err := firmware.Install(m, firmware.DefaultEnv()); err != nil {
		return 1
	}
	return 0
}
