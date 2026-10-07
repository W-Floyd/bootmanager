package boot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func rawFile(t *testing.T, size int64) rawStore {
	t.Helper()
	p := filepath.Join(t.TempDir(), "boot_a")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, size); err != nil {
		t.Fatal(err)
	}
	return rawStore{p}
}

func TestRawRecordRoundTrip(t *testing.T) {
	r := rawFile(t, 96<<20)
	if got, err := r.load(); err != nil || got != nil {
		t.Fatalf("nothing saved: %q, %v", got, err)
	}
	for i, s := range []string{"one", "two", "three"} {
		if err := r.save([]byte(s)); err != nil {
			t.Fatal(err)
		}
		if got, err := r.load(); err != nil || string(got) != s {
			t.Fatalf("save %d: loaded %q, %v", i, got, err)
		}
	}
}

// A copy torn mid-write leaves the other standing.
func TestRawRecordSurvivesATornWrite(t *testing.T) {
	r := rawFile(t, 96<<20)
	r.save([]byte("old"))
	r.save([]byte("new"))
	f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
	defer f.Close()
	size, _ := sizeOf(f)
	a, b, _ := r.offsets(size)
	// "new" is seq 2: copy B. Corrupt its payload.
	f.WriteAt([]byte("X"), b+rawHeaderLen)
	if got, _ := r.load(); string(got) != "old" {
		t.Fatalf("with the newer copy torn, loaded %q, want old", got)
	}
	_ = a
}

func TestRawRecordLeavesTheImageAlone(t *testing.T) {
	r := rawFile(t, 96<<20)
	img := bytes.Repeat([]byte{0xAB}, 54<<20)
	f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
	f.WriteAt(img, 0)
	f.Close()
	if err := r.save([]byte("x")); err != nil {
		t.Fatal(err)
	}
	g, _ := os.Open(r.path)
	defer g.Close()
	got := make([]byte, len(img))
	g.ReadAt(got, 0)
	if !bytes.Equal(got, img) {
		t.Fatal("the image was touched")
	}
	// And the last 4 KiB (the AVB footer) too.
	tail := make([]byte, 4096)
	g.ReadAt(tail, 96<<20-4096)
	if !bytes.Equal(tail, make([]byte, 4096)) {
		t.Fatal("the footer was touched")
	}
}

func TestRawRecordRefusesASmallPartition(t *testing.T) {
	r := rawFile(t, 4<<20)
	if err := r.save([]byte("x")); err == nil {
		t.Fatal("saved into a partition too small to be safe")
	}
}

func TestPartitionByName(t *testing.T) {
	sys := t.TempDir()
	for name, body := range map[string]string{"sdd1": "DEVTYPE=partition\nPARTNAME=abl_a\n", "sdd24": "PARTNAME=boot_a\n"} {
		os.MkdirAll(filepath.Join(sys, name), 0o755)
		os.WriteFile(filepath.Join(sys, name, "uevent"), []byte(body), 0o644)
	}
	got, err := partitionByName(sys, "/dev", "boot_a")
	if err != nil || got != "/dev/sdd24" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := partitionByName(sys, "/dev", "nope"); err == nil {
		t.Fatal("found a partition that is not there")
	}
}
