package btrfs

import (
	"os"
	"testing"
)

// BTRFS_TEST_MOUNT: a btrfs mounted at its top level, with subvolumes
// "@" and "@/nested" and "other" (set up by the script that runs this).
func TestSubvolumesReal(t *testing.T) {
	dir := os.Getenv("BTRFS_TEST_MOUNT")
	if dir == "" {
		t.Skip("BTRFS_TEST_MOUNT not set")
	}
	subs, err := Subvolumes(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range subs {
		got = append(got, s.Path)
	}
	t.Logf("%+v", subs)
	want := []string{"@", "@/nested", "other"}
	if len(got) != len(want) {
		t.Fatalf("%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, want %v", got, want)
		}
	}
	id, err := MountedID(dir)
	if err != nil || id != 5 {
		t.Errorf("mounted id %d, %v; want 5", id, err)
	}
}
