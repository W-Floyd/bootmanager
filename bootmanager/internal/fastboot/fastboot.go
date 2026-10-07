// Package fastboot is the device side of Android's fastboot protocol
// (AOSP system/core/fastboot/README.md), for a host's stock `fastboot` to
// flash the device's partitions, and whole disks, from the stage: GPT
// partitions by name, any block device by its kernel name (mmcblk1), and
// the SD card as a whole as "sdcard".
//
// It answers as userspace fastboot (fastbootd) does, is-userspace:yes, but
// has no logical partitions: nothing in super is flashable by name.
package fastboot

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	qbootctl "github.com/W-Floyd/go-qbootctl"
)

// Transport is one fastboot connection. Read returns one command packet,
// or, in a download's data phase, up to len(p) bytes of it; Write sends
// one response packet.
type Transport interface {
	io.Reader
	io.Writer
}

// Partition is a flash target: a partition or a whole disk.
type Partition struct {
	Dev   string // kernel name: mmcblk0p3, sda, mmcblk1
	Label string // GPT name; "" for none
	Size  int64
	Disk  bool // a whole disk, not a partition
	SD    bool // an SD card, or a partition on one
}

// Target is an open Partition being written. Close flushes it: an error
// there means the write may not have landed.
type Target interface {
	io.WriterAt
	Close() error
}

// zeroer is a Target that can zero a range faster than writing zeros
// (BLKZEROOUT).
type zeroer interface {
	ZeroRange(off, n int64) error
}

// Server answers fastboot commands. The zero value refuses every flash;
// the hooks are optional.
type Server struct {
	// MaxDownload caps a download, which is held in memory. Larger images
	// the host splits into sparse pieces by itself.
	MaxDownload int
	// Vars are the fixed variables: product, serialno, current-slot, ...
	Vars map[string]string
	// Partitions lists what can be flashed.
	Partitions func() ([]Partition, error)
	// Open opens a partition for writing.
	Open func(Partition) (Target, error)
	// BeforeWrite releases what holds p (mounts) before it is written;
	// an error refuses the flash. AfterWrite follows every BeforeWrite
	// that returned nil.
	BeforeWrite func(p Partition) error
	AfterWrite  func(p Partition)
	// Slots is the A/B slot state, for set_active and the slot-*
	// variables; nil for none.
	Slots SlotStore
	// Busy shows what the server is doing; "" when it is done.
	Busy func(text string)
	// Progress is told how a download and a flash are going, as often as
	// there is news (every few tens of kilobytes): the caller throttles.
	Progress func(Progress)
	// Reboot and Continue run after the reply, on a goroutine of their
	// own: they may stop the server.
	Reboot   func(mode string)
	Continue func()

	buf  []byte
	data []byte // the last download

	// What the flashes so far of one image (the host sends a big one as
	// several sparse pieces) have covered: the partition, the image's
	// size, how far in the last piece ended, and how many pieces.
	lastName  string
	lastTotal int64
	lastEnd   int64
	pieces    int

	abort atomic.Bool
}

// ErrAborted ends a download or a flash that Abort stopped.
var ErrAborted = errors.New("fastboot stopped")

// Abort stops what the server is doing: a download is dropped and a flash
// stops after the chunk it is writing (leaving the partition partly
// written, as a cable pulled would). The server answers nothing more; it
// is for an emergency stop, before the service is switched off.
func (s *Server) Abort() { s.abort.Store(true) }

// Progress is a download, or a flash, under way. A big image comes as
// several pieces (a download and a flash each), so there are two scales:
// the piece, and the whole image.
type Progress struct {
	// Phase is "receiving" a piece, "writing" it, "waiting" for the next
	// piece or "done" with the image.
	Phase string `json:"phase"`
	// Name is the partition, once a flash has named it.
	Name string `json:"name"`
	// Piece counts the pieces flashed so far of this image, from 1.
	Piece int `json:"piece"`
	// Recv of RecvTotal bytes of this piece have come in; Wrote of
	// WroteTotal have been written (the bytes a sparse piece holds, not
	// the holes it skips).
	Recv       int64 `json:"recv"`
	RecvTotal  int64 `json:"recv_total"`
	Wrote      int64 `json:"wrote"`
	WroteTotal int64 `json:"wrote_total"`
	// End of Total bytes into the image: the offset its last written
	// chunk ended at, and the image's size.
	End   int64 `json:"end"`
	Total int64 `json:"total"`
}

func (s *Server) report(p Progress) {
	if s.Progress == nil {
		return
	}
	p.Name, p.Total = s.lastName, s.lastTotal
	if p.End == 0 {
		p.End = s.lastEnd
	}
	if p.Piece == 0 {
		p.Piece = s.pieces + 1
	}
	s.Progress(p)
}

// maxCommand: a command, and a response, are at most this long (the
// protocol's limits are 4096 and 256).
const (
	maxCommand  = 4096
	maxResponse = 256
	dataChunk   = 64 << 10 // one read of the data phase: one USB request
)

// SlotStore is what the server needs of qbootctl.Store.
type SlotStore interface {
	List() ([]qbootctl.Slot, error)
	SetActive(slot string) error
}

var errUnknownVar = errors.New("unknown variable")

// Serve answers commands on t until t fails.
func (s *Server) Serve(t Transport) error {
	cmd := make([]byte, maxCommand)
	for {
		n, err := t.Read(cmd)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := s.handle(t, string(cmd[:n])); err != nil {
			return err
		}
	}
}

func reply(t Transport, status, msg string) error {
	b := []byte(status + msg)
	if len(b) > maxResponse {
		b = b[:maxResponse]
	}
	_, err := t.Write(b)
	return err
}

func okay(t Transport, msg string) error { return reply(t, "OKAY", msg) }
func fail(t Transport, msg string) error { return reply(t, "FAIL", msg) }

func (s *Server) handle(t Transport, cmd string) error {
	name, arg, _ := strings.Cut(cmd, ":")
	switch name {
	case "getvar":
		if arg == "all" {
			for _, kv := range s.allVars() {
				if err := reply(t, "INFO", kv); err != nil {
					return err
				}
			}
			return okay(t, "")
		}
		v, err := s.getvar(arg)
		if err != nil {
			return fail(t, err.Error())
		}
		return okay(t, v)
	case "download":
		return s.download(t, arg)
	case "flash":
		if err := s.flash(arg); err != nil {
			log.Printf("fastboot: flash %s: %v", arg, err)
			return fail(t, err.Error())
		}
		return okay(t, "")
	case "set_active":
		if s.Slots == nil {
			return fail(t, "no A/B slots")
		}
		if err := s.Slots.SetActive(strings.TrimPrefix(arg, "_")); err != nil {
			log.Printf("fastboot: set_active %s: %v", arg, err)
			return fail(t, err.Error())
		}
		log.Printf("fastboot: slot %s active", arg)
		return okay(t, "")
	case "continue":
		if s.Continue == nil {
			return fail(t, "not supported")
		}
		if err := okay(t, ""); err != nil {
			return err
		}
		go s.Continue()
		return nil
	}
	if mode, ok := strings.CutPrefix(cmd, "reboot"); ok && (mode == "" || mode[0] == '-') {
		if s.Reboot == nil {
			return fail(t, "not supported")
		}
		if err := okay(t, ""); err != nil {
			return err
		}
		go s.Reboot(strings.TrimPrefix(mode, "-"))
		return nil
	}
	return fail(t, "unknown command "+strconv.Quote(name))
}

func (s *Server) download(t Transport, arg string) error {
	size, err := strconv.ParseUint(arg, 16, 32)
	switch {
	case err != nil || len(arg) != 8:
		return fail(t, "bad size "+strconv.Quote(arg))
	case size == 0 || size > uint64(s.MaxDownload):
		return fail(t, fmt.Sprintf("size 0x%x is over max-download-size 0x%x", size, s.MaxDownload))
	}
	s.data = nil
	if cap(s.buf) < int(size) {
		s.buf = nil // before the new one: both may not fit
		s.buf = make([]byte, size)
	}
	buf := s.buf[:size]
	if err := reply(t, "DATA", fmt.Sprintf("%08x", size)); err != nil {
		return err
	}
	for got := 0; got < len(buf); {
		if s.abort.Load() {
			return ErrAborted
		}
		n, err := t.Read(buf[got:min(len(buf), got+dataChunk)])
		if err != nil {
			return err
		}
		got += n
		s.report(Progress{Phase: "receiving", Recv: int64(got), RecvTotal: int64(len(buf))})
	}
	s.data = buf
	return okay(t, "")
}

// resolve finds name among parts: "sdcard", a GPT name, a kernel name.
func resolve(parts []Partition, name string) (Partition, error) {
	var found []Partition
	for _, p := range parts {
		if name == "sdcard" && p.SD && p.Disk || p.Label == name {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		for _, p := range parts {
			if p.Dev == name {
				found = append(found, p)
			}
		}
	}
	switch len(found) {
	case 0:
		return Partition{}, fmt.Errorf("no partition %q", name)
	case 1:
		return found[0], nil
	}
	var devs []string
	for _, p := range found {
		devs = append(devs, p.Dev)
	}
	return Partition{}, fmt.Errorf("%q is %s: name one", name, strings.Join(devs, ", "))
}

func (s *Server) partition(name string) (Partition, error) {
	if s.Partitions == nil {
		return Partition{}, fmt.Errorf("no partition %q", name)
	}
	parts, err := s.Partitions()
	if err != nil {
		return Partition{}, err
	}
	return resolve(parts, name)
}

func (s *Server) getvar(name string) (string, error) {
	base, arg, _ := strings.Cut(name, ":")
	switch base {
	case "version":
		return "0.4", nil
	case "max-download-size":
		return fmt.Sprintf("0x%x", s.MaxDownload), nil
	case "is-userspace":
		return "yes", nil
	case "partition-size", "partition-type", "is-logical":
		p, err := s.partition(arg)
		if err != nil {
			return "", err
		}
		switch base {
		case "partition-size":
			return fmt.Sprintf("0x%x", p.Size), nil
		case "partition-type":
			return "raw", nil
		}
		return "no", nil
	case "slot-successful", "slot-unbootable", "slot-retry-count":
		if s.Slots == nil {
			return "", errUnknownVar
		}
		list, err := s.Slots.List()
		if err != nil {
			return "", err
		}
		for _, sl := range list {
			if sl.Name == strings.TrimPrefix(arg, "_") {
				return slotVar(base, sl), nil
			}
		}
		return "", fmt.Errorf("no slot %q", arg)
	case "has-slot":
		// The host adds the current slot's suffix to a name that has one.
		if _, err := s.partition(arg + "_a"); err == nil {
			return "yes", nil
		}
		return "no", nil
	}
	if v, ok := s.Vars[name]; ok {
		return v, nil
	}
	return "", errUnknownVar
}

var slotVars = []string{"slot-successful", "slot-unbootable", "slot-retry-count"}

func slotVar(name string, sl qbootctl.Slot) string {
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	switch name {
	case "slot-successful":
		return yesNo(sl.Successful)
	case "slot-unbootable":
		return yesNo(sl.Unbootable)
	}
	return strconv.Itoa(sl.Retries)
}

// allVars is getvar:all's list, as name:value.
func (s *Server) allVars() []string {
	var out []string
	for _, n := range []string{"version", "max-download-size", "is-userspace"} {
		v, _ := s.getvar(n)
		out = append(out, n+":"+v)
	}
	var fixed []string
	for n, v := range s.Vars {
		fixed = append(fixed, n+":"+v)
	}
	slices.Sort(fixed)
	out = append(out, fixed...)
	if s.Slots != nil {
		list, _ := s.Slots.List()
		for _, sl := range list {
			for _, n := range slotVars {
				out = append(out, n+":"+sl.Name+":"+slotVar(n, sl))
			}
		}
	}
	if s.Partitions == nil {
		return out
	}
	parts, _ := s.Partitions()
	for _, p := range parts {
		name := p.Label
		switch {
		case p.SD && p.Disk:
			name = "sdcard"
		case name == "":
			name = p.Dev
		}
		if _, err := resolve(parts, name); err != nil {
			name = p.Dev
		}
		out = append(out, fmt.Sprintf("partition-size:%s:0x%x", name, p.Size))
	}
	return out
}

func (s *Server) flash(name string) error {
	if s.data == nil {
		return errors.New("nothing downloaded")
	}
	p, err := s.partition(name)
	if err != nil {
		return err
	}
	img := s.data
	var sp *sparseImage
	size := int64(len(img))
	if isSparse(img) {
		if sp, err = parseSparse(img); err != nil {
			return err
		}
		size = sp.size()
	}
	if size > p.Size {
		return fmt.Errorf("image is %d bytes, %s %d", size, p.Dev, p.Size)
	}
	// Another partition, or another size of image, is another image: a
	// piece of the same one carries on from where the last ended.
	if p.Dev != s.lastName || size != s.lastTotal {
		s.lastName, s.lastTotal, s.lastEnd, s.pieces = p.Dev, size, 0, 0
	}
	if s.Open == nil {
		return errors.New("flashing is not supported")
	}
	if s.Busy != nil {
		s.Busy("Flashing " + name + "…")
		defer s.Busy("")
	}
	if s.BeforeWrite != nil {
		if err := s.BeforeWrite(p); err != nil {
			return err
		}
	}
	if s.AfterWrite != nil {
		defer s.AfterWrite(p)
	}
	w, err := s.Open(p)
	if err != nil {
		return err
	}
	pieceBytes := int64(len(img))
	if sp != nil {
		pieceBytes = sp.dataBytes()
	}
	s.report(Progress{Phase: "writing", WroteTotal: pieceBytes})
	if sp != nil {
		err = sp.writeToProgress(w, func(wrote, end int64) error {
			if s.abort.Load() {
				return ErrAborted
			}
			s.report(Progress{Phase: "writing", Wrote: wrote, WroteTotal: pieceBytes, End: end})
			return nil
		})
	} else {
		_, err = w.WriteAt(img, 0)
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	s.pieces++
	if sp != nil {
		s.lastEnd = max(s.lastEnd, sp.end())
	} else {
		s.lastEnd = size
	}
	phase := "waiting"
	if s.lastEnd >= s.lastTotal {
		phase = "done"
	}
	s.report(Progress{Phase: phase, Wrote: pieceBytes, WroteTotal: pieceBytes, Piece: s.pieces})
	kind := "raw"
	if sp != nil {
		kind = "sparse"
	}
	log.Printf("fastboot: flashed %s (%s): %d bytes %s", name, p.Dev, len(img), kind)
	return nil
}

var le = binary.LittleEndian
