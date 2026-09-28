package fastboot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bootmanager.local/bootmanager/internal/slots"
)

type fakeSlots struct {
	mu     sync.Mutex
	active string
}

func (f *fakeSlots) List() ([]slots.Slot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slots.Slot
	for _, n := range []string{"a", "b"} {
		sl := slots.Slot{Name: n, Booted: n == "a"}
		if n == f.active {
			sl.Active, sl.Priority, sl.Retries = true, 3, 7
		}
		out = append(out, sl)
	}
	return out, nil
}

func (f *fakeSlots) SetActive(slot string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slot != "a" && slot != "b" {
		return errors.New("no slot " + slot)
	}
	f.active = slot
	return nil
}

type memTarget struct {
	mu     sync.Mutex
	b      []byte
	closed int
}

func (m *memTarget) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if off+int64(len(p)) > int64(len(m.b)) {
		return 0, errors.New("past the end")
	}
	return copy(m.b[off:], p), nil
}

func (m *memTarget) Close() error { m.closed++; return nil }

func (m *memTarget) bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return bytes.Clone(m.b)
}

// testDevice is a Server over in-memory partitions: boot_a and boot_b,
// a whole SD card and a partition on it.
func testDevice() (*Server, map[string]*memTarget) {
	parts := []Partition{
		{Dev: "sda", Size: 1 << 20, Disk: true},
		{Dev: "sda1", Label: "boot_a", Size: 64 << 10},
		{Dev: "sda2", Label: "boot_b", Size: 64 << 10},
		{Dev: "mmcblk1", Size: 1 << 20, Disk: true, SD: true},
		{Dev: "mmcblk1p1", Label: "pmOS_boot", Size: 256 << 10, SD: true},
	}
	mem := map[string]*memTarget{}
	for _, p := range parts {
		mem[p.Dev] = &memTarget{b: make([]byte, p.Size)}
	}
	s := &Server{
		MaxDownload: 128 << 10,
		Vars:        map[string]string{"product": "test", "current-slot": "a", "slot-count": "2"},
		Partitions:  func() ([]Partition, error) { return parts, nil },
		Open:        func(p Partition) (Target, error) { return mem[p.Dev], nil },
		Slots:       &fakeSlots{active: "a"},
	}
	return s, mem
}

// pipeTransport feeds Serve a script of packets and records its replies.
type pipeTransport struct {
	in      [][]byte
	replies []string
}

func (p *pipeTransport) Read(b []byte) (int, error) {
	if len(p.in) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.in[0])
	if p.in[0] = p.in[0][n:]; len(p.in[0]) == 0 {
		p.in = p.in[1:]
	}
	return n, nil
}

func (p *pipeTransport) Write(b []byte) (int, error) {
	p.replies = append(p.replies, string(b))
	return len(b), nil
}

func TestServe(t *testing.T) {
	s, mem := testDevice()
	img := make([]byte, 5000)
	rand.New(rand.NewSource(1)).Read(img)
	p := &pipeTransport{in: [][]byte{
		[]byte("getvar:has-slot:boot"),
		[]byte("getvar:partition-size:sdcard"),
		[]byte("getvar:nope"),
		[]byte("flash:boot_b"),
		[]byte("download:00001388"), img[:3000], img[3000:],
		[]byte("flash:boot_b"),
		[]byte("flash:mmcblk1p9"),
		[]byte("download:00100000"),
		[]byte("frobnicate"),
	}}
	if err := s.Serve(p); err != io.EOF {
		t.Fatalf("Serve: %v", err)
	}
	want := []string{
		"OKAYyes",
		"OKAY0x100000",
		"FAILunknown variable",
		"FAILnothing downloaded",
		"DATA00001388", "OKAY",
		"OKAY",
		`FAILno partition "mmcblk1p9"`,
		"FAILsize 0x100000 is over max-download-size 0x20000",
		`FAILunknown command "frobnicate"`,
	}
	if strings.Join(p.replies, "\n") != strings.Join(want, "\n") {
		t.Errorf("replies:\n%s\nwant:\n%s", strings.Join(p.replies, "\n"), strings.Join(want, "\n"))
	}
	if got := mem["sda2"].bytes(); !bytes.Equal(got[:len(img)], img) {
		t.Error("boot_b does not hold the image")
	}
}

func TestResolve(t *testing.T) {
	s, _ := testDevice()
	parts, _ := s.Partitions()
	parts = append(parts, Partition{Dev: "sdb1", Label: "boot_a"})
	for name, want := range map[string]string{"sdcard": "mmcblk1", "pmOS_boot": "mmcblk1p1", "sda2": "sda2", "boot_b": "sda2"} {
		p, err := resolve(parts, name)
		if err != nil || p.Dev != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", name, p.Dev, err, want)
		}
	}
	if _, err := resolve(parts, "boot_a"); err == nil || !strings.Contains(err.Error(), "sda1, sdb1") {
		t.Errorf("resolve(boot_a), labelled twice: %v", err)
	}
}

// fastbootTCP is the host's TCP transport (AOSP fastboot's tcp.cpp): a
// "FB01" handshake each way, then every packet prefixed with its length,
// big-endian u64. A data phase may span packets.
type fastbootTCP struct {
	c    net.Conn
	left int // of the packet being read
}

func (f *fastbootTCP) Read(p []byte) (int, error) {
	if f.left == 0 {
		var hdr [8]byte
		if _, err := io.ReadFull(f.c, hdr[:]); err != nil {
			return 0, err
		}
		f.left = int(binary.BigEndian.Uint64(hdr[:]))
	}
	n, err := f.c.Read(p[:min(len(p), f.left)])
	f.left -= n
	return n, err
}

func (f *fastbootTCP) Write(p []byte) (int, error) {
	_, err := f.c.Write(binary.BigEndian.AppendUint64(nil, uint64(len(p))))
	if err == nil {
		_, err = f.c.Write(p)
	}
	return len(p), err
}

// TestHostFastboot drives the server with the host's own fastboot, over
// TCP, when it is installed.
func TestHostFastboot(t *testing.T) {
	bin, err := exec.LookPath("fastboot")
	if err != nil {
		t.Skip("no fastboot on PATH")
	}
	s, mem := testDevice()
	reboots := make(chan string, 1)
	s.Reboot = func(mode string) { reboots <- mode }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var hs [4]byte
			if _, err := io.ReadFull(c, hs[:]); err == nil && string(hs[:2]) == "FB" {
				c.Write([]byte("FB01"))
				s.Serve(&fastbootTCP{c: c})
			}
			c.Close()
		}
	}()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(bin, append([]string{"-s", "tcp:" + ln.Addr().String()}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fastboot %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	file := func(name string, n int, seed int64) (string, []byte) {
		b := make([]byte, n)
		r := rand.New(rand.NewSource(seed))
		for i := 0; i < n; i += 4096 {
			if i/4096%3 != 0 { // leave zero blocks, for fill chunks
				r.Read(b[i:min(n, i+4096)])
			}
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p, b
	}

	if out := run("getvar", "current-slot"); !strings.Contains(out, "current-slot: a") {
		t.Errorf("getvar current-slot:\n%s", out)
	}
	if out := run("getvar", "all"); !strings.Contains(out, "partition-size:sdcard:0x100000") {
		t.Errorf("getvar all:\n%s", out)
	}

	// boot takes the current slot's suffix.
	p, want := file("boot.img", 40<<10, 1)
	run("flash", "boot", p)
	if !bytes.Equal(mem["sda1"].bytes()[:len(want)], want) {
		t.Error("flash boot: boot_a does not hold the image")
	}
	p, want = file("boot_b.img", 50<<10, 2)
	run("flash", "boot_b", p)
	if !bytes.Equal(mem["sda2"].bytes()[:len(want)], want) {
		t.Error("flash boot_b: boot_b does not hold the image")
	}

	// A disk image over max-download-size, which the host sends as sparse
	// pieces.
	p, want = file("sd.img", 900<<10, 3)
	copy(mem["mmcblk1"].b, bytes.Repeat([]byte{0xaa}, 1<<20))
	run("flash", "sdcard", p)
	got := mem["mmcblk1"].bytes()
	if !bytes.Equal(got[:len(want)], want) {
		t.Error("flash sdcard: the card does not hold the image")
	}

	p, _ = file("big.img", 100<<10, 4)
	if out, err := exec.Command(bin, "-s", "tcp:"+ln.Addr().String(), "flash", "boot_a", p).CombinedOutput(); err == nil {
		t.Errorf("flash boot_a with an image over its size succeeded:\n%s", out)
	}

	run("set_active", "b")
	if out := run("getvar", "slot-retry-count:b"); !strings.Contains(out, "slot-retry-count:b: 7") {
		t.Errorf("getvar slot-retry-count:b after set_active b:\n%s", out)
	}
	if out := run("getvar", "all"); !strings.Contains(out, "slot-retry-count:a:0") {
		t.Errorf("getvar all, slot vars:\n%s", out)
	}

	run("reboot", "bootloader")
	if m := <-reboots; m != "bootloader" {
		t.Errorf("reboot bootloader: mode %q", m)
	}
}
