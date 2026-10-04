package storage

import (
	"os"
	"path/filepath"
	"strings"
)

// A device-mapper device (an unlocked LUKS volume) is dm-N, with its name
// under dm/ and the devices it is made of under slaves/.

func isDM(dev string) bool { return strings.HasPrefix(dev, "dm-") }

// DMName is the mapper name of dm-N ("" for any other device).
func DMName(dev string) string {
	b, _ := os.ReadFile(filepath.Join("/sys/class/block", dev, "dm", "name"))
	return strings.TrimSpace(string(b))
}

// DMDevice finds the dm-N device with mapper name name, "" if none.
func DMDevice(name string) string {
	ents, _ := os.ReadDir("/sys/class/block")
	for _, e := range ents {
		if isDM(e.Name()) && DMName(e.Name()) == name {
			return e.Name()
		}
	}
	return ""
}

// removableDM: a mapped device is removable when what it is made of is, so
// the volume unlocked from an SD card is offered repair like the card.
func removableDM(dev string, depth int) bool {
	if depth > 4 {
		return false
	}
	ents, _ := os.ReadDir(filepath.Join("/sys/class/block", dev, "slaves"))
	for _, e := range ents {
		if isDM(e.Name()) {
			if removableDM(e.Name(), depth+1) {
				return true
			}
		} else if removable(e.Name()) {
			return true
		}
	}
	return false
}
