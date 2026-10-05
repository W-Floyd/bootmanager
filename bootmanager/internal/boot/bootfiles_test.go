package boot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBootFilesIn(t *testing.T) {
	mk := func(dirs ...string) string {
		root := t.TempDir()
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	cases := []struct {
		name string
		root string
		want string
		ok   bool
	}{
		{"the card, which is /boot", mk("linuxboot", "extlinux"), "linuxboot", true},
		{"a root file system with /boot", mk("boot/linuxboot", "etc"), "boot/linuxboot", true},
		{"both: the root's own wins", mk("linuxboot", "boot/linuxboot"), "linuxboot", true},
		{"neither", mk("boot", "etc"), "", false},
	}
	for _, c := range cases {
		got, ok := bootFilesIn(c.root)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
	// A file called linuxboot is not the directory.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "linuxboot"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := bootFilesIn(root); ok {
		t.Errorf("a file called linuxboot was taken for the directory: %q", got)
	}
}

func TestSearchOrder(t *testing.T) {
	in := []string{"sde27", "sda", "mmcblk0p2", "sdd13", "mmcblk0p1", "mmcblk0"}
	want := []string{"mmcblk0", "mmcblk0p1", "mmcblk0p2", "sda", "sdd13", "sde27"}
	got := searchOrder(in)
	if !slices.Equal(got, want) {
		t.Errorf("searchOrder = %v, want %v", got, want)
	}
	if in[0] != "sde27" {
		t.Errorf("searchOrder changed its argument: %v", in)
	}
}
