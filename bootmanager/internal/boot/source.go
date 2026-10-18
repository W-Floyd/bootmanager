package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/u-root/u-root/pkg/boot"
)

// sysBlock is where the kernel lists block devices; a variable for the tests.
var sysBlock = "/sys/class/block"

// mountedDev finds the device a scan mounted an entry's files from: u-root
// mounts each under /tmp/u-root-mountsN/<kernel name>/.
var mountedDev = regexp.MustCompile(`/u-root-mounts\d+/([^/\s]+)/`)

// sourced is an entry that knows where it was found: those that cannot be
// told by the path their files were mounted at.
type sourced interface{ source() string }

// sourceOf says where an entry was found, for the card that shows it: a
// microSD card, a UFS partition by its GPT name, an unlocked volume as what it
// is made of. "" when it is not known (an Android image, read raw).
func sourceOf(img boot.OSImage) string {
	if s, ok := img.(sourced); ok {
		return s.source()
	}
	dev := entryDev(img)
	if dev == "" {
		return ""
	}
	return describeDev(dev, 0)
}

// entryDev is the kernel name of the device an entry's files were mounted
// from ("sdc3", "dm-0"), "" when it is not known.
func entryDev(img boot.OSImage) string {
	m := mountedDev.FindStringSubmatch(fmt.Sprint(img))
	if m == nil {
		return ""
	}
	return m[1]
}

// describeDev names the device dev for a person: "microSD card", "UFS
// userdata", and for a mapped (unlocked) volume what it is made of, with
// "(encrypted)".
func describeDev(dev string, depth int) string {
	if depth > 4 {
		return dev
	}
	switch {
	case strings.HasPrefix(dev, "dm-"):
		slaves, _ := os.ReadDir(filepath.Join(sysBlock, dev, "slaves"))
		if len(slaves) == 0 {
			return dev + " (mapped)"
		}
		return describeDev(slaves[0].Name(), depth+1) + " (encrypted)"
	case strings.HasPrefix(dev, "mmcblk"):
		return "microSD card"
	case strings.HasPrefix(dev, "sd"):
		if name := partName(dev); name != "" {
			return "UFS " + name
		}
		return "UFS " + dev
	}
	return dev
}

// partName is the GPT name of a partition (PARTNAME in its uevent), "" for
// none.
func partName(dev string) string {
	b, err := os.ReadFile(filepath.Join(sysBlock, dev, "uevent"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PARTNAME="); ok {
			return v
		}
	}
	return ""
}
