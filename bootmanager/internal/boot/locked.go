package boot

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/mount/block"

	"bootmanager.local/bootmanager/internal/storage"
)

// Locked is an encrypted volume that is not open: the boot entries it may hold
// (an encrypted install keeps its kernel inside) cannot be found until it is
// unlocked, so the carousel offers it as a tile of its own.
type Locked struct {
	// Dev is the kernel name of the partition (sde27).
	Dev string `json:"dev"`
	// Name is its GPT name (userdata), or Dev without one.
	Name string `json:"name"`
	// Source is where it is: "UFS userdata", "microSD card".
	Source string `json:"source"`
	// UUID is the volume's LUKS UUID, "" when it could not be read.
	UUID string `json:"uuid,omitempty"`
}

// minLUKS: a LUKS2 header alone is 16 MiB, so nothing smaller is a volume.
const minLUKS = 16 << 20

// findLocked lists the LUKS partitions among devs that no mapped device is
// made of already.
func findLocked(devs block.BlockDevices) []Locked {
	var names []string
	for _, d := range devs {
		names = append(names, d.Name)
	}
	return lockedAmong(names, isLUKS)
}

// isLUKS reads the head of a block device for a LUKS header.
func isLUKS(dev string) bool {
	if sizeBytes(dev) < minLUKS {
		return false
	}
	f, err := os.Open("/dev/" + dev)
	if err != nil {
		return false
	}
	defer f.Close()
	return storage.SuperblockType(f) == storage.LUKS
}

func sizeBytes(dev string) int64 {
	b, err := os.ReadFile(filepath.Join(sysBlock, dev, "size"))
	if err != nil {
		return 0
	}
	var n int64
	for _, c := range strings.TrimSpace(string(b)) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n * 512
}

// lockedAmong is findLocked with the probe given, for the tests.
func lockedAmong(names []string, luks func(dev string) bool) []Locked {
	open := mappedFrom()
	var out []Locked
	for _, dev := range names {
		if open[dev] || !luks(dev) {
			continue
		}
		name := partName(dev)
		if name == "" {
			name = dev
		}
		out = append(out, Locked{Dev: dev, Name: name, Source: describeDev(dev, 0)})
	}
	return out
}

// mappedFrom is the devices some mapped (dm-N) device is made of.
func mappedFrom() map[string]bool {
	m := map[string]bool{}
	dms, _ := filepath.Glob(filepath.Join(sysBlock, "dm-*"))
	for _, dm := range dms {
		slaves, _ := os.ReadDir(filepath.Join(dm, "slaves"))
		for _, s := range slaves {
			m[s.Name()] = true
		}
	}
	return m
}
