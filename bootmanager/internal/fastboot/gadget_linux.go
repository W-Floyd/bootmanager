package fastboot

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The fastboot function joins the stage's configfs gadget (the USB
// network's) as a FunctionFS function: an interface of class ff/42/03,
// which is how the host's fastboot finds a device, beside the gadget's
// others. Linking it in means unbinding the gadget from its UDC and
// binding it again, which drops the USB network for a moment.
const (
	gadgetRoot = "/sys/kernel/config/usb_gadget"
	ffsName    = "fastboot"
	ffsMount   = "/dev/usb-ffs/" + ffsName
)

// linux/usb/functionfs.h
const (
	descsMagicV2   = 3
	stringsMagic   = 2
	hasFSDesc      = 1
	hasHSDesc      = 2
	hasSSDesc      = 4
	eventSize      = 12
	eventUnbind    = 1
	eventEnable    = 2
	eventDisable   = 3
	eventSetup     = 4
	usbDirIn       = 0x80
	interfaceClass = 0xff
	interfaceSub   = 0x42
	interfaceProto = 0x03
)

var errClosed = errors.New("fastboot stopped")

type gadget struct {
	dir, udc, link, fn string
	serial, oldSerial  string // the USB serial number while the function is in, and before
	mounted            bool
	unbound            bool // by us, and not bound again yet
	ep0                *os.File
	out, in            int // ep1, ep2; -1 until open

	mu      sync.Mutex
	cond    *sync.Cond
	enabled bool // the host configured the function
	closed  bool
}

// Start adds the fastboot function to the configfs gadget at dir (""
// for the one bound to a UDC) and serves s on it until stop. The gadget
// takes s's serialno as its USB serial number meanwhile, as fastboot
// devices lists it; without one, serialno is the gadget's.
func Start(dir string, s *Server) (stop func(), err error) {
	g := &gadget{out: -1, in: -1, serial: s.Vars["serialno"]}
	g.cond = sync.NewCond(&g.mu)
	if err := g.setUp(dir); err != nil {
		g.tearDown()
		return nil, err
	}
	if g.serial == "" {
		if s.Vars == nil {
			s.Vars = map[string]string{}
		}
		s.Vars["serialno"] = g.oldSerial
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.serve(s)
	}()
	return func() {
		g.mu.Lock()
		g.closed = true
		g.cond.Broadcast()
		g.mu.Unlock()
		// Ends a transfer in flight (ESHUTDOWN); the endpoints stay
		// open until serve is done with them.
		g.writeUDC("")
		<-done
		g.tearDown()
	}, nil
}

func (g *gadget) serve(s *Server) {
	// A signal ends a transfer early (f_fs dequeues it, EINTR), losing
	// what it had received: keep them off the thread doing the I/O. The
	// thread goes with the goroutine, and its mask with it.
	runtime.LockOSThread()
	var all unix.Sigset_t
	for i := range all.Val {
		all.Val[i] = ^all.Val[i]
	}
	unix.PthreadSigmask(unix.SIG_BLOCK, &all, nil)
	for {
		err := s.Serve(g)
		if g.isClosed() {
			return
		}
		// The host went away (ESHUTDOWN) or let the interface go (EPIPE).
		if !errors.Is(err, unix.ESHUTDOWN) && !errors.Is(err, unix.EPIPE) {
			log.Printf("fastboot: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (g *gadget) setUp(dir string) error {
	if dir == "" {
		var err error
		if dir, err = boundGadget(); err != nil {
			return err
		}
	}
	g.dir = dir
	udc, err := os.ReadFile(filepath.Join(dir, "UDC"))
	if err != nil {
		return err
	}
	if g.udc = strings.TrimSpace(string(udc)); g.udc == "" {
		return fmt.Errorf("%s is bound to no UDC", dir)
	}
	configs, _ := filepath.Glob(filepath.Join(dir, "configs", "*"))
	if len(configs) == 0 {
		return fmt.Errorf("%s has no configuration", dir)
	}
	slices.Sort(configs)

	g.fn = filepath.Join(dir, "functions", "ffs."+ffsName)
	if err := os.Mkdir(g.fn, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("FunctionFS (CONFIG_USB_CONFIGFS_F_FS): %w", err)
	}
	if err := os.MkdirAll(ffsMount, 0o755); err != nil {
		return err
	}
	unix.Unmount(ffsMount, 0) // a crashed run's
	if err := unix.Mount(ffsName, ffsMount, "functionfs", 0, ""); err != nil {
		return fmt.Errorf("mount functionfs: %w", err)
	}
	g.mounted = true

	if g.ep0, err = os.OpenFile(filepath.Join(ffsMount, "ep0"), os.O_RDWR, 0); err != nil {
		return err
	}
	if _, err := g.ep0.Write(descriptors()); err != nil {
		return fmt.Errorf("descriptors: %w", err)
	}
	if _, err := g.ep0.Write(stringTable()); err != nil {
		return fmt.Errorf("strings: %w", err)
	}
	// Non-blocking: an endpoint the host has not enabled answers EAGAIN
	// instead of waiting, where only a signal would end the wait.
	for _, ep := range []struct {
		fd   *int
		name string
	}{{&g.out, "ep1"}, {&g.in, "ep2"}} {
		if *ep.fd, err = unix.Open(filepath.Join(ffsMount, ep.name), unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0); err != nil {
			return fmt.Errorf("%s: %w", ep.name, err)
		}
	}
	go g.events()

	if err := g.writeUDC(""); err != nil {
		return err
	}
	if b, err := os.ReadFile(g.serialFile()); err == nil {
		g.oldSerial = strings.TrimSpace(string(b))
	}
	if g.serial != "" && g.serial != g.oldSerial {
		if err := g.writeSerial(g.serial); err != nil {
			return err
		}
	}
	g.link = filepath.Join(configs[0], ffsName)
	os.Remove(g.link)
	if err := os.Symlink(g.fn, g.link); err != nil {
		g.link = ""
		return err
	}
	return g.writeUDC(g.udc)
}

// tearDown undoes what setUp did, as far as it got, and binds the gadget
// again without the function.
func (g *gadget) tearDown() {
	if g.link != "" {
		g.writeUDC("")
		os.Remove(g.link)
	}
	if g.serial != "" && g.serial != g.oldSerial && g.oldSerial != "" {
		g.writeUDC("")
		g.writeSerial(g.oldSerial)
	}
	for _, fd := range []int{g.out, g.in} {
		if fd >= 0 {
			unix.Close(fd)
		}
	}
	if g.ep0 != nil {
		g.ep0.Close()
	}
	if g.mounted {
		if err := unix.Unmount(ffsMount, 0); err != nil {
			log.Printf("fastboot: unmount %s: %v", ffsMount, err)
		}
	}
	if g.fn != "" {
		os.Remove(g.fn)
	}
	if g.unbound {
		if err := g.writeUDC(g.udc); err != nil {
			log.Printf("fastboot: rebinding %s: %v", g.dir, err)
		}
	}
}

// writeUDC binds the gadget to udc, or unbinds it for "". One write, as
// configfs takes each write as the whole value.
func (g *gadget) writeUDC(udc string) error {
	err := os.WriteFile(filepath.Join(g.dir, "UDC"), []byte(udc+"\n"), 0)
	if err == nil {
		g.unbound = udc == ""
	}
	return err
}

func (g *gadget) serialFile() string { return filepath.Join(g.dir, "strings/0x409/serialnumber") }

func (g *gadget) writeSerial(s string) error { return os.WriteFile(g.serialFile(), []byte(s+"\n"), 0) }

// boundGadget is the one gadget bound to a UDC.
func boundGadget() (string, error) {
	dirs, _ := filepath.Glob(filepath.Join(gadgetRoot, "*"))
	var bound []string
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, "UDC")); err == nil && strings.TrimSpace(string(b)) != "" {
			bound = append(bound, d)
		}
	}
	if len(bound) != 1 {
		return "", fmt.Errorf("%d USB gadgets bound under %s; name one", len(bound), gadgetRoot)
	}
	return bound[0], nil
}

// events follows ep0: whether the host has the function configured, and
// control requests, which it stalls.
func (g *gadget) events() {
	buf := make([]byte, 4*eventSize)
	for {
		n, err := g.ep0.Read(buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return // closed
		}
		for ev := buf[:n]; len(ev) >= eventSize; ev = ev[eventSize:] {
			switch ev[8] {
			case eventEnable:
				g.setEnabled(true)
			case eventDisable, eventUnbind:
				g.setEnabled(false)
			case eventSetup:
				// Stalled by a transfer the wrong way (functionfs.rst).
				stall := []byte{0}
				if ev[0]&usbDirIn != 0 {
					g.ep0.Read(stall)
				} else {
					g.ep0.Write(stall)
				}
			}
		}
	}
}

func (g *gadget) setEnabled(on bool) {
	g.mu.Lock()
	g.enabled = on
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *gadget) isClosed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// waitEnabled waits for the host to configure the function; false once
// the gadget is closing.
func (g *gadget) waitEnabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.enabled && !g.closed {
		g.cond.Wait()
	}
	return !g.closed
}

func (g *gadget) Read(p []byte) (int, error)  { return g.io(g.out, p, unix.Read) }
func (g *gadget) Write(p []byte) (int, error) { return g.io(g.in, p, unix.Write) }

func (g *gadget) io(fd int, p []byte, op func(int, []byte) (int, error)) (int, error) {
	for {
		if !g.waitEnabled() {
			return 0, errClosed
		}
		n, err := op(fd, p)
		switch {
		case err == nil:
			return n, nil
		case errors.Is(err, unix.EINTR):
		case errors.Is(err, unix.EAGAIN):
			// Disabled, and ep0's event not read yet.
			time.Sleep(10 * time.Millisecond)
		default:
			return n, err
		}
	}
}

var _ io.ReadWriter = (*gadget)(nil)

// descriptors is the function's interface at full, high and super speed:
// fastboot's class, a bulk endpoint out (ep1) and one in (ep2).
func descriptors() []byte {
	iface := []byte{9, 4, 0, 0, 2, interfaceClass, interfaceSub, interfaceProto, 1}
	ep := func(addr byte, maxPacket int) []byte {
		return []byte{7, 5, addr, 2, byte(maxPacket), byte(maxPacket >> 8), 0}
	}
	ssComp := []byte{6, 0x30, 0, 0, 0, 0}
	fs := slices.Concat(iface, ep(0x01, 64), ep(0x82, 64))
	hs := slices.Concat(iface, ep(0x01, 512), ep(0x82, 512))
	ss := slices.Concat(iface, ep(0x01, 1024), ssComp, ep(0x82, 1024), ssComp)
	body := slices.Concat(u32(3), u32(3), u32(5), fs, hs, ss)
	return slices.Concat(u32(descsMagicV2), u32(12+len(body)), u32(hasFSDesc|hasHSDesc|hasSSDesc), body)
}

// stringTable names the interface, in US English.
func stringTable() []byte {
	body := slices.Concat(u32(1), u32(1), []byte{0x09, 0x04}, []byte(ffsName+"\x00"))
	return slices.Concat(u32(stringsMagic), u32(8+len(body)), body)
}

func u32(v int) []byte { return le.AppendUint32(nil, uint32(v)) }
