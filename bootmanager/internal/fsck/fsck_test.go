package fsck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exit codes seen from the real checkers on damaged images.
func TestStatus(t *testing.T) {
	for _, c := range []struct {
		name string
		f    func(int) string
		code int
		want string
	}{
		{"e2 clean", bitmask, 0, Clean},
		{"e2 check of damage", bitmask, 12, Errors},
		{"e2 corrected", bitmask, 1, Errors},
		{"e2 operational", bitmask, 8, Failed},
		{"e2 usage", bitmask, 16, Failed},
		{"exfat check of damage", bitmask, 4, Errors},
		{"fat found", fat, 1, Errors},
		{"fat usage", fat, 2, Failed},
		{"f2fs damage", f2fs, 1, Errors},
		{"f2fs no checkpoint", f2fs, 255, Errors},
		{"xfs corrupt", xfs, 1, Errors},
		{"xfs dirty", xfs, 2, DirtyLog},
	} {
		if got := c.f(c.code); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// A fake fsck.fat that finds damage the first time and none the second:
// its repair is judged by the check that follows.
func TestRepairChecksAgain(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	script := `#!/bin/sh
echo "run $*"
case "$1" in
-a) touch ` + state + `; exit 1 ;;
-n) [ -e ` + state + ` ] && exit 0; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "fsck.fat"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	run := Runner{Dir: dir}
	res, err := run.Run("nonexistent1", "vfat", false, func(s string) { got = append(got, s) })
	if err != nil || res.Status != Errors || res.Code != 1 {
		t.Fatalf("check: %+v %v", res, err)
	}
	res, err = run.Run("nonexistent1", "vfat", true, func(s string) { got = append(got, s) })
	if err != nil || res.Status != Fixed || res.Code != 0 {
		t.Fatalf("repair: %+v %v", res, err)
	}
	if !strings.Contains(strings.Join(got, "|"), "-- checking again") {
		t.Errorf("no re-check in %v", got)
	}
	// Nothing found: no repair, no second check.
	os.Remove(state)
	os.WriteFile(filepath.Join(dir, "fsck.fat"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	got = nil
	if res, _ = run.Run("nonexistent1", "vfat", true, func(s string) { got = append(got, s) }); res.Status != Clean || len(got) != 1 {
		t.Errorf("clean repair: %+v %v", res, got)
	}
}

func TestRefusals(t *testing.T) {
	r := Runner{Dir: t.TempDir()}
	for name, c := range map[string]struct {
		dev, fs string
		repair  bool
	}{
		"bad device": {"a/b", "vfat", false},
		"xfs repair": {"x", "xfs", true},
		"unknown":    {"x", "ntfs", false},
		"no binary":  {"x", "vfat", false},
	} {
		if _, err := r.Run(c.dev, c.fs, c.repair, func(string) {}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
