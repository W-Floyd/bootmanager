package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	micom "github.com/W-Floyd/go-micomd"
	"golang.org/x/sys/unix"
)

// evdevNode finds the event device of the input device called name, making
// its node when there is none (a container has no devtmpfs for it).
func evdevNode(t *testing.T, name string) string {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		names, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, n := range names {
			if b, _ := os.ReadFile(n); strings.TrimSpace(string(b)) != name {
				continue
			}
			ev := filepath.Base(filepath.Dir(filepath.Dir(n)))
			node := "/dev/input/" + ev
			if _, err := os.Stat(node); err != nil {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(n)), "dev"))
				if err != nil {
					t.Fatal(err)
				}
				var major, minor uint32
				if _, err := fmt.Sscanf(string(b), "%d:%d", &major, &minor); err != nil {
					t.Fatal(err)
				}
				os.MkdirAll("/dev/input", 0o755)
				if err := unix.Mknod(node, unix.S_IFCHR|0o600, int(unix.Mkdev(major, minor))); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(node) })
			}
			return node
		}
	}
	t.Fatalf("no input device called %q", name)
	return ""
}

// keyEvents reads the EV_KEY events off an event device, "code=value", until
// there are n of them or the time is up.
func keyEvents(t *testing.T, node string, n int, wait time.Duration) []string {
	t.Helper()
	fd, err := syscall.Open(node, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	size := int(unsafe.Sizeof(syscall.Timeval{})) + 8 // struct input_event
	buf := make([]byte, size*16)
	var got []string
	for deadline := time.Now().Add(wait); time.Now().Before(deadline) && len(got) < n; {
		r, err := syscall.Read(fd, buf)
		if err != nil || r < size {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		for off := 0; off+size <= r; off += size {
			e := buf[off+size-8:]
			if binary.LittleEndian.Uint16(e) == 1 { // EV_KEY
				got = append(got, fmt.Sprintf("%d=%d", binary.LittleEndian.Uint16(e[2:]), int32(binary.LittleEndian.Uint32(e[4:]))))
			}
		}
	}
	return got
}

// The hard keys, end to end as far as the kernel: a short press of a mapped
// key arrives on a real event device as KEY_UP pressed and released, which is
// what the UI reads; a long press, a release and an unmapped key arrive as
// nothing.
func TestNavKeysUinputReal(t *testing.T) {
	if _, err := os.Stat("/dev/uinput"); err != nil || os.Geteuid() != 0 {
		t.Skip("needs root and /dev/uinput")
	}
	km, err := parseNavKeys("5.1=up,5.2=down")
	if err != nil {
		t.Fatal(err)
	}
	const name = "bootmanager micom keys test"
	ui, err := micom.OpenUinput(name, km.codes())
	if err != nil {
		t.Skipf("uinput: %v", err)
	}
	defer ui.Close()
	node := evdevNode(t, name)

	n := &navKeys{fn: 0x8802, keys: km, out: ui, logf: t.Logf}
	frame := func(typ, code, press byte) micom.Frame {
		return micom.Frame{Hdr: [4]byte{0x0c, 0, 0, 0x88}, Cmd: 0x02, Payload: []byte{typ, press<<4 | code}}
	}
	// The reader has the device open before the first key, as the UI has.
	done := make(chan []string)
	go func() { done <- keyEvents(t, node, 4, 3*time.Second) }()
	time.Sleep(200 * time.Millisecond)
	n.frame(frame(5, 1, micom.PressLong))    // nothing
	n.frame(frame(5, 1, micom.PressRelease)) // nothing
	n.frame(frame(5, 9, micom.PressShort))   // not mapped: nothing
	n.frame(frame(5, 1, micom.PressShort))   // KEY_UP down and up
	n.frame(frame(5, 2, micom.PressShort))   // KEY_DOWN down and up
	got := <-done
	want := []string{"103=1", "103=0", "108=1", "108=0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("events %v, want %v", got, want)
	}
}
