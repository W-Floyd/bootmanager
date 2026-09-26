package power

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestModes(t *testing.T) {
	dt := t.TempDir()
	pon := filepath.Join(dt, "soc", "pon@800")
	os.MkdirAll(pon, 0o755)
	for _, p := range []string{"mode-bootloader", "mode-recovery", "mode-edl", "compatible"} {
		os.WriteFile(filepath.Join(pon, p), nil, 0o644)
	}
	got := modesIn(dt, []string{"bootloader", "fastboot", "recovery", "download"})
	if want := []string{"bootloader", "recovery"}; !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}
