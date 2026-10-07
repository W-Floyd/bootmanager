package boot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDescribeDev(t *testing.T) {
	sys := t.TempDir()
	sysBlock = sys
	defer func() { sysBlock = "/sys/class/block" }()
	mk := func(rel, content string) {
		p := filepath.Join(sys, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("sde27/uevent", "MAJOR=259\nPARTNAME=userdata\nDEVTYPE=partition\n")
	mk("sdd5/uevent", "DEVTYPE=partition\n")
	mk("dm-0/slaves/sde27", "")
	mk("dm-1/slaves/dm-0", "")
	mk("dm-1/slaves/sde27", "")
	for dev, want := range map[string]string{
		"mmcblk0p1": "microSD card",
		"sde27":     "UFS userdata",
		"sdd5":      "UFS sdd5",
		"dm-0":      "UFS userdata (encrypted)",
		"dm-9":      "dm-9 (mapped)",
		"nvme0n1":   "nvme0n1",
	} {
		if got := describeDev(dev, 0); got != want {
			t.Errorf("describeDev(%q) = %q, want %q", dev, got, want)
		}
	}
	m := mountedDev.FindStringSubmatch("Kernel: file:///tmp/u-root-mounts558220486/dm-0/boot/vmlinuz")
	if len(m) != 2 || m[1] != "dm-0" {
		t.Errorf("mountedDev: %v", m)
	}
}
