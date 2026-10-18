package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/boot"
)

var findBootFiles = flag.Bool("find-boot-files", false, "print where the stage keeps its files and exit: DEV FSTYPE DIR, the first block device (kernel name) whose file system has a linuxboot directory at its root or under boot/, a microSD card's partitions first; a client, not the backend")

// cardWait is how long to give a microSD card to appear before looking
// without it. It probes asynchronously, about a second after the kernel
// starts, and a stage with no card should not wait long for one.
const cardWait = 2500 * time.Millisecond

// cardNode is the card's first partition: its appearing means the card is up
// and has been scanned for partitions.
const cardNode = "/dev/mmcblk0p1"

func findBootFilesCmd() int {
	for deadline := time.Now().Add(cardWait); time.Now().Before(deadline); {
		if _, err := os.Stat(cardNode); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	dev, fstype, dir, err := boot.FindBootFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "find-boot-files:", err)
		return 1
	}
	fmt.Println(dev, fstype, dir)
	return 0
}
