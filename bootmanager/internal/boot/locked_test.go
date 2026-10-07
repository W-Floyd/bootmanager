package boot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockedAmong(t *testing.T) {
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
	mk("sde27/uevent", "PARTNAME=userdata\n")
	mk("sde28/uevent", "PARTNAME=other\n")
	mk("dm-0/slaves/sde28", "")
	luks := func(dev string) bool { return dev == "sde27" || dev == "sde28" }
	got := lockedAmong([]string{"sdd1", "sde27", "sde28", "mmcblk0p2"}, luks)
	if len(got) != 1 {
		t.Fatalf("%+v, want only sde27 (sde28 is open already, the rest are not LUKS)", got)
	}
	if got[0] != (Locked{Dev: "sde27", Name: "userdata", Source: "UFS userdata"}) {
		t.Errorf("%+v", got[0])
	}
}
